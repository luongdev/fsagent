package exporter

import (
	"context"
	"fmt"
	"time"

	"github.com/luongdev/fsagent/pkg/calculator"
	"github.com/luongdev/fsagent/pkg/config"
	"github.com/luongdev/fsagent/pkg/logger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	serviceName    = "fsagent"
	serviceVersion = "0.1.0"
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

// otelExporter implements MetricsExporter interface
type otelExporter struct {
	config           *config.OTelConfig
	meterProvider    *sdkmetric.MeterProvider
	meter            metric.Meter
	batchChan        chan interface{}
	batchInterval    time.Duration
	stopChan         chan struct{}
	skipZeroValues   bool
	includeTimestamp bool

	// RTCP Metrics (real-time)
	rtcpJitter       metric.Float64Gauge
	rtcpPacketsLost  metric.Int64Gauge
	rtcpFractionLost metric.Int64Gauge
	rtcpPacketsSent  metric.Int64Gauge
	rtcpOctetsSent   metric.Int64Gauge

	// QoS Metrics (end of call summary)
	qosMOS          metric.Float64Gauge
	qosAvgJitter    metric.Float64Gauge
	qosMinJitter    metric.Float64Gauge
	qosMaxJitter    metric.Float64Gauge
	qosDelta        metric.Float64Gauge
	qosTotalPackets metric.Int64Gauge
	qosPacketLoss   metric.Int64Gauge
	qosTotalBytes   metric.Int64Gauge
}

// NewMetricsExporter creates a new OpenTelemetry metrics exporter
func NewMetricsExporter(cfg *config.OTelConfig) (MetricsExporter, error) {
	exporter := &otelExporter{
		config:           cfg,
		batchChan:        make(chan interface{}, 1000),
		batchInterval:    10 * time.Second,
		stopChan:         make(chan struct{}),
		skipZeroValues:   cfg.SkipZeroValues,
		includeTimestamp: cfg.IncludeTimestamp,
	}

	// Initialize OTel SDK
	if err := exporter.initOTel(); err != nil {
		return nil, fmt.Errorf("failed to initialize OTel: %w", err)
	}

	// Create metric instruments
	if err := exporter.createMetricInstruments(); err != nil {
		return nil, fmt.Errorf("failed to create metric instruments: %w", err)
	}

	return exporter, nil
}

// initOTel initializes OpenTelemetry SDK with OTLP gRPC exporter
func (e *otelExporter) initOTel() error {
	ctx := context.Background()

	// Create resource with service attributes
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create resource: %w", err)
	}

	// Create OTLP gRPC exporter options
	exporterOpts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(e.config.Endpoint),
	}

	// Add insecure option if configured
	if e.config.Insecure {
		exporterOpts = append(exporterOpts, otlpmetricgrpc.WithInsecure())
	}

	// Add headers if configured
	if len(e.config.Headers) > 0 {
		exporterOpts = append(exporterOpts, otlpmetricgrpc.WithHeaders(e.config.Headers))
	}

	// Create OTLP gRPC exporter
	metricExporter, err := otlpmetricgrpc.New(ctx, exporterOpts...)
	if err != nil {
		return fmt.Errorf("failed to create OTLP exporter: %w", err)
	}

	// Create MeterProvider with periodic reader
	e.meterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
			sdkmetric.WithInterval(e.batchInterval),
		)),
	)

	// Set global meter provider
	otel.SetMeterProvider(e.meterProvider)

	// Create meter for fsagent
	e.meter = e.meterProvider.Meter(serviceName)

	logger.InfoWithFields(map[string]interface{}{
		"endpoint": e.config.Endpoint,
		"insecure": e.config.Insecure,
	}, "OpenTelemetry exporter initialized")

	return nil
}

