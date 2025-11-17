package processor

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/luongdev/fsagent/pkg/calculator"
	"github.com/luongdev/fsagent/pkg/connection"
	"github.com/luongdev/fsagent/pkg/exporter"
	"github.com/luongdev/fsagent/pkg/logger"
	"github.com/luongdev/fsagent/pkg/metrics"
	"github.com/luongdev/fsagent/pkg/store"
)

// EventProcessor handles incoming FreeSWITCH events and routes them to appropriate handlers
type EventProcessor interface {
	// ProcessEvent handles incoming FSEvent and routes to appropriate handler
	ProcessEvent(ctx context.Context, event interface{}, instanceName string) error

	// Start begins event processing
	Start(ctx context.Context) error

	// Stop gracefully stops event processing
	Stop() error
}

// eventProcessor implements EventProcessor interface
type eventProcessor struct {
	store          store.StateStore
	rtcpCalculator calculator.RTCPCalculator
	qosCalculator  calculator.QoSCalculator
	exporter       exporter.MetricsExporter
	rtcpEnabled    bool
	qosEnabled     bool
}

// NewEventProcessor creates a new event processor
func NewEventProcessor(store store.StateStore, rtcpCalculator calculator.RTCPCalculator, qosCalculator calculator.QoSCalculator, metricsExporter exporter.MetricsExporter, rtcpEnabled bool, qosEnabled bool) EventProcessor {
	return &eventProcessor{
		store:          store,
		rtcpCalculator: rtcpCalculator,
		qosCalculator:  qosCalculator,
		exporter:       metricsExporter,
		rtcpEnabled:    rtcpEnabled,
		qosEnabled:     qosEnabled,
	}
}

// Start begins event processing
func (ep *eventProcessor) Start(ctx context.Context) error {
	// Start metrics exporter
	if ep.exporter != nil {
		if err := ep.exporter.Start(ctx); err != nil {
			return fmt.Errorf("failed to start metrics exporter: %w", err)
		}
	}
	logger.Info("Event processor started")
	return nil
}

// Stop gracefully stops event processing
func (ep *eventProcessor) Stop() error {
	// Stop metrics exporter
	if ep.exporter != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ep.exporter.Stop(ctx); err != nil {
			logger.Error("Failed to stop metrics exporter: %v", err)
		}
	}
	logger.Info("Event processor stopped")
	return nil
}

// ProcessEvent routes events to appropriate handlers based on event name
func (ep *eventProcessor) ProcessEvent(ctx context.Context, event interface{}, instanceName string) error {
	if event == nil {
		return fmt.Errorf("received nil event")
	}

	// Type assert to FSEvent from connection package
	fsEvent, ok := event.(*connection.FSEvent)
	if !ok {
		return fmt.Errorf("invalid event type: expected *connection.FSEvent, got %T", event)
	}

	eventName := fsEvent.GetHeader("Event-Name")
	if eventName == "" {
		return fmt.Errorf("event missing Event-Name header")
	}

	// Increment events received counter
	m := metrics.GetMetrics()
	m.IncrementEventsReceived(instanceName, eventName)

	logger.DebugWithFields(map[string]interface{}{
		"fs_instance": instanceName,
		"event_type":  eventName,
	}, "Processing event")

	// Route to appropriate handler based on event type
	var err error
	switch eventName {
	case "CHANNEL_CREATE":
		err = ep.handleChannelCreate(ctx, fsEvent, instanceName)
	case "CHANNEL_PROGRESS_MEDIA":
		err = ep.handleChannelProgressMedia(ctx, fsEvent, instanceName)
	case "CHANNEL_BRIDGE":
		err = ep.handleChannelBridge(ctx, fsEvent, instanceName)
	case "CHANNEL_ANSWER":
		err = ep.handleChannelAnswer(ctx, fsEvent, instanceName)
	case "CHANNEL_DESTROY":
		err = ep.handleChannelDestroy(ctx, fsEvent, instanceName)
	case "RECV_RTCP_MESSAGE":
		if ep.rtcpEnabled {
			err = ep.handleRTCPMessage(ctx, fsEvent, instanceName, "inbound")
		}
	case "SEND_RTCP_MESSAGE":
		if ep.rtcpEnabled {
			err = ep.handleRTCPMessage(ctx, fsEvent, instanceName, "outbound")
		}
	default:
		// Unknown event type - log and skip
		logger.DebugWithFields(map[string]interface{}{
			"fs_instance": instanceName,
			"event_type":  eventName,
		}, "Skipping unknown event type")
		return nil
	}

	// Increment events processed counter if successful
	if err == nil {
		m.IncrementEventsProcessed(instanceName, eventName)
	}

	return err
}

