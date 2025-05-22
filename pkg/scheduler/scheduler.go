package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/training-scheduler/pkg/metrics"
	pb "github.com/training-scheduler/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Scheduler struct {
	pb.UnimplementedTrainingSchedulerServer

	mu             sync.RWMutex
	workers        map[string]*Worker
	tasks          map[string]*Task
	pendingTasks   []*Task
	workloadPolicy WorkloadPolicy
	metrics        *metrics.SchedulerMetrics
}

type Worker struct {
	ID      string
	GPUs    []*GPU
	Address string
	Status  pb.WorkerStatus
	Tasks   map[string]*Task
}

type GPU struct {
	ID        string
	Model     string
	MemoryMB  uint64
	Available bool
}

type Task struct {
	ID            string
	Name          string
	RequiredGPUs  uint32
	MinGPUMemory  uint64
	Configuration []byte
	Status        pb.TaskStatus
	WorkerID      string
	AssignedGPUs  []string
	StartTime     time.Time
	Progress      float32
	Metrics       []*pb.Metric
}

type WorkloadPolicy interface {
	AssignTask(workers map[string]*Worker, task *Task) (string, []string, error)
}

func NewScheduler(policy WorkloadPolicy) *Scheduler {
	return &Scheduler{
		workers:        make(map[string]*Worker),
		tasks:          make(map[string]*Task),
		pendingTasks:   make([]*Task, 0),
		workloadPolicy: policy,
	}
}

func (s *Scheduler) RegisterWorker(ctx context.Context, req *pb.RegisterWorkerRequest) (*pb.RegisterWorkerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	workerID := req.WorkerId
	if workerID == "" {
		workerID = fmt.Sprintf("worker-%d", len(s.workers)+1)
	}

	if _, exists := s.workers[workerID]; exists {
		return &pb.RegisterWorkerResponse{
			Success:    false,
			Message:    "Worker ID already registered",
			AssignedId: workerID,
		}, nil
	}

	gpus := make([]*GPU, 0, len(req.Gpus))
	for _, g := range req.Gpus {
		gpus = append(gpus, &GPU{
			ID:        g.Id,
			Model:     g.Model,
			MemoryMB:  g.MemoryMb,
			Available: g.Available,
		})
	}

	worker := &Worker{
		ID:      workerID,
		GPUs:    gpus,
		Address: req.Address,
		Status:  pb.WorkerStatus_IDLE,
		Tasks:   make(map[string]*Task),
	}

	s.workers[workerID] = worker

	log.Printf("Worker registered: %s with %d GPUs", workerID, len(gpus))

	s.recordWorkerRegistration()
	s.assignPendingTasks()

	return &pb.RegisterWorkerResponse{
		Success:    true,
		Message:    "Worker registered successfully",
		AssignedId: workerID,
	}, nil
}

func (s *Scheduler) RequestTask(ctx context.Context, req *pb.TaskRequest) (*pb.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	workerID := req.WorkerId
	worker, exists := s.workers[workerID]
	if !exists {
		return nil, status.Errorf(codes.NotFound, "Worker not registered: %s", workerID)
	}

	if len(s.pendingTasks) == 0 {
		return nil, status.Error(codes.NotFound, "No pending tasks available")
	}

	for i, task := range s.pendingTasks {
		workerID, gpuIDs, err := s.workloadPolicy.AssignTask(s.workers, task)
		if err != nil {
			continue
		}

		task.Status = pb.TaskStatus_RUNNING
		task.WorkerID = workerID
		task.AssignedGPUs = gpuIDs
		task.StartTime = time.Now()

		worker.Status = pb.WorkerStatus_BUSY
		s.recordWorkerStatusChange()
		worker.Tasks[task.ID] = task

		for _, gpu := range worker.GPUs {
			for _, id := range gpuIDs {
				if gpu.ID == id {
					gpu.Available = false
				}
			}
		}

		s.pendingTasks = append(s.pendingTasks[:i], s.pendingTasks[i+1:]...)

		pbTask := &pb.Task{
			Id:            task.ID,
			Name:          task.Name,
			RequiredGpus:  task.RequiredGPUs,
			MinGpuMemory:  task.MinGPUMemory,
			Configuration: task.Configuration,
			Status:        task.Status,
		}

		log.Printf("Task %s assigned to worker %s", task.ID, workerID)

		return pbTask, nil
	}

	return nil, status.Error(codes.ResourceExhausted, "No suitable tasks for this worker")
}

