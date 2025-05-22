package worker

import (
	"github.com/training-scheduler/pkg/metrics"
	pb "github.com/training-scheduler/proto"
)

func (w *Worker) SetMetrics(metrics *metrics.WorkerMetrics) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.metrics = metrics
}

func (w *Worker) updateMetrics() {
	if w.metrics == nil {
		return
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	w.metrics.SetWorkerStatus(w.Status)

	for _, gpu := range w.GPUs {
		utilization := 0.0
		memoryUsage := 0.0

		if !gpu.Available {
			for _, task := range w.ActiveTasks {
				for _, gpuID := range task.GPUIDs {
					if gpuID == gpu.Id {

						for _, metric := range task.Metrics {

							if metric.Name == "gpu_utilization" {
								utilization = float64(metric.Value)
							}
							if metric.Name == "gpu_memory_usage" {
								memoryUsage = float64(metric.Value)
							}
						}
					}
				}
			}
		}

		w.metrics.SetGPUUtilization(gpu.Id, utilization)
		w.metrics.SetGPUMemoryUsage(gpu.Id, memoryUsage)
	}

	for taskID, task := range w.ActiveTasks {
		w.metrics.SetTaskProgress(taskID, task.Name, float64(task.Progress))
	}
}

func (w *Worker) recordTaskStart(task *Task) {
	if w.metrics == nil {
		return
	}

	w.metrics.SetTaskProgress(task.ID, task.Name, 0.0)
	w.updateMetrics()
}

func (w *Worker) recordTaskProgress(task *Task) {
	if w.metrics == nil {
		return
	}

	w.metrics.SetTaskProgress(task.ID, task.Name, float64(task.Progress))

	if len(task.Metrics) > 0 {
		taskUpdate := &pb.TaskStatusUpdate{
			TaskId:   task.ID,
			WorkerId: w.ID,
			Status:   task.Status,
			Progress: task.Progress,
			Metrics:  task.Metrics,
		}
		w.metrics.UpdateMetricsFromTaskStatus(taskUpdate)
	}

	w.updateMetrics()
}

func (w *Worker) recordTaskCompletion(task *Task) {
	if w.metrics == nil {
		return
	}

	w.metrics.SetTaskProgress(task.ID, task.Name, float64(task.Progress))
	w.updateMetrics()
}

func (w *Worker) recordStatusChange() {
	if w.metrics == nil {
		return
	}

	w.metrics.SetWorkerStatus(w.Status)
	w.updateMetrics()
}
