package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/yourusername/task-orchestrator/pkg/tasks"
)

// HandlerFunc is the function signature for task handlers
type HandlerFunc func(ctx context.Context, task *tasks.Task) error

// Server is the main orchestrator server
// Server is the main orchestrator server
type Server struct {
	handlers      map[string]HandlerFunc
	configs       map[string]RetryConfig
	db            *sql.DB
	config        *Config
	metrics       *Metrics
	worker        *Worker
	workerStore   *WorkerStore
	leaderElector *LeaderElection

	// Worker pool management
	semaphore chan struct{}
	wg        sync.WaitGroup
	cancel    context.CancelFunc

	// Active task management
	activeTasks map[string]context.CancelFunc
	tasksMu     sync.Mutex

	// Log streaming
	taskLogs map[string]*LogStream
	logsMu   sync.RWMutex
}

// NewServer creates a new orchestrator server
func NewServer(db *sql.DB, config *Config, worker *Worker) *Server {
	if config == nil {
		config = DefaultConfig()
	}

	workerStore := NewWorkerStore(db)
	leaderElector := NewLeaderElection(db, worker.ID, config.LeaderTerm)

	return &Server{
		handlers:      make(map[string]HandlerFunc),
		configs:       make(map[string]RetryConfig),
		db:            db,
		config:        config,
		metrics:       NewMetrics(),
		worker:        worker,
		workerStore:   workerStore,
		leaderElector: leaderElector,
		activeTasks:   make(map[string]context.CancelFunc),
		taskLogs:      make(map[string]*LogStream),
	}
}

// HandleFunc registers a handler for a task type
func (s *Server) HandleFunc(taskType string, handler HandlerFunc, retryConfig ...RetryConfig) {
	s.handlers[taskType] = handler

	config := DefaultRetryConfig()
	if len(retryConfig) > 0 {
		config = retryConfig[0]
	}
	s.configs[taskType] = config

	slog.Info("handler registered", "task_type", taskType, "max_retries", config.MaxRetries)
}

// GetHandlers returns a list of all registered task types
func (s *Server) GetHandlers() []string {
	var types []string
	for k := range s.handlers {
		types = append(types, k)
	}
	return types
}

