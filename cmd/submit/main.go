package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	pb "github.com/training-scheduler/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type TaskConfig struct {
	Name         string            `json:"name"`
	RequiredGPUs uint32            `json:"required_gpus"`
	MinGPUMemory uint64            `json:"min_gpu_memory"`
	Params       map[string]string `json:"params"`
}

func main() {
	schedulerAddr := flag.String("scheduler", "localhost:50051", "Address of the scheduler server")
	taskName := flag.String("name", "", "Name of the training task")
	requiredGPUs := flag.Uint("gpus", 1, "Number of GPUs required for the task")
	minGPUMemory := flag.Uint64("memory", 4096, "Minimum GPU memory required in MB")
	configFile := flag.String("config", "", "Path to task configuration file (JSON)")
	flag.Parse()

	var taskConfig TaskConfig

	if *configFile != "" {
		configData, err := os.ReadFile(*configFile)
		if err != nil {
			log.Fatalf("Failed to read config file: %v", err)
		}

		if err := json.Unmarshal(configData, &taskConfig); err != nil {
			log.Fatalf("Failed to parse config file: %v", err)
		}
	} else {
		if *taskName == "" {
			log.Fatal("Task name must be specified")
		}

		taskConfig = TaskConfig{
			Name:         *taskName,
			RequiredGPUs: uint32(*requiredGPUs),
			MinGPUMemory: *minGPUMemory,
			Params:       make(map[string]string),
		}
	}

	conn, err := grpc.NewClient(*schedulerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect to scheduler: %v", err)
	}
	defer conn.Close()

	configJSON, err := json.Marshal(taskConfig.Params)
	if err != nil {
		log.Fatalf("Failed to marshal task parameters: %v", err)
	}

	task := &pb.Task{
		Id:            fmt.Sprintf("task-%d", time.Now().UnixNano()),
		Name:          taskConfig.Name,
		RequiredGpus:  taskConfig.RequiredGPUs,
		MinGpuMemory:  taskConfig.MinGPUMemory,
		Configuration: configJSON,
		Status:        pb.TaskStatus_PENDING,
	}

	client := pb.NewTrainingSchedulerClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req := &pb.TaskRequest{
		Task: task,
	}

	_, err = client.RequestTask(ctx, req)
	if err != nil {
		log.Fatalf("Failed to submit task: %v", err)
	}

	log.Printf("Task submitted successfully: %s", task.Id)
	log.Printf("  Name: %s", task.Name)
	log.Printf("  Required GPUs: %d", task.RequiredGpus)
	log.Printf("  Min GPU Memory: %d MB", task.MinGpuMemory)
}
