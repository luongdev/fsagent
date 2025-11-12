package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// StateStore defines the interface for storing channel state
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
	ChannelID     string    `json:"channel_id"`     // Unique-ID
	CorrelationID string    `json:"correlation_id"` // SIP Call-ID
	DomainName    string    `json:"domain_name"`    // SIP domain for filtering
	InstanceName  string    `json:"instance_name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`

	// Media Information
	LocalMediaIP    string `json:"local_media_ip"`
	LocalMediaPort  uint16 `json:"local_media_port"`
	RemoteMediaIP   string `json:"remote_media_ip"`
	RemoteMediaPort uint16 `json:"remote_media_port"`

	// RTCP State - Receive
	RecvSSRC        uint32 `json:"recv_ssrc"`
	RecvPackets     int64  `json:"recv_packets"`      // Cumulative
	RecvOctets      int64  `json:"recv_octets"`       // Cumulative
	RecvPacketsLost int32  `json:"recv_packets_lost"` // Cumulative

	// RTCP State - Send
	SendSSRC        uint32 `json:"send_ssrc"`
	SendPackets     int64  `json:"send_packets"`      // Cumulative
	SendOctets      int64  `json:"send_octets"`       // Cumulative
	SendPacketsLost int32  `json:"send_packets_lost"` // Cumulative
}

// MemoryStore implements StateStore using in-memory storage
type MemoryStore struct {
	data      map[string]*stateEntry
	mu        sync.RWMutex
	stopChan  chan struct{}
	cleanupWg sync.WaitGroup
}

type stateEntry struct {
	state     *ChannelState
	expiresAt time.Time
}

// NewMemoryStore creates a new in-memory state store
func NewMemoryStore() *MemoryStore {
	store := &MemoryStore{
		data:     make(map[string]*stateEntry),
		stopChan: make(chan struct{}),
	}
	store.startCleanup()
	return store
}

// Set stores channel state with TTL
func (m *MemoryStore) Set(ctx context.Context, channelID string, state *ChannelState, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data[channelID] = &stateEntry{
		state:     state,
		expiresAt: time.Now().Add(ttl),
	}
	return nil
}

// Get retrieves channel state
func (m *MemoryStore) Get(ctx context.Context, channelID string) (*ChannelState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.data[channelID]
	if !exists {
		return nil, fmt.Errorf("channel state not found: %s", channelID)
	}

	// Check if expired
	if time.Now().After(entry.expiresAt) {
		return nil, fmt.Errorf("channel state expired: %s", channelID)
	}

	return entry.state, nil
}

// Delete removes channel state
func (m *MemoryStore) Delete(ctx context.Context, channelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, channelID)
	return nil
}

// Close closes the store and stops cleanup goroutine
func (m *MemoryStore) Close() error {
	close(m.stopChan)
	m.cleanupWg.Wait()
	return nil
}

// startCleanup starts the TTL cleanup goroutine
func (m *MemoryStore) startCleanup() {
	m.cleanupWg.Add(1)
	go func() {
		defer m.cleanupWg.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				m.cleanup()
			case <-m.stopChan:
				return
			}
		}
	}()
}

// cleanup removes expired entries
func (m *MemoryStore) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for channelID, entry := range m.data {
		if now.After(entry.expiresAt) {
			delete(m.data, channelID)
		}
	}
}

// RedisStore implements StateStore using Redis
type RedisStore struct {
	client *redis.Client
	prefix string
}

// NewRedisStore creates a new Redis state store
func NewRedisStore(host string, port int, password string, db int) (*RedisStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", host, port),
		Password: password,
		DB:       db,
	})

	// Test connection with retry logic
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &RedisStore{
		client: client,
		prefix: "fsagent:channel:",
	}, nil
}

// Set stores channel state with TTL
func (r *RedisStore) Set(ctx context.Context, channelID string, state *ChannelState, ttl time.Duration) error {
	key := r.prefix + channelID

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal channel state: %w", err)
	}

	if err := r.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("failed to set channel state in Redis: %w", err)
	}

	return nil
}

// Get retrieves channel state
func (r *RedisStore) Get(ctx context.Context, channelID string) (*ChannelState, error) {
	key := r.prefix + channelID

	data, err := r.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("channel state not found: %s", channelID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get channel state from Redis: %w", err)
	}

	var state ChannelState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal channel state: %w", err)
	}

	return &state, nil
}

// Delete removes channel state
func (r *RedisStore) Delete(ctx context.Context, channelID string) error {
	key := r.prefix + channelID

	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete channel state from Redis: %w", err)
	}

	return nil
}

// Close closes the Redis connection
func (r *RedisStore) Close() error {
	return r.client.Close()
}

// NewStateStore creates a StateStore based on the provided configuration
func NewStateStore(storageType string, redisHost string, redisPort int, redisPassword string, redisDB int) (StateStore, error) {
	switch storageType {
	case "redis":
		if redisHost == "" {
			return nil, fmt.Errorf("redis host is required when storage type is redis")
		}
		return NewRedisStore(redisHost, redisPort, redisPassword, redisDB)
	case "memory", "":
		return NewMemoryStore(), nil
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", storageType)
	}
}
