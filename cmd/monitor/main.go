package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/training-scheduler/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	schedulerAddr := flag.String("scheduler", "localhost:50051", "Address of the scheduler server")
	workerID := flag.String("worker", "", "Worker ID to monitor (leave empty to monitor all)")
	refreshRate := flag.Int("refresh", 1, "Refresh rate in seconds")
	flag.Parse()

	conn, err := grpc.Dial(*schedulerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect to scheduler: %v", err)
	}
	defer conn.Close()

	client := pb.NewTrainingSchedulerClient(conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
		os.Exit(0)
	}()

	if *workerID != "" {
		monitorWorker(ctx, client, *workerID, *refreshRate)
	} else {
		// In a real implementation, we would have an API to list all workers
		// For now, just display a message
		fmt.Println("Worker ID must be specified for monitoring")
		fmt.Println("Usage: monitor --worker=<worker_id>")
	}
}

func monitorWorker(ctx context.Context, client pb.TrainingSchedulerClient, workerID string, refreshRate int) {
	req := &pb.WorkerStatusRequest{
		WorkerId: workerID,
	}

	stream, err := client.MonitorWorker(ctx, req)
	if err != nil {
		log.Fatalf("Failed to start monitoring: %v", err)
	}

	fmt.Printf("Monitoring worker: %s\n\n", workerID)

	for {
		resp, err := stream.Recv()
		if err != nil {
			log.Fatalf("Error receiving status update: %v", err)
		}

		fmt.Print("\033[H\033[2J")

		displayWorkerStatus(resp)

		time.Sleep(time.Duration(refreshRate) * time.Second)
	}
}

func displayWorkerStatus(status *pb.WorkerStatusResponse) {
	fmt.Printf("Worker ID: %s\n", status.WorkerId)
	fmt.Printf("Status: %s\n", getWorkerStatusString(status.Status))
	fmt.Printf("Timestamp: %s\n\n", time.Unix(int64(status.Timestamp), 0).Format(time.RFC3339))

	fmt.Println("GPUs:")
	fmt.Println("-----")
	for i, gpu := range status.Gpus {
		fmt.Printf("GPU %d:\n", i+1)
		fmt.Printf("  ID: %s\n", gpu.Id)
		fmt.Printf("  Model: %s\n", gpu.Model)
		fmt.Printf("  Memory: %d MB\n", gpu.MemoryMb)
		fmt.Printf("  Available: %t\n\n", gpu.Available)
	}

	fmt.Println("Active Tasks:")
	fmt.Println("------------")
	if len(status.ActiveTasks) == 0 {
		fmt.Println("No active tasks")
	} else {
		for i, task := range status.ActiveTasks {
			fmt.Printf("Task %d:\n", i+1)
			fmt.Printf("  ID: %s\n", task.Id)
			fmt.Printf("  Name: %s\n", task.Name)
			fmt.Printf("  Status: %s\n", getTaskStatusString(task.Status))
			fmt.Printf("  Progress: %.2f%%\n\n", task.Progress*100)
		}
	}
}

func getWorkerStatusString(status pb.WorkerStatus) string {
	switch status {
	case pb.WorkerStatus_IDLE:
		return "IDLE"
	case pb.WorkerStatus_BUSY:
		return "BUSY"
	case pb.WorkerStatus_OFFLINE:
		return "OFFLINE"
	case pb.WorkerStatus_ERROR:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

func getTaskStatusString(status pb.TaskStatus) string {
	switch status {
	case pb.TaskStatus_PENDING:
		return "PENDING"
	case pb.TaskStatus_RUNNING:
		return "RUNNING"
	case pb.TaskStatus_COMPLETED:
		return "COMPLETED"
	case pb.TaskStatus_FAILED:
		return "FAILED"
	case pb.TaskStatus_CANCELED:
		return "CANCELED"
	default:
		return "UNKNOWN"
	}
}
