package exporter

import (
	"context"

	"github.com/luongdev/fsagent/pkg/calculator"
)

// MetricsExporter exports metrics to OpenTelemetry Collector
type MetricsExporter interface {
	// ExportRTCP sends RTCP metrics to OTel
	ExportRTCP(ctx context.Context, metrics *calculator.RTCPMetrics) error

	// ExportQoS sends QoS metrics to OTel
	ExportQoS(ctx context.Context, metrics *calculator.QoSMetrics) error

	// Start begins metric export with batching
	Start(ctx context.Context) error

	// Stop flushes and stops exporter
	Stop(ctx context.Context) error
}
