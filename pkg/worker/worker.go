package worker

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/training-scheduler/pkg/metrics"
	pb "github.com/training-scheduler/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Worker struct {
	ID          string
	Address     string
	GPUs        []*pb.GPU
	Status      pb.WorkerStatus
	Client      pb.TrainingSchedulerClient
	Conn        *grpc.ClientConn
	ActiveTasks map[string]*Task
	mu          sync.RWMutex
	Paused      bool

	MetricFrequency time.Duration
	TaskPriority    map[string]int
	GPUAllocation   string
	metrics         *metrics.WorkerMetrics
}

type Task struct {
	ID            string
	Name          string
	Configuration []byte
	Status        pb.TaskStatus
	Progress      float32
	GPUIDs        []string
	Metrics       []*pb.Metric
	DoneCh        chan struct{}
}

func NewWorker(schedulerAddr string, gpus []*pb.GPU) (*Worker, error) {
	conn, err := grpc.Dial(schedulerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to scheduler: %v", err)
	}

	client := pb.NewTrainingSchedulerClient(conn)

	return &Worker{
		Address:         schedulerAddr,
		GPUs:            gpus,
		Status:          pb.WorkerStatus_IDLE,
		Client:          client,
		Conn:            conn,
		ActiveTasks:     make(map[string]*Task),
		MetricFrequency: 2 * time.Second,
		TaskPriority:    make(map[string]int),
		GPUAllocation:   "packed",
	}, nil
}

func (w *Worker) Register(ctx context.Context) error {
	req := &pb.RegisterWorkerRequest{
		WorkerId: w.ID,
		Gpus:     w.GPUs,
		Address:  w.Address,
	}

	resp, err := w.Client.RegisterWorker(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to register worker: %v", err)
	}

	if !resp.Success {
		return fmt.Errorf("registration failed: %s", resp.Message)
	}

	w.ID = resp.AssignedId
	log.Printf("Worker registered with ID: %s", w.ID)
	return nil
}

func (w *Worker) Start(ctx context.Context) error {
	if err := w.Register(ctx); err != nil {
		return err
	}

	go w.pollForTasks(ctx)

	go w.reportStatus(ctx)

	return nil
}

func (w *Worker) pollForTasks(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			w.mu.RLock()
			if w.Paused {
				w.mu.RUnlock()
				log.Printf("Worker is paused, not requesting new tasks")
				time.Sleep(5 * time.Second)
				continue
			}

			availableGPUIDs := make([]string, 0)
			for _, gpu := range w.GPUs {
				if gpu.Available {
					availableGPUIDs = append(availableGPUIDs, gpu.Id)
				}
			}

			taskPriorities := w.TaskPriority
			w.mu.RUnlock()

			if len(availableGPUIDs) == 0 {
				time.Sleep(5 * time.Second)
				continue
			}

			req := &pb.TaskRequest{
				WorkerId:        w.ID,
				AvailableGpuIds: availableGPUIDs,
			}

			if len(taskPriorities) > 0 {
				priorityInfo := make(map[string]string)
				for taskType, priority := range taskPriorities {
					priorityInfo[taskType] = fmt.Sprintf("%d", priority)
				}

				req.PriorityInfo = priorityInfo
			}

			task, err := w.Client.RequestTask(ctx, req)
			if err != nil {
				time.Sleep(5 * time.Second)
				continue
			}

			if priority, exists := taskPriorities[task.Name]; exists {
				log.Printf("Task %s has priority %d", task.Name, priority)
			}

			w.executeTask(ctx, task)
		}
	}
}

