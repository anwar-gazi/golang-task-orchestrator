# Task Orchestrator

A production-grade task orchestration engine written in Go, designed for both single-instance and distributed multi-instance deployments. Built with PostgreSQL persistence, Prometheus metrics, and exponential backoff with jitter.

## Features

- **Handler/Mux Pattern**: Familiar interface similar to `net/http` or `asynq`
- **PostgreSQL Persistence**: Durable task queue with atomic operations
- **Distributed Mode**: Multi-instance support with leader election and worker coordination
- **Exponential Backoff with Jitter**: Prevents retry storms
- **Prometheus Metrics**: Complete observability out of the box
- **Graceful Shutdown**: Ensures tasks complete safely
- **Priority Tasks**: Higher priority tasks execute first
- **Idempotency Keys**: Prevent duplicate task execution
- **Dead Letter Queue**: Handle permanently failed tasks

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Task Orchestrator                         │
├─────────────────────────────────────────────────────────────┤
│                                                               │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐              │
│  │ Worker 1 │    │ Worker 2 │    │ Worker 3 │ (Distributed)│
│  └────┬─────┘    └────┬─────┘    └────┬─────┘              │
│       │               │               │                      │
│       └───────────────┼───────────────┘                      │
│                       │                                      │
│              ┌────────▼─────────┐                           │
│              │   PostgreSQL     │                           │
│              │   (Task Queue)   │                           │
│              └──────────────────┘                           │
│                                                               │
│  ┌─────────────────────────────────────────────┐            │
│  │  Handlers: NextJS, Backup, Custom...        │            │
│  └─────────────────────────────────────────────┘            │
│                                                               │
│  ┌─────────────────────────────────────────────┐            │
│  │  Prometheus Metrics + Grafana Dashboards    │            │
│  └─────────────────────────────────────────────┘            │
└─────────────────────────────────────────────────────────────┘
```

## Quick Start

### Prerequisites

- Docker and Docker Compose
- Go 1.22+ (for local development)
- PostgreSQL 16+ (if running locally without Docker)

### Single Instance Mode (Default)

```bash
# Clone the repository
git clone <repository-url>
cd task-orchestrator

# Copy environment variables
cp .env.example .env

# Start services
make docker-up

# View logs
make docker-logs

# Check health
curl http://localhost:9090/health
```

The orchestrator will be available at:
- Health: http://localhost:9090/health
- Metrics: http://localhost:9090/metrics

### Distributed Mode (3 Workers)

```bash
# Start distributed deployment
make docker-up-dist

# View logs from all workers
make docker-logs-dist
```

Workers will be available at:
- Worker 1: http://localhost:9091
- Worker 2: http://localhost:9092
- Worker 3: http://localhost:9093
- Prometheus: http://localhost:9090
- Grafana: http://localhost:3000 (admin/admin)

## Configuration

Configure via environment variables (see `.env.example`):

```bash
# Database
DATABASE_URL=postgres://postgres:secret@localhost:5433/orchestrator?sslmode=disable

# Worker Settings
WORKER_POOL_SIZE=10        # Max concurrent tasks per worker
CLAIM_INTERVAL=1s          # How often to poll for tasks

# Distributed Mode
DISTRIBUTED_MODE=false     # Enable multi-instance support
HEARTBEAT_INTERVAL=10s     # Worker heartbeat frequency
WORKER_TIMEOUT=30s         # Consider worker dead after this
LEADER_TERM=30s            # Leadership duration
CLEANUP_INTERVAL=1m        # How often leader cleans up

# Application
METRICS_PORT=9090
DEBUG=false
```

## Usage

### Registering Handlers

```go
package main

import (
    "context"
    "github.com/yourusername/task-orchestrator/internal/orchestrator"
    "github.com/yourusername/task-orchestrator/pkg/tasks"
)