// Enqueue enqueues a new task
func (s *Server) Enqueue(ctx context.Context, taskType string, payload []byte, opts ...EnqueueOption) (string, error) {
	options := &EnqueueOptions{
		Priority:   0,
		MaxRetries: s.configs[taskType].MaxRetries,
	}

	for _, opt := range opts {
		opt(options)
	}

	var id string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO tasks (type, payload, priority, idempotency_key, max_retries)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5)
		ON CONFLICT (type, idempotency_key) 
		DO UPDATE SET updated_at = NOW()
		RETURNING id
	`, taskType, payload, options.Priority, options.IdempotencyKey, options.MaxRetries).Scan(&id)

	if err != nil {
		return "", fmt.Errorf("failed to enqueue task: %w", err)
	}

	s.metrics.tasksEnqueued.WithLabelValues(taskType).Inc()
	slog.Info("task enqueued", "task_id", id, "type", taskType, "priority", options.Priority)

	return id, nil
}

// Start starts the orchestrator server
func (s *Server) Start(ctx context.Context) error {
	ctx, s.cancel = context.WithCancel(ctx)

	// Register worker in distributed mode
	if s.config.Distributed {
		if err := s.workerStore.Register(ctx, s.worker); err != nil {
			return fmt.Errorf("failed to register worker: %w", err)
		}
		defer func() {
			// Use background context for cleanup since ctx may be cancelled
			s.workerStore.Deregister(context.Background(), s.worker.ID)
		}()

		// Start heartbeat goroutine
		go s.heartbeatLoop(ctx)

		// Start leader election and duties goroutine
		go s.leaderDutiesLoop(ctx)
	}

	// Crash recovery (only in single-instance mode)
	if !s.config.Distributed {
		if err := s.recoverStaleTasks(ctx); err != nil {
			return fmt.Errorf("crash recovery failed: %w", err)
		}
	}

	// Initialize worker pool
	s.semaphore = make(chan struct{}, s.config.WorkerPoolSize)
	s.metrics.workersCapacity.Set(float64(s.config.WorkerPoolSize))

	// Start queue metrics updater
	go s.updateQueueMetrics(ctx)

	// Main claim loop
	ticker := time.NewTicker(s.config.ClaimInterval)
	defer ticker.Stop()

	slog.Info("orchestrator started",
		"worker_id", s.worker.ID,
		"pool_size", s.config.WorkerPoolSize,
		"distributed", s.config.Distributed)

	for {
		select {
		case <-ctx.Done():
			slog.Info("orchestrator stopping")
			return ctx.Err()
		case <-ticker.C:
			s.claimAndExecuteTasks(ctx)
		}
	}
}

// heartbeatLoop sends periodic heartbeats (distributed mode only)
func (s *Server) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(s.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.workerStore.Heartbeat(ctx, s.worker.ID); err != nil {
				slog.Error("heartbeat failed", "worker_id", s.worker.ID, "error", err)
			}
		}
	}
}

// leaderDutiesLoop handles leader election and cleanup duties
func (s *Server) leaderDutiesLoop(ctx context.Context) {
	electionTicker := time.NewTicker(s.config.LeaderTerm / 2)
	cleanupTicker := time.NewTicker(s.config.CleanupInterval)
	defer electionTicker.Stop()
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Release leadership on shutdown
			s.leaderElector.Release(context.Background())
			s.metrics.leaderStatus.WithLabelValues(s.worker.ID).Set(0)
			return

		case <-electionTicker.C:
			// Try to become/stay leader
			isLeader, err := s.leaderElector.TryAcquire(ctx)
			if err != nil {
				slog.Error("leader election failed", "error", err)
				s.metrics.leaderStatus.WithLabelValues(s.worker.ID).Set(0)
			} else if isLeader {
				slog.Debug("leader status maintained", "worker_id", s.worker.ID)
				s.metrics.leaderStatus.WithLabelValues(s.worker.ID).Set(1)
			} else {
				s.metrics.leaderStatus.WithLabelValues(s.worker.ID).Set(0)
			}

		case <-cleanupTicker.C:
			// Only leader performs cleanup
			isLeader, err := s.leaderElector.IsLeader(ctx)
			if err != nil || !isLeader {
				continue
			}

			slog.Debug("leader performing cleanup", "worker_id", s.worker.ID)

			// Cleanup dead workers
			if count, err := s.workerStore.CleanupDeadWorkers(ctx, s.config.WorkerTimeout, s.worker.ID); err != nil {
				slog.Error("cleanup dead workers failed", "error", err)
			} else if count > 0 {
				slog.Warn("cleaned up dead workers", "count", count)
			}

			// Recover orphaned tasks
			if count, err := s.recoverOrphanedTasks(ctx); err != nil {
				slog.Error("recover orphaned tasks failed", "error", err)
			} else if count > 0 {
				slog.Warn("recovered orphaned tasks", "count", count)
			}

			// Update worker count metric
			if workers, err := s.workerStore.GetActiveWorkers(ctx, s.config.WorkerTimeout); err == nil {
				s.metrics.workersTotal.Set(float64(len(workers)))
			}
		}
	}
}

// claimAndExecuteTasks claims available tasks and executes them
func (s *Server) claimAndExecuteTasks(ctx context.Context) {
	// Calculate available worker slots
	available := s.config.WorkerPoolSize - len(s.semaphore)
	if available <= 0 {
		return
	}

	// Claim tasks atomically
	claimedTasks, err := s.claimTasks(ctx, available)
	if err != nil {
		slog.Error("failed to claim tasks", "error", err)
		return
	}

	if len(claimedTasks) == 0 {
		return
	}

	slog.Debug("claimed tasks", "count", len(claimedTasks))

	// Execute each task in worker pool
	for _, task := range claimedTasks {
		s.semaphore <- struct{}{} // Acquire semaphore
		s.wg.Add(1)

		go func(t *tasks.Task) {
			defer func() {
				<-s.semaphore // Release semaphore
				s.wg.Done()
			}()
			s.executeTask(ctx, t)
		}(task)
	}
}

// claimTasks claims tasks from the database
func (s *Server) claimTasks(ctx context.Context, limit int) ([]*tasks.Task, error) {
	start := time.Now()
	defer func() {
		s.metrics.claimDuration.Observe(time.Since(start).Seconds())
	}()

	workerID := ""
	if s.config.Distributed {
		workerID = s.worker.ID
	}

	rows, err := s.db.QueryContext(ctx, `
		WITH claimable AS (
			SELECT id 
			FROM tasks
			WHERE status = 'pending' 
			  AND (next_retry_at IS NULL OR next_retry_at <= NOW())
			ORDER BY priority DESC, created_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE tasks
		SET status = 'claimed',
			claimed_at = NOW(),
			worker_id = NULLIF($2, ''),
			updated_at = NOW()
		FROM claimable
		WHERE tasks.id = claimable.id
		RETURNING tasks.*
	`, limit, workerID)

	if err != nil {
		return nil, fmt.Errorf("failed to claim tasks: %w", err)
	}
	defer rows.Close()

	var taskList []*tasks.Task
	for rows.Next() {
		task := &tasks.Task{}
		var idempotencyKey, workerID, lastError sql.NullString
		var claimedAt, startedAt, completedAt, nextRetryAt sql.NullTime

		err := rows.Scan(
			&task.ID, &task.Type, &task.Payload, &task.Status,
			&task.Priority, &idempotencyKey, &workerID,
			&task.CreatedAt, &claimedAt, &startedAt, &completedAt,
			&task.UpdatedAt, &task.Attempts, &task.MaxRetries,
			&lastError, &nextRetryAt,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan task: %w", err)
		}

		// Handle nullable fields
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

		taskList = append(taskList, task)
	}

	return taskList, nil
}

// executeTask executes a single task
func (s *Server) executeTask(ctx context.Context, task *tasks.Task) {
	// Create cancellable context for this task
	taskCtx, cancel := context.WithCancel(ctx)

	// Register active task
	s.tasksMu.Lock()
	s.activeTasks[task.ID] = cancel
	s.tasksMu.Unlock()

	// Setup log streaming
	// We use a delayed cleanup so logs are available for a bit after completion
	stream := s.GetTaskLogStream(task.ID)
	taskCtx = context.WithValue(taskCtx, "logStream", stream)

	defer func() {
		// Cleanup active task registry
		s.tasksMu.Lock()
		delete(s.activeTasks, task.ID)
		s.tasksMu.Unlock()
		cancel() // Ensure resources are released

		// Scavenge logs after 1 hour (simple approach)
		time.AfterFunc(1*time.Hour, func() {
			s.cleanupTaskLogs(task.ID)
		})
	}()

	// Update to running
	if err := s.updateTaskStatus(taskCtx, task.ID, tasks.StatusRunning); err != nil {
		slog.Error("failed to update task status", "task_id", task.ID, "error", err)
		return
	}

	// Get handler
	handler, exists := s.handlers[task.Type]
	if !exists {
		err := fmt.Errorf("no handler registered for task type: %s", task.Type)
		s.markTaskFailed(taskCtx, task, err)
		slog.Error("handler not found", "task_id", task.ID, "type", task.Type)
		return
	}

	// Execute handler with metrics middleware
	wrappedHandler := s.withMetrics(handler)
	err := wrappedHandler(taskCtx, task)

	if err != nil {
		s.handleTaskFailure(taskCtx, task, err)
	} else {
		s.markTaskCompleted(taskCtx, task)
	}
}

// withMetrics wraps a handler with metrics collection
func (s *Server) withMetrics(handler HandlerFunc) HandlerFunc {
	return func(ctx context.Context, task *tasks.Task) error {
		start := time.Now()

		// Update running gauge
		s.metrics.tasksRunning.WithLabelValues(task.Type).Inc()
		s.metrics.workersActive.Inc()
		defer func() {
			s.metrics.tasksRunning.WithLabelValues(task.Type).Dec()
			s.metrics.workersActive.Dec()
		}()

		// Execute handler
		err := handler(ctx, task)

		// Record duration
		duration := time.Since(start).Seconds()
		s.metrics.taskDuration.WithLabelValues(task.Type).Observe(duration)

		// Record completion status
		status := "success"
		if err != nil {
			status = "failed"
		}
		s.metrics.tasksCompleted.WithLabelValues(task.Type, status).Inc()

		return err
	}
}

// updateTaskStatus updates the status of a task
func (s *Server) updateTaskStatus(ctx context.Context, taskID, status string) error {
	var setClause string
	switch status {
	case tasks.StatusRunning:
		setClause = "status = $2, started_at = NOW()"
	case tasks.StatusCompleted:
		setClause = "status = $2, completed_at = NOW()"
	default:
		setClause = "status = $2"
	}

	query := fmt.Sprintf(`
		UPDATE tasks
		SET %s
		WHERE id = $1
	`, setClause)

	_, err := s.db.ExecContext(ctx, query, taskID, status)
	return err
}

// markTaskCompleted marks a task as completed
func (s *Server) markTaskCompleted(ctx context.Context, task *tasks.Task) {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks
		SET status = 'completed',
			completed_at = NOW()
		WHERE id = $1
	`, task.ID)

	if err != nil {
		slog.Error("failed to mark task completed", "task_id", task.ID, "error", err)
		return
	}

	slog.Info("task completed", "task_id", task.ID, "type", task.Type, "attempts", task.Attempts+1)
}

