package calculator

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/luongdev/fsagent/pkg/connection"
	"github.com/luongdev/fsagent/pkg/logger"
	"github.com/luongdev/fsagent/pkg/store"
)

// RTCPCalculator processes RTCP events and aggregates metrics
type RTCPCalculator interface {
	// AggregateMetrics processes RTCP event and updates running averages in state
	// Returns (shouldExport bool, metrics *RTCPMetrics, error) - shouldExport is true if 30s elapsed since last export
	AggregateMetrics(ctx context.Context, event *connection.FSEvent, direction string, instanceName string) (bool, *RTCPMetrics, error)

	// GetAggregatedMetrics retrieves final aggregated metrics for a channel (called at end of call)
	GetAggregatedMetrics(ctx context.Context, channelID string, instanceName string) (*RTCPMetrics, error)
}

// RTCPMetrics represents calculated RTCP metrics
type RTCPMetrics struct {
	Timestamp     time.Time
	InstanceName  string
	ChannelID     string // Unique-ID for per-leg monitoring
	CorrelationID string // SIP Call-ID for per-call aggregation
	DomainName    string // SIP domain for filtering by tenant/domain
	Direction     string // "aggregated" for end-of-call metrics

	// Aggregated RTCP metrics
	Jitter      float64 // Average jitter in milliseconds
	PacketsLost int32   // Total packets lost

	// Traffic stats
	PacketsSent int64 // Total packets sent
	OctetsSent  int64 // Total octets sent

	// Media endpoints
	SrcIP   string
	SrcPort uint16
	DstIP   string
	DstPort uint16

	// Legacy fields (kept for compatibility)
	SSRC             uint32
	FractionLost     uint8
	HighestSeqNo     uint32
	LSR              uint32
	DLSR             uint32
	NTPTimestampSec  uint32
	NTPTimestampUsec uint32
	RTPTimestamp     uint32
}

// rtcpCalculator implements RTCPCalculator interface
type rtcpCalculator struct {
	store store.StateStore
}

// NewRTCPCalculator creates a new RTCP calculator
func NewRTCPCalculator(store store.StateStore) RTCPCalculator {
	return &rtcpCalculator{
		store: store,
	}
}

