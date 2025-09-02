# Incidents API: Include Check Details

## Overview

The incidents API should support including related check information in the response to avoid additional API calls.

## Query Parameter

Use `?with=check` to embed check details within each incident.

**Endpoint**: `GET /api/v1/orgs/$org/incidents?with=check`

## Response Structure

When `with=check` is specified, each incident object will include a `check` property containing:

| Field    | Description                          |
|----------|--------------------------------------|
| `slug`   | The unique identifier for the check  |
| `type`   | The check type (e.g., `http`, `tcp`) |
| `config` | The check configuration object       |

## Example Response

```json
{
  "data": [
    {
      "uid": "inc_abc123",
      "check_uid": "chk_xyz789",
      "status": "ongoing",
      "started_at": "2026-01-03T10:00:00Z",
      "check": {
        "slug": "api-health",
        "type": "http",
        "config": {
          "url": "https://api.example.com/health",
          "method": "GET"
        }
      }
    }
  ]
}
```