// markTaskFailed marks a task as failed (no retry)
func (s *Server) markTaskFailed(ctx context.Context, task *tasks.Task, err error) {
	_, dbErr := s.db.ExecContext(ctx, `
		UPDATE tasks
		SET status = 'failed',
			last_error = $2
		WHERE id = $1
	`, task.ID, err.Error())

	if dbErr != nil {
		slog.Error("failed to mark task failed", "task_id", task.ID, "error", dbErr)
	}
}

// handleTaskFailure handles a task failure with retry logic
func (s *Server) handleTaskFailure(ctx context.Context, task *tasks.Task, err error) {
	config := s.configs[task.Type]
	nextAttempt := task.Attempts + 1

	var status string
	var nextRetry *time.Time

	if nextAttempt >= config.MaxRetries {
		status = tasks.StatusDeadLetter
		s.metrics.tasksDeadLetter.WithLabelValues(task.Type).Inc()
		slog.Error("task exceeded max retries",
			"task_id", task.ID,
			"type", task.Type,
			"attempts", nextAttempt,
			"error", err)
	} else {
		status = tasks.StatusPending
		retry := calculateNextRetry(nextAttempt, config)
		nextRetry = &retry
		s.metrics.tasksRetried.WithLabelValues(task.Type, fmt.Sprintf("%d", nextAttempt)).Inc()
		slog.Warn("task failed, will retry",
			"task_id", task.ID,
			"type", task.Type,
			"attempt", nextAttempt,
			"next_retry", retry,
			"error", err)
	}

	_, dbErr := s.db.ExecContext(ctx, `
		UPDATE tasks 
		SET status = $1,
			attempts = attempts + 1,
			last_error = $2,
			next_retry_at = $3
		WHERE id = $4
	`, status, err.Error(), nextRetry, task.ID)

	if dbErr != nil {
		slog.Error("failed to update failed task", "task_id", task.ID, "error", dbErr)
	}
}

