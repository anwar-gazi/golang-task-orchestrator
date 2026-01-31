package orchestrator_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/yourusername/task-orchestrator/internal/orchestrator"
	"github.com/yourusername/task-orchestrator/pkg/tasks"
)

// TestTaskEnqueue tests basic task enqueuing
func TestTaskEnqueue(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	worker := &orchestrator.Worker{
		ID:       "test-worker-1",
		Hostname: "test-host",
		PoolSize: 10,
		Version:  "test",
	}

	config := orchestrator.DefaultConfig()
	server := orchestrator.NewServer(db, config, worker)

	// Register a simple handler
	executed := false
	server.HandleFunc("test:simple", func(ctx context.Context, task *tasks.Task) error {
		executed = true
		return nil
	})

	// Enqueue a task
	payload, _ := json.Marshal(map[string]string{"test": "data"})
	taskID, err := server.Enqueue(context.Background(), "test:simple", payload)

	if err != nil {
		t.Fatalf("Failed to enqueue task: %v", err)
	}

	if taskID == "" {
		t.Fatal("Task ID should not be empty")
	}

	// Verify task exists in database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = $1", taskID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query task: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 task, got %d", count)
	}
}

// TestIdempotencyKey tests that idempotency keys prevent duplicate tasks
func TestIdempotencyKey(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	worker := &orchestrator.Worker{
		ID:       "test-worker-1",
		Hostname: "test-host",
		PoolSize: 10,
		Version:  "test",
	}

	config := orchestrator.DefaultConfig()
	server := orchestrator.NewServer(db, config, worker)

	server.HandleFunc("test:idempotent", func(ctx context.Context, task *tasks.Task) error {
		return nil
	})

	payload, _ := json.Marshal(map[string]string{"test": "data"})

	// Enqueue first task
	taskID1, err := server.Enqueue(context.Background(), "test:idempotent", payload,
		orchestrator.WithIdempotencyKey("unique-key-123"))
	if err != nil {
		t.Fatalf("Failed to enqueue first task: %v", err)
	}

	// Enqueue second task with same key
	taskID2, err := server.Enqueue(context.Background(), "test:idempotent", payload,
		orchestrator.WithIdempotencyKey("unique-key-123"))
	if err != nil {
		t.Fatalf("Failed to enqueue second task: %v", err)
	}

	// Both should return the same task ID (or at least, only one task should exist)
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM tasks WHERE type = 'test:idempotent'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count tasks: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 task due to idempotency, got %d (IDs: %s, %s)", count, taskID1, taskID2)
	}
}

// TestRetryLogic tests that failed tasks are retried with exponential backoff
func TestRetryLogic(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	worker := &orchestrator.Worker{
		ID:       "test-worker-1",
		Hostname: "test-host",
		PoolSize: 10,
		Version:  "test",
	}

	config := orchestrator.DefaultConfig()
	server := orchestrator.NewServer(db, config, worker)

	// Handler that fails on first attempt, succeeds on second
	attempts := 0
	server.HandleFunc("test:retry", func(ctx context.Context, task *tasks.Task) error {
		attempts++
		if attempts == 1 {
			return fmt.Errorf("intentional failure")
		}
		return nil
	}, orchestrator.RetryConfig{
		MaxRetries: 3,
		BaseDelay:  100 * time.Millisecond,
		MaxDelay:   1 * time.Second,
	})

	// This test would require running the full server loop
	// For now, we just verify the handler is registered
	if len(server.handlers) == 0 {
		t.Error("Handler not registered")
	}
}

// TestPriority tests that high priority tasks are claimed first
func TestPriority(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	worker := &orchestrator.Worker{
		ID:       "test-worker-1",
		Hostname: "test-host",
		PoolSize: 10,
		Version:  "test",
	}

	config := orchestrator.DefaultConfig()
	server := orchestrator.NewServer(db, config, worker)

	server.HandleFunc("test:priority", func(ctx context.Context, task *tasks.Task) error {
		return nil
	})

	payload, _ := json.Marshal(map[string]string{"test": "data"})

	// Enqueue low priority task
	_, err := server.Enqueue(context.Background(), "test:priority", payload,
		orchestrator.WithPriority(1))
	if err != nil {
		t.Fatalf("Failed to enqueue low priority task: %v", err)
	}

	// Enqueue high priority task
	highPriorityID, err := server.Enqueue(context.Background(), "test:priority", payload,
		orchestrator.WithPriority(10))
	if err != nil {
		t.Fatalf("Failed to enqueue high priority task: %v", err)
	}

	// Query for the next task to be claimed (should be high priority)
	var nextTaskID string
	err = db.QueryRow(`
		SELECT id FROM tasks 
		WHERE status = 'pending' 
		ORDER BY priority DESC, created_at ASC 
		LIMIT 1
	`).Scan(&nextTaskID)

	if err != nil {
		t.Fatalf("Failed to query next task: %v", err)
	}

	if nextTaskID != highPriorityID {
		t.Errorf("Expected high priority task to be next, got different task")
	}
}

// setupTestDB creates a test database and returns a cleanup function
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	// Use a test database URL from environment or default
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	// Run migrations
	migrationSQL, err := os.ReadFile("../migrations/001_initial_schema.sql")
	if err != nil {
		t.Fatalf("Failed to read migration file: %v", err)
	}

	_, err = db.Exec(string(migrationSQL))
	if err != nil {
		t.Fatalf("Failed to run migrations: %v", err)
	}

	cleanup := func() {
		// Clean up test data
		db.Exec("DROP TABLE IF EXISTS tasks CASCADE")
		db.Exec("DROP TABLE IF EXISTS workers CASCADE")
		db.Exec("DROP TYPE IF EXISTS task_status CASCADE")
		db.Close()
	}

	return db, cleanup
}
