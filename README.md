# FSAgent - FreeSWITCH Metrics Collection Agent

FSAgent is a high-performance Golang application that connects to multiple FreeSWITCH instances via ESL (Event Socket Library), collects real-time RTCP and QoS metrics, and exports them to OpenTelemetry Collector.

## Project Structure

```
fsagent/
├── cmd/
│   └── fsagent/          # Main application entry point
│       └── main.go
├── internal/             # Private application code
├── pkg/                  # Public libraries and interfaces
│   ├── calculator/       # RTCP and QoS metric calculators
│   │   ├── rtcp.go
│   │   └── qos.go
│   ├── config/           # Configuration structures
│   │   └── config.go
│   ├── connection/       # FreeSWITCH ESL connection management
│   │   └── manager.go
│   ├── exporter/         # OpenTelemetry metrics exporter
│   │   └── metrics_exporter.go
│   ├── processor/        # Event processing and routing
│   │   └── event_processor.go
│   └── store/            # State storage (in-memory and Redis)
│       └── state_store.go
├── go.mod
└── README.md
```

## Core Interfaces

### Connection Manager
Manages ESL connections to multiple FreeSWITCH instances with automatic reconnection and keepalive.

### Event Processor
Routes incoming ESL events to appropriate calculators and manages event flow.

### RTCP Calculator
Calculates real-time RTCP metrics from RECV_RTCP_MESSAGE and SEND_RTCP_MESSAGE events.

### QoS Calculator
Calculates comprehensive QoS metrics when calls terminate.

### State Store
Persists channel state with support for in-memory and Redis backends.

### Metrics Exporter
Exports metrics to OpenTelemetry Collector using OTLP protocol.

## Dependencies

- `github.com/cgrates/fsock` - FreeSWITCH Event Socket Library
- `github.com/redis/go-redis/v9` - Redis client
- `go.opentelemetry.io/otel` - OpenTelemetry SDK
- `gopkg.in/yaml.v3` - YAML configuration parsing

## Getting Started

```bash
# Build the application
go build -o bin/fsagent ./cmd/fsagent

# Run the application
./bin/fsagent
```

## Configuration

Configuration can be provided via `config.yaml` file or environment variables with `FSAGENT_` prefix.

See the design document for detailed configuration options.

## Development Status

This project is currently under development. Core interfaces have been defined and implementation is in progress.