// calculateNextRetry calculates the next retry time with exponential backoff and jitter
func calculateNextRetry(attempt int, config RetryConfig) time.Time {
	// Exponential: 2^attempt * baseDelay
	exponential := math.Pow(2, float64(attempt)) * float64(config.BaseDelay)

	// Cap maximum delay
	maxDelay := float64(config.MaxDelay)
	if exponential > maxDelay {
		exponential = maxDelay
	}

	// Full jitter: random value between 0 and exponential delay
	jitter := rand.Float64() * exponential

	return time.Now().Add(time.Duration(jitter))
}

// recoverStaleTasks resets stale tasks (single-instance mode)
func (s *Server) recoverStaleTasks(ctx context.Context) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE tasks
		SET status = 'pending',
			claimed_at = NULL,
			started_at = NULL,
			worker_id = NULL
		WHERE status IN ('claimed', 'running')
	`)

	if err != nil {
		return fmt.Errorf("failed to recover stale tasks: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		slog.Info("recovered stale tasks on startup", "count", rows)
	}

	return nil
}

// recoverOrphanedTasks recovers tasks from dead workers (distributed mode)
func (s *Server) recoverOrphanedTasks(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE tasks
		SET status = 'pending',
			claimed_at = NULL,
			started_at = NULL,
			worker_id = NULL
		WHERE status IN ('claimed', 'running')
		  AND worker_id IS NOT NULL
		  AND NOT EXISTS (
			  SELECT 1 FROM workers 
			  WHERE workers.id = tasks.worker_id
		  )
	`)

	if err != nil {
		return 0, fmt.Errorf("failed to recover orphaned tasks: %w", err)
	}

	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// updateQueueMetrics periodically updates queue depth metrics
