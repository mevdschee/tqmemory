package tqmemory

import "time"

// Default configuration values matching memcached
const (
	DefaultThreadCount     = 4                // memcached -t default
	DefaultMaxMemory       = 64 * 1024 * 1024 // memcached -m default (64MB)
	DefaultMaxConnections  = 1024             // memcached -c default
	DefaultPort            = 11211            // memcached -p default
	DefaultMaxKeySize      = 250              // memcached max key size
	DefaultMaxValueSize    = 1 * 1024 * 1024  // memcached default item size (1MB)
	DefaultChannelCapacity = 1000             // internal buffer size
)

// Config holds the configuration for TQMemory
type Config struct {
	DefaultTTL      time.Duration // Default TTL for keys (0 = no expiry)
	MaxKeySize      int           // Maximum key size (250 bytes)
	MaxValueSize    int           // Maximum value size (1MB)
	MaxMemory       int64         // Maximum memory in bytes (0 = unlimited)
	ChannelCapacity int           // Request channel capacity per worker
}

// DefaultConfig returns memcached-compatible defaults
func DefaultConfig() Config {
	return Config{
		DefaultTTL:      0,
		MaxKeySize:      DefaultMaxKeySize,
		MaxValueSize:    DefaultMaxValueSize,
		MaxMemory:       DefaultMaxMemory,
		ChannelCapacity: DefaultChannelCapacity,
	}
}
