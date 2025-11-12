package processor

import (
	"context"
)

// EventProcessor routes incoming ESL events to appropriate calculators
type EventProcessor interface {
	// ProcessEvent handles incoming FSEvent and routes to appropriate handler
	ProcessEvent(ctx context.Context, event interface{}, instanceName string) error

	// Start begins event processing
	Start(ctx context.Context) error

	// Stop gracefully stops event processing
	Stop() error
}

// EventHandler handles specific FreeSWITCH events
type EventHandler interface {
	HandleChannelCreate(event interface{}, instanceName string) error
	HandleChannelProgressMedia(event interface{}, instanceName string) error
	HandleChannelBridge(event interface{}, instanceName string) error
	HandleChannelAnswer(event interface{}, instanceName string) error
	HandleChannelDestroy(event interface{}, instanceName string) error
	HandleRTCPMessage(event interface{}, instanceName string, direction string) error
}
