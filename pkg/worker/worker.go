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
	for {
		select {
		case <-ctx.Done():
			return
		default:
			time.Sleep(10 * time.Second)
		}
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
