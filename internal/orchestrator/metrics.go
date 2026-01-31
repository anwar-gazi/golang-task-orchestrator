package orchestrator

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all Prometheus metrics
type Metrics struct {
	// Counters
	tasksEnqueued   *prometheus.CounterVec
	tasksCompleted  *prometheus.CounterVec
	tasksRetried    *prometheus.CounterVec
	tasksDeadLetter *prometheus.CounterVec

	// Gauges
	tasksPending    *prometheus.GaugeVec
	tasksRunning    *prometheus.GaugeVec
	workersActive   prometheus.Gauge
	workersCapacity prometheus.Gauge
	workersTotal    prometheus.Gauge
	leaderStatus    *prometheus.GaugeVec

	// Histograms
	taskDuration  *prometheus.HistogramVec
	claimDuration prometheus.Histogram
}

// NewMetrics creates and registers all Prometheus metrics
func NewMetrics() *Metrics {
	m := &Metrics{
		tasksEnqueued: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tasks_enqueued_total",
				Help: "Total number of tasks enqueued",
			},
			[]string{"type"},
		),
		tasksCompleted: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tasks_completed_total",
				Help: "Total number of tasks completed",
			},
			[]string{"type", "status"},
		),
		tasksRetried: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "task_retries_total",
				Help: "Total number of task retries",
			},
			[]string{"type", "attempt"},
		),
		tasksDeadLetter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tasks_dead_letter_total",
				Help: "Total number of tasks moved to dead letter queue",
			},
			[]string{"type"},
		),
		tasksPending: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "tasks_pending",
				Help: "Current number of pending tasks",
			},
			[]string{"type"},
		),
		tasksRunning: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "tasks_running",
				Help: "Current number of running tasks",
			},
			[]string{"type"},
		),
		workersActive: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "worker_pool_active",
				Help: "Number of active workers",
			},
		),
		workersCapacity: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "worker_pool_capacity",
				Help: "Total worker pool capacity",
			},
		),
		workersTotal: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "workers_total",
				Help: "Total number of registered workers (distributed mode)",
			},
		),
		leaderStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "leader_status",
				Help: "Leader status (1 if leader, 0 otherwise)",
			},
			[]string{"worker_id"},
		),
		taskDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "task_duration_seconds",
				Help:    "Task execution duration in seconds",
				Buckets: []float64{.1, .5, 1, 2.5, 5, 10, 30, 60, 300, 600},
			},
			[]string{"type"},
		),
		claimDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "task_claim_duration_seconds",
				Help:    "Time to claim tasks from database",
				Buckets: prometheus.DefBuckets,
			},
		),
	}

	// Register all metrics
	prometheus.MustRegister(
		m.tasksEnqueued,
		m.tasksCompleted,
		m.tasksRetried,
		m.tasksDeadLetter,
		m.tasksPending,
		m.tasksRunning,
		m.workersActive,
		m.workersCapacity,
		m.workersTotal,
		m.leaderStatus,
		m.taskDuration,
		m.claimDuration,
	)

	return m
}
