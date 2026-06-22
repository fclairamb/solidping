# Pingdom — Integration Examples

## Creating HTTP Monitor

```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "API Health Check",
    "host": "api.example.com",
    "type": "http",
    "url": "/v1/health",
    "resolution": 1,
    "shouldcontain": "healthy",
    "requestheaders": {
      "X-API-Key": "secret123",
      "Accept": "application/json"
    }
  }'
```

## Creating TCP Port Monitor

```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "PostgreSQL",
    "host": "db.example.com",
    "type": "tcp",
    "port": 5432,
    "resolution": 5,
    "stringtoexpect": "PostgreSQL"
  }'
```

## Creating DNS Monitor

```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "DNS Check",
    "host": "example.com",
    "type": "dns",
    "expectedip": "93.184.216.34",
    "nameserver": "8.8.8.8"
  }'
```

## Creating SMTP Monitor

```bash
curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/checks' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "name": "Mail Server",
    "host": "smtp.example.com",
    "type": "smtp",
    "port": 587,
    "encryption": true,
    "stringtoexpect": "220"
  }'
```

## Getting Check Results

```bash
# Get last 100 results for a check
curl --request GET \
  --url 'https://api.pingdom.com/api/3.1/results/12345?limit=100&includeanalysis=true' \
  --header 'Authorization: Bearer YOUR_API_TOKEN'
```

## Getting Uptime Summary

```bash
# Get average performance for last 7 days
FROM=$(date -d '7 days ago' +%s)
TO=$(date +%s)

curl --request GET \
  --url "https://api.pingdom.com/api/3.1/summary.average/12345?from=$FROM&to=$TO&includeuptime=true" \
  --header 'Authorization: Bearer YOUR_API_TOKEN'
```

## Pausing Multiple Checks

```bash
# Pause checks for maintenance
curl --request PUT \
  --url 'https://api.pingdom.com/api/3.1/checks/12345,67890,11111' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{"paused": true}'
```

## Creating Maintenance Window

```bash
# Schedule maintenance for 2 hours
START=$(date -d 'tomorrow 2am' +%s)
END=$(date -d 'tomorrow 4am' +%s)

curl --request POST \
  --url 'https://api.pingdom.com/api/3.1/maintenance' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data "{
    \"description\": \"Database migration\",
    \"from\": $START,
    \"to\": $END,
    \"uptimeids\": \"12345,67890\"
  }"
```

## Listing Probe Servers

```bash
curl --request GET \
  --url 'https://api.pingdom.com/api/3.1/probes' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  | jq '.probes[] | {name: .name, country: .country, city: .city, ip: .ip}'
```
