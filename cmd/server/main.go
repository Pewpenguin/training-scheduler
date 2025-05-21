package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/training-scheduler/pkg/scheduler"
	pb "github.com/training-scheduler/proto"
	"google.golang.org/grpc"
)

func main() {
	port := flag.Int("port", 50051, "The server port")
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

	schedulerService := scheduler.NewScheduler(policy)

	pb.RegisterTrainingSchedulerServer(grpcServer, schedulerService)

	log.Printf("Starting scheduler server on port %d", *port)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down server...")
	grpcServer.GracefulStop()
	log.Println("Server stopped")
}