func (w *Worker) executeTask(ctx context.Context, pbTask *pb.Task) {
	log.Printf("Executing task: %s", pbTask.Id)

	w.mu.Lock()
	assignedGPUIDs := w.allocateGPUs(pbTask)

	task := &Task{
		ID:            pbTask.Id,
		Name:          pbTask.Name,
		Configuration: pbTask.Configuration,
		Status:        pb.TaskStatus_RUNNING,
		Progress:      0.0,
		GPUIDs:        assignedGPUIDs,
		Metrics:       make([]*pb.Metric, 0),
		DoneCh:        make(chan struct{}),
	}

	w.Status = pb.WorkerStatus_BUSY
	w.ActiveTasks[task.ID] = task
	w.mu.Unlock()

	w.recordTaskStart(task)
	w.reportTaskStatus(ctx, task)

	go func() {
		for progress := 0.0; progress <= 1.0; progress += 0.05 {
			select {
			case <-ctx.Done():
				w.mu.Lock()
				task.Status = pb.TaskStatus_FAILED
				task.Progress = float32(progress)
				w.mu.Unlock()

				w.reportTaskStatus(ctx, task)
				return
			default:
				w.mu.RLock()
				paused := w.Paused
				w.mu.RUnlock()

				if paused {
					log.Printf("Task %s paused at %.1f%%", task.ID, float32(progress)*100)
					time.Sleep(5 * time.Second)
					continue
				}

				w.mu.Lock()
				task.Progress = float32(progress)

				task.Metrics = append(task.Metrics, &pb.Metric{
					Name:      "loss",
					Value:     float32(1.0 - progress),
					Timestamp: uint64(time.Now().Unix()),
				})
				w.mu.Unlock()

				w.recordTaskProgress(task)
				w.reportTaskStatus(ctx, task)

				w.mu.RLock()
				metricFreq := w.MetricFrequency
				w.mu.RUnlock()
				time.Sleep(metricFreq)
			}
		}

		w.mu.Lock()
		task.Status = pb.TaskStatus_COMPLETED
		task.Progress = 1.0
		w.mu.Unlock()

		w.recordTaskCompletion(task)
		w.reportTaskStatus(ctx, task)

		w.mu.Lock()
		delete(w.ActiveTasks, task.ID)

		for _, gpu := range w.GPUs {
			for _, id := range task.GPUIDs {
				if gpu.Id == id {
					gpu.Available = true
				}
			}
		}

		if len(w.ActiveTasks) == 0 {
			w.Status = pb.WorkerStatus_IDLE
		}
		w.mu.Unlock()

		close(task.DoneCh)
	}()
}

func (w *Worker) reportTaskStatus(ctx context.Context, task *Task) {
	w.mu.RLock()
	update := &pb.TaskStatusUpdate{
		TaskId:   task.ID,
		WorkerId: w.ID,
		Status:   task.Status,
		Progress: task.Progress,
		Message:  fmt.Sprintf("Task %s progress: %.2f%%", task.ID, task.Progress*100),
		Metrics:  task.Metrics,
	}
	w.mu.RUnlock()

	_, err := w.Client.ReportTaskStatus(ctx, update)
	if err != nil {
		log.Printf("Failed to report task status: %v", err)
	}
}

func (w *Worker) reportStatus(ctx context.Context) {
	var streamCancel context.CancelFunc
	var streamCtx context.Context
	var streamMutex sync.Mutex

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	establishStream := func() {
		streamMutex.Lock()
		defer streamMutex.Unlock()

		if streamCancel != nil {
			streamCancel()
		}

		streamCtx, streamCancel = context.WithCancel(ctx)

		req := &pb.WorkerStatusRequest{
			WorkerId: w.ID,
		}

		stream, err := w.Client.MonitorWorker(streamCtx, req)
		if err != nil {
			log.Printf("Failed to establish worker status monitoring: %v", err)
			return
		}

		log.Printf("Worker %s status monitoring established", w.ID)

		go func() {
			defer streamCancel()

			for {
				resp, err := stream.Recv()
				if err != nil {
					log.Printf("Error receiving from status stream: %v", err)
					return
				}

				log.Printf("Received status update from scheduler for worker %s", resp.WorkerId)

				w.processStatusUpdate(streamCtx, resp)

				if resp.Command != nil {
					w.handleCommand(streamCtx, resp.Command)
				}

				w.sendStatusUpdate()
			}
		}()
	}

	establishStream()

	reconnectTicker := time.NewTicker(2 * time.Minute)
	defer reconnectTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			streamMutex.Lock()
			if streamCancel != nil {
				streamCancel()
			}
			streamMutex.Unlock()
			return

		case <-reconnectTicker.C:
			establishStream()

		case <-ticker.C:
			w.sendStatusUpdate()
		}
	}
}