func (s *Scheduler) ReportTaskStatus(ctx context.Context, update *pb.TaskStatusUpdate) (*pb.TaskStatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	taskID := update.TaskId
	workerID := update.WorkerId

	task, exists := s.tasks[taskID]
	if !exists {
		return nil, status.Errorf(codes.NotFound, "Task not found: %s", taskID)
	}

	worker, exists := s.workers[workerID]
	if !exists {
		return nil, status.Errorf(codes.NotFound, "Worker not found: %s", workerID)
	}

	task.Status = update.Status
	task.Progress = update.Progress
	task.Metrics = update.Metrics

	if update.Status == pb.TaskStatus_COMPLETED || update.Status == pb.TaskStatus_FAILED {
		s.recordTaskCompletion(task)
		for _, gpu := range worker.GPUs {
			for _, id := range task.AssignedGPUs {
				if gpu.ID == id {
					gpu.Available = true
				}
			}
		}

		delete(worker.Tasks, taskID)

		if len(worker.Tasks) == 0 {
			worker.Status = pb.WorkerStatus_IDLE
			s.recordWorkerStatusChange()
		}

		s.assignPendingTasks()
	}

	log.Printf("Task %s status updated: %s, progress: %.2f%%", taskID, update.Status, update.Progress*100)

	return &pb.TaskStatusResponse{
		Acknowledged: true,
		Message:      "Status update received",
	}, nil
}

func (s *Scheduler) MonitorWorker(req *pb.WorkerStatusRequest, stream pb.TrainingScheduler_MonitorWorkerServer) error {
	workerID := req.WorkerId

	s.mu.RLock()
	_, exists := s.workers[workerID]
	s.mu.RUnlock()

	if !exists {
		return status.Errorf(codes.NotFound, "Worker not found: %s", workerID)
	}

	for {
		s.mu.RLock()
		worker, stillExists := s.workers[workerID]
		if !stillExists {
			s.mu.RUnlock()
			return status.Errorf(codes.NotFound, "Worker no longer exists: %s", workerID)
		}

		pbGPUs := make([]*pb.GPU, 0, len(worker.GPUs))
		for _, gpu := range worker.GPUs {
			pbGPUs = append(pbGPUs, &pb.GPU{
				Id:        gpu.ID,
				Model:     gpu.Model,
				MemoryMb:  gpu.MemoryMB,
				Available: gpu.Available,
			})
		}

		activeTasks := make([]*pb.TaskSummary, 0, len(worker.Tasks))
		for _, task := range worker.Tasks {
			activeTasks = append(activeTasks, &pb.TaskSummary{
				Id:       task.ID,
				Name:     task.Name,
				Status:   task.Status,
				Progress: task.Progress,
			})
		}

		response := &pb.WorkerStatusResponse{
			WorkerId:    workerID,
			Status:      worker.Status,
			Gpus:        pbGPUs,
			ActiveTasks: activeTasks,
			Timestamp:   uint64(time.Now().Unix()),
		}
		s.mu.RUnlock()

		if err := stream.Send(response); err != nil {
			return err
		}

		time.Sleep(1 * time.Second)
	}
}

func (s *Scheduler) AddTask(task *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[task.ID] = task
	s.pendingTasks = append(s.pendingTasks, task)

	log.Printf("New task added: %s, required GPUs: %d", task.ID, task.RequiredGPUs)
	s.recordTaskSubmission()

	s.assignPendingTasks()
}

func (s *Scheduler) assignPendingTasks() {
	for i := 0; i < len(s.pendingTasks); i++ {
		task := s.pendingTasks[i]
		workerID, gpuIDs, err := s.workloadPolicy.AssignTask(s.workers, task)
		if err != nil {
			continue
		}

		task.Status = pb.TaskStatus_RUNNING
		task.WorkerID = workerID
		task.AssignedGPUs = gpuIDs
		task.StartTime = time.Now()

		worker := s.workers[workerID]
		worker.Status = pb.WorkerStatus_BUSY
		worker.Tasks[task.ID] = task

		for _, gpu := range worker.GPUs {
			for _, id := range gpuIDs {
				if gpu.ID == id {
					gpu.Available = false
				}
			}
		}

		s.pendingTasks = append(s.pendingTasks[:i], s.pendingTasks[i+1:]...)
		i--

		log.Printf("Task %s assigned to worker %s", task.ID, workerID)
	}
}

func (s *Scheduler) ListWorkers(ctx context.Context, req *pb.ListWorkersRequest) (*pb.ListWorkersResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	response := &pb.ListWorkersResponse{
		Workers: make([]*pb.WorkerInfo, 0, len(s.workers)),
	}

	for id, worker := range s.workers {
		response.Workers = append(response.Workers, &pb.WorkerInfo{
			WorkerId: id,
			Status:   worker.Status,
			Address:  worker.Address,
			GpuCount: uint32(len(worker.GPUs)),
		})
	}

	return response, nil
}
