package processor

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/luongdev/fsagent/pkg/calculator"
	"github.com/luongdev/fsagent/pkg/connection"
	"github.com/luongdev/fsagent/pkg/logger"
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
	// TODO: Add QoS calculator when implemented
	// qosCalculator  calculator.QoSCalculator
}

// NewEventProcessor creates a new event processor
func NewEventProcessor(store store.StateStore, rtcpCalculator calculator.RTCPCalculator) EventProcessor {
	return &eventProcessor{
		store:          store,
		rtcpCalculator: rtcpCalculator,
	}
}

// Start begins event processing
func (ep *eventProcessor) Start(ctx context.Context) error {
	logger.Info("Event processor started")
	return nil
}

// Stop gracefully stops event processing
func (ep *eventProcessor) Stop() error {
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

	logger.Debug("Processing event: %s from instance: %s", eventName, instanceName)

	// Route to appropriate handler based on event type
	switch eventName {
	case "CHANNEL_CREATE":
		return ep.handleChannelCreate(ctx, fsEvent, instanceName)
	case "CHANNEL_PROGRESS_MEDIA":
		return ep.handleChannelProgressMedia(ctx, fsEvent, instanceName)
	case "CHANNEL_BRIDGE":
		return ep.handleChannelBridge(ctx, fsEvent, instanceName)
	case "CHANNEL_ANSWER":
		return ep.handleChannelAnswer(ctx, fsEvent, instanceName)
	case "CHANNEL_DESTROY":
		return ep.handleChannelDestroy(ctx, fsEvent, instanceName)
	case "RECV_RTCP_MESSAGE":
		return ep.handleRTCPMessage(ctx, fsEvent, instanceName, "inbound")
	case "SEND_RTCP_MESSAGE":
		return ep.handleRTCPMessage(ctx, fsEvent, instanceName, "outbound")
	default:
		// Unknown event type - log and skip
		logger.Debug("Skipping unknown event type: %s", eventName)
		return nil
	}
}

// extractCorrelationID extracts correlation ID with fallback priority
func (ep *eventProcessor) extractCorrelationID(event *connection.FSEvent) string {
	// Priority 1: Other-Leg-Unique-ID (B-leg)
	if correlationID := event.GetHeader("Other-Leg-Unique-ID"); correlationID != "" {
		logger.Debug("🔗 Correlation ID from Other-Leg-Unique-ID: %s", correlationID)
		return correlationID
	}

	// Priority 2: Unique-ID (A-leg)
	if correlationID := event.GetHeader("Unique-ID"); correlationID != "" {
		logger.Debug("🔗 Correlation ID from Unique-ID: %s", correlationID)
		return correlationID
	}

	// Priority 3: variable_sip_call_id
	if correlationID := event.GetHeader("variable_sip_call_id"); correlationID != "" {
		logger.Debug("🔗 Correlation ID from variable_sip_call_id: %s", correlationID)
		return correlationID
	}

	// Priority 4: variable_global_call_id
	if correlationID := event.GetHeader("variable_global_call_id"); correlationID != "" {
		logger.Debug("🔗 Correlation ID from variable_global_call_id: %s", correlationID)
		return correlationID
	}

	logger.Debug("⚠️  No correlation ID found in event headers")
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

	if correlationID == "" {
		logger.Debug("⚠️  Created channel state: channel_id=%s, correlation_id=<EMPTY>, domain=%s", channelID, domainName)
	} else {
		logger.Debug("✅ Created channel state: channel_id=%s, correlation_id=%s, domain=%s", channelID, correlationID, domainName)
	}
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

	logger.Debug("📡 Updated media info: channel_id=%s, correlation_id=%s, local=%s:%d, remote=%s:%d",
		channelID, state.CorrelationID, state.LocalMediaIP, state.LocalMediaPort, state.RemoteMediaIP, state.RemoteMediaPort)
	return nil
}

// handleChannelDestroy handles CHANNEL_DESTROY events
func (ep *eventProcessor) handleChannelDestroy(ctx context.Context, event *connection.FSEvent, instanceName string) error {
	channelID := event.GetHeader("Unique-ID")
	if channelID == "" {
		return fmt.Errorf("CHANNEL_DESTROY event missing Unique-ID")
	}

	// Get channel state to log correlation_id before deletion
	state, err := ep.store.Get(ctx, channelID)
	correlationID := ""
	if err == nil && state != nil {
		correlationID = state.CorrelationID
	}

	// TODO: Trigger QoS calculation when QoS calculator is implemented
	// For now, just log and delete the state
	logger.Info("🔚 Channel destroyed: channel_id=%s, correlation_id=%s, instance=%s", channelID, correlationID, instanceName)

	// Delete channel state
	if err := ep.store.Delete(ctx, channelID); err != nil {
		logger.Warn("Failed to delete channel state: %v", err)
	}

	return nil
}

// handleRTCPMessage handles RECV_RTCP_MESSAGE and SEND_RTCP_MESSAGE events
func (ep *eventProcessor) handleRTCPMessage(ctx context.Context, event *connection.FSEvent, instanceName string, direction string) error {
	channelID := event.GetHeader("Unique-ID")
	if channelID == "" {
		return fmt.Errorf("RTCP event missing Unique-ID")
	}

	// Calculate RTCP metrics if calculator is available
	if ep.rtcpCalculator != nil {
		metrics, err := ep.rtcpCalculator.CalculateMetrics(ctx, event, direction, instanceName)
		if err != nil {
			logger.Error("Error calculating RTCP metrics: %v", err)
			return err
		}

		// TODO: Export metrics to OTel when exporter is implemented
		logger.Info("📊 RTCP metrics: channel_id=%s, correlation_id=%s, domain=%s, direction=%s, jitter=%.2fms, packets_lost=%d",
			metrics.ChannelID, metrics.CorrelationID, metrics.DomainName, metrics.Direction, metrics.Jitter, metrics.PacketsLost)
	} else {
		// Get channel state to log correlation_id
		state, err := ep.store.Get(ctx, channelID)
		correlationID := ""
		if err == nil && state != nil {
			correlationID = state.CorrelationID
		}

		logger.Debug("📊 RTCP message: channel_id=%s, correlation_id=%s, direction=%s, instance=%s",
			channelID, correlationID, direction, instanceName)
	}

	return nil
}
