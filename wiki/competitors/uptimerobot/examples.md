# UptimeRobot — Integration Examples

## Monitoring a Web Service

```bash
# Create HTTP monitor with custom headers
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "HTTP",
    "friendly_name": "API Health Check",
    "url": "https://api.example.com/health",
    "interval": 60,
    "http_method": "GET",
    "custom_http_headers": [
      {"name": "X-API-Key", "value": "secret123"},
      {"name": "Accept", "value": "application/json"}
    ],
    "custom_http_statuses": [200, 201, 204],
    "timeout": 30
  }'
```

## Keyword Monitoring

```bash
# Alert when "error" appears on page
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "KEYWORD",
    "friendly_name": "Error Detection",
    "url": "https://example.com/status",
    "keyword_type": "NOT_EXISTS",
    "keyword_value": "error",
    "interval": 300
  }'
```

## Port Monitoring

```bash
# Monitor database port
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "PORT",
    "friendly_name": "PostgreSQL",
    "url": "db.example.com",
    "port": 5432,
    "interval": 300
  }'
```

## Heartbeat Monitoring

```bash
# 1. Create heartbeat monitor
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "HEARTBEAT",
    "friendly_name": "Daily Backup Job",
    "heartbeat_interval": 86400,
    "heartbeat_grace_period": 3600
  }'

# 2. Response includes heartbeat URL
# 3. Add to your cron job:
0 2 * * * /usr/local/bin/backup.sh && curl https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx
```

## Creating Slack Integration

```bash
# Create Slack alert contact
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/integrations' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "type": "SLACK",
    "webhook_url": "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXX",
    "friendly_name": "DevOps Channel"
  }'
```

## Checking Monitor Status

```bash
# List all monitors with their status
curl --request GET \
  --url 'https://api.uptimerobot.com/v3/monitors' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  | jq '.monitors[] | {name: .friendly_name, status: .status, uptime: .uptime}'
```

## Creating Public Status Page

```bash
# Create status page with selected monitors
curl --request POST \
  --url 'https://api.uptimerobot.com/v3/psps' \
  --header 'Authorization: Bearer YOUR_API_KEY' \
  --header 'Content-Type: application/json' \
  --data '{
    "friendly_name": "Service Status",
    "monitors": ["123456", "789012", "345678"],
    "sort": "FRIENDLY_NAME_A_Z",
    "hide_url_links": false
  }'
```
