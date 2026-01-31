package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// Worker represents a worker instance
type Worker struct {
	ID       string
	Hostname string
	PoolSize int
	Version  string
}

// WorkerStore handles worker registration and management
type WorkerStore struct {
	db *sql.DB
}

// NewWorkerStore creates a new worker store
func NewWorkerStore(db *sql.DB) *WorkerStore {
	return &WorkerStore{db: db}
}

// Register registers a worker in the database
func (ws *WorkerStore) Register(ctx context.Context, worker *Worker) error {
	_, err := ws.db.ExecContext(ctx, `
		INSERT INTO workers (id, hostname, pool_size, version, last_heartbeat)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (id) DO UPDATE
		SET last_heartbeat = NOW(),
			pool_size = EXCLUDED.pool_size,
			version = EXCLUDED.version
	`, worker.ID, worker.Hostname, worker.PoolSize, worker.Version)

	if err != nil {
		return fmt.Errorf("failed to register worker: %w", err)
	}

	slog.Info("worker registered", "worker_id", worker.ID, "hostname", worker.Hostname)
	return nil
}

// Heartbeat updates the worker's last heartbeat timestamp
func (ws *WorkerStore) Heartbeat(ctx context.Context, workerID string) error {
	result, err := ws.db.ExecContext(ctx, `
		UPDATE workers
		SET last_heartbeat = NOW()
		WHERE id = $1
	`, workerID)

	if err != nil {
		return fmt.Errorf("heartbeat failed: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("worker not found: %s", workerID)
	}

	slog.Debug("heartbeat sent", "worker_id", workerID)
	return nil
}

// Deregister removes a worker from the database
func (ws *WorkerStore) Deregister(ctx context.Context, workerID string) error {
	_, err := ws.db.ExecContext(ctx, `
		DELETE FROM workers WHERE id = $1
	`, workerID)

	if err != nil {
		return fmt.Errorf("failed to deregister worker: %w", err)
	}

	slog.Info("worker deregistered", "worker_id", workerID)
	return nil
}

// GetActiveWorkers returns all active workers (heartbeat within timeout period)
func (ws *WorkerStore) GetActiveWorkers(ctx context.Context, timeout time.Duration) ([]Worker, error) {
	rows, err := ws.db.QueryContext(ctx, `
		SELECT id, hostname, pool_size, version
		FROM workers
		WHERE last_heartbeat > NOW() - $1
		ORDER BY started_at ASC
	`, timeout)

	if err != nil {
		return nil, fmt.Errorf("failed to get active workers: %w", err)
	}
	defer rows.Close()

	var workers []Worker
	for rows.Next() {
		var w Worker
		if err := rows.Scan(&w.ID, &w.Hostname, &w.PoolSize, &w.Version); err != nil {
			return nil, fmt.Errorf("failed to scan worker: %w", err)
		}
		workers = append(workers, w)
	}

	return workers, nil
}

// CleanupDeadWorkers removes workers that haven't sent heartbeat within timeout
func (ws *WorkerStore) CleanupDeadWorkers(ctx context.Context, timeout time.Duration, currentWorkerID string) (int, error) {
	result, err := ws.db.ExecContext(ctx, `
		DELETE FROM workers
		WHERE last_heartbeat < NOW() - $1
		  AND id != $2
	`, timeout, currentWorkerID)

	if err != nil {
		return 0, fmt.Errorf("failed to cleanup dead workers: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		slog.Warn("cleaned up dead workers", "count", rows)
	}

	return int(rows), nil
}