// AggregateMetrics processes RTCP event and updates running averages in state
// Returns (shouldExport bool, metrics *RTCPMetrics, error) - shouldExport is true if 30s elapsed
func (rc *rtcpCalculator) AggregateMetrics(ctx context.Context, event *connection.FSEvent, direction string, instanceName string) (bool, *RTCPMetrics, error) {
	channelID := event.GetHeader("Unique-ID")
	if channelID == "" {
		return false, nil, fmt.Errorf("RTCP event missing Unique-ID")
	}

	// Get channel state
	state, err := rc.store.Get(ctx, channelID)
	if err != nil {
		logger.WarnWithFields(map[string]interface{}{
			"channel_id":  channelID,
			"fs_instance": instanceName,
			"direction":   direction,
			"error":       err.Error(),
		}, "Channel state not found for RTCP aggregation")
		return false, nil, fmt.Errorf("failed to get channel state: %w", err)
	}

	// Extract jitter from event
	var jitter float64
	if direction == "inbound" {
		if jitterStr := event.GetHeader("Source0-Jitter"); jitterStr != "" {
			if j, err := strconv.ParseFloat(jitterStr, 64); err == nil {
				jitter = j
			}
		}
	} else {
		if jitterStr := event.GetHeader("Source-Jitter"); jitterStr != "" {
			if j, err := strconv.ParseFloat(jitterStr, 64); err == nil {
				jitter = j
			}
		}
	}

	// Extract packet loss
	var packetsLost int32
	if direction == "inbound" {
		if lostStr := event.GetHeader("Source0-Lost"); lostStr != "" {
			if lost, err := strconv.ParseInt(lostStr, 10, 32); err == nil {
				packetsLost = int32(lost)
			}
		}
	} else {
		if lostStr := event.GetHeader("Source-Lost"); lostStr != "" {
			if lost, err := strconv.ParseInt(lostStr, 10, 32); err == nil {
				packetsLost = int32(lost)
			}
		}
	}

	// Extract packet count and octets from RTCP headers
	var packetCount int64
	var octetCount int64

	if direction == "inbound" {
		// For inbound RTCP, read from Source0-* headers
		if packetStr := event.GetHeader("Source0-Packet-Count"); packetStr != "" {
			if packets, err := strconv.ParseInt(packetStr, 10, 64); err == nil {
				packetCount = packets
			}
		}
		if octetStr := event.GetHeader("Source0-Octet-Count"); octetStr != "" {
			if octets, err := strconv.ParseInt(octetStr, 10, 64); err == nil {
				octetCount = octets
			}
		}
	} else {
		// For outbound RTCP, read from Sender-* headers
		if packetStr := event.GetHeader("Sender-Packet-Count"); packetStr != "" {
			if packets, err := strconv.ParseInt(packetStr, 10, 64); err == nil {
				packetCount = packets
			}
		}
		if octetStr := event.GetHeader("Sender-Octet-Count"); octetStr != "" {
			if octets, err := strconv.ParseInt(octetStr, 10, 64); err == nil {
				octetCount = octets
			}
		}
	}

	// Update aggregation
	state.RTCPSampleCount++

	// Update running average jitter
	if state.RTCPSampleCount == 1 {
		state.AvgJitter = jitter
		state.MinJitter = jitter
		state.MaxJitter = jitter
	} else {
		// Running average: new_avg = old_avg + (new_value - old_avg) / count
		state.AvgJitter = state.AvgJitter + (jitter-state.AvgJitter)/float64(state.RTCPSampleCount)

		if jitter > state.MaxJitter {
			state.MaxJitter = jitter
		}
		if jitter < state.MinJitter {
			state.MinJitter = jitter
		}
	}

	// Update packet loss (Source-Lost is cumulative, not incremental)
	state.TotalPacketLoss = int64(packetsLost)

	// Update packet and octet counts based on direction
	if direction == "inbound" {
		// Update receive counters (cumulative values from RTCP)
		state.RecvPackets = packetCount
		state.RecvOctets = octetCount
		state.RecvPacketsLost = packetsLost
	} else {
		// Update send counters (cumulative values from RTCP)
		state.SendPackets = packetCount
		state.SendOctets = octetCount
		state.SendPacketsLost = packetsLost
	}

	state.UpdatedAt = time.Now()

	// Store updated state
	ttl := 24 * time.Hour
	if err := rc.store.Set(ctx, channelID, state, ttl); err != nil {
		return false, nil, fmt.Errorf("failed to update state with aggregated metrics: %w", err)
	}

	logger.DebugWithFields(map[string]interface{}{
		"channel_id":        channelID,
		"correlation_id":    state.CorrelationID,
		"direction":         direction,
		"sample_count":      state.RTCPSampleCount,
		"current_jitter":    jitter,
		"avg_jitter":        state.AvgJitter,
		"total_packet_loss": state.TotalPacketLoss,
		"packet_count":      packetCount,
		"octet_count":       octetCount,
		"recv_packets":      state.RecvPackets,
		"send_packets":      state.SendPackets,
		"recv_octets":       state.RecvOctets,
		"send_octets":       state.SendOctets,
	}, "RTCP metrics aggregated")

	// Check if we should export (every 30 seconds)
	now := time.Now()
	shouldExport := false
	var metrics *RTCPMetrics

	if state.LastExportedAt.IsZero() || now.Sub(state.LastExportedAt) >= 30*time.Second {
		// Time to export periodic metrics
		shouldExport = true
		metrics = &RTCPMetrics{
			Timestamp:     now,
			InstanceName:  instanceName,
			ChannelID:     channelID,
			CorrelationID: state.CorrelationID,
			DomainName:    state.DomainName,
			Direction:     "aggregated",

			// Aggregated values
			Jitter:      state.AvgJitter,
			PacketsLost: int32(state.TotalPacketLoss),

			// Traffic stats
			PacketsSent: state.RecvPackets + state.SendPackets,
			OctetsSent:  state.RecvOctets + state.SendOctets,

			// Endpoints
			SrcIP:   state.LocalMediaIP,
			SrcPort: state.LocalMediaPort,
			DstIP:   state.RemoteMediaIP,
			DstPort: state.RemoteMediaPort,
		}

		// Update last exported time
		state.LastExportedAt = now
		state.UpdatedAt = now

		// Store updated state
		ttl := 24 * time.Hour
		if err := rc.store.Set(ctx, channelID, state, ttl); err != nil {
			return false, nil, fmt.Errorf("failed to update last exported time: %w", err)
		}

		logger.InfoWithFields(map[string]interface{}{
			"channel_id":        channelID,
			"correlation_id":    state.CorrelationID,
			"sample_count":      state.RTCPSampleCount,
			"avg_jitter":        state.AvgJitter,
			"total_packet_loss": state.TotalPacketLoss,
		}, "Periodic RTCP metrics ready for export")
	}

	return shouldExport, metrics, nil
}

// GetAggregatedMetrics retrieves final aggregated metrics for a channel
func (rc *rtcpCalculator) GetAggregatedMetrics(ctx context.Context, channelID string, instanceName string) (*RTCPMetrics, error) {
	// Get channel state
	state, err := rc.store.Get(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("failed to get channel state: %w", err)
	}

	if state.RTCPSampleCount == 0 {
		return nil, fmt.Errorf("no RTCP samples collected for channel")
	}

	// Build aggregated metrics from state
	metrics := &RTCPMetrics{
		Timestamp:     time.Now(),
		InstanceName:  instanceName,
		ChannelID:     channelID,
		CorrelationID: state.CorrelationID,
		DomainName:    state.DomainName,
		Direction:     "aggregated", // This is aggregated from both directions

		// Aggregated values
		Jitter:      state.AvgJitter,              // Average jitter
		PacketsLost: int32(state.TotalPacketLoss), // Total packet loss

		// Additional stats (can be used for monitoring)
		PacketsSent: state.RecvPackets + state.SendPackets,
		OctetsSent:  state.RecvOctets + state.SendOctets,

		// Endpoints
		SrcIP:   state.LocalMediaIP,
		SrcPort: state.LocalMediaPort,
		DstIP:   state.RemoteMediaIP,
		DstPort: state.RemoteMediaPort,
	}

	logger.InfoWithFields(map[string]interface{}{
		"channel_id":        channelID,
		"correlation_id":    state.CorrelationID,
		"sample_count":      state.RTCPSampleCount,
		"avg_jitter":        state.AvgJitter,
		"min_jitter":        state.MinJitter,
		"max_jitter":        state.MaxJitter,
		"total_packet_loss": state.TotalPacketLoss,
	}, "Retrieved aggregated RTCP metrics")

	return metrics, nil
}
