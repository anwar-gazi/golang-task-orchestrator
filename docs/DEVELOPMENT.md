# Development Guide

This guide covers adding new task handlers, configuring retry behavior, and testing patterns.

---

## Adding a New Handler

### 1. Define Your Payload Structure

```go
// internal/orchestrator/handlers.go

type EmailConfig struct {
    To      string `json:"to"`
    Subject string `json:"subject"`
    Body    string `json:"body"`
}
```

### 2. Implement the Handler Function

```go
func EmailHandler(ctx context.Context, task *tasks.Task) error {
    // Parse payload
    var config EmailConfig
    if err := json.Unmarshal(task.Payload, &config); err != nil {
        return fmt.Errorf("invalid payload: %w", err)
    }

    // Validate required fields
    if config.To == "" {
        return fmt.Errorf("email 'to' address is required")
    }

    // Do the work
    slog.Info("sending email", 
        "task_id", task.ID,
        "to", config.To,
        "subject", config.Subject)

    // Simulate sending email
    if err := sendEmail(config); err != nil {
        return fmt.Errorf("failed to send email: %w", err)
    }

    slog.Info("email sent", "task_id", task.ID)
    return nil
}
```

### 3. Register the Handler

In `cmd/orchestrator/main.go`:

```go
server.HandleFunc("email:send", orchestrator.EmailHandler, orchestrator.RetryConfig{
    MaxRetries: 5,
    BaseDelay:  1 * time.Second,
    MaxDelay:   10 * time.Minute,
})
```

### 4. Enqueue Tasks

```go
payload, _ := json.Marshal(EmailConfig{
    To:      "user@example.com",
    Subject: "Welcome!",
    Body:    "Hello, world!",
})

taskID, err := server.Enqueue(ctx, "email:send", payload,
    orchestrator.WithPriority(5),                     // Higher = more urgent
    orchestrator.WithIdempotencyKey("welcome-user-123"), // Prevent duplicates
)
```

---

## Handler Function Signature

```go
type HandlerFunc func(ctx context.Context, task *tasks.Task) error
```

### Context

- **Cancellation**: Listen to `ctx.Done()` for graceful shutdown
- **Timeout**: The context does NOT have a default timeout — add one if needed
- **Values**: Do not rely on context values; use task payload

### Task Struct

```go
type Task struct {
    ID             string     // Unique task ID (UUID)
    Type           string     // Task type (matches handler registration)
    Payload        []byte     // Raw JSON payload
    Status         string     // Current status (will be "running")
    Priority       int        // Task priority
    IdempotencyKey string     // Optional dedup key
    WorkerID       string     // Worker executing this task
    Attempts       int        // Previous execution attempts
    MaxRetries     int        // Configured max retries
    LastError      string     // Error from last attempt
    CreatedAt      time.Time
    // ... timestamps
}
```

### Return Values

| Return | Effect |
|--------|--------|
| `nil` | Task marked `completed` |
| `error` (attempts < MaxRetries) | Task marked `pending` with backoff delay |
| `error` (attempts >= MaxRetries) | Task marked `dead_letter` |
| `context.Canceled` / `context.DeadlineExceeded` | Treated as failure, eligible for retry |

---

## RetryConfig

```go
type RetryConfig struct {
    MaxRetries int           // Maximum retry attempts (default: 3)
    BaseDelay  time.Duration // Initial delay (default: 1s)
    MaxDelay   time.Duration // Maximum delay cap (default: 1h)
}
```

### Backoff Formula

The orchestrator uses **exponential backoff with full jitter**:

```
delay = random(0, min(MaxDelay, 2^attempt × BaseDelay))
```

Example progression with `BaseDelay=1s`, `MaxDelay=1h`:

| Attempt | Max Delay | Actual (random) |
|---------|-----------|-----------------|
| 1 | 2s | 0-2s |
| 2 | 4s | 0-4s |
| 3 | 8s | 0-8s |
| 4 | 16s | 0-16s |
| 5 | 32s | 0-32s |
| ... | ... | ... |
| 12 | 1h (capped) | 0-1h |

### Choosing Retry Config

