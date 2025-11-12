package calculator

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/luongdev/fsagent/pkg/connection"
	"github.com/luongdev/fsagent/pkg/store"
)

// RTCPCalculator processes RTCP events and calculates metrics
type RTCPCalculator interface {
	// CalculateMetrics processes RTCP event and returns metrics
	CalculateMetrics(ctx context.Context, event *connection.FSEvent, direction string, instanceName string) (*RTCPMetrics, error)
}

// RTCPMetrics represents calculated RTCP metrics
type RTCPMetrics struct {
	Timestamp     time.Time
	InstanceName  string
	ChannelID     string // Unique-ID for per-leg monitoring
	CorrelationID string // SIP Call-ID for per-call aggregation
	DomainName    string // SIP domain for filtering by tenant/domain
	Direction     string // "inbound" or "outbound"

	// RTCP Report Block
	SSRC         uint32
	FractionLost uint8
	PacketsLost  int32 // Incremental
	HighestSeqNo uint32
	Jitter       float64 // in milliseconds
	LSR          uint32
	DLSR         uint32

	// Sender Information
	PacketsSent      int64 // Incremental
	OctetsSent       int64 // Incremental
	NTPTimestampSec  uint32
	NTPTimestampUsec uint32
	RTPTimestamp     uint32

	// Media endpoints
	SrcIP   string
	SrcPort uint16
	DstIP   string
	DstPort uint16
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

// CalculateMetrics processes RTCP event and returns metrics
func (rc *rtcpCalculator) CalculateMetrics(ctx context.Context, event *connection.FSEvent, direction string, instanceName string) (*RTCPMetrics, error) {
	channelID := event.GetHeader("Unique-ID")
	if channelID == "" {
		return nil, fmt.Errorf("RTCP event missing Unique-ID")
	}

	// Get channel state
	state, err := rc.store.Get(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("failed to get channel state: %w", err)
	}

	// Initialize metrics
	metrics := &RTCPMetrics{
		Timestamp:     time.Now(),
		InstanceName:  instanceName,
		ChannelID:     channelID,
		CorrelationID: state.CorrelationID,
		DomainName:    state.DomainName,
		Direction:     direction,
	}

	// Extract metrics based on direction
	if direction == "inbound" {
		// RECV_RTCP_MESSAGE: uses Source0-* headers
		if err := rc.extractRecvMetrics(event, metrics, state); err != nil {
			return nil, err
		}
		// Set endpoints: srcIp=local, dstIp=remote
		metrics.SrcIP = state.LocalMediaIP
		metrics.SrcPort = state.LocalMediaPort
		metrics.DstIP = state.RemoteMediaIP
		metrics.DstPort = state.RemoteMediaPort
	} else {
		// SEND_RTCP_MESSAGE: uses Source-* headers
		if err := rc.extractSendMetrics(event, metrics, state); err != nil {
			return nil, err
		}
		// Set endpoints: srcIp=remote, dstIp=local
		metrics.SrcIP = state.RemoteMediaIP
		metrics.SrcPort = state.RemoteMediaPort
		metrics.DstIP = state.LocalMediaIP
		metrics.DstPort = state.LocalMediaPort
	}

	// Update state with new cumulative values
	if err := rc.updateState(ctx, channelID, state, metrics, direction); err != nil {
		return nil, fmt.Errorf("failed to update state: %w", err)
	}

	return metrics, nil
}

// extractRecvMetrics extracts metrics from RECV_RTCP_MESSAGE event
func (rc *rtcpCalculator) extractRecvMetrics(event *connection.FSEvent, metrics *RTCPMetrics, state *store.ChannelState) error {
	// Extract SSRC
	if ssrcStr := event.GetHeader("Source0-SSRC"); ssrcStr != "" {
		if ssrc, err := strconv.ParseUint(ssrcStr, 10, 32); err == nil {
			metrics.SSRC = uint32(ssrc)
		}
	}

	// Extract jitter (convert to milliseconds if needed)
	if jitterStr := event.GetHeader("Source0-Jitter"); jitterStr != "" {
		if jitter, err := strconv.ParseFloat(jitterStr, 64); err == nil {
			metrics.Jitter = jitter
		}
	}

	// Extract cumulative packets lost
	var currentPacketsLost int32
	if lostStr := event.GetHeader("Source0-Lost"); lostStr != "" {
		if lost, err := strconv.ParseInt(lostStr, 10, 32); err == nil {
			currentPacketsLost = int32(lost)
		}
	}

	// Calculate incremental packet loss
	metrics.PacketsLost = currentPacketsLost - state.RecvPacketsLost

	// Extract fraction lost
	if fractionStr := event.GetHeader("Source0-Fraction"); fractionStr != "" {
		if fraction, err := strconv.ParseUint(fractionStr, 10, 8); err == nil {
			metrics.FractionLost = uint8(fraction)
		}
	}

	// Extract highest sequence number
	if seqStr := event.GetHeader("Source0-Highest-Sequence-Number-Received"); seqStr != "" {
		if seq, err := strconv.ParseUint(seqStr, 10, 32); err == nil {
			metrics.HighestSeqNo = uint32(seq)
		}
	}

	// Extract LSR (Last SR timestamp)
	if lsrStr := event.GetHeader("Source0-LSR"); lsrStr != "" {
		if lsr, err := strconv.ParseUint(lsrStr, 10, 32); err == nil {
			metrics.LSR = uint32(lsr)
		}
	}

	// Extract DLSR (Delay since last SR)
	if dlsrStr := event.GetHeader("Source0-DLSR"); dlsrStr != "" {
		if dlsr, err := strconv.ParseUint(dlsrStr, 10, 32); err == nil {
			metrics.DLSR = uint32(dlsr)
		}
	}

	// Extract sender information
	if err := rc.extractSenderInfo(event, metrics, state, "recv"); err != nil {
		return err
	}

	return nil
}

// extractSendMetrics extracts metrics from SEND_RTCP_MESSAGE event
func (rc *rtcpCalculator) extractSendMetrics(event *connection.FSEvent, metrics *RTCPMetrics, state *store.ChannelState) error {
	// Extract SSRC
	if ssrcStr := event.GetHeader("SSRC"); ssrcStr != "" {
		if ssrc, err := strconv.ParseUint(ssrcStr, 10, 32); err == nil {
			metrics.SSRC = uint32(ssrc)
		}
	}

	// Extract jitter (convert to milliseconds if needed)
	if jitterStr := event.GetHeader("Source-Jitter"); jitterStr != "" {
		if jitter, err := strconv.ParseFloat(jitterStr, 64); err == nil {
			metrics.Jitter = jitter
		}
	}

	// Extract cumulative packets lost
	var currentPacketsLost int32
	if lostStr := event.GetHeader("Source-Lost"); lostStr != "" {
		if lost, err := strconv.ParseInt(lostStr, 10, 32); err == nil {
			currentPacketsLost = int32(lost)
		}
	}

	// Calculate incremental packet loss
	metrics.PacketsLost = currentPacketsLost - state.SendPacketsLost

	// Extract fraction lost
	if fractionStr := event.GetHeader("Source-Fraction"); fractionStr != "" {
		if fraction, err := strconv.ParseUint(fractionStr, 10, 8); err == nil {
			metrics.FractionLost = uint8(fraction)
		}
	}

	// Extract highest sequence number
	if seqStr := event.GetHeader("Source-Highest-Sequence-Number-Received"); seqStr != "" {
		if seq, err := strconv.ParseUint(seqStr, 10, 32); err == nil {
			metrics.HighestSeqNo = uint32(seq)
		}
	}

	// Extract LSR (Last SR timestamp)
	if lsrStr := event.GetHeader("Source-LSR"); lsrStr != "" {
		if lsr, err := strconv.ParseUint(lsrStr, 10, 32); err == nil {
			metrics.LSR = uint32(lsr)
		}
	}

	// Extract DLSR (Delay since last SR)
	if dlsrStr := event.GetHeader("Source-DLSR"); dlsrStr != "" {
		if dlsr, err := strconv.ParseUint(dlsrStr, 10, 32); err == nil {
			metrics.DLSR = uint32(dlsr)
		}
	}

	// Extract sender information
	if err := rc.extractSenderInfo(event, metrics, state, "send"); err != nil {
		return err
	}

	return nil
}

// extractSenderInfo extracts sender information and calculates deltas
func (rc *rtcpCalculator) extractSenderInfo(event *connection.FSEvent, metrics *RTCPMetrics, state *store.ChannelState, direction string) error {
	// Extract sender packet count
	var currentPackets int64
	if packetsStr := event.GetHeader("Sender-Packet-Count"); packetsStr != "" {
		if packets, err := strconv.ParseInt(packetsStr, 10, 64); err == nil {
			currentPackets = packets
		}
	}

	// Calculate incremental packets sent
	if direction == "recv" {
		metrics.PacketsSent = currentPackets - state.RecvPackets
	} else {
		metrics.PacketsSent = currentPackets - state.SendPackets
	}

	// Extract octet count
	var currentOctets int64
	if octetsStr := event.GetHeader("Octect-Packet-Count"); octetsStr != "" {
		if octets, err := strconv.ParseInt(octetsStr, 10, 64); err == nil {
			currentOctets = octets
		}
	}

	// Calculate incremental octets sent
	if direction == "recv" {
		metrics.OctetsSent = currentOctets - state.RecvOctets
	} else {
		metrics.OctetsSent = currentOctets - state.SendOctets
	}

	// Extract NTP timestamps
	if ntpSecStr := event.GetHeader("NTP-Most-Significant-Word"); ntpSecStr != "" {
		if ntpSec, err := strconv.ParseUint(ntpSecStr, 10, 32); err == nil {
			metrics.NTPTimestampSec = uint32(ntpSec)
		}
	}

	if ntpUsecStr := event.GetHeader("NTP-Least-Significant-Word"); ntpUsecStr != "" {
		if ntpUsec, err := strconv.ParseUint(ntpUsecStr, 10, 32); err == nil {
			metrics.NTPTimestampUsec = uint32(ntpUsec)
		}
	}

	// Extract RTP timestamp
	if rtpStr := event.GetHeader("RTP-Timestamp"); rtpStr != "" {
		if rtp, err := strconv.ParseUint(rtpStr, 10, 32); err == nil {
			metrics.RTPTimestamp = uint32(rtp)
		}
	}

	return nil
}

// updateState updates the channel state with new cumulative values
func (rc *rtcpCalculator) updateState(ctx context.Context, channelID string, state *store.ChannelState, metrics *RTCPMetrics, direction string) error {
	// Update cumulative values based on direction
	if direction == "inbound" {
		// Update receive state
		state.RecvSSRC = metrics.SSRC
		state.RecvPacketsLost = state.RecvPacketsLost + metrics.PacketsLost
		state.RecvPackets = state.RecvPackets + metrics.PacketsSent
		state.RecvOctets = state.RecvOctets + metrics.OctetsSent
	} else {
		// Update send state
		state.SendSSRC = metrics.SSRC
		state.SendPacketsLost = state.SendPacketsLost + metrics.PacketsLost
		state.SendPackets = state.SendPackets + metrics.PacketsSent
		state.SendOctets = state.SendOctets + metrics.OctetsSent
	}

	state.UpdatedAt = time.Now()

	// Store updated state with 24 hour TTL
	ttl := 24 * time.Hour
	if err := rc.store.Set(ctx, channelID, state, ttl); err != nil {
		return err
	}

	return nil
}