func (w *Worker) sendStatusUpdate() {
	w.mu.RLock()
	defer w.mu.RUnlock()

	activeTasks := make([]string, 0, len(w.ActiveTasks))
	for id, task := range w.ActiveTasks {
		activeTasks = append(activeTasks, fmt.Sprintf("%s(%.1f%%)", id, task.Progress*100))
	}

	availableGPUs := 0
	totalGPUs := len(w.GPUs)
	for _, gpu := range w.GPUs {
		if gpu.Available {
			availableGPUs++
		}
	}

	log.Printf("Worker status: ID=%s, Status=%v, GPUs=%d/%d available",
		w.ID, w.Status, availableGPUs, totalGPUs)

	if len(activeTasks) > 0 {
		log.Printf("Worker %s active tasks: %v", w.ID, activeTasks)
	}
}

func (w *Worker) handleCommand(ctx context.Context, command *pb.WorkerCommand) {
	log.Printf("Received command: %s", command.Type)

	switch command.Type {
	case "PAUSE":
		w.mu.Lock()
		w.Paused = true
		log.Printf("Worker paused: %s", w.ID)
		w.mu.Unlock()
		w.recordStatusChange()

	case "RESUME":
		w.mu.Lock()
		w.Paused = false
		log.Printf("Worker resumed: %s", w.ID)
		w.mu.Unlock()
		w.recordStatusChange()

	case "STOP_TASK":
		taskID := command.Params["task_id"]
		if taskID != "" {
			w.stopTask(ctx, taskID)
		}

	case "UPDATE_CONFIG":
		config := command.Params["config"]
		if config != "" {
			log.Printf("Updating worker configuration: %s", config)

			if metricFreq, exists := command.Params["metric_frequency"]; exists {
				log.Printf("Updating metric collection frequency: %s", metricFreq)
				w.updateMetricFrequency(metricFreq)
			}

			if taskPriority, exists := command.Params["task_priority"]; exists {
				log.Printf("Updating task priority settings: %s", taskPriority)
				w.updateTaskPrioritySettings(taskPriority)
			}

			if gpuAllocation, exists := command.Params["gpu_allocation"]; exists {
				log.Printf("Updating GPU allocation strategy: %s", gpuAllocation)
				w.updateGPUAllocationStrategy(gpuAllocation)
			}
		}

	case "SYNC_STATE":
		log.Printf("Synchronizing worker state with scheduler")
		go w.reportStatus(ctx)

	default:
		log.Printf("Unknown command type: %s", command.Type)
	}
}

