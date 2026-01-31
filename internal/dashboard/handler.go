package dashboard

import (
	"net/http"
	"strings"
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
	mux.HandleFunc("/workers", h.HandleWorkers)
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
