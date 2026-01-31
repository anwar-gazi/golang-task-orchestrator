package orchestrator

import (
	"time"
)

// Config holds the orchestrator configuration
type Config struct {
	// Worker pool
	WorkerPoolSize int           // Max concurrent tasks (default: 10)
	ClaimInterval  time.Duration // How often to poll for tasks (default: 1s)

	// Distributed mode
	Distributed       bool          // Enable multi-instance support (default: false)
	HeartbeatInterval time.Duration // Worker heartbeat interval (default: 10s)
	WorkerTimeout     time.Duration // Consider worker dead after (default: 30s)

	// Shutdown
	ShutdownTimeout time.Duration // Grace period for shutdown (default: 30s)

	// Leader duties
	LeaderTerm      time.Duration // Leadership duration (default: 30s)
	CleanupInterval time.Duration // How often leader cleans up (default: 1m)
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		WorkerPoolSize:    10,
		ClaimInterval:     1 * time.Second,
		Distributed:       false,
		HeartbeatInterval: 10 * time.Second,
		WorkerTimeout:     30 * time.Second,
		ShutdownTimeout:   30 * time.Second,
		LeaderTerm:        30 * time.Second,
		CleanupInterval:   1 * time.Minute,
	}
}

// RetryConfig holds retry configuration for a task type
type RetryConfig struct {
	MaxRetries int           // default: 3
	BaseDelay  time.Duration // default: 1 * time.Second
	MaxDelay   time.Duration // default: 1 * time.Hour
}

// DefaultRetryConfig returns a retry configuration with sensible defaults
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   1 * time.Hour,
	}
}

// EnqueueOptions holds options for enqueuing a task
type EnqueueOptions struct {
	Priority       int
	IdempotencyKey string
	MaxRetries     int
}

// EnqueueOption is a functional option for enqueuing tasks
type EnqueueOption func(*EnqueueOptions)

// WithPriority sets the task priority
func WithPriority(priority int) EnqueueOption {
	return func(o *EnqueueOptions) { o.Priority = priority }
}

// WithIdempotencyKey sets an idempotency key
func WithIdempotencyKey(key string) EnqueueOption {
	return func(o *EnqueueOptions) { o.IdempotencyKey = key }
}

// WithMaxRetries sets the maximum number of retries
func WithMaxRetries(retries int) EnqueueOption {
	return func(o *EnqueueOptions) { o.MaxRetries = retries }
}
