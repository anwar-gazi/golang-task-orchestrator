# Quick Start Guide

## Getting Started in 5 Minutes

### 1. Prerequisites
- Docker and Docker Compose installed
- 2GB free disk space

### 2. Start the Orchestrator

```bash
# Navigate to the project directory
cd task-orchestrator

# Copy environment variables
cp .env.example .env

# Start services
docker-compose up -d

# Check status
docker-compose ps

# View logs
docker-compose logs -f orchestrator
```

### 3. Verify It's Running

Open your browser and visit:
- Health Check: http://localhost:9090/health
- Metrics: http://localhost:9090/metrics

You should see a JSON response like:
```json
{
  "status": "healthy",
  "worker_id": "hostname-1234-abc123",
  "version": "1.0.0",
  "distributed": false
}
```

### 4. Create a Custom Handler

Edit `cmd/orchestrator/main.go` and add:

```go
// Register custom handler
server.HandleFunc("email:send", func(ctx context.Context, task *tasks.Task) error {
    var data map[string]string
    json.Unmarshal(task.Payload, &data)
    
    slog.Info("sending email", 
        "to", data["to"],
        "subject", data["subject"])
    
    // Your email sending logic here
    time.Sleep(2 * time.Second)
    
    return nil
}, orchestrator.RetryConfig{
    MaxRetries: 3,
    BaseDelay:  1 * time.Second,
    MaxDelay:   1 * time.Minute,
})
```

### 5. Enqueue a Task

Add this to your application code:

```go
payload, _ := json.Marshal(map[string]string{
    "to":      "user@example.com",
    "subject": "Welcome!",
    "body":    "Thanks for signing up",
})

taskID, err := server.Enqueue(ctx, "email:send", payload,
    orchestrator.WithPriority(10))

if err != nil {
    log.Printf("Failed to enqueue task: %v", err)
} else {
    log.Printf("Task enqueued: %s", taskID)
}
```

### 6. Monitor Tasks

Watch the orchestrator logs:
```bash
docker-compose logs -f orchestrator
```

You should see:
```
INFO task enqueued task_id=abc-123 type=email:send priority=10
INFO sending email to=user@example.com subject=Welcome!
INFO task completed task_id=abc-123 type=email:send attempts=1
```

### 7. View Metrics

```bash
# Get all metrics
curl http://localhost:9090/metrics

# Or filter specific metrics
curl http://localhost:9090/metrics | grep tasks_enqueued_total
```

## Next Steps

### Try Distributed Mode

```bash
# Stop single instance
docker-compose down

# Start 3 workers in distributed mode
docker-compose -f docker-compose.distributed.yml up -d

# Check all workers
curl http://localhost:9091/health  # Worker 1
curl http://localhost:9092/health  # Worker 2
curl http://localhost:9093/health  # Worker 3
```

### Add Monitoring

```bash
# Start with Prometheus and Grafana
docker-compose --profile monitoring up -d

# Access services
# Orchestrator: http://localhost:9090
# Prometheus: http://localhost:9091
# Grafana: http://localhost:3000 (admin/admin)
```

### Explore the Database

```bash
# Connect to PostgreSQL
docker-compose exec postgres psql -U postgres -d orchestrator

# View pending tasks
SELECT id, type, status, priority, created_at FROM tasks WHERE status = 'pending';

# View completed tasks
SELECT id, type, status, attempts, completed_at FROM tasks WHERE status = 'completed';

# View workers (distributed mode)
SELECT id, hostname, last_heartbeat, is_leader FROM workers;
```

## Common Commands

```bash
# View logs
make docker-logs

# Restart services
docker-compose restart

# Stop services
docker-compose down

# Clean up everything
docker-compose down -v

# Build from scratch
make docker-build
make docker-up
```

## Troubleshooting

**Services won't start:**
```bash
# Check if ports are already in use
lsof -i :5433  # PostgreSQL
lsof -i :9090  # Orchestrator

# View detailed logs
docker-compose logs
```

**Can't connect to database:**
```bash
# Check PostgreSQL is healthy
docker-compose ps postgres

# Test connection
docker-compose exec postgres pg_isready
```

**Tasks not being processed:**
```bash
# Check worker logs
docker-compose logs orchestrator

# Verify database has tasks
docker-compose exec postgres psql -U postgres -d orchestrator \
  -c "SELECT COUNT(*), status FROM tasks GROUP BY status;"
```

## Example Use Cases

### 1. Background Email Sending
Process email tasks with retry logic for transient failures.

### 2. Database Backups
Schedule periodic backups with automatic cleanup of old files.

### 3. Image Processing
Resize, compress, or transform uploaded images asynchronously.

### 4. Webhook Delivery
Deliver webhooks with exponential backoff for failed attempts.

### 5. Report Generation
Generate large reports in the background and notify when complete.

### 6. Data Synchronization
Sync data between systems with idempotency to prevent duplicates.

## Learn More

- **Full Documentation**: See `README.md`
- **API Reference**: See `internal/orchestrator/` package docs
- **Examples**: See `tests/` directory
- **Configuration**: See `.env.example`

## Get Help

- Check the `README.md` for detailed documentation
- Review logs with `make docker-logs`
- Inspect the database directly
- Check Prometheus metrics at http://localhost:9090/metrics