// extractCorrelationID extracts correlation ID with fallback priority
func (ep *eventProcessor) extractCorrelationID(event *connection.FSEvent) string {
	// Priority 1: Other-Leg-Unique-ID (B-leg)
	if correlationID := event.GetHeader("Other-Leg-Unique-ID"); correlationID != "" {
		logger.DebugWithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"source":         "Other-Leg-Unique-ID",
		}, "Extracted correlation ID")
		return correlationID
	}

	// Priority 2: Unique-ID (A-leg)
	if correlationID := event.GetHeader("Unique-ID"); correlationID != "" {
		logger.DebugWithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"source":         "Unique-ID",
		}, "Extracted correlation ID")
		return correlationID
	}

	// Priority 3: variable_sip_call_id
	if correlationID := event.GetHeader("variable_sip_call_id"); correlationID != "" {
		logger.DebugWithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"source":         "variable_sip_call_id",
		}, "Extracted correlation ID")
		return correlationID
	}

	// Priority 4: variable_global_call_id
	if correlationID := event.GetHeader("variable_global_call_id"); correlationID != "" {
		logger.DebugWithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"source":         "variable_global_call_id",
		}, "Extracted correlation ID")
		return correlationID
	}

	logger.Debug("No correlation ID found in event headers")
	return ""
}

// extractDomainName extracts domain name with fallback priority
func (ep *eventProcessor) extractDomainName(event *connection.FSEvent) string {
	// Priority 1: variable_domain_name
	if domainName := event.GetHeader("variable_domain_name"); domainName != "" {
		return domainName
	}

	// Priority 2: variable_sip_from_host
	if domainName := event.GetHeader("variable_sip_from_host"); domainName != "" {
		return domainName
	}

	// Priority 3: variable_sip_to_host
	if domainName := event.GetHeader("variable_sip_to_host"); domainName != "" {
		return domainName
	}

	// Return empty string if none available
	return ""
}

