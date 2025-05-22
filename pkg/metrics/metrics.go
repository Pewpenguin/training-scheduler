package metrics

import (
	"net/http"
	"sync"

	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	pb "github.com/training-scheduler/proto"
)

var (
	totalTasks = promauto.NewCounter(prometheus.CounterOpts{
		Name: "scheduler_tasks_total",
		Help: "The total number of tasks submitted to the scheduler",
	})

	activeTasks = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "scheduler_tasks_active",
		Help: "The number of currently active tasks",
	})

	pendingTasks = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "scheduler_tasks_pending",
		Help: "The number of pending tasks waiting for assignment",
	})

	completedTasks = promauto.NewCounter(prometheus.CounterOpts{
		Name: "scheduler_tasks_completed",
		Help: "The total number of completed tasks",
	})

	failedTasks = promauto.NewCounter(prometheus.CounterOpts{
		Name: "scheduler_tasks_failed",
		Help: "The total number of failed tasks",
	})

	taskDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scheduler_task_duration_seconds",
			Help:    "The duration of tasks in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"task_name"},
	)

	activeWorkers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "scheduler_workers_active",
		Help: "The number of active workers",
	})

	workerGPUUtilization = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "worker_gpu_utilization",
			Help: "GPU utilization percentage per worker and GPU",
		},
		[]string{"worker_id", "gpu_id"},
	)

	workerGPUMemoryUsage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "worker_gpu_memory_usage_mb",
			Help: "GPU memory usage in MB per worker and GPU",
		},
		[]string{"worker_id", "gpu_id"},
	)

	workerTaskProgress = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "worker_task_progress",
			Help: "Task progress percentage per worker and task",
		},
		[]string{"worker_id", "task_id", "task_name"},
	)

	workerStatus = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "worker_status",
			Help: "Worker status (0=IDLE, 1=BUSY, 2=OFFLINE, 3=ERROR)",
		},
		[]string{"worker_id"},
	)
)

type MetricsServer struct {
	server *http.Server
	mu     sync.Mutex
}

func NewMetricsServer(addr string) *MetricsServer {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return &MetricsServer{
		server: server,
	}
}

func (ms *MetricsServer) Start() error {
	return ms.server.ListenAndServe()
}

func (ms *MetricsServer) Stop() error {
	return ms.server.Close()
}

type SchedulerMetrics struct{}

func NewSchedulerMetrics() *SchedulerMetrics {
	return &SchedulerMetrics{}
}

func (sm *SchedulerMetrics) IncrementTotalTasks() {
	totalTasks.Inc()
}

func (sm *SchedulerMetrics) SetActiveTasks(count int) {
	activeTasks.Set(float64(count))
}

func (sm *SchedulerMetrics) SetPendingTasks(count int) {
	pendingTasks.Set(float64(count))
}

func (sm *SchedulerMetrics) IncrementCompletedTasks() {
	completedTasks.Inc()
}

func (sm *SchedulerMetrics) IncrementFailedTasks() {
	failedTasks.Inc()
}

func (sm *SchedulerMetrics) ObserveTaskDuration(taskName string, durationSeconds float64) {
	taskDuration.WithLabelValues(taskName).Observe(durationSeconds)
}

func (sm *SchedulerMetrics) SetActiveWorkers(count int) {
	activeWorkers.Set(float64(count))
}

type WorkerMetrics struct {
	WorkerID string
}

func NewWorkerMetrics(workerID string) *WorkerMetrics {
	return &WorkerMetrics{
		WorkerID: workerID,
	}
}

func (wm *WorkerMetrics) SetGPUUtilization(gpuID string, utilization float64) {
	workerGPUUtilization.WithLabelValues(wm.WorkerID, gpuID).Set(utilization)
}

func (wm *WorkerMetrics) SetGPUMemoryUsage(gpuID string, memoryMB float64) {
	workerGPUMemoryUsage.WithLabelValues(wm.WorkerID, gpuID).Set(memoryMB)
}

func (wm *WorkerMetrics) SetTaskProgress(taskID, taskName string, progress float64) {
	workerTaskProgress.WithLabelValues(wm.WorkerID, taskID, taskName).Set(progress)
}

func (wm *WorkerMetrics) SetWorkerStatus(status pb.WorkerStatus) {
	workerStatus.WithLabelValues(wm.WorkerID).Set(float64(status))
}

func (wm *WorkerMetrics) UpdateMetricsFromTaskStatus(update *pb.TaskStatusUpdate) {
	if update == nil {
		return
	}

	wm.SetTaskProgress(update.TaskId, "", float64(update.Progress))

	for _, metric := range update.Metrics {
		var gpuID string
		if strings.HasPrefix(metric.Name, "gpu_utilization_") {
			gpuID = strings.TrimPrefix(metric.Name, "gpu_utilization_")
			if gpuID != "" && gpuID != metric.Name {
				wm.SetGPUUtilization(gpuID, float64(metric.Value))
			}
		} else if strings.HasPrefix(metric.Name, "gpu_memory_usage_") {
			gpuID = strings.TrimPrefix(metric.Name, "gpu_memory_usage_")
			if gpuID != "" && gpuID != metric.Name {
				wm.SetGPUMemoryUsage(gpuID, float64(metric.Value))
			}
		}
	}
}