| Scenario | Recommendation |
|----------|----------------|
| Transient network errors | `MaxRetries: 5, BaseDelay: 1s, MaxDelay: 1m` |
| External API rate limits | `MaxRetries: 10, BaseDelay: 5s, MaxDelay: 1h` |
| Database deadlocks | `MaxRetries: 3, BaseDelay: 100ms, MaxDelay: 5s` |
| Non-retryable errors | Return error that causes immediate `dead_letter` |

---

## Best Practices

### 1. Idempotency

Handlers may execute more than once (at-least-once delivery). Design for idempotency:

```go
func PaymentHandler(ctx context.Context, task *tasks.Task) error {
    var config PaymentConfig
    json.Unmarshal(task.Payload, &config)

    // Check if already processed
    if exists, _ := checkPaymentProcessed(config.PaymentID); exists {
        slog.Info("payment already processed, skipping", 
            "payment_id", config.PaymentID)
        return nil // Idempotent: succeed without re-processing
    }

    // Process payment
    if err := processPayment(config); err != nil {
        return err
    }

    // Mark as processed
    markPaymentProcessed(config.PaymentID)
    return nil
}
```

### 2. Context Awareness

Respect context cancellation for graceful shutdown:

```go
func LongRunningHandler(ctx context.Context, task *tasks.Task) error {
    for i := 0; i < 100; i++ {
        select {
        case <-ctx.Done():
            slog.Warn("task cancelled during processing", "task_id", task.ID)
            return ctx.Err()
        default:
            processChunk(i)
        }
    }
    return nil
}
```

### 3. Structured Logging

Use `slog` with task context:

```go
slog.Info("processing started",
    "task_id", task.ID,
    "type", task.Type,
    "attempt", task.Attempts+1,
    "custom_field", myValue)
```

### 4. Error Wrapping

Wrap errors with context for debugging:

```go
result, err := callExternalAPI()
if err != nil {
    return fmt.Errorf("API call failed for user %s: %w", userID, err)
}
```

### 5. Validate Early

Fail fast on invalid payloads (no retry needed):

```go
func Handler(ctx context.Context, task *tasks.Task) error {
    var config MyConfig
    if err := json.Unmarshal(task.Payload, &config); err != nil {
        // Invalid JSON - no point retrying
        return fmt.Errorf("invalid payload: %w", err)
    }

    if config.RequiredField == "" {
        // Missing required field - permanent failure
        return fmt.Errorf("required_field is empty")
    }

    // Continue with valid config...
}
```

---

## Testing Handlers

### Unit Testing

```go
func TestEmailHandler(t *testing.T) {
    task := &tasks.Task{
        ID:   "test-123",
        Type: "email:send",
        Payload: []byte(`{"to":"test@example.com","subject":"Test"}`),
    }

    err := EmailHandler(context.Background(), task)
    if err != nil {
        t.Errorf("unexpected error: %v", err)
    }
}
```

### Integration Testing

See `tests/integration_test.go` for patterns:

```go
func TestTaskEnqueue(t *testing.T) {
    db, cleanup := setupTestDB(t)
    defer cleanup()

    server := orchestrator.NewServer(db, nil, testWorker)
    server.HandleFunc("test:simple", myHandler)

    taskID, err := server.Enqueue(ctx, "test:simple", payload)
    // Assert task exists in database
}
```

### Testing Retries

```go
func TestRetryBehavior(t *testing.T) {
    attempts := 0
    server.HandleFunc("test:retry", func(ctx context.Context, task *tasks.Task) error {
        attempts++
        if attempts < 3 {
            return fmt.Errorf("transient error")
        }
        return nil
    }, orchestrator.RetryConfig{MaxRetries: 5})

    // Enqueue and run claim loops, verify attempts == 3
}
```

---

## Task Type Naming Convention

Recommended format: `namespace:action`

| Type | Description |
|------|-------------|
| `email:send` | Send an email |
| `email:verify` | Verify email address |
| `payment:process` | Process a payment |
| `report:generate` | Generate a report |
| `service:nextjs` | Long-running NextJS server |
| `cron:daily-cleanup` | Scheduled cleanup job |