func (w *Worker) processStatusUpdate(ctx context.Context, resp *pb.WorkerStatusResponse) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(resp.Gpus) > 0 {
		log.Printf("Synchronizing GPU status with scheduler's view")
		schedulerGPUs := make(map[string]*pb.GPU)
		for _, gpu := range resp.Gpus {
			schedulerGPUs[gpu.Id] = gpu
		}

		for i, gpu := range w.GPUs {
			if schedulerGPU, exists := schedulerGPUs[gpu.Id]; exists {
				gpuInUse := false
				for _, task := range w.ActiveTasks {
					for _, gpuID := range task.GPUIDs {
						if gpuID == gpu.Id {
							gpuInUse = true
							break
						}
					}
					if gpuInUse {
						break
					}
				}

				if !gpuInUse {
					oldAvailable := w.GPUs[i].Available
					w.GPUs[i].Available = schedulerGPU.Available
					if oldAvailable != w.GPUs[i].Available {
						log.Printf("GPU %s availability changed: %v -> %v",
							gpu.Id, oldAvailable, w.GPUs[i].Available)
					}
				}
			}
		}
	}

	if resp.Status != w.Status {
		log.Printf("Worker status updated: %v -> %v", w.Status, resp.Status)
		w.Status = resp.Status
		w.recordStatusChange()
	}

	if len(resp.ActiveTasks) > 0 {
		schedulerTasks := make(map[string]*pb.TaskSummary)
		for _, task := range resp.ActiveTasks {
			schedulerTasks[task.Id] = task
		}

		for taskID, taskSummary := range schedulerTasks {
			if _, exists := w.ActiveTasks[taskID]; !exists {
				log.Printf("Task %s exists in scheduler but not in worker, will request it", taskID)
			} else {
				w.updateTaskPriority(taskID, taskSummary)
			}
		}

		for taskID, task := range w.ActiveTasks {
			if _, exists := schedulerTasks[taskID]; !exists {
				log.Printf("Task %s exists in worker but not in scheduler, marking as canceled", taskID)
				task.Status = pb.TaskStatus_CANCELED
				go func(taskID string, task *Task) {
					w.mu.Lock()
					delete(w.ActiveTasks, taskID)
					for _, gpu := range w.GPUs {
						for _, id := range task.GPUIDs {
							if gpu.Id == id {
								gpu.Available = true
							}
						}
					}
					w.mu.Unlock()
					w.reportTaskStatus(ctx, task)
				}(taskID, task)
			}
		}
	}

}

