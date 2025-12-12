package connection

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cgrates/fsock"
	"github.com/luongdev/fsagent/pkg/config"
	"github.com/luongdev/fsagent/pkg/logger"
	"github.com/luongdev/fsagent/pkg/metrics"
)

// ConnectionManager manages ESL connections to multiple FreeSWITCH instances
type ConnectionManager interface {
	// Start initiates connections to all configured FreeSWITCH instances
	Start(ctx context.Context) error

	// Stop gracefully closes all connections
	Stop() error

	// GetStatus returns connection status for all instances
	GetStatus() map[string]ConnectionStatus
}

// ConnectionStatus represents the status of a FreeSWITCH connection
type ConnectionStatus struct {
	InstanceName string
	Connected    bool
	LastError    error
	LastEventAt  time.Time
}

// FSEvent represents a FreeSWITCH event
type FSEvent struct {
	Headers map[string]string
	Body    string
}

// GetHeader retrieves a header value from the event
func (e *FSEvent) GetHeader(key string) string {
	if e.Headers == nil {
		return ""
	}
	return e.Headers[key]
}

// FSConnection represents a connection to a single FreeSWITCH instance
type FSConnection struct {
	config      config.FSConfig
	conn        *fsock.FSConn
	eventChan   chan *FSEvent
	reconnectCh chan struct{}
	status      ConnectionStatus
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	connErr     chan error
}

// NewFSConnection creates a new FSConnection instance
func NewFSConnection(cfg config.FSConfig) *FSConnection {
	ctx, cancel := context.WithCancel(context.Background())
	return &FSConnection{
		config:      cfg,
		eventChan:   make(chan *FSEvent, 100),
		reconnectCh: make(chan struct{}, 1),
		connErr:     make(chan error, 1),
		status: ConnectionStatus{
			InstanceName: cfg.Name,
			Connected:    false,
		},
		ctx:    ctx,
		cancel: cancel,
	}
}

// Connect establishes connection to FreeSWITCH and subscribes to events
func (fc *FSConnection) Connect() error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	// Create FSConn connection
	addr := fmt.Sprintf("%s:%d", fc.config.Host, fc.config.Port)

	// Event handlers map: event name -> list of handler functions
	eventHandlers := make(map[string][]func(string, int))

	// Build event list - only essential events
	events := []string{
		"CHANNEL_CREATE",    // Need for initial state
		"CHANNEL_DESTROY",   // Need for exporting aggregated metrics
		"RECV_RTCP_MESSAGE", // RTCP metrics (inbound)
		"SEND_RTCP_MESSAGE", // RTCP metrics (outbound)
	}

	for _, eventName := range events {
		eventHandlers[eventName] = []func(string, int){fc.eventHandler}
	}

	// Event filters: subscribe to specific events
	eventFilters := make(map[string][]string)
	for _, eventName := range events {
		eventFilters[eventName] = []string{}
	}

	conn, err := fsock.NewFSConn(
		addr,
		fc.config.Password,
		0,              // connIdx
		30*time.Second, // replyTimeout - increased to 30s to avoid timeout on busy systems
		fc.connErr,
		&simpleLogger{},
		eventFilters,
		eventHandlers,
		false, // bgapi
	)
	if err != nil {
		fc.status.LastError = fmt.Errorf("failed to connect to %s: %w", addr, err)
		fc.status.Connected = false
		return fc.status.LastError
	}

	fc.conn = conn

	// Note: Event subscription is handled automatically by fsock through eventFilters
	// No need to manually subscribe to events

	fc.status.Connected = true
	fc.status.LastError = nil
	fc.status.LastEventAt = time.Now()

	logger.InfoWithFields(map[string]interface{}{
		"fs_instance": fc.config.Name,
		"address":     addr,
	}, "Successfully connected to FreeSWITCH instance")

	// Update connection metrics
	metrics.GetMetrics().SetFSConnectionStatus(fc.config.Name, true)

	return nil
}