func main() {
    server := orchestrator.NewServer(db, config, worker)
    
    // Register handler with custom retry config
    server.HandleFunc("task:backup", BackupHandler, orchestrator.RetryConfig{
        MaxRetries: 3,
        BaseDelay:  1 * time.Second,
        MaxDelay:   1 * time.Hour,
    })
    
    // Start server
    server.Start(ctx)
}

func BackupHandler(ctx context.Context, task *tasks.Task) error {
    // Your task logic here
    return nil
}
```

### Enqueuing Tasks

```go
// Simple enqueue
taskID, err := server.Enqueue(ctx, "task:backup", payload)

// With priority
taskID, err := server.Enqueue(ctx, "task:backup", payload,
    orchestrator.WithPriority(10))

// With idempotency key (prevents duplicates)
taskID, err := server.Enqueue(ctx, "task:backup", payload,
    orchestrator.WithIdempotencyKey("backup-2024-01-30"))

// With custom max retries
taskID, err := server.Enqueue(ctx, "task:backup", payload,
    orchestrator.WithMaxRetries(5))
```

### Cancelling Tasks

```go
err := server.Cancel(ctx, taskID)
```

## Built-in Handlers

### NextJS Dev Server Handler

Starts and monitors a NextJS development server:

```go
payload := orchestrator.NextJSConfig{
    WorkingDir: "/app/frontend",
    Port:       3000,
}
payloadBytes, _ := json.Marshal(payload)

taskID, err := server.Enqueue(ctx, "service:nextjs", payloadBytes)
```

### PostgreSQL Backup Handler

Performs database backup with automatic cleanup:

```go
payload := orchestrator.BackupConfig{
    Host:          "localhost",
    Port:          5433,
    User:          "postgres",
    Password:      "secret",
    Database:      "myapp",
    BackupDir:     "/backups",
    RetentionDays: 7,
}
payloadBytes, _ := json.Marshal(payload)

taskID, err := server.Enqueue(ctx, "task:backup", payloadBytes)
```

### Notion Clone Handler

Starts the local Notion Clone development server with preset configuration:

```go
// Enqueue without payload (config is hardcoded)
taskID, err := server.Enqueue(ctx, "service:notion-clone", nil)
```

- **Working Directory**: `/home/resgef/works/notion-clone`
- **Port**: 3000

## Database Schema

The orchestrator uses two main tables:

### Tasks Table

```sql
CREATE TABLE tasks (
    id UUID PRIMARY KEY,
    type TEXT NOT NULL,
    payload JSONB,
    status task_status NOT NULL DEFAULT 'pending',
    priority INT NOT NULL DEFAULT 0,
    idempotency_key TEXT,
    worker_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMP,
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    attempts INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 3,
    last_error TEXT,
    next_retry_at TIMESTAMP
);
```

### Workers Table (Distributed Mode)

```sql
CREATE TABLE workers (
    id TEXT PRIMARY KEY,
    hostname TEXT NOT NULL,
    started_at TIMESTAMP NOT NULL DEFAULT NOW(),
    last_heartbeat TIMESTAMP NOT NULL DEFAULT NOW(),
    pool_size INT NOT NULL,
    version TEXT,
    is_leader BOOLEAN NOT NULL DEFAULT FALSE,
    leader_until TIMESTAMP
);
```

## Metrics

### Available Prometheus Metrics

**Counters:**
- `tasks_enqueued_total{type}` - Total tasks enqueued
- `tasks_completed_total{type,status}` - Total tasks completed
- `task_retries_total{type,attempt}` - Total retry attempts
- `tasks_dead_letter_total{type}` - Tasks moved to dead letter queue

**Gauges:**
- `tasks_pending{type}` - Current pending tasks
- `tasks_running{type}` - Current running tasks
- `worker_pool_active` - Active workers
- `worker_pool_capacity` - Total worker capacity
- `workers_total` - Total registered workers (distributed mode)
- `leader_status{worker_id}` - Leader status (1 if leader, 0 otherwise)

**Histograms:**
- `task_duration_seconds{type}` - Task execution duration
- `task_claim_duration_seconds` - Time to claim tasks from database

### Example Queries

```promql
# Task throughput (tasks/sec)
rate(tasks_completed_total[5m])

