package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/training-scheduler/pkg/metrics"
	"github.com/training-scheduler/pkg/worker"
	pb "github.com/training-scheduler/proto"
)

func main() {
	schedulerAddr := flag.String("scheduler", "localhost:50051", "Address of the scheduler server")
	gpuIDs := flag.String("gpus", "", "Comma-separated list of GPU IDs")
	gpuModels := flag.String("models", "", "Comma-separated list of GPU models")
	gpuMemories := flag.String("memories", "", "Comma-separated list of GPU memory sizes in MB")
	workerAddr := flag.String("addr", "localhost:0", "Address of this worker")
	metricsPort := flag.Int("metrics-port", 9092, "The metrics server port")
	flag.Parse()

	ids := strings.Split(*gpuIDs, ",")
	models := strings.Split(*gpuModels, ",")
	memories := strings.Split(*gpuMemories, ",")

	if len(ids) == 0 || ids[0] == "" {
		log.Fatal("At least one GPU ID must be specified")
	}

	if len(models) != len(ids) {
		log.Fatal("Number of GPU models must match number of GPU IDs")
	}

	if len(memories) != len(ids) {
		log.Fatal("Number of GPU memory sizes must match number of GPU IDs")
	}

	gpus := make([]*pb.GPU, 0, len(ids))
	for i, id := range ids {
		memory, err := strconv.ParseUint(memories[i], 10, 64)
		if err != nil {
			log.Fatalf("Invalid GPU memory size: %s", memories[i])
		}

		gpus = append(gpus, &pb.GPU{
			Id:        id,
			Model:     models[i],
			MemoryMb:  memory,
			Available: true,
		})
	}

	metricsServer := metrics.NewMetricsServer(fmt.Sprintf(":%d", *metricsPort))

	log.Printf("Starting metrics server on port %d", *metricsPort)
	go func() {
		if err := metricsServer.Start(); err != nil {
			log.Printf("Failed to start metrics server: %v", err)
		}
	}()

	worker, err := worker.NewWorker(*schedulerAddr, gpus)
	if err != nil {
		log.Fatalf("Failed to create worker: %v", err)
	}

	workerMetrics := metrics.NewWorkerMetrics(worker.ID)
	worker.SetMetrics(workerMetrics)

	worker.Address = *workerAddr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := worker.Start(ctx); err != nil {
		log.Fatalf("Failed to start worker: %v", err)
	}

	log.Printf("Worker started with %d GPUs, connected to scheduler at %s", len(gpus), *schedulerAddr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down worker...")
	cancel()
	worker.Stop()
	log.Println("Worker stopped")
}