// eventHandler handles incoming events from FreeSWITCH
func (fc *FSConnection) eventHandler(eventBody string, connIdx int) {
	fc.mu.Lock()
	fc.status.LastEventAt = time.Now()
	fc.mu.Unlock()

	// Parse event body into headers map
	headers := fsock.FSEventStrToMap(eventBody, []string{})

	event := &FSEvent{
		Headers: headers,
		Body:    eventBody,
	}

	// Forward event to event channel for processing
	select {
	case fc.eventChan <- event:
		// Event sent successfully
	case <-fc.ctx.Done():
		// Connection is closing
		return
	default:
		// Channel is full, log warning
		logger.WarnWithFields(map[string]interface{}{
			"fs_instance": fc.config.Name,
		}, "Event channel full, dropping event")
	}
}

// GetEventChannel returns the channel for receiving events
func (fc *FSConnection) GetEventChannel() <-chan *FSEvent {
	return fc.eventChan
}

// GetStatus returns the current connection status
func (fc *FSConnection) GetStatus() ConnectionStatus {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.status
}

// GetReconnectChannel returns the channel for reconnection signals
func (fc *FSConnection) GetReconnectChannel() <-chan struct{} {
	return fc.reconnectCh
}

// StartKeepalive starts the keepalive mechanism that sends periodic status checks
func (fc *FSConnection) StartKeepalive() {
	go fc.keepaliveLoop()
}

// keepaliveLoop sends keepalive messages every 30 seconds
func (fc *FSConnection) keepaliveLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Check if we've received events recently (within last 60 seconds)
			fc.mu.RLock()
			lastEventAt := fc.status.LastEventAt
			fc.mu.RUnlock()

			// If we received events recently, skip keepalive
			if time.Since(lastEventAt) < 10*time.Second {
				logger.DebugWithFields(map[string]interface{}{
					"fs_instance": fc.config.Name,
				}, "Skipping keepalive (recent activity)")
				continue
			}

			if err := fc.sendKeepalive(); err != nil {
				logger.WarnWithFields(map[string]interface{}{
					"fs_instance": fc.config.Name,
					"error":       err.Error(),
				}, "Keepalive failed")
				// Connection might be dead, trigger reconnection
				select {
				case fc.reconnectCh <- struct{}{}:
				default:
					// Reconnect already signaled
				}
			}
		case <-fc.ctx.Done():
			// Connection is closing
			return
		}
	}
}

// sendKeepalive sends a status command to keep the connection alive
func (fc *FSConnection) sendKeepalive() error {
	fc.mu.RLock()
	conn := fc.conn
	fc.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("connection not established")
	}

	// Send "api status" command (FreeSWITCH API command via ESL)
	_, err := conn.Send("show status")
	if err != nil {
		fc.mu.Lock()
		fc.status.LastError = fmt.Errorf("keepalive failed: %w", err)
		fc.status.Connected = false
		fc.mu.Unlock()

		// Update connection metrics
		metrics.GetMetrics().SetFSConnectionStatus(fc.config.Name, false)

		return err
	}

	// Update LastEventAt timestamp on successful keepalive
	fc.mu.Lock()
	fc.status.LastEventAt = time.Now()
	fc.mu.Unlock()

	logger.DebugWithFields(map[string]interface{}{
		"fs_instance": fc.config.Name,
	}, "Keepalive sent successfully")

	return nil
}

// StartReconnectionLoop starts the reconnection goroutine with exponential backoff
func (fc *FSConnection) StartReconnectionLoop() {
	go fc.reconnectionLoop()
}

