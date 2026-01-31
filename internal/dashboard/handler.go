package dashboard

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Handler serves dashboard pages
type Handler struct {
	service *Service
}

// NewHandler creates a new dashboard handler
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes registers dashboard routes
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", h.HandleIndex)
	mux.HandleFunc("/tasks", h.HandleTasks)
	mux.HandleFunc("/tasks/", h.HandleTaskDetails)
	mux.HandleFunc("/tasks/run", h.HandleTaskRun)
	mux.HandleFunc("/tasks/cancel", h.HandleTaskCancel)
	mux.HandleFunc("/workers", h.HandleWorkers)
	mux.HandleFunc("/api/logs/", h.HandleTaskLogs)
}

// HandleTaskLogs handles real-time log streaming via SSE
func (h *Handler) HandleTaskLogs(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/logs/")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Get log stream
	ch, history, cleanup, err := h.service.GetTaskLogs(id)
	if err != nil {
		// Can't send JSON error easily in SSE stream start if headers set,
		// allowing simplistic error:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cleanup()

	// Send history
	for _, entry := range history {
		data := fmt.Sprintf(`{"time": "%s", "message": %q}`, entry.Time.Format(time.RFC3339), entry.Message)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	w.(http.Flusher).Flush()

	// Stream new logs
	// Create a ticker to keep connection alive
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case entry, ok := <-ch:
			if !ok {
				// Channel closed
				return
			}
			data := fmt.Sprintf(`{"time": "%s", "message": %q}`, entry.Time.Format(time.RFC3339), entry.Message)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		case <-ticker.C:
			// Keepalive comment
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// HandleTaskCancel handles task cancellation
func (h *Handler) HandleTaskCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	taskID := r.FormValue("id")
	if taskID == "" {
		http.Error(w, "Task ID is required", http.StatusBadRequest)
		return
	}

	if err := h.service.CancelTask(r.Context(), taskID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to referrer or home
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
	}
	http.Redirect(w, r, referer, http.StatusSeeOther)
}

// HandleTaskRun handles manual task execution
func (h *Handler) HandleTaskRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	taskType := r.FormValue("type")
	if taskType == "" {
		http.Error(w, "Task type is required", http.StatusBadRequest)
		return
	}

	// For now, we support empty payloads for manual runs
	// In the future, we could add a payload field to the form
	_, err := h.service.EnqueueTask(r.Context(), taskType, []byte("{}"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to home
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleIndex renders the home page
func (h *Handler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	stats, err := h.service.GetStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	recentTasks, err := h.service.GetRecentTasks(r.Context(), 5)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":       "Dashboard",
		"Stats":       stats,
		"RecentTasks": recentTasks,
		"ActivePage":  "home",
	}

	if err := Render(w, "index.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleTasks renders the task list
func (h *Handler) HandleTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.service.GetRecentTasks(r.Context(), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":      "Tasks",
		"Tasks":      tasks,
		"ActivePage": "tasks",
	}

	if err := Render(w, "tasks.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleTaskDetails renders a single task
func (h *Handler) HandleTaskDetails(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/tasks/")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	task, err := h.service.GetTask(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":      "Task " + task.ID,
		"Task":       task,
		"Payload":    string(task.Payload),
		"ActivePage": "tasks",
	}

	if err := Render(w, "task_details.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleWorkers renders the worker list
func (h *Handler) HandleWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := h.service.GetWorkers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":      "Workers",
		"Workers":    workers,
		"ActivePage": "workers",
	}

	if err := Render(w, "workers.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
