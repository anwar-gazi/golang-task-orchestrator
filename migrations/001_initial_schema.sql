-- Task Orchestrator Database Schema
-- Version: 1.0.0

-- Task statuses
DO $$ BEGIN
    CREATE TYPE task_status AS ENUM (
        'pending',
        'claimed', 
        'running',
        'completed',
        'failed',
        'dead_letter'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- Main tasks table
CREATE TABLE IF NOT EXISTS tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type TEXT NOT NULL,
    payload JSONB,
    status task_status NOT NULL DEFAULT 'pending',
    
    -- Priority (higher = more urgent, default 0)
    priority INT NOT NULL DEFAULT 0,
    
    -- Idempotency key (optional, prevents duplicates)
    idempotency_key TEXT,
    
    -- Worker tracking
    worker_id TEXT,
    
    -- Timestamps
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMP,
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    
    -- Retry logic
    attempts INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 3,
    last_error TEXT,
    next_retry_at TIMESTAMP,
    
    -- Ensure idempotency key uniqueness per task type
    CONSTRAINT unique_idempotency_key UNIQUE (type, idempotency_key)
);

-- Indexes for efficient task claiming
CREATE INDEX IF NOT EXISTS idx_tasks_claimable ON tasks(priority DESC, created_at ASC)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_type_status ON tasks(type, status);
CREATE INDEX IF NOT EXISTS idx_tasks_worker_id ON tasks(worker_id) WHERE worker_id IS NOT NULL;

-- Worker registration table (for distributed coordination)
CREATE TABLE IF NOT EXISTS workers (
    id TEXT PRIMARY KEY,
    hostname TEXT NOT NULL,
    started_at TIMESTAMP NOT NULL DEFAULT NOW(),
    last_heartbeat TIMESTAMP NOT NULL DEFAULT NOW(),
    
    -- Worker metadata
    pool_size INT NOT NULL,
    version TEXT,
    
    -- Leader election (simple timestamp-based)
    is_leader BOOLEAN NOT NULL DEFAULT FALSE,
    leader_until TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workers_heartbeat ON workers(last_heartbeat);
CREATE INDEX IF NOT EXISTS idx_workers_leader ON workers(is_leader, leader_until) WHERE is_leader = TRUE;

-- Function to update updated_at timestamp
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

-- Trigger to automatically update updated_at
DO $$ BEGIN
    CREATE TRIGGER update_tasks_updated_at BEFORE UPDATE ON tasks
        FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;
