package calculator

import (
	"context"
	"time"
)

// QoSCalculator calculates comprehensive QoS metrics when call terminates
type QoSCalculator interface {
	// CalculateMetrics processes CHANNEL_DESTROY event and returns QoS metrics
	CalculateMetrics(ctx context.Context, event interface{}, instanceName string) (*QoSMetrics, error)
}

// QoSMetrics represents QoS metrics for a terminated call
type QoSMetrics struct {
	Timestamp     time.Time
	InstanceName  string
	ChannelID     string // Unique-ID for per-leg monitoring
	CorrelationID string // SIP Call-ID for per-call aggregation
	DomainName    string // SIP domain for filtering by tenant/domain

	// Quality Metrics
	MOSScore  float64
	AvgJitter float64 // (min + max) / 2
	MinJitter float64
	MaxJitter float64
	Delta     float64 // mean interval

	// Traffic Metrics
	TotalPackets int64 // in + out
	PacketLoss   int64 // in + out skip packets
	TotalBytes   int64 // in + out

	// Codec Information
	CodecName string
	CodecPT   int
	PTime     int
	ClockRate int

	// Endpoints
	SrcIP   string
	SrcPort uint16
	DstIP   string
	DstPort uint16

	// Timing
	ReportTimestamp int64
}
