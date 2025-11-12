package calculator

import (
	"context"
	"time"
)

// RTCPCalculator calculates real-time RTCP metrics
type RTCPCalculator interface {
	// CalculateMetrics processes RTCP event and returns metrics
	CalculateMetrics(ctx context.Context, event interface{}, direction string, instanceName string) (*RTCPMetrics, error)
}

// RTCPMetrics represents RTCP metrics for a channel
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