// handleChannelCreate handles CHANNEL_CREATE events
func (ep *eventProcessor) handleChannelCreate(ctx context.Context, event *connection.FSEvent, instanceName string) error {
	channelID := event.GetHeader("Unique-ID")
	if channelID == "" {
		return fmt.Errorf("CHANNEL_CREATE event missing Unique-ID")
	}

	correlationID := ep.extractCorrelationID(event)
	domainName := ep.extractDomainName(event)

	// Create initial channel state
	state := &store.ChannelState{
		ChannelID:     channelID,
		CorrelationID: correlationID,
		DomainName:    domainName,
		InstanceName:  instanceName,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	// Store with 24 hour TTL
	ttl := 24 * time.Hour
	if err := ep.store.Set(ctx, channelID, state, ttl); err != nil {
		return fmt.Errorf("failed to store channel state: %w", err)
	}

	logger.DebugWithFields(map[string]interface{}{
		"channel_id":     channelID,
		"correlation_id": correlationID,
		"domain_name":    domainName,
		"fs_instance":    instanceName,
	}, "Created channel state")
	return nil
}

// handleChannelProgressMedia handles CHANNEL_PROGRESS_MEDIA events
func (ep *eventProcessor) handleChannelProgressMedia(ctx context.Context, event *connection.FSEvent, instanceName string) error {
	return ep.updateMediaInfo(ctx, event, instanceName)
}

// handleChannelBridge handles CHANNEL_BRIDGE events
func (ep *eventProcessor) handleChannelBridge(ctx context.Context, event *connection.FSEvent, instanceName string) error {
	return ep.updateMediaInfo(ctx, event, instanceName)
}

// handleChannelAnswer handles CHANNEL_ANSWER events
func (ep *eventProcessor) handleChannelAnswer(ctx context.Context, event *connection.FSEvent, instanceName string) error {
	return ep.updateMediaInfo(ctx, event, instanceName)
}

// updateMediaInfo updates channel state with media IP addresses and ports
func (ep *eventProcessor) updateMediaInfo(ctx context.Context, event *connection.FSEvent, instanceName string) error {
	channelID := event.GetHeader("Unique-ID")
	if channelID == "" {
		return fmt.Errorf("event missing Unique-ID")
	}

	// Get existing state
	state, err := ep.store.Get(ctx, channelID)
	if err != nil {
		// State might not exist yet, create new one
		correlationID := ep.extractCorrelationID(event)
		domainName := ep.extractDomainName(event)
		state = &store.ChannelState{
			ChannelID:     channelID,
			CorrelationID: correlationID,
			DomainName:    domainName,
			InstanceName:  instanceName,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
	}

	// Update domain name if present in event (may change during call lifecycle)
	if domainName := ep.extractDomainName(event); domainName != "" {
		state.DomainName = domainName
	}

	// Update media information
	if localIP := event.GetHeader("variable_local_media_ip"); localIP != "" {
		state.LocalMediaIP = localIP
	}
	if localPort := event.GetHeader("variable_local_media_port"); localPort != "" {
		if port, err := strconv.ParseUint(localPort, 10, 16); err == nil {
			state.LocalMediaPort = uint16(port)
		}
	}
	if remoteIP := event.GetHeader("variable_remote_media_ip"); remoteIP != "" {
		state.RemoteMediaIP = remoteIP
	}
	if remotePort := event.GetHeader("variable_remote_media_port"); remotePort != "" {
		if port, err := strconv.ParseUint(remotePort, 10, 16); err == nil {
			state.RemoteMediaPort = uint16(port)
		}
	}

	state.UpdatedAt = time.Now()

	// Store updated state with 24 hour TTL
	ttl := 24 * time.Hour
	if err := ep.store.Set(ctx, channelID, state, ttl); err != nil {
		return fmt.Errorf("failed to update channel state: %w", err)
	}

	logger.DebugWithFields(map[string]interface{}{
		"channel_id":        channelID,
		"correlation_id":    state.CorrelationID,
		"domain_name":       state.DomainName,
		"fs_instance":       instanceName,
		"local_media_ip":    state.LocalMediaIP,
		"local_media_port":  state.LocalMediaPort,
		"remote_media_ip":   state.RemoteMediaIP,
		"remote_media_port": state.RemoteMediaPort,
	}, "Updated media info")
	return nil
}

// handleChannelDestroy handles CHANNEL_DESTROY events
func (ep *eventProcessor) handleChannelDestroy(ctx context.Context, event *connection.FSEvent, instanceName string) error {
	channelID := event.GetHeader("Unique-ID")
	if channelID == "" {
		return fmt.Errorf("CHANNEL_DESTROY event missing Unique-ID")
	}

	// Calculate QoS metrics if enabled, calculator is available and codec rate is present
	if ep.qosEnabled && ep.qosCalculator != nil && event.GetHeader("variable_rtp_use_codec_rate") != "" {
		qosMetrics, err := ep.qosCalculator.CalculateMetrics(ctx, event, instanceName)
		if err != nil {
			logger.ErrorWithFields(map[string]interface{}{
				"channel_id":  channelID,
				"fs_instance": instanceName,
				"error":       err.Error(),
			}, "Error calculating QoS metrics")
		} else {
			// Increment QoS messages generated counter
			m := metrics.GetMetrics()
			m.IncrementQoSMessagesGenerated(instanceName)

			// Export QoS metrics
			if ep.exporter != nil {
				if err := ep.exporter.ExportQoS(ctx, qosMetrics); err != nil {
					logger.ErrorWithFields(map[string]interface{}{
						"channel_id":     channelID,
						"correlation_id": qosMetrics.CorrelationID,
						"fs_instance":    instanceName,
						"error":          err.Error(),
					}, "Error exporting QoS metrics")
				}
			}
			logger.InfoWithFields(map[string]interface{}{
				"channel_id":     qosMetrics.ChannelID,
				"correlation_id": qosMetrics.CorrelationID,
				"domain_name":    qosMetrics.DomainName,
				"fs_instance":    instanceName,
				"mos_score":      qosMetrics.MOSScore,
				"avg_jitter_ms":  qosMetrics.AvgJitter,
				"packet_loss":    qosMetrics.PacketLoss,
			}, "QoS metrics calculated")
		}
	} else {
		// Get channel state to log correlation_id before deletion
		state, err := ep.store.Get(ctx, channelID)
		correlationID := ""
		if err == nil && state != nil {
			correlationID = state.CorrelationID
		}
		logger.InfoWithFields(map[string]interface{}{
			"channel_id":     channelID,
			"correlation_id": correlationID,
			"fs_instance":    instanceName,
		}, "Channel destroyed")
	}

	return nil
}

// handleRTCPMessage handles RECV_RTCP_MESSAGE and SEND_RTCP_MESSAGE events
func (ep *eventProcessor) handleRTCPMessage(ctx context.Context, event *connection.FSEvent, instanceName string, direction string) error {
	channelID := event.GetHeader("Unique-ID")
	if channelID == "" {
		return fmt.Errorf("RTCP event missing Unique-ID")
	}

	// Update domain name in state if present in RTCP event
	if domainName := ep.extractDomainName(event); domainName != "" {
		state, err := ep.store.Get(ctx, channelID)
		if err == nil && state != nil && state.DomainName != domainName {
			state.DomainName = domainName
			state.UpdatedAt = time.Now()
			ttl := 24 * time.Hour
			if err := ep.store.Set(ctx, channelID, state, ttl); err != nil {
				logger.ErrorWithFields(map[string]interface{}{
					"channel_id":  channelID,
					"domain_name": domainName,
					"error":       err.Error(),
				}, "Failed to update domain name in state")
			}
		}
	}

	// Calculate RTCP metrics if calculator is available
	if ep.rtcpCalculator != nil {
		rtcpMetrics, err := ep.rtcpCalculator.CalculateMetrics(ctx, event, direction, instanceName)
		if err != nil {
			logger.ErrorWithFields(map[string]interface{}{
				"channel_id":  channelID,
				"fs_instance": instanceName,
				"direction":   direction,
				"error":       err.Error(),
			}, "Error calculating RTCP metrics")
			return err
		}

		// Increment RTCP messages processed counter
		m := metrics.GetMetrics()
		m.IncrementRTCPMessagesProcessed(instanceName, direction)

		// Export metrics to OTel
		if ep.exporter != nil {
			if err := ep.exporter.ExportRTCP(ctx, rtcpMetrics); err != nil {
				logger.ErrorWithFields(map[string]interface{}{
					"channel_id":     channelID,
					"correlation_id": rtcpMetrics.CorrelationID,
					"fs_instance":    instanceName,
					"direction":      direction,
					"error":          err.Error(),
				}, "Error exporting RTCP metrics")
			}
		}

		logger.DebugWithFields(map[string]interface{}{
			"channel_id":     rtcpMetrics.ChannelID,
			"correlation_id": rtcpMetrics.CorrelationID,
			"domain_name":    rtcpMetrics.DomainName,
			"fs_instance":    instanceName,
			"direction":      rtcpMetrics.Direction,
			"jitter_ms":      rtcpMetrics.Jitter,
			"packets_lost":   rtcpMetrics.PacketsLost,
		}, "RTCP metrics calculated")
	} else {
		// Get channel state to log correlation_id
		state, err := ep.store.Get(ctx, channelID)
		correlationID := ""
		if err == nil && state != nil {
			correlationID = state.CorrelationID
		}

		logger.DebugWithFields(map[string]interface{}{
			"channel_id":     channelID,
			"correlation_id": correlationID,
			"fs_instance":    instanceName,
			"direction":      direction,
		}, "RTCP message received")
	}

	return nil
}