// reconnectionLoop handles reconnection with exponential backoff
func (fc *FSConnection) reconnectionLoop() {
	backoffDurations := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		60 * time.Second,
	}
	backoffIndex := 0

	for {
		select {
		case <-fc.reconnectCh:
			// Reconnection triggered
			logger.InfoWithFields(map[string]interface{}{
				"fs_instance": fc.config.Name,
			}, "Reconnection triggered")

			// Close existing connection if any
			fc.mu.Lock()
			if fc.conn != nil {
				fc.conn.Disconnect()
				fc.conn = nil
			}
			fc.status.Connected = false
			fc.mu.Unlock()

			// Update connection metrics
			metrics.GetMetrics().SetFSConnectionStatus(fc.config.Name, false)

			// Attempt reconnection with exponential backoff
			reconnected := false
			for !reconnected {
				backoffDuration := backoffDurations[backoffIndex]
				logger.InfoWithFields(map[string]interface{}{
					"fs_instance": fc.config.Name,
					"backoff":     backoffDuration.String(),
					"attempt":     backoffIndex + 1,
				}, "Attempting to reconnect")

				// Wait for backoff duration
				select {
				case <-time.After(backoffDuration):
					// Try to reconnect
					if err := fc.Connect(); err != nil {
						logger.WarnWithFields(map[string]interface{}{
							"fs_instance": fc.config.Name,
							"attempt":     backoffIndex + 1,
							"error":       err.Error(),
						}, "Reconnection attempt failed")

						// Increase backoff index, but cap at max
						if backoffIndex < len(backoffDurations)-1 {
							backoffIndex++
						}
						continue
					}

					// Reconnection successful
					logger.InfoWithFields(map[string]interface{}{
						"fs_instance": fc.config.Name,
					}, "Successfully reconnected")
					backoffIndex = 0 // Reset backoff on successful connection

					// Restart keepalive
					// fc.StartKeepalive()

					// Break out of reconnection loop
					reconnected = true

				case <-fc.ctx.Done():
					// Connection manager is shutting down
					return
				}
			}

		case <-fc.ctx.Done():
			// Connection manager is shutting down
			return
		}
	}
}

// Close closes the connection and cleans up resources
func (fc *FSConnection) Close() error {
	fc.cancel()

	fc.mu.Lock()
	defer fc.mu.Unlock()

	if fc.conn != nil {
		fc.conn.Disconnect()
		fc.conn = nil
	}

	close(fc.eventChan)
	fc.status.Connected = false

	logger.InfoWithFields(map[string]interface{}{
		"fs_instance": fc.config.Name,
	}, "Closed connection to FreeSWITCH instance")

	return nil
}

// EventForwarder is an interface for forwarding events to a processor
type EventForwarder interface {
	ProcessEvent(ctx context.Context, event interface{}, instanceName string) error
}

// DefaultConnectionManager implements ConnectionManager interface
type DefaultConnectionManager struct {
	connections    []*FSConnection
	eventForwarder EventForwarder
	mu             sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
}

// NewConnectionManager creates a new ConnectionManager instance
func NewConnectionManager(configs []config.FSConfig) ConnectionManager {
	ctx, cancel := context.WithCancel(context.Background())

	connections := make([]*FSConnection, 0, len(configs))
	for _, cfg := range configs {
		conn := NewFSConnection(cfg)
		connections = append(connections, conn)
	}

	return &DefaultConnectionManager{
		connections:    connections,
		eventForwarder: nil, // Set via SetEventForwarder
		ctx:            ctx,
		cancel:         cancel,
	}
}

// SetEventForwarder sets the event forwarder for processing events
func (cm *DefaultConnectionManager) SetEventForwarder(forwarder EventForwarder) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.eventForwarder = forwarder
}

// Start initiates connections to all configured FreeSWITCH instances
func (cm *DefaultConnectionManager) Start(ctx context.Context) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	var wg sync.WaitGroup
	errChan := make(chan error, len(cm.connections))

	// Connect to all instances concurrently
	for _, conn := range cm.connections {
		wg.Add(1)
		go func(c *FSConnection) {
			defer wg.Done()

			// Attempt initial connection
			if err := c.Connect(); err != nil {
				logger.WarnWithFields(map[string]interface{}{
					"fs_instance": c.config.Name,
					"error":       err.Error(),
				}, "Initial connection failed")
				errChan <- fmt.Errorf("failed to connect to %s: %w", c.config.Name, err)
				// Don't return - we'll start reconnection loop anyway
			} else {
				logger.InfoWithFields(map[string]interface{}{
					"fs_instance": c.config.Name,
				}, "Initial connection successful")
			}

			// Start keepalive mechanism
			// c.StartKeepalive()

			// Start reconnection loop
			c.StartReconnectionLoop()

			// Start event forwarding for this connection
			cm.startEventForwarding(c)
		}(conn)
	}

	// Wait for all connection attempts to complete
	wg.Wait()
	close(errChan)

	// Collect any errors
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	// If all connections failed, return error
	if len(errors) == len(cm.connections) {
		return fmt.Errorf("all FreeSWITCH connections failed: %v", errors)
	}

	// Log warnings for partial failures
	if len(errors) > 0 {
		logger.WarnWithFields(map[string]interface{}{
			"failed_count": len(errors),
			"total_count":  len(cm.connections),
		}, "Some connections failed initially, will retry")
	}

	logger.InfoWithFields(map[string]interface{}{
		"instance_count": len(cm.connections),
	}, "ConnectionManager started")
	return nil
}

