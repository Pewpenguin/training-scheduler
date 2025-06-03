# Training Scheduler Alerting System

## Overview

This document describes the alerting system implemented for the Training Scheduler application. The system monitors critical conditions and sends notifications through configured channels when alert conditions are met.

## Alert Rules

The following alert rules have been implemented:

1. **Worker Disconnected**: Triggers when a worker has been disconnected for more than 5 minutes.
2. **High Task Failure Rate**: Triggers when the task failure rate exceeds 30% over a 15-minute period.
3. **GPU Memory Saturation**: Triggers when a GPU is using more than 90% of its memory for over 5 minutes.
4. **Task Stuck**: Triggers when a task has been running for over 1 hour with progress less than 95%.
5. **Worker Error**: Triggers when a worker is in an error state for more than 2 minutes.
6. **No Active Workers**: Triggers when there are no active workers in the system for more than 5 minutes.

## Components

### Prometheus Alert Rules

Alert rules are defined in `monitoring/prometheus/alert_rules.yml` and loaded by Prometheus. These rules define the conditions that trigger alerts and the labels and annotations associated with each alert.

### AlertManager

AlertManager handles the routing and delivery of alerts to notification channels. The configuration is in `monitoring/alertmanager/config.yml`. It defines:

- Global SMTP and Slack settings
- Routing rules based on alert severity
- Notification receivers for different channels
- Grouping and timing settings for notifications

### Grafana Notification Channels

Grafana notification channels are configured in `monitoring/grafana/provisioning/alerting/contact_points.yml` and include:

- Email notifications
- Slack channels for critical alerts
- Slack channels for warning alerts

## Configuration

### Email Notifications

To configure email notifications:

1. Edit `monitoring/alertmanager/config.yml` and update the SMTP settings:
   ```yaml
   global:
     smtp_smarthost: 'your-smtp-server:587'
     smtp_from: 'your-email@example.com'
     smtp_auth_username: 'your-username'
     smtp_auth_password: 'your-password'
   ```

2. Edit `monitoring/grafana/provisioning/alerting/contact_points.yml` and update the email addresses:
   ```yaml
   settings:
     addresses: your-email@example.com
   ```

### Slack Notifications

To configure Slack notifications:

1. Create a Slack webhook URL by following [Slack's documentation](https://api.slack.com/messaging/webhooks).

2. Edit `monitoring/alertmanager/config.yml` and update the Slack webhook URL:
   ```yaml
   global:
     slack_api_url: 'https://hooks.slack.com/services/YOUR_SLACK_WEBHOOK_URL'
   ```

3. Edit `monitoring/grafana/provisioning/alerting/contact_points.yml` and update the Slack webhook URLs and channel names:
   ```yaml
   settings:
     url: https://hooks.slack.com/services/YOUR_SLACK_WEBHOOK_URL
     recipient: "#your-channel-name"
   ```

## Alert Dashboard

A dedicated dashboard for monitoring alerts has been created at `monitoring/grafana/dashboards/alerts-dashboard.json`. This dashboard provides:

- Overview of critical and warning alerts
- Timeline of alerts by type
- Detailed table of current alerts

Access this dashboard in Grafana after starting the monitoring stack.

## Starting the Alerting System

The alerting system is integrated with the existing monitoring stack. Start it using:

```bash
docker-compose -f docker-compose.monitoring.yml up -d
```

This will start Prometheus, AlertManager, Grafana, and other monitoring components with the alerting configuration.

## Testing Alerts

To test the alerting system:

1. Stop a worker node to trigger the "Worker Disconnected" alert
2. Submit tasks with errors to trigger the "High Task Failure Rate" alert
3. Run tasks that consume high GPU memory to trigger the "GPU Memory Saturation" alert

## Troubleshooting

- Check Prometheus status page at http://localhost:9090/alerts to see if alerts are firing
- Check AlertManager status page at http://localhost:9093/#/alerts to see if alerts are being routed
- Check Grafana alert list at http://localhost:3000/alerting/list to see Grafana-managed alerts
- Check logs for each component using `docker-compose -f docker-compose.monitoring.yml logs [service]`