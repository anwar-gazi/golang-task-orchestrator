package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/yourusername/task-orchestrator/internal/dashboard"
	"github.com/yourusername/task-orchestrator/internal/orchestrator"
)

const version = "1.0.0"

func main() {
	// Setup structured logging
	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") == "true" {
		logLevel = slog.LevelDebug
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	slog.Info("starting task orchestrator", "version", version)

	// Connect to PostgreSQL
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		slog.Error("DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Test database connection
	if err := db.Ping(); err != nil {
		slog.Error("failed to ping database", "error", err)
		os.Exit(1)
	}

	// Run migrations
	if err := runMigrations(db); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	// Generate unique worker ID
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	workerID := fmt.Sprintf("%s-%d-%s", hostname, os.Getpid(), randomString(6))

	worker := &orchestrator.Worker{
		ID:       workerID,
		Hostname: hostname,
		PoolSize: getEnvInt("WORKER_POOL_SIZE", 10),
		Version:  version,
	}

	// Create configuration
	config := &orchestrator.Config{
		WorkerPoolSize:    getEnvInt("WORKER_POOL_SIZE", 10),
		ClaimInterval:     getEnvDuration("CLAIM_INTERVAL", 1*time.Second),
		Distributed:       getEnvBool("DISTRIBUTED_MODE", false),
		HeartbeatInterval: getEnvDuration("HEARTBEAT_INTERVAL", 10*time.Second),
		WorkerTimeout:     getEnvDuration("WORKER_TIMEOUT", 30*time.Second),
		ShutdownTimeout:   getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second),
		LeaderTerm:        getEnvDuration("LEADER_TERM", 30*time.Second),
		CleanupInterval:   getEnvDuration("CLEANUP_INTERVAL", 1*time.Minute),
	}

	// Create orchestrator server
	server := orchestrator.NewServer(db, config, worker)

	// Register handlers with custom retry configs
	server.HandleFunc("service:nextjs", orchestrator.NextJSHandler, orchestrator.RetryConfig{
		MaxRetries: 5,
		BaseDelay:  2 * time.Second,
		MaxDelay:   5 * time.Minute,
	})

	server.HandleFunc("task:backup", orchestrator.BackupHandler, orchestrator.RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   1 * time.Hour,
	})

	server.HandleFunc("example:task", orchestrator.ExampleHandler, orchestrator.RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   1 * time.Minute,
	})

	// Optionally enqueue initial tasks (useful for testing)
	if getEnvBool("ENQUEUE_EXAMPLES", false) {
		ctx := context.Background()

		// Enqueue an example task
		examplePayload, _ := json.Marshal(map[string]interface{}{
			"message": "Hello from task orchestrator!",
		})
		if id, err := server.Enqueue(ctx, "example:task", examplePayload); err != nil {
			slog.Error("failed to enqueue example task", "error", err)
		} else {
			slog.Info("enqueued example task", "task_id", id)
		}
	}

	// Start Prometheus metrics server and Dashboard
	metricsPort := getEnv("METRICS_PORT", "9090")
	go func() {
		mux := http.NewServeMux()

		// Register metrics handlers
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":      "healthy",
				"worker_id":   worker.ID,
				"version":     version,
				"distributed": config.Distributed,
			})
		})

		// Initialize and register dashboard
		dashboardService := dashboard.NewService(db)
		dashboardHandler := dashboard.NewHandler(dashboardService)
		dashboardHandler.RegisterRoutes(mux)

		addr := ":" + metricsPort
		slog.Info("dashboard and metrics server listening", "addr", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Error("server failed", "error", err)
		}
	}()

	// Start task orchestrator
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		if err := server.Start(ctx); err != nil && err != context.Canceled {
			slog.Error("server error", "error", err)
			cancel()
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	slog.Info("shutdown signal received")

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
		os.Exit(1)
	}

	slog.Info("shutdown complete")
}

func runMigrations(db *sql.DB) error {
	// Read migration file
	migrationSQL, err := os.ReadFile("migrations/001_initial_schema.sql")
	if err != nil {
		// Try alternative path for when running from different directory
		migrationSQL, err = os.ReadFile("/app/migrations/001_initial_schema.sql")
		if err != nil {
			return fmt.Errorf("failed to read migration file: %w", err)
		}
	}

	// Execute migration
	_, err = db.Exec(string(migrationSQL))
	if err != nil {
		return fmt.Errorf("failed to execute migration: %w", err)
	}

	slog.Info("database migrations completed")
	return nil
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		var i int
		if _, err := fmt.Sscanf(value, "%d", &i); err == nil {
			return i
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		return value == "true" || value == "1"
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}
