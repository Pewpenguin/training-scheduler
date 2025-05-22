package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/training-scheduler/pkg/metrics"
	"github.com/training-scheduler/pkg/scheduler"
	pb "github.com/training-scheduler/proto"
	"google.golang.org/grpc"
)

func main() {
	port := flag.Int("port", 50051, "The server port")
	metricsPort := flag.Int("metrics-port", 9091, "The metrics server port")
	policyType := flag.String("policy", "balanced", "Workload distribution policy (simple or balanced)")
	flag.Parse()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()

	var policy scheduler.WorkloadPolicy
	switch *policyType {
	case "simple":
		policy = scheduler.NewSimpleWorkloadPolicy()
		log.Println("Using simple workload distribution policy")
	case "balanced":
		policy = scheduler.NewBalancedWorkloadPolicy()
		log.Println("Using balanced workload distribution policy")
	default:
		log.Fatalf("Unknown policy type: %s", *policyType)
	}

	metricsServer := metrics.NewMetricsServer(fmt.Sprintf(":%d", *metricsPort))
	schedulerMetrics := metrics.NewSchedulerMetrics()

	log.Printf("Starting metrics server on port %d", *metricsPort)
	go func() {
		if err := metricsServer.Start(); err != nil {
			log.Printf("Failed to start metrics server: %v", err)
		}
	}()

	schedulerService := scheduler.NewScheduler(policy)
	schedulerService.SetMetrics(schedulerMetrics)

	pb.RegisterTrainingSchedulerServer(grpcServer, schedulerService)

	log.Printf("Starting scheduler server on port %d", *port)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()

	schedulerMetrics.SetActiveTasks(0)
	schedulerMetrics.SetPendingTasks(0)
	schedulerMetrics.SetActiveWorkers(0)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down server...")
	grpcServer.GracefulStop()
	log.Println("Server stopped")
}
