package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/training-scheduler/pkg/logging"
	"github.com/training-scheduler/pkg/metrics"
	"github.com/training-scheduler/pkg/scheduler"
	pb "github.com/training-scheduler/proto"
	"google.golang.org/grpc"
)

func main() {
	port := flag.Int("port", 50051, "The server port")
	metricsPort := flag.Int("metrics-port", 9091, "The metrics server port")
	policyType := flag.String("policy", "balanced", "Workload distribution policy (simple or balanced)")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	logDir := flag.String("log-dir", "/var/log/scheduler", "Directory for log files")
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
	case "balanced":
		policy = scheduler.NewBalancedWorkloadPolicy()
	default:
		log.Fatalf("Unknown policy type: %s", *policyType)
	}

	metricsServer := metrics.NewMetricsServer(fmt.Sprintf(":%d", *metricsPort))
	schedulerMetrics := metrics.NewSchedulerMetrics()

	schedulerService := scheduler.NewScheduler(policy)
	schedulerService.SetMetrics(schedulerMetrics)

	pb.RegisterTrainingSchedulerServer(grpcServer, schedulerService)

	// Initialize logger
	loggerConfig := logging.Config{
		Level:     logging.LogLevel(*logLevel),
		Component: "scheduler",
		LogDir:    *logDir,
		LogFile:   "scheduler.log",
	}

	logger, err := logging.NewLogger(loggerConfig)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	logger.Info("Starting scheduler server", map[string]interface{}{
		"port":         *port,
		"metrics_port": *metricsPort,
		"policy":       *policyType,
	})

	logger.Info("Starting metrics server", map[string]interface{}{"port": *metricsPort})
	go func() {
		if err := metricsServer.Start(); err != nil {
			logger.Error("Failed to start metrics server", map[string]interface{}{"error": err.Error()})
		}
	}()

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			logger.Fatal("Failed to serve", map[string]interface{}{"error": err.Error()})
		}
	}()

	schedulerMetrics.SetActiveTasks(0)
	schedulerMetrics.SetPendingTasks(0)
	schedulerMetrics.SetActiveWorkers(0)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("Shutting down server", nil)
	grpcServer.GracefulStop()
	logger.Info("Server stopped", nil)
}
