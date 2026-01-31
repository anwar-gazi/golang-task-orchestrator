package tasks

import (
	"time"
)

// Task represents a task in the orchestration system
type Task struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	Payload        []byte     `json:"payload"`
	Status         string     `json:"status"`
	Priority       int        `json:"priority"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	WorkerID       string     `json:"worker_id,omitempty"`
	Attempts       int        `json:"attempts"`
	MaxRetries     int        `json:"max_retries"`
	LastError      string     `json:"last_error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	ClaimedAt      *time.Time `json:"claimed_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
}

// TaskStatus constants
const (
	StatusPending    = "pending"
	StatusClaimed    = "claimed"
	StatusRunning    = "running"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusDeadLetter = "dead_letter"
)