// startEventForwarding starts a goroutine to forward events from a connection to the event processor
func (cm *DefaultConnectionManager) startEventForwarding(conn *FSConnection) {
	go func() {
		instanceName := conn.config.Name
		eventChan := conn.GetEventChannel()

		logger.DebugWithFields(map[string]interface{}{
			"fs_instance": instanceName,
		}, "Started event forwarding")

		for {
			select {
			case event, ok := <-eventChan:
				if !ok {
					// Channel closed, connection is shutting down
					logger.InfoWithFields(map[string]interface{}{
						"fs_instance": instanceName,
					}, "Event channel closed")
					return
				}

				// Forward event to processor if configured
				cm.mu.RLock()
				forwarder := cm.eventForwarder
				cm.mu.RUnlock()

				if forwarder != nil {
					if err := forwarder.ProcessEvent(cm.ctx, event, instanceName); err != nil {
						logger.WarnWithFields(map[string]interface{}{
							"fs_instance": instanceName,
							"error":       err.Error(),
						}, "Failed to process event")
					}
				} else {
					logger.WarnWithFields(map[string]interface{}{
						"fs_instance": instanceName,
					}, "No event forwarder configured, dropping event")
				}

			case <-cm.ctx.Done():
				// Connection manager is shutting down
				logger.InfoWithFields(map[string]interface{}{
					"fs_instance": instanceName,
				}, "Stopping event forwarding")
				return
			}
		}
	}()
}

// Stop gracefully closes all connections
func (cm *DefaultConnectionManager) Stop() error {
	cm.cancel()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	var wg sync.WaitGroup
	for _, conn := range cm.connections {
		wg.Add(1)
		go func(c *FSConnection) {
			defer wg.Done()
			if err := c.Close(); err != nil {
				logger.WarnWithFields(map[string]interface{}{
					"fs_instance": c.config.Name,
					"error":       err.Error(),
				}, "Failed to close connection")
			}
		}(conn)
	}

	wg.Wait()
	logger.Info("ConnectionManager stopped, all connections closed")
	return nil
}

// GetStatus returns connection status for all instances
func (cm *DefaultConnectionManager) GetStatus() map[string]ConnectionStatus {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	status := make(map[string]ConnectionStatus)
	for _, conn := range cm.connections {
		connStatus := conn.GetStatus()
		status[connStatus.InstanceName] = connStatus
	}

	return status
}

// GetConnections returns all FSConnection instances (for event forwarding)
func (cm *DefaultConnectionManager) GetConnections() []*FSConnection {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.connections
}

// simpleLogger implements the logger interface required by fsock
type simpleLogger struct{}

func (l *simpleLogger) Alert(m string) error {
	logger.Error("FSock ALERT: %s", m)
	return nil
}

func (l *simpleLogger) Close() error {
	return nil
}

func (l *simpleLogger) Crit(m string) error {
	logger.Error("FSock CRIT: %s", m)
	return nil
}

func (l *simpleLogger) Debug(m string) error {
	logger.Debug("FSock: %s", m)
	return nil
}

func (l *simpleLogger) Emerg(m string) error {
	logger.Error("FSock EMERG: %s", m)
	return nil
}

func (l *simpleLogger) Err(m string) error {
	logger.Error("FSock: %s", m)
	return nil
}

func (l *simpleLogger) Info(m string) error {
	logger.Debug("FSock: %s", m)
	return nil
}

func (l *simpleLogger) Notice(m string) error {
	logger.Info("FSock: %s", m)
	return nil
}

func (l *simpleLogger) Warning(m string) error {
	logger.Warn("FSock: %s", m)
	return nil
}