# Task failure rate
rate(tasks_completed_total{status="failed"}[5m])

# Average task duration
rate(task_duration_seconds_sum[5m]) / rate(task_duration_seconds_count[5m])

# Queue backlog
sum(tasks_pending)

# Worker utilization
worker_pool_active / worker_pool_capacity
```

## State Machine

Tasks flow through the following states:

```
pending → claimed → running → completed
            ↓          ↓
         timeout    failed → retry_scheduled → pending
            ↓          ↓
         pending   dead_letter (max retries exceeded)
```

## Distributed Mode Details

### Leader Election

In distributed mode, one worker is elected as the leader using a simple timestamp-based mechanism. The leader is responsible for:

- Cleaning up dead workers (no heartbeat)
- Recovering orphaned tasks from crashed workers
- Monitoring the dead letter queue
- Aggregate metrics reporting

Leadership automatically fails over if the leader crashes.

### Worker Coordination

Workers coordinate through PostgreSQL:

1. **Registration**: Each worker registers on startup with unique ID
2. **Heartbeat**: Workers send periodic heartbeats (default: 10s)
3. **Task Claiming**: Atomic task claiming with `FOR UPDATE SKIP LOCKED`
4. **Crash Recovery**: Leader detects dead workers and recovers their tasks

### Scaling

Add more workers by:

```bash
# Docker Compose
docker-compose -f docker-compose.distributed.yml up --scale orchestrator-1=5

# Kubernetes
kubectl scale deployment orchestrator --replicas=5
```

## Development

### Local Development

```bash
# Install dependencies
go mod download

# Run tests
make test

# Build
make build

# Run locally (requires PostgreSQL)
export DATABASE_URL="postgres://postgres:secret@localhost:5433/orchestrator?sslmode=disable"
make run
```

### Project Structure

```
task-orchestrator/
├── cmd/
│   └── orchestrator/
│       └── main.go              # Application entry point
├── internal/
│   └── orchestrator/
│       ├── server.go            # Core orchestrator logic
│       ├── worker.go            # Worker registration & heartbeat
│       ├── leader.go            # Leader election
│       ├── metrics.go           # Prometheus metrics
│       ├── handlers.go          # Built-in task handlers
│       └── config.go            # Configuration structs
├── pkg/
│   └── tasks/
│       └── task.go              # Task data structures
├── migrations/
│   └── 001_initial_schema.sql  # Database schema
├── configs/
│   ├── prometheus.yml           # Prometheus config
│   └── grafana-datasource.yml  # Grafana config
├── docker-compose.yml           # Single instance deployment
├── docker-compose.distributed.yml # Distributed deployment
├── Dockerfile
├── Makefile
└── README.md
```

### Adding Custom Handlers

1. Create your handler function:

```go
func MyCustomHandler(ctx context.Context, task *tasks.Task) error {
    var payload MyPayload
    if err := json.Unmarshal(task.Payload, &payload); err != nil {
        return fmt.Errorf("invalid payload: %w", err)
    }
    
    // Your logic here
    
    return nil
}
```

2. Register it in `main.go`:

```go
server.HandleFunc("custom:mytask", MyCustomHandler, orchestrator.RetryConfig{
    MaxRetries: 3,
    BaseDelay:  1 * time.Second,
    MaxDelay:   1 * time.Minute,
})
```

3. Enqueue tasks:

```go
payload, _ := json.Marshal(MyPayload{...})
taskID, err := server.Enqueue(ctx, "custom:mytask", payload)
```

## Deployment

### Docker

```bash
# Build image
docker build -t task-orchestrator:latest .

