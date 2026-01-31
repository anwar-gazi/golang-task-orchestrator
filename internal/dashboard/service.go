package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yourusername/task-orchestrator/internal/orchestrator"
	"github.com/yourusername/task-orchestrator/pkg/tasks"
)

// Service handles data fetching for the dashboard
// Service handles data fetching for the dashboard
// Service handles data fetching for the dashboard
type Service struct {
	db          *sql.DB
	getHandlers func() []string
	enqueueTask func(ctx context.Context, taskType string, payload []byte) (string, error)
	cancelTask  func(ctx context.Context, taskID string) error
	getTaskLogs func(taskID string) (<-chan orchestrator.LogEntry, []orchestrator.LogEntry, func())
}

// NewService creates a new dashboard service
func NewService(db *sql.DB, getHandlers func() []string,
	enqueueTask func(ctx context.Context, taskType string, payload []byte) (string, error),
	cancelTask func(ctx context.Context, taskID string) error,
	getTaskLogs func(taskID string) (<-chan orchestrator.LogEntry, []orchestrator.LogEntry, func())) *Service {
	return &Service{
		db:          db,
		getHandlers: getHandlers,
		enqueueTask: enqueueTask,
		cancelTask:  cancelTask,
		getTaskLogs: getTaskLogs,
	}
}

// GetTaskLogs returns a log stream for a task
func (s *Service) GetTaskLogs(taskID string) (<-chan orchestrator.LogEntry, []orchestrator.LogEntry, func(), error) {
	if s.getTaskLogs == nil {
		return nil, nil, nil, fmt.Errorf("log streaming not configured")
	}
	ch, history, cleanup := s.getTaskLogs(taskID)
	return ch, history, cleanup, nil
}

// CancelTask cancels a task
func (s *Service) CancelTask(ctx context.Context, taskID string) error {
	if s.cancelTask == nil {
		return fmt.Errorf("cancel task function not configured")
	}
	return s.cancelTask(ctx, taskID)
}

// EnqueueTask enqueues a new task
func (s *Service) EnqueueTask(ctx context.Context, taskType string, payload []byte) (string, error) {
	if s.enqueueTask == nil {
		return "", fmt.Errorf("enqueue task function not configured")
	}
	return s.enqueueTask(ctx, taskType, payload)
}

// Stats holds high-level dashboard statistics
type Stats struct {
	PendingTasks       int
	RunningTasks       int
	CompletedTasks     int
	FailedTasks        int
	ActiveWorkers      int
	RegisteredHandlers []string
}

// GetStats returns dashboard statistics
func (s *Service) GetStats(ctx context.Context) (*Stats, error) {
	stats := &Stats{
		RegisteredHandlers: s.getHandlers(),
	}

	// Get task counts
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COUNT(*) 
		FROM tasks 
		GROUP BY status
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query task stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			continue
		}

		switch status {
		case tasks.StatusPending:
			stats.PendingTasks = count
		case tasks.StatusRunning:
			stats.RunningTasks = count
		case tasks.StatusCompleted:
			stats.CompletedTasks = count
		case tasks.StatusFailed, tasks.StatusDeadLetter:
			stats.FailedTasks += count
		}
	}

	// Get active worker count (heartbeat within last minute)
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) 
		FROM workers 
		WHERE last_heartbeat > NOW() - INTERVAL '1 minute'
	`).Scan(&stats.ActiveWorkers)

	if err != nil {
		return nil, fmt.Errorf("failed to query active workers: %w", err)
	}

	return stats, nil
}

// GetRecentTasks returns a list of recent tasks
func (s *Service) GetRecentTasks(ctx context.Context, limit int) ([]*tasks.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT 
			id, type, status, priority, created_at, 
			started_at, completed_at, attempts, last_error
		FROM tasks
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)

	if err != nil {
		return nil, fmt.Errorf("failed to query recent tasks: %w", err)
	}
	defer rows.Close()

	var taskList []*tasks.Task
	for rows.Next() {
		task := &tasks.Task{}
		var startedAt, completedAt sql.NullTime
		var lastError sql.NullString

		err := rows.Scan(
			&task.ID, &task.Type, &task.Status, &task.Priority,
			&task.CreatedAt, &startedAt, &completedAt,
			&task.Attempts, &lastError,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan task: %w", err)
		}

		if startedAt.Valid {
			task.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			task.CompletedAt = &completedAt.Time
		}
		if lastError.Valid {
			task.LastError = lastError.String
		}

		taskList = append(taskList, task)
	}

	return taskList, nil
}

// GetTask returns full details for a task
func (s *Service) GetTask(ctx context.Context, id string) (*tasks.Task, error) {
	task := &tasks.Task{}
	var idempotencyKey, workerID, lastError sql.NullString
	var claimedAt, startedAt, completedAt, nextRetryAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT 
			id, type, payload, status, priority, idempotency_key, worker_id,
			created_at, claimed_at, started_at, completed_at, updated_at,
			attempts, max_retries, last_error, next_retry_at
		FROM tasks
		WHERE id = $1
	`, id).Scan(
		&task.ID, &task.Type, &task.Payload, &task.Status, &task.Priority,
		&idempotencyKey, &workerID, &task.CreatedAt,
		&claimedAt, &startedAt, &completedAt, &task.UpdatedAt,
		&task.Attempts, &task.MaxRetries, &lastError, &nextRetryAt,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

	if idempotencyKey.Valid {
		task.IdempotencyKey = idempotencyKey.String
	}
	if workerID.Valid {
		task.WorkerID = workerID.String
	}
	if lastError.Valid {
		task.LastError = lastError.String
	}
	if claimedAt.Valid {
		task.ClaimedAt = &claimedAt.Time
	}
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}
	if nextRetryAt.Valid {
		task.NextRetryAt = &nextRetryAt.Time
	}

	return task, nil
}

// WorkerInfo represents a worker node
type WorkerInfo struct {
	ID            string
	Hostname      string
	PoolSize      int
	Version       string
	StartedAt     time.Time
	LastHeartbeat time.Time
	IsLeader      bool
	Status        string // "active" or "dead"
}

// GetWorkers returns a list of all workers
func (s *Service) GetWorkers(ctx context.Context) ([]*WorkerInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT 
			id, hostname, pool_size, version, 
			started_at, last_heartbeat, is_leader
		FROM workers
		ORDER BY last_heartbeat DESC
	`)

	if err != nil {
		return nil, fmt.Errorf("failed to query workers: %w", err)
	}
	defer rows.Close()

	var workers []*WorkerInfo
	for rows.Next() {
		w := &WorkerInfo{}
		err := rows.Scan(
			&w.ID, &w.Hostname, &w.PoolSize, &w.Version,
			&w.StartedAt, &w.LastHeartbeat, &w.IsLeader,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan worker: %w", err)
		}

		// Determine status based on heartbeat
		if time.Since(w.LastHeartbeat) > 1*time.Minute {
			w.Status = "dead"
		} else {
			w.Status = "active"
		}

		workers = append(workers, w)
	}

	return workers, nil
}
