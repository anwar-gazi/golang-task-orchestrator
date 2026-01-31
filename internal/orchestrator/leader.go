package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// LeaderElection handles leader election in distributed mode
type LeaderElection struct {
	db       *sql.DB
	workerID string
	term     time.Duration
}

// NewLeaderElection creates a new leader election instance
func NewLeaderElection(db *sql.DB, workerID string, term time.Duration) *LeaderElection {
	return &LeaderElection{
		db:       db,
		workerID: workerID,
		term:     term,
	}
}

// TryAcquire attempts to become the leader
func (le *LeaderElection) TryAcquire(ctx context.Context) (bool, error) {
	result, err := le.db.ExecContext(ctx, `
		UPDATE workers
		SET is_leader = TRUE,
			leader_until = NOW() + $1
		WHERE id = $2
		  AND (
			  -- No current leader
			  NOT EXISTS (
				  SELECT 1 FROM workers 
				  WHERE is_leader = TRUE 
					AND leader_until > NOW()
			  )
			  -- Or I'm already the leader and extending my term
			  OR (id = $2 AND is_leader = TRUE)
		  )
	`, le.term, le.workerID)

	if err != nil {
		return false, fmt.Errorf("leader election failed: %w", err)
	}

	rows, _ := result.RowsAffected()
	isLeader := rows > 0

	if isLeader {
		slog.Debug("leader election won", "worker_id", le.workerID)
	}

	return isLeader, nil
}

// Release releases leadership
func (le *LeaderElection) Release(ctx context.Context) error {
	_, err := le.db.ExecContext(ctx, `
		UPDATE workers
		SET is_leader = FALSE,
			leader_until = NULL
		WHERE id = $1
	`, le.workerID)

	if err != nil {
		return fmt.Errorf("failed to release leadership: %w", err)
	}

	slog.Info("leadership released", "worker_id", le.workerID)
	return nil
}

// IsLeader checks if the current worker is the leader
func (le *LeaderElection) IsLeader(ctx context.Context) (bool, error) {
	var isLeader bool
	err := le.db.QueryRowContext(ctx, `
		SELECT is_leader 
		FROM workers
		WHERE id = $1 AND leader_until > NOW()
	`, le.workerID).Scan(&isLeader)

	if err == sql.ErrNoRows {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("failed to check leader status: %w", err)
	}

	return isLeader, nil
}

// GetCurrentLeader returns the ID of the current leader, if any
func (le *LeaderElection) GetCurrentLeader(ctx context.Context) (string, error) {
	var leaderID string
	err := le.db.QueryRowContext(ctx, `
		SELECT id
		FROM workers
		WHERE is_leader = TRUE AND leader_until > NOW()
		LIMIT 1
	`).Scan(&leaderID)

	if err == sql.ErrNoRows {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("failed to get current leader: %w", err)
	}

	return leaderID, nil
}
