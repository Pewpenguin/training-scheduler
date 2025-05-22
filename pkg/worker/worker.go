package worker

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

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
		Address:     schedulerAddr,
		GPUs:        gpus,
		Status:      pb.WorkerStatus_IDLE,
		Client:      client,
		Conn:        conn,
		ActiveTasks: make(map[string]*Task),
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
			w.mu.RUnlock()

			if len(availableGPUIDs) == 0 {
				time.Sleep(5 * time.Second)
				continue
			}

			req := &pb.TaskRequest{
				WorkerId:        w.ID,
				AvailableGpuIds: availableGPUIDs,
			}

			task, err := w.Client.RequestTask(ctx, req)
			if err != nil {
				time.Sleep(5 * time.Second)
				continue
			}

			w.executeTask(ctx, task)
		}
	}
}

func (w *Worker) executeTask(ctx context.Context, pbTask *pb.Task) {
	log.Printf("Executing task: %s", pbTask.Id)

	w.mu.Lock()
	assignedGPUIDs := make([]string, 0, pbTask.RequiredGpus)
	assignedCount := uint32(0)

	for _, gpu := range w.GPUs {
		if gpu.Available && assignedCount < pbTask.RequiredGpus {
			gpu.Available = false
			assignedGPUIDs = append(assignedGPUIDs, gpu.Id)
			assignedCount++
		}
	}

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

				w.reportTaskStatus(ctx, task)

				time.Sleep(2 * time.Second)
			}
		}

		w.mu.Lock()
		task.Status = pb.TaskStatus_COMPLETED
		task.Progress = 1.0
		w.mu.Unlock()

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

				if resp.Command != nil {
					w.handleCommand(streamCtx, resp.Command)
				}
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
			w.mu.RLock()

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
			w.mu.RUnlock()
		}
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

	case "RESUME":
		w.mu.Lock()
		w.Paused = false
		log.Printf("Worker resumed: %s", w.ID)
		w.mu.Unlock()

	case "STOP_TASK":
		taskID := command.Params["task_id"]
		if taskID != "" {
			w.stopTask(ctx, taskID)
		}

	case "UPDATE_CONFIG":
		config := command.Params["config"]
		if config != "" {
			log.Printf("Updating worker configuration: %s", config)
			// Apply configuration update logic here
		}

	default:
		log.Printf("Unknown command type: %s", command.Type)
	}
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