# Run
docker run -d \
  -e DATABASE_URL="postgres://..." \
  -e WORKER_POOL_SIZE=10 \
  -p 9090:9090 \
  task-orchestrator:latest
```

### Kubernetes

Example deployment (see `k8s/` directory for complete manifests):

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: orchestrator
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: orchestrator
        image: task-orchestrator:latest
        env:
        - name: DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: db-secret
              key: url
        - name: DISTRIBUTED_MODE
          value: "true"
        ports:
        - containerPort: 9090
```

## Monitoring and Alerting

### Recommended Alerts

```yaml
groups:
  - name: orchestrator
    rules:
      - alert: HighTaskFailureRate
        expr: rate(tasks_completed_total{status="failed"}[5m]) > 0.1
        annotations:
          summary: "High task failure rate detected"
      
      - alert: DeadLetterQueueGrowth
        expr: increase(tasks_dead_letter_total[1h]) > 0
        annotations:
          summary: "Tasks being moved to dead letter queue"
      
      - alert: QueueBacklog
        expr: sum(tasks_pending) > 100
        for: 5m
        annotations:
          summary: "Task queue backlog detected"
      
      - alert: WorkerDown
        expr: up{job="orchestrator"} == 0
        for: 1m
        annotations:
          summary: "Worker instance is down"
      
      - alert: NoLeader
        expr: sum(leader_status) == 0
        for: 1m
        annotations:
          summary: "No leader elected in distributed mode"
```

### Grafana Dashboards

Import the provided dashboard JSON (see `configs/grafana-dashboard.json`) or create panels for:

- Task throughput over time
- Task success/failure rates
- Queue depth by task type
- Worker pool utilization
- Task duration percentiles (p50, p95, p99)
- Leader election history

## Troubleshooting

### Tasks stuck in 'claimed' status

This happens when a worker crashes while claiming tasks. Solutions:

- **Single-instance**: Tasks are automatically recovered on restart
- **Distributed mode**: Leader automatically recovers orphaned tasks every minute

### High task failure rate

Check:
1. Handler logs for specific errors
2. Retry configuration (may need adjustment)
3. External dependencies (database, APIs)
4. Resource constraints (memory, CPU)

### No leader elected

Verify:
- All workers can connect to PostgreSQL
- `DISTRIBUTED_MODE=true` is set
- No network partitions between workers and database

### Tasks not being claimed

Check:
- Worker pool not saturated: `worker_pool_active < worker_pool_capacity`
- Tasks are in 'pending' status: `SELECT * FROM tasks WHERE status = 'pending'`
- `next_retry_at` is not in the future for pending tasks

## Performance Tuning

### Worker Pool Size

```bash
# High throughput, short tasks
WORKER_POOL_SIZE=50

# Long-running tasks
WORKER_POOL_SIZE=5
```

### Claim Interval

```bash
# High task volume
CLAIM_INTERVAL=100ms

# Low task volume, save CPU
CLAIM_INTERVAL=5s
```

### Database Connection Pool

```go
db.SetMaxOpenConns(100)
db.SetMaxIdleConns(10)
db.SetConnMaxLifetime(time.Hour)
```

## Security Considerations

1. **Database Credentials**: Use secrets management (Vault, AWS Secrets Manager)
2. **Network Isolation**: Run workers in private subnets
3. **TLS**: Enable SSL for PostgreSQL connections
4. **Least Privilege**: Use dedicated database user with minimal permissions
5. **Metrics Auth**: Protect Prometheus endpoint with authentication

## License

MIT License - see LICENSE file for details

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Support

- Documentation: This README
- Issues: GitHub Issues
- Examples: See `examples/` directory

## Changelog

### v1.0.0 (2024-01-30)

- Initial release
- Single-instance and distributed mode support
- PostgreSQL persistence
- Prometheus metrics
- Built-in handlers (NextJS, Backup)
- Exponential backoff with jitter
- Leader election
- Graceful shutdown