func (w *Worker) updateTaskPriority(taskID string, schedulerTask *pb.TaskSummary) {
	task, exists := w.ActiveTasks[taskID]
	if !exists {
		return
	}

	if task.Status != schedulerTask.Status {
		log.Printf("Task %s status updated from scheduler: %v -> %v",
			taskID, task.Status, schedulerTask.Status)
		task.Status = schedulerTask.Status

		if schedulerTask.Status == pb.TaskStatus_COMPLETED ||
			schedulerTask.Status == pb.TaskStatus_FAILED ||
			schedulerTask.Status == pb.TaskStatus_CANCELED {
			go func(taskID string) {
				w.mu.Lock()
				delete(w.ActiveTasks, taskID)
				for _, gpu := range w.GPUs {
					for _, id := range task.GPUIDs {
						if gpu.Id == id {
							gpu.Available = true
						}
					}
				}
				w.mu.Unlock()
			}(taskID)
		}
	}

	if abs(float64(task.Progress-schedulerTask.Progress)) > 0.05 {
		log.Printf("Task %s progress synced with scheduler: %.2f -> %.2f",
			taskID, task.Progress, schedulerTask.Progress)
		task.Progress = schedulerTask.Progress
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func (w *Worker) stopTask(ctx context.Context, taskID string) {
	w.mu.Lock()
	task, exists := w.ActiveTasks[taskID]
	if exists {
		task.Status = pb.TaskStatus_CANCELED
		log.Printf("Task stopped: %s", taskID)
	}
	w.mu.Unlock()

	if exists {
		w.reportTaskStatus(ctx, task)
	}
}

func (w *Worker) Stop() {
	if w.Conn != nil {
		w.Conn.Close()
	}

	w.mu.RLock()
	for _, task := range w.ActiveTasks {
		<-task.DoneCh
	}
	w.mu.RUnlock()

	log.Println("Worker stopped")
}

func (w *Worker) updateMetricFrequency(freqStr string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	freq, err := time.ParseDuration(freqStr)
	if err != nil {
		log.Printf("Invalid metric frequency format: %v", err)
		return
	}

	if freq < 100*time.Millisecond || freq > time.Minute {
		log.Printf("Metric frequency out of range (100ms-1m): %s", freqStr)
		return
	}

	log.Printf("Setting metric frequency to %v", freq)
	w.MetricFrequency = freq

	for _, task := range w.ActiveTasks {
		log.Printf("Applied new metric frequency to task %s", task.ID)
	}
}

func (w *Worker) updateTaskPrioritySettings(priorityStr string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	priorities := make(map[string]int)
	for _, pair := range strings.Split(priorityStr, ",") {
		parts := strings.Split(pair, "=")
		if len(parts) != 2 {
			continue
		}

		taskType := strings.TrimSpace(parts[0])
		priority, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			log.Printf("Invalid priority value for %s: %v", taskType, err)
			continue
		}

		priorities[taskType] = priority
	}

	if len(priorities) > 0 {
		log.Printf("Setting task priorities: %v", priorities)
		w.TaskPriority = priorities
	}
}

func (w *Worker) updateGPUAllocationStrategy(strategy string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	validStrategies := map[string]bool{
		"packed":      true,
		"spread":      true,
		"memory":      true,
		"performance": true,
	}

	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if !validStrategies[strategy] {
		log.Printf("Invalid GPU allocation strategy: %s", strategy)
		return
	}

	log.Printf("Setting GPU allocation strategy to %s", strategy)
	w.GPUAllocation = strategy
}

func (w *Worker) allocateGPUs(task *pb.Task) []string {
	assignedGPUIDs := make([]string, 0, task.RequiredGpus)
	availableGPUs := make([]*pb.GPU, 0)

	for _, gpu := range w.GPUs {
		if gpu.Available {
			availableGPUs = append(availableGPUs, gpu)
		}
	}

	if uint32(len(availableGPUs)) < task.RequiredGpus {
		log.Printf("Not enough available GPUs for task %s", task.Id)
		return assignedGPUIDs
	}

	switch w.GPUAllocation {
	case "packed":
		for i := 0; i < int(task.RequiredGpus) && i < len(availableGPUs); i++ {
			availableGPUs[i].Available = false
			assignedGPUIDs = append(assignedGPUIDs, availableGPUs[i].Id)
		}

	case "spread":
		for i, gpu := range availableGPUs {
			if i%2 == 0 && uint32(len(assignedGPUIDs)) < task.RequiredGpus {
				gpu.Available = false
				assignedGPUIDs = append(assignedGPUIDs, gpu.Id)
			}
		}

		for i, gpu := range availableGPUs {
			if i%2 == 1 && uint32(len(assignedGPUIDs)) < task.RequiredGpus {
				gpu.Available = false
				assignedGPUIDs = append(assignedGPUIDs, gpu.Id)
			}
		}

	case "memory":
		sort.Slice(availableGPUs, func(i, j int) bool {
			return availableGPUs[i].MemoryMb > availableGPUs[j].MemoryMb
		})
		for i := 0; i < int(task.RequiredGpus) && i < len(availableGPUs); i++ {
			availableGPUs[i].Available = false
			assignedGPUIDs = append(assignedGPUIDs, availableGPUs[i].Id)
		}

	case "performance":
		sort.Slice(availableGPUs, func(i, j int) bool {
			if availableGPUs[i].CudaCores > 0 && availableGPUs[j].CudaCores > 0 {
				return availableGPUs[i].CudaCores > availableGPUs[j].CudaCores
			}
			return availableGPUs[i].MemoryBandwidth > availableGPUs[j].MemoryBandwidth
		})
		for i := 0; i < int(task.RequiredGpus) && i < len(availableGPUs); i++ {
			availableGPUs[i].Available = false
			assignedGPUIDs = append(assignedGPUIDs, availableGPUs[i].Id)
		}

	default:
		for i := 0; i < int(task.RequiredGpus) && i < len(availableGPUs); i++ {
			availableGPUs[i].Available = false
			assignedGPUIDs = append(assignedGPUIDs, availableGPUs[i].Id)
		}
	}

	log.Printf("Allocated %d GPUs to task %s using '%s' strategy", len(assignedGPUIDs), task.Id, w.GPUAllocation)
	return assignedGPUIDs
}
