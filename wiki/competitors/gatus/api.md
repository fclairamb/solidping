# Gatus — API

## Endpoints

**Status API**:
- `GET /api/v1/endpoints/statuses` - All endpoint statuses
- `GET /api/v1/endpoints/{key}/statuses` - Single endpoint status

**Health API**:
- `GET /health` - Gatus health check

**Metrics API**:
- `GET /metrics` - Prometheus metrics export

**Example**:
```bash
# Get all statuses
curl https://status.example.com/api/v1/endpoints/statuses

# Get specific endpoint
curl https://status.example.com/api/v1/endpoints/production_website/statuses

# Prometheus metrics
curl https://status.example.com/metrics
```

## Response Format

**JSON structure**:
```json
{
  "key": "production_website",
  "name": "website",
  "group": "production",
  "results": [
    {
      "status": 200,
      "hostname": "example.com",
      "duration": 123456789,
      "conditionResults": [
        {
          "condition": "[STATUS] == 200",
          "success": true
        }
      ],
      "success": true,
      "timestamp": "2025-12-25T10:00:00Z"
    }
  ]
}
```
