package connection

import (
	"context"
	"time"
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