// createMetricInstruments creates all metric instruments
func (e *otelExporter) createMetricInstruments() error {
	var err error

	// RTCP Metrics
	e.rtcpJitter, err = e.meter.Float64Gauge("freeswitch.rtcp.jitter",
		metric.WithDescription("RTCP jitter in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("failed to create rtcp jitter gauge: %w", err)
	}

	e.rtcpPacketsLost, err = e.meter.Int64Gauge("freeswitch.rtcp.packets_lost",
		metric.WithDescription("Incremental RTCP packets lost"),
	)
	if err != nil {
		return fmt.Errorf("failed to create rtcp packets lost gauge: %w", err)
	}

	e.rtcpFractionLost, err = e.meter.Int64Gauge("freeswitch.rtcp.fraction_lost",
		metric.WithDescription("RTCP fraction lost"),
	)
	if err != nil {
		return fmt.Errorf("failed to create rtcp fraction lost gauge: %w", err)
	}

	e.rtcpPacketsSent, err = e.meter.Int64Gauge("freeswitch.rtcp.packets_sent",
		metric.WithDescription("Incremental RTCP packets sent"),
	)
	if err != nil {
		return fmt.Errorf("failed to create rtcp packets sent gauge: %w", err)
	}

	e.rtcpOctetsSent, err = e.meter.Int64Gauge("freeswitch.rtcp.octets_sent",
		metric.WithDescription("Incremental RTCP octets sent"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return fmt.Errorf("failed to create rtcp octets sent gauge: %w", err)
	}

	// QoS Metrics
	e.qosMOS, err = e.meter.Float64Gauge("freeswitch.qos.mos_score",
		metric.WithDescription("Mean Opinion Score"),
	)
	if err != nil {
		return fmt.Errorf("failed to create qos mos gauge: %w", err)
	}

	e.qosAvgJitter, err = e.meter.Float64Gauge("freeswitch.qos.jitter_avg",
		metric.WithDescription("Average jitter (min+max)/2"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("failed to create qos avg jitter gauge: %w", err)
	}

	e.qosMinJitter, err = e.meter.Float64Gauge("freeswitch.qos.jitter_min",
		metric.WithDescription("Minimum jitter variance during call"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("failed to create qos min jitter gauge: %w", err)
	}

	e.qosMaxJitter, err = e.meter.Float64Gauge("freeswitch.qos.jitter_max",
		metric.WithDescription("Maximum jitter variance during call"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("failed to create qos max jitter gauge: %w", err)
	}

	e.qosDelta, err = e.meter.Float64Gauge("freeswitch.qos.delta",
		metric.WithDescription("Mean interval between RTP packets (expected: ptime)"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("failed to create qos delta gauge: %w", err)
	}

	e.qosTotalPackets, err = e.meter.Int64Gauge("freeswitch.qos.total_packets",
		metric.WithDescription("Total packets (inbound + outbound)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create qos total packets gauge: %w", err)
	}

	e.qosPacketLoss, err = e.meter.Int64Gauge("freeswitch.qos.packet_loss",
		metric.WithDescription("Total packet loss"),
	)
	if err != nil {
		return fmt.Errorf("failed to create qos packet loss gauge: %w", err)
	}

	e.qosTotalBytes, err = e.meter.Int64Gauge("freeswitch.qos.total_bytes",
		metric.WithDescription("Total bytes (inbound + outbound)"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return fmt.Errorf("failed to create qos total bytes gauge: %w", err)
	}

	return nil
}

// Start begins metric export with batching
func (e *otelExporter) Start(ctx context.Context) error {
	go e.batchProcessor(ctx)
	return nil
}

// batchProcessor processes metrics in batches
func (e *otelExporter) batchProcessor(ctx context.Context) {
	ticker := time.NewTicker(e.batchInterval)
	defer ticker.Stop()

	batch := make([]interface{}, 0, 100)

	for {
		select {
		case <-ctx.Done():
			// Flush remaining metrics
			e.processBatch(ctx, batch)
			return
		case <-e.stopChan:
			// Flush remaining metrics
			e.processBatch(ctx, batch)
			return
		case metric := <-e.batchChan:
			batch = append(batch, metric)
			// Process batch if it reaches threshold
			if len(batch) >= 100 {
				e.processBatch(ctx, batch)
				batch = make([]interface{}, 0, 100)
			}
		case <-ticker.C:
			// Process batch on interval
			if len(batch) > 0 {
				e.processBatch(ctx, batch)
				batch = make([]interface{}, 0, 100)
			}
		}
	}
}

// processBatch processes a batch of metrics
func (e *otelExporter) processBatch(ctx context.Context, batch []interface{}) {
	if len(batch) == 0 {
		return
	}

	logger.DebugWithFields(map[string]interface{}{
		"batch_size": len(batch),
	}, "Processing metrics batch")

	for _, m := range batch {
		switch metric := m.(type) {
		case *calculator.RTCPMetrics:
			if err := e.recordRTCPMetrics(ctx, metric); err != nil {
				// Log error but continue processing
				logger.ErrorWithFields(map[string]interface{}{
					"channel_id":     metric.ChannelID,
					"correlation_id": metric.CorrelationID,
					"fs_instance":    metric.InstanceName,
					"error":          err.Error(),
				}, "Error recording RTCP metrics")
			}
		case *calculator.QoSMetrics:
			if err := e.recordQoSMetrics(ctx, metric); err != nil {
				// Log error but continue processing
				logger.ErrorWithFields(map[string]interface{}{
					"channel_id":     metric.ChannelID,
					"correlation_id": metric.CorrelationID,
					"fs_instance":    metric.InstanceName,
					"error":          err.Error(),
				}, "Error recording QoS metrics")
			}
		}
	}
}

// ExportRTCP sends RTCP metrics to OTel
func (e *otelExporter) ExportRTCP(ctx context.Context, metrics *calculator.RTCPMetrics) error {
	select {
	case e.batchChan <- metrics:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Channel full, record immediately
		return e.recordRTCPMetrics(ctx, metrics)
	}
}

// recordRTCPMetrics records RTCP metrics with appropriate labels
func (e *otelExporter) recordRTCPMetrics(ctx context.Context, metrics *calculator.RTCPMetrics) error {
	// Create common attributes
	attrs := []attribute.KeyValue{
		attribute.String("fs_instance", metrics.InstanceName),
		attribute.String("channel_id", metrics.ChannelID),
		attribute.String("correlation_id", metrics.CorrelationID),
		attribute.String("domain_name", metrics.DomainName),
		attribute.String("direction", metrics.Direction),
	}

	// Add timestamp if configured
	if e.includeTimestamp {
		attrs = append(attrs, attribute.Int64("timestamp", time.Now().Unix()))
	}

	// Record jitter (skip if zero and skipZeroValues is enabled)
	if !e.skipZeroValues || metrics.Jitter != 0 {
		e.rtcpJitter.Record(ctx, metrics.Jitter, metric.WithAttributes(attrs...))
	}

	// Record packets lost (skip if zero and skipZeroValues is enabled)
	if !e.skipZeroValues || metrics.PacketsLost != 0 {
		e.rtcpPacketsLost.Record(ctx, int64(metrics.PacketsLost), metric.WithAttributes(attrs...))
	}

	// Record fraction lost (skip if zero and skipZeroValues is enabled)
	if !e.skipZeroValues || metrics.FractionLost != 0 {
		e.rtcpFractionLost.Record(ctx, int64(metrics.FractionLost), metric.WithAttributes(attrs...))
	}

	// Record packets sent (incremental) (skip if zero and skipZeroValues is enabled)
	if !e.skipZeroValues || metrics.PacketsSent != 0 {
		e.rtcpPacketsSent.Record(ctx, metrics.PacketsSent, metric.WithAttributes(attrs...))
	}

	// Record octets sent (incremental) (skip if zero and skipZeroValues is enabled)
	if !e.skipZeroValues || metrics.OctetsSent != 0 {
		e.rtcpOctetsSent.Record(ctx, metrics.OctetsSent, metric.WithAttributes(attrs...))
	}

	return nil
}

// ExportQoS sends QoS metrics to OTel
func (e *otelExporter) ExportQoS(ctx context.Context, metrics *calculator.QoSMetrics) error {
	select {
	case e.batchChan <- metrics:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Channel full, record immediately
		return e.recordQoSMetrics(ctx, metrics)
	}
}

// recordQoSMetrics records QoS metrics with appropriate labels
func (e *otelExporter) recordQoSMetrics(ctx context.Context, metrics *calculator.QoSMetrics) error {
	// Create common attributes
	commonAttrs := []attribute.KeyValue{
		attribute.String("fs_instance", metrics.InstanceName),
		attribute.String("channel_id", metrics.ChannelID),
		attribute.String("correlation_id", metrics.CorrelationID),
		attribute.String("domain_name", metrics.DomainName),
	}

	// Add timestamp if configured
	if e.includeTimestamp {
		commonAttrs = append(commonAttrs, attribute.Int64("timestamp", time.Now().Unix()))
	}

	// Record MOS score with codec and endpoint info (skip if zero and skipZeroValues is enabled)
	if !e.skipZeroValues || metrics.MOSScore != 0 {
		mosAttrs := append(commonAttrs,
			attribute.String("codec_name", metrics.CodecName),
			attribute.String("src_ip", metrics.SrcIP),
			attribute.String("dst_ip", metrics.DstIP),
		)
		e.qosMOS.Record(ctx, metrics.MOSScore, metric.WithAttributes(mosAttrs...))
	}

	// Record jitter metrics with codec info
	jitterAttrs := append(commonAttrs,
		attribute.String("codec_name", metrics.CodecName),
	)

	if !e.skipZeroValues || metrics.AvgJitter != 0 {
		e.qosAvgJitter.Record(ctx, metrics.AvgJitter, metric.WithAttributes(jitterAttrs...))
	}

	if !e.skipZeroValues || metrics.MinJitter != 0 {
		e.qosMinJitter.Record(ctx, metrics.MinJitter, metric.WithAttributes(jitterAttrs...))
	}

	if !e.skipZeroValues || metrics.MaxJitter != 0 {
		e.qosMaxJitter.Record(ctx, metrics.MaxJitter, metric.WithAttributes(jitterAttrs...))
	}

	if !e.skipZeroValues || metrics.Delta != 0 {
		e.qosDelta.Record(ctx, metrics.Delta, metric.WithAttributes(jitterAttrs...))
	}

	// Record traffic metrics (skip if zero and skipZeroValues is enabled)
	if !e.skipZeroValues || metrics.TotalPackets != 0 {
		e.qosTotalPackets.Record(ctx, metrics.TotalPackets, metric.WithAttributes(commonAttrs...))
	}

	if !e.skipZeroValues || metrics.PacketLoss != 0 {
		e.qosPacketLoss.Record(ctx, metrics.PacketLoss, metric.WithAttributes(commonAttrs...))
	}

	if !e.skipZeroValues || metrics.TotalBytes != 0 {
		e.qosTotalBytes.Record(ctx, metrics.TotalBytes, metric.WithAttributes(commonAttrs...))
	}

	return nil
}

// Stop flushes and stops exporter
func (e *otelExporter) Stop(ctx context.Context) error {
	logger.Info("Stopping metrics exporter, flushing remaining metrics")

	// Signal batch processor to stop
	close(e.stopChan)

	// Give some time for final batch processing
	time.Sleep(100 * time.Millisecond)

	// Shutdown meter provider (this will flush remaining metrics)
	if err := e.meterProvider.Shutdown(ctx); err != nil {
		logger.ErrorWithFields(map[string]interface{}{
			"error": err.Error(),
		}, "Failed to shutdown meter provider")
		return fmt.Errorf("failed to shutdown meter provider: %w", err)
	}

	logger.Info("Metrics exporter stopped successfully")
	return nil
}
