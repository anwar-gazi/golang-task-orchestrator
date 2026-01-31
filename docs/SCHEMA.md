# Database Schema

The Task Orchestrator uses PostgreSQL with two core tables: `tasks` (the job queue) and `workers` (distributed coordination).

---

## Table: `tasks`

The primary job queue storing all task state.

| Column | Type | Nullable | Default | Description |
|--------|------|----------|---------|-------------|
| `id` | `UUID` | No | `gen_random_uuid()` | Unique task identifier |
| `type` | `TEXT` | No | — | Task type (e.g., `service:nextjs`, `task:backup`) |
| `payload` | `JSONB` | Yes | — | Task-specific data passed to handler |
| `status` | `task_status` | No | `'pending'` | Current state (enum, see below) |
| `priority` | `INT` | No | `0` | Higher = more urgent, processed first |
| `idempotency_key` | `TEXT` | Yes | — | Prevents duplicate task creation |
| `worker_id` | `TEXT` | Yes | — | ID of worker that claimed/is running this task |
| `created_at` | `TIMESTAMP` | No | `NOW()` | When task was enqueued |
| `claimed_at` | `TIMESTAMP` | Yes | — | When a worker claimed the task |
| `started_at` | `TIMESTAMP` | Yes | — | When handler execution began |
| `completed_at` | `TIMESTAMP` | Yes | — | When task finished (success or dead-letter) |
| `updated_at` | `TIMESTAMP` | No | `NOW()` | Auto-updated on any modification |
| `attempts` | `INT` | No | `0` | Number of execution attempts so far |
| `max_retries` | `INT` | No | `3` | Maximum retry attempts before dead-letter |
| `last_error` | `TEXT` | Yes | — | Error message from last failed attempt |
| `next_retry_at` | `TIMESTAMP` | Yes | — | When to retry (after failure with backoff) |

### Status Enum: `task_status`

```sql
CREATE TYPE task_status AS ENUM (
    'pending',      -- Waiting to be claimed
    'claimed',      -- Claimed by worker, not yet started
    'running',      -- Handler is executing
    'completed',    -- Finished successfully
    'failed',       -- Terminal failure (no handler, immediate fail)
    'dead_letter'   -- Exceeded max_retries
);
```

### Constraints

| Constraint | Type | Description |
|------------|------|-------------|
| `tasks_pkey` | Primary Key | On `id` |
| `unique_idempotency_key` | Unique | On `(type, idempotency_key)` — prevents duplicate tasks |

### Key Column: `idempotency_key`

The `idempotency_key` column enables **exactly-once enqueue semantics**:

```sql
-- Enqueue with idempotency key
INSERT INTO tasks (type, payload, idempotency_key)
VALUES ('email:send', '{"to":"user@example.com"}', 'email-user-123')
ON CONFLICT (type, idempotency_key) 
DO UPDATE SET updated_at = NOW()
RETURNING id;
```

**Behavior:**
- First insert: Creates task, returns new `id`
- Duplicate insert (same `type` + `idempotency_key`): Returns existing `id`, no new task
- Different `type` with same key: Creates new task (scoped to type)

**Use Cases:**
- Event-driven systems where events may be delivered multiple times
- User actions that might be submitted twice (double-click)
- Webhook handlers with idempotency tokens

---

## Table: `workers`

Tracks active worker instances for distributed coordination.

| Column | Type | Nullable | Default | Description |
|--------|------|----------|---------|-------------|
| `id` | `TEXT` | No | — | Unique worker ID (hostname-pid-random) |
| `hostname` | `TEXT` | No | — | Host machine name |
| `started_at` | `TIMESTAMP` | No | `NOW()` | When worker started |
| `last_heartbeat` | `TIMESTAMP` | No | `NOW()` | Last heartbeat timestamp |
| `pool_size` | `INT` | No | — | Worker's concurrent task capacity |
| `version` | `TEXT` | Yes | — | Application version |
| `is_leader` | `BOOLEAN` | No | `FALSE` | Is this worker the current leader? |
| `leader_until` | `TIMESTAMP` | Yes | — | When leader lease expires |

### Constraints

| Constraint | Type | Description |
|------------|------|-------------|
| `workers_pkey` | Primary Key | On `id` |

### Leadership Semantics

The `is_leader` and `leader_until` columns implement simple lease-based leader election:

```sql
-- Acquire leadership (atomic conditional update)
UPDATE workers
SET is_leader = TRUE, leader_until = NOW() + interval '30 seconds'
WHERE id = $my_worker_id
  AND (
      -- No current leader
      NOT EXISTS (SELECT 1 FROM workers WHERE is_leader = TRUE AND leader_until > NOW())
      -- Or I'm already leader (extending my lease)
      OR (id = $my_worker_id AND is_leader = TRUE)
  );
```

> [!NOTE]
> Workers with `last_heartbeat` older than `WORKER_TIMEOUT` are considered dead and removed by the leader. Their tasks are recovered to `pending` status.

---

## Indexes

### Tasks Table

| Index | Columns | Partial | Purpose |
|-------|---------|---------|---------|
| `tasks_pkey` | `id` | No | Primary key lookup |
| `idx_tasks_claimable` | `priority DESC, created_at ASC` | `WHERE status = 'pending' AND (next_retry_at IS NULL OR next_retry_at <= NOW())` | Fast task claiming query |
| `idx_tasks_status` | `status` | No | Status filtering |
| `idx_tasks_type_status` | `type, status` | No | Type + status filtering |
| `idx_tasks_worker_id` | `worker_id` | `WHERE worker_id IS NOT NULL` | Orphan task recovery |

### Workers Table

| Index | Columns | Partial | Purpose |
|-------|---------|---------|---------|
| `workers_pkey` | `id` | No | Primary key lookup |
| `idx_workers_heartbeat` | `last_heartbeat` | No | Dead worker cleanup |
| `idx_workers_leader` | `is_leader, leader_until` | `WHERE is_leader = TRUE` | Leader lookup |

---

## Trigger: Auto-Update `updated_at`

```sql
CREATE TRIGGER update_tasks_updated_at 
BEFORE UPDATE ON tasks
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
```

This trigger automatically sets `updated_at = NOW()` on every UPDATE, ensuring the column always reflects the last modification time.

---

## Migration

The schema is applied via `migrations/001_initial_schema.sql` on application startup. The migration is idempotent (uses `CREATE TYPE ... IF NOT EXISTS` pattern with error handling).

To manually apply:

```bash
psql $DATABASE_URL -f migrations/001_initial_schema.sql
```
