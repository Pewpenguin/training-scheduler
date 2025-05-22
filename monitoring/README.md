# Monitoring with Prometheus and Grafana

This directory contains configuration files for monitoring the distributed training scheduler using Prometheus and Grafana.

## Overview

The monitoring stack consists of:

- **Prometheus**: Collects and stores metrics from the scheduler and worker components
- **Grafana**: Visualizes metrics in customizable dashboards

## Metrics Collected

### Scheduler Metrics

- Total tasks submitted
- Active tasks count
- Pending tasks count
- Completed tasks count
- Failed tasks count
- Task duration distribution
- Active workers count

### Worker Metrics

- GPU utilization per worker and GPU
- GPU memory usage per worker and GPU
- Task progress per worker and task
- Worker status

## Setup Instructions

1. Start the monitoring stack:

```bash
docker-compose -f docker-compose.monitoring.yml up -d
```

2. Access the monitoring interfaces:

- Prometheus: http://localhost:9090
- Grafana: http://localhost:3000 (default credentials: admin/admin)

3. The scheduler and worker components will automatically expose metrics on their respective endpoints:

- Scheduler metrics: http://localhost:9091/metrics
- Worker metrics: http://localhost:9092/metrics

## Grafana Dashboards

Pre-configured dashboards are available in the `grafana/dashboards` directory:

- **Scheduler Overview**: Shows task and worker statistics
- **Worker Performance**: Shows detailed GPU utilization and task progress

## Custom Metrics

You can add custom metrics by modifying the `pkg/metrics/metrics.go` file and updating the Prometheus configuration as needed.