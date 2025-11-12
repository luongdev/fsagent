package store

import (
	"context"
	"time"
)

// StateStore persists channel state with support for in-memory and Redis backends
type StateStore interface {
	// Set stores channel state with TTL
	Set(ctx context.Context, channelID string, state *ChannelState, ttl time.Duration) error

	// Get retrieves channel state
	Get(ctx context.Context, channelID string) (*ChannelState, error)

	// Delete removes channel state
	Delete(ctx context.Context, channelID string) error

	// Close closes the store connection
	Close() error
}

// ChannelState represents the state of a FreeSWITCH channel
type ChannelState struct {
	ChannelID     string // Unique-ID
	CorrelationID string // SIP Call-ID
	DomainName    string // SIP domain for filtering
	InstanceName  string
	CreatedAt     time.Time
	UpdatedAt     time.Time

	// Media Information
	LocalMediaIP    string
	LocalMediaPort  uint16
	RemoteMediaIP   string
	RemoteMediaPort uint16

	// RTCP State - Receive
	RecvSSRC        uint32
	RecvPackets     int64 // Cumulative
	RecvOctets      int64 // Cumulative
	RecvPacketsLost int32 // Cumulative

	// RTCP State - Send
	SendSSRC        uint32
	SendPackets     int64 // Cumulative
	SendOctets      int64 // Cumulative
	SendPacketsLost int32 // Cumulative
}
