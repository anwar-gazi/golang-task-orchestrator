# Operations Guide

This document covers deployment, configuration, monitoring, and troubleshooting for the Task Orchestrator.

---

## Environment Variables

### Database Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | **Yes** | — | PostgreSQL connection string. Format: `postgres://user:pass@host:port/db?sslmode=disable` |
| `POSTGRES_PASSWORD` | Docker only | — | Used by docker-compose for the PostgreSQL container |

### Worker Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `WORKER_POOL_SIZE` | No | `10` | Maximum concurrent tasks per worker instance |
| `CLAIM_INTERVAL` | No | `1s` | How often to poll for new tasks |
| `SHUTDOWN_TIMEOUT` | No | `30s` | Grace period to wait for in-flight tasks during shutdown |

### Distributed Mode

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DISTRIBUTED_MODE` | No | `false` | Enable multi-instance coordination |
| `HEARTBEAT_INTERVAL` | No | `10s` | How often workers send heartbeats |
| `WORKER_TIMEOUT` | No | `30s` | Consider worker dead if no heartbeat |
| `LEADER_TERM` | No | `30s` | Duration of leader election lease |
| `CLEANUP_INTERVAL` | No | `1m` | How often leader runs maintenance |

### Application Settings

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `METRICS_PORT` | No | `9090` | Port for Prometheus metrics and health endpoint |
| `DEBUG` | No | `false` | Enable debug-level structured logging |
| `ENQUEUE_EXAMPLES` | No | `false` | Enqueue sample tasks on startup (for testing) |

---

## Prometheus Metrics

All metrics are exposed at `http://localhost:9090/metrics` (or your configured `METRICS_PORT`).

### Counter Metrics

| Metric | Labels | Description |
|--------|--------|-------------|
| `tasks_enqueued_total` | `type` | Total tasks added to the queue |
| `tasks_completed_total` | `type`, `status` | Total tasks finished (`success` or `failed`) |
| `task_retries_total` | `type`, `attempt` | Total retry attempts across all tasks |
| `tasks_dead_letter_total` | `type` | Tasks that exceeded max retries |

### Gauge Metrics

| Metric | Labels | Description |
|--------|--------|-------------|
| `tasks_pending` | `type` | Current pending tasks by type |
| `tasks_running` | `type` | Currently executing tasks by type |
| `worker_pool_active` | — | Number of goroutines currently processing tasks |
| `worker_pool_capacity` | — | Total worker pool size (= `WORKER_POOL_SIZE`) |
| `workers_total` | — | Number of registered workers (distributed mode) |
| `leader_status` | `worker_id` | `1` if this worker is leader, `0` otherwise |

### Histogram Metrics

| Metric | Labels | Buckets | Description |
|--------|--------|---------|-------------|
| `task_duration_seconds` | `type` | 0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 600 | Task execution duration |
| `task_claim_duration_seconds` | — | Default | Time to execute claim query |

---

## Understanding `leader_status`

The `leader_status` metric is crucial for distributed deployments:

```promql
# Which worker is currently leader?
leader_status == 1

# Alert if no leader for 2 minutes
ALERT NoLeader
  IF sum(leader_status) == 0
  FOR 2m
  LABELS { severity = "warning" }
  ANNOTATIONS {
    summary = "No task orchestrator leader elected",
    description = "Orphan task recovery and dead worker cleanup are not running"
  }

# Alert if multiple leaders (split-brain)
ALERT SplitBrain
  IF sum(leader_status) > 1
  FOR 30s
  LABELS { severity = "critical" }
  ANNOTATIONS {
    summary = "Multiple task orchestrator leaders detected",
    description = "This may indicate network partitioning or clock skew"
  }
```

---

## Recommended Alerts

### Queue Depth

```promql
# Alert if pending tasks growing
ALERT TaskQueueBacklog
  IF sum(tasks_pending) > 1000
  FOR 5m
  LABELS { severity = "warning" }
  ANNOTATIONS {
    summary = "Task queue backlog growing",
    description = "{{ $value }} tasks pending. Consider scaling workers."
  }
```

### Worker Health

```promql
# Alert if worker pool saturated
ALERT WorkerPoolSaturated
  IF (worker_pool_active / worker_pool_capacity) > 0.9
  FOR 10m
  LABELS { severity = "warning" }
  ANNOTATIONS {
    summary = "Worker pool nearly exhausted",
    description = "{{ $value | humanizePercentage }} utilization"
  }
```

### Dead Letter Queue

```promql
# Alert on dead letter tasks
ALERT TasksDeadLettered
  IF increase(tasks_dead_letter_total[5m]) > 0
  LABELS { severity = "warning" }
  ANNOTATIONS {
    summary = "Tasks moved to dead letter queue",
    description = "{{ $value }} tasks failed after max retries"
  }
```

---

## Health Endpoint

```bash
curl http://localhost:9090/health
```

Response:
```json
{
  "status": "healthy",
  "worker_id": "hostname-12345-abc123",
  "version": "1.0.0",
  "distributed": true
}
```

---

## Common Operations

### Scaling Workers (Distributed Mode)

1. Set `DISTRIBUTED_MODE=true` on all instances
2. Each worker auto-registers on startup
3. Leader election happens automatically
4. Scale horizontally by adding more container replicas

### Viewing Queue State

```sql
-- Pending tasks by type
SELECT type, COUNT(*) 
FROM tasks 
WHERE status = 'pending' 
GROUP BY type;

-- Dead letter tasks
SELECT id, type, last_error, attempts 
FROM tasks 
WHERE status = 'dead_letter' 
ORDER BY updated_at DESC;

-- Currently running tasks
SELECT id, type, worker_id, started_at 
FROM tasks 
WHERE status = 'running';

-- Orphaned tasks (assigned to non-existent workers)
SELECT t.id, t.type, t.worker_id 
FROM tasks t 
LEFT JOIN workers w ON t.worker_id = w.id 
WHERE t.status IN ('claimed', 'running') 
  AND t.worker_id IS NOT NULL 
  AND w.id IS NULL;
```

### Manual Task Retry

```sql
-- Retry a specific dead-letter task
UPDATE tasks 
SET status = 'pending', 
    attempts = 0, 
    next_retry_at = NULL 
WHERE id = 'task-uuid-here';

-- Retry all dead-letter tasks of a type
UPDATE tasks 
SET status = 'pending', 
    attempts = 0, 
    next_retry_at = NULL 
WHERE status = 'dead_letter' 
  AND type = 'example:task';
```

---

## Troubleshooting

### Tasks Stuck in `claimed` Status

**Cause**: Worker crashed after claiming but before starting execution.

**Solution**: In distributed mode, the leader automatically recovers orphaned tasks every `CLEANUP_INTERVAL`. In single-instance mode, restart the worker to trigger `recoverStaleTasks`.

### High `task_claim_duration_seconds`

**Cause**: Database under load, missing indexes, or too many pending tasks.

**Solutions**:
1. Verify `idx_tasks_claimable` partial index exists
2. Reduce `WORKER_POOL_SIZE` to claim fewer tasks per query
3. Scale database resources

### No Leader Elected

**Cause**: Clock skew between workers, or all workers crashing.

**Solutions**:
1. Ensure NTP is configured on all hosts
2. Check worker logs for registration errors
3. Verify `workers` table has entries with recent `last_heartbeat`
