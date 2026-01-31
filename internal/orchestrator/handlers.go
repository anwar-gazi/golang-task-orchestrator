package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yourusername/task-orchestrator/pkg/tasks"
)

// NextJSConfig holds configuration for NextJS dev server
type NextJSConfig struct {
	WorkingDir string `json:"working_dir"`
	Port       int    `json:"port"`
}

// NextJSHandler starts a NextJS development server
// This is a long-running task that blocks until the server exits or context is cancelled
func NextJSHandler(ctx context.Context, task *tasks.Task) error {
	var config NextJSConfig
	if err := json.Unmarshal(task.Payload, &config); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	// Validate working directory exists
	if _, err := os.Stat(config.WorkingDir); os.IsNotExist(err) {
		return fmt.Errorf("working directory does not exist: %s", config.WorkingDir)
	}

	cmd := exec.CommandContext(ctx, "npm", "run", "dev")
	cmd.Dir = config.WorkingDir

	// Critical: Use process group to ensure child processes are killed
	// when the parent Go process dies
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // Create new process group
	}

	// Stream stdout/stderr to structured logger and log stream
	stdoutWriter := io.Writer(&logWriter{prefix: "nextjs", level: slog.LevelInfo})
	stderrWriter := io.Writer(&logWriter{prefix: "nextjs", level: slog.LevelError})

	// If a log stream is available in the context, pipe output there too
	if stream, ok := ctx.Value("logStream").(io.Writer); ok {
		stdoutWriter = io.MultiWriter(stdoutWriter, stream)
		stderrWriter = io.MultiWriter(stderrWriter, stream)
	}

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	// Set environment variables
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", config.Port))

	slog.Info("starting nextjs dev server",
		"task_id", task.ID,
		"dir", config.WorkingDir,
		"port", config.Port)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start nextjs: %w", err)
	}

	// Block until context cancelled or process exits
	if err := cmd.Wait(); err != nil {
		// Check if it was cancelled by context
		if ctx.Err() != nil {
			slog.Info("nextjs server stopped by context cancellation", "task_id", task.ID)
			return ctx.Err()
		}
		return fmt.Errorf("nextjs exited with error: %w", err)
	}

	slog.Info("nextjs server exited normally", "task_id", task.ID)
	return nil
}

// NotionCloneHandler is a wrapper around NextJSHandler with preset configuration
func NotionCloneHandler(ctx context.Context, task *tasks.Task) error {
	// Create preset configuration
	config := NextJSConfig{
		WorkingDir: "/home/resgef/works/notion-clone",
		Port:       3000,
	}

	// Marshaling the config to JSON to replace the task payload
	payload, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal notion clone config: %w", err)
	}

	// Create a shallow copy of the task with the new payload
	// We don't want to modify the original task pointer as it might be used elsewhere
	taskWithConfig := *task
	taskWithConfig.Payload = payload

	// Delegate to the generic NextJS handler
	return NextJSHandler(ctx, &taskWithConfig)
}

type BackupConfig struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	User          string `json:"user"`
	Password      string `json:"password"`
	Database      string `json:"database"`
	BackupDir     string `json:"backup_dir"`
	RetentionDays int    `json:"retention_days"`
}

// BackupHandler executes a PostgreSQL backup and cleans up old backups
func BackupHandler(ctx context.Context, task *tasks.Task) error {
	var config BackupConfig
	if err := json.Unmarshal(task.Payload, &config); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	// Ensure backup directory exists
	if err := os.MkdirAll(config.BackupDir, 0755); err != nil {
		return fmt.Errorf("failed to create backup dir: %w", err)
	}

	// Generate filename with timestamp
	timestamp := time.Now().Format("20060102_150405")
	filename := filepath.Join(config.BackupDir, fmt.Sprintf("backup_%s.sql", timestamp))

	// Execute pg_dump
	cmd := exec.CommandContext(ctx, "pg_dump",
		"-h", config.Host,
		"-p", strconv.Itoa(config.Port),
		"-U", config.User,
		"-f", filename,
		config.Database,
	)

	// Set password via environment variable
	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", config.Password))

	slog.Info("starting database backup",
		"task_id", task.ID,
		"database", config.Database,
		"file", filename)

	// Capture output for debugging
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_dump failed: %w\nOutput: %s", err, output)
	}

	slog.Info("backup created",
		"task_id", task.ID,
		"file", filename,
		"size_bytes", getFileSize(filename))

	// Clean up old backups
	deleted, err := deleteOlderThan(config.BackupDir, config.RetentionDays)
	if err != nil {
		// Don't fail the task if cleanup fails
		slog.Error("failed to cleanup old backups", "task_id", task.ID, "error", err)
	} else if deleted > 0 {
		slog.Info("cleaned up old backups", "task_id", task.ID, "count", deleted)
	}

	return nil
}

// logWriter implements io.Writer for structured logging
type logWriter struct {
	prefix string
	level  slog.Level
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	message := strings.TrimSpace(string(p))
	if message != "" {
		slog.Log(context.Background(), w.level, message, "source", w.prefix)
	}
	return len(p), nil
}

// deleteOlderThan deletes backup files older than the specified number of days
func deleteOlderThan(dir string, retentionDays int) (int, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	deleted := 0

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("failed to read directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// Only delete backup files
		if !strings.HasPrefix(entry.Name(), "backup_") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			slog.Warn("failed to get file info", "file", entry.Name(), "error", err)
			continue
		}

		if info.ModTime().Before(cutoff) {
			filepath := filepath.Join(dir, entry.Name())
			if err := os.Remove(filepath); err != nil {
				slog.Error("failed to delete old backup", "file", filepath, "error", err)
			} else {
				deleted++
				ageHours := time.Since(info.ModTime()).Hours()
				slog.Info("deleted old backup",
					"file", entry.Name(),
					"age_days", int(ageHours/24),
					"size_bytes", info.Size())
			}
		}
	}

	return deleted, nil
}

// getFileSize returns the size of a file in bytes
func getFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// ExampleHandler demonstrates a simple task handler
func ExampleHandler(ctx context.Context, task *tasks.Task) error {
	var data map[string]interface{}
	if len(task.Payload) > 0 {
		if err := json.Unmarshal(task.Payload, &data); err != nil {
			return fmt.Errorf("invalid payload: %w", err)
		}
	}

	slog.Info("executing example task",
		"task_id", task.ID,
		"type", task.Type,
		"payload", data)

	// Simulate work
	select {
	case <-time.After(2 * time.Second):
		slog.Info("example task completed", "task_id", task.ID)
		return nil
	case <-ctx.Done():
		slog.Warn("example task cancelled", "task_id", task.ID)
		return ctx.Err()
	}
}

// FailingHandler demonstrates a handler that always fails (for testing retries)
func FailingHandler(ctx context.Context, task *tasks.Task) error {
	slog.Warn("failing task handler called", "task_id", task.ID, "attempt", task.Attempts+1)
	return fmt.Errorf("intentional failure for testing (attempt %d)", task.Attempts+1)
}

// Ensure io.Writer interface is implemented
var _ io.Writer = (*logWriter)(nil)