func (s *Server) updateQueueMetrics(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := s.db.QueryContext(ctx, `
				SELECT type, status, COUNT(*) 
				FROM tasks 
				WHERE status IN ('pending', 'running')
				GROUP BY type, status
			`)
			if err != nil {
				slog.Error("failed to query queue metrics", "error", err)
				continue
			}

			// Reset gauges
			s.metrics.tasksPending.Reset()
			s.metrics.tasksRunning.Reset()

			for rows.Next() {
				var taskType, status string
				var count int
				if err := rows.Scan(&taskType, &status, &count); err != nil {
					continue
				}

				if status == tasks.StatusPending {
					s.metrics.tasksPending.WithLabelValues(taskType).Set(float64(count))
				} else if status == tasks.StatusRunning {
					s.metrics.tasksRunning.WithLabelValues(taskType).Set(float64(count))
				}
			}
			rows.Close()
		}
	}
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	slog.Info("initiating graceful shutdown", "worker_id", s.worker.ID)

	// Stop claiming new tasks
	if s.cancel != nil {
		s.cancel()
	}

	// In distributed mode, deregister worker and release leadership
	if s.config.Distributed {
		s.leaderElector.Release(context.Background())
		s.workerStore.Deregister(context.Background(), s.worker.ID)
	}

	// Wait for in-flight tasks with timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("all tasks completed gracefully")
		return nil
	case <-ctx.Done():
		slog.Warn("shutdown timeout exceeded, tasks will be recovered on restart")
		return ctx.Err()
	}
}

// Cancel cancels a task by ID
func (s *Server) Cancel(ctx context.Context, taskID string) error {
	// check if task is running locally
	s.tasksMu.Lock()
	cancel, exists := s.activeTasks[taskID]
	s.tasksMu.Unlock()

	if exists {
		slog.Info("cancelling active task", "task_id", taskID)
		cancel()
		return nil
	}

	// Falls back to cancelling pending/claimed tasks or cleaning up orphaned running tasks
	// 1. Try to delete pending/claimed tasks
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM tasks 
		WHERE id = $1 AND status IN ('pending', 'claimed')
	`, taskID)

	if err != nil {
		return fmt.Errorf("failed to cancel task: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		slog.Info("task cancelled (pending)", "task_id", taskID)
		return nil
	}

	// 2. If not pending, it might be an orphaned running task (zombie from restart)
	// Since we checked activeTasks map first, if it is 'running' in DB but not in map, it's a zombie.
	result, err = s.db.ExecContext(ctx, `
		UPDATE tasks 
		SET status = 'failed', 
			last_error = 'force cancelled (orphaned/zombie)',
			completed_at = NOW()
		WHERE id = $1 AND status = 'running'
	`, taskID)

	if err != nil {
		return fmt.Errorf("failed to force cancel running task: %w", err)
	}

	rows, _ = result.RowsAffected()
	if rows > 0 {
		slog.Info("task force cancelled (zombie)", "task_id", taskID)
		return nil
	}

	return fmt.Errorf("task not found or already completed")
}

// GetTaskLogStream returns the log stream for a task, creating it if necessary
func (s *Server) GetTaskLogStream(taskID string) *LogStream {
	s.logsMu.Lock()
	defer s.logsMu.Unlock()

	stream, exists := s.taskLogs[taskID]
	if !exists {
		stream = NewLogStream(1000) // Keep last 1000 lines
		s.taskLogs[taskID] = stream
	}
	return stream
}

// cleanupTaskLogs removes the log stream for a task
func (s *Server) cleanupTaskLogs(taskID string) {
	s.logsMu.Lock()
	defer s.logsMu.Unlock()
	delete(s.taskLogs, taskID)
}
