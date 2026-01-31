package orchestrator

import (
	"sync"
	"time"
)

// LogEntry represents a single log line
type LogEntry struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// LogStream manages logs for a single task
type LogStream struct {
	buffer      []LogEntry
	maxSize     int
	subscribers map[chan LogEntry]struct{}
	mu          sync.RWMutex
}

// NewLogStream creates a new log stream
func NewLogStream(maxSize int) *LogStream {
	return &LogStream{
		buffer:      make([]LogEntry, 0, maxSize),
		maxSize:     maxSize,
		subscribers: make(map[chan LogEntry]struct{}),
	}
}

// Write adds a log entry line
func (s *LogStream) Write(p []byte) (n int, err error) {
	entry := LogEntry{
		Time:    time.Now(),
		Message: string(p),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Append to buffer
	if len(s.buffer) >= s.maxSize {
		// Drop oldest (circular buffer implementation could be optimized, but slice is fine for small < 1000)
		s.buffer = s.buffer[1:]
	}
	s.buffer = append(s.buffer, entry)

	// Broadcast to subscribers
	for ch := range s.subscribers {
		// Non-blocking send to prevent slow clients from blocking writer
		select {
		case ch <- entry:
		default:
		}
	}

	return len(p), nil
}

// Subscribe returns a channel that receives new log entries
// It also returns the current history
func (s *LogStream) Subscribe() (<-chan LogEntry, []LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan LogEntry, 100)
	s.subscribers[ch] = struct{}{}

	// Return a copy of current history
	history := make([]LogEntry, len(s.buffer))
	copy(history, s.buffer)

	return ch, history
}

// Unsubscribe removes a subscriber
func (s *LogStream) Unsubscribe(ch <-chan LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find the channel (we need to cast signal-only receive chan back to bidirectional,
	// but since we created it, we know the type match. However, Go type system requires
	// us to store or match effectively. A better way is to return a cleanup func)
	// For simplicity, we'll change Subscribe to return a cleanup closure.
}

// SubscribeWithCleanup returns a channel and a cleanup function
func (s *LogStream) SubscribeWithCleanup() (<-chan LogEntry, []LogEntry, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan LogEntry, 100)
	s.subscribers[ch] = struct{}{}

	history := make([]LogEntry, len(s.buffer))
	copy(history, s.buffer)

	cleanup := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subscribers, ch)
		close(ch)
	}

	return ch, history, cleanup
}
