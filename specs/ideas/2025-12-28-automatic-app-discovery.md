# Automatic Application Discovery

## Overview

When a user provides a URL to monitor, SolidPing can automatically detect the type of application and suggest a more appropriate healthcheck endpoint instead of just checking the provided URL.

## How it works

1. User provides a URL (e.g., `https://metabase.example.com`)
2. SolidPing fetches the page and analyzes:
   - The HTML `<title>` tag
   - Meta tags (e.g., `<meta name="generator">`)
   - Response headers (e.g., `X-Powered-By`, `Server`)
   - Known URL patterns in the path
   - Favicon or specific static assets
3. If a known application is detected, suggest using its healthcheck endpoint

## Detection Rules

### Analytics & BI Tools

| Application | Detection Method | Healthcheck Endpoint |
|-------------|------------------|---------------------|
| Metabase | Title contains "Metabase" | `/api/health` |
| Grafana | Title contains "Grafana" | `/api/health` |
| Kibana | Title contains "Kibana" | `/api/status` |
| Plausible | Title contains "Plausible" | `/api/health` |
| Matomo | Title contains "Matomo" or "Piwik" | `/index.php?module=API&method=API.getPiwikVersion` |
| SonarQube | Title contains "SonarQube" | `/api/system/health` |

### DevOps & Infrastructure

| Application | Detection Method | Healthcheck Endpoint |
|-------------|------------------|---------------------|
| Prometheus | Title contains "Prometheus" | `/-/healthy` |
| Alertmanager | Title contains "Alertmanager" | `/-/healthy` |
| GitLab | Title contains "GitLab" | `/-/health` |
| Jenkins | Title contains "Jenkins" | `/api/json` |
| Portainer | Title contains "Portainer" | `/api/status` |
| Traefik | Title contains "Traefik" | `/ping` |
| Vault | Header or path contains "vault" | `/v1/sys/health` |
| Consul | Title contains "Consul" | `/v1/status/leader` |
| ArgoCD | Title contains "Argo CD" | `/healthz` |

### Collaboration & Communication

| Application | Detection Method | Healthcheck Endpoint |
|-------------|------------------|---------------------|
| Mattermost | Title contains "Mattermost" | `/api/v4/system/ping` |
| Rocket.Chat | Title contains "Rocket.Chat" | `/api/v1/info` |
| Discourse | Meta generator contains "Discourse" | `/srv/status` |
| Outline | Title contains "Outline" | `/api/health` |

### Content Management

| Application | Detection Method | Healthcheck Endpoint |
|-------------|------------------|---------------------|
| WordPress | Meta generator contains "WordPress" | `/wp-json` |
| Ghost | Meta generator contains "Ghost" | `/ghost/api/admin/site/` |
| Strapi | Response contains Strapi identifier | `/_health` |
| Nextcloud | Title contains "Nextcloud" | `/status.php` |

### Identity & Auth

| Application | Detection Method | Healthcheck Endpoint |
|-------------|------------------|---------------------|
| Keycloak | Title contains "Keycloak" | `/health/ready` |
| Authentik | Title contains "authentik" | `/-/health/ready/` |

### Databases & Storage

| Application | Detection Method | Healthcheck Endpoint |
|-------------|------------------|---------------------|
| Elasticsearch | Response contains ES cluster info | `/_cluster/health` |
| MinIO | Title or headers contain "MinIO" | `/minio/health/live` |
| PgAdmin | Title contains "pgAdmin" | `/misc/ping` |
| RabbitMQ | Title contains "RabbitMQ" | `/api/health/checks/alarms` |

### Automation & Workflows

| Application | Detection Method | Healthcheck Endpoint |
|-------------|------------------|---------------------|
| n8n | Title contains "n8n" | `/healthz` |
| Airbyte | Title contains "Airbyte" | `/api/v1/health` |

### Monitoring

| Application | Detection Method | Healthcheck Endpoint |
|-------------|------------------|---------------------|
| Uptime Kuma | Title contains "Uptime Kuma" | `/api/status-page/heartbeat` |
| Sentry | Title contains "Sentry" | `/_health/` |

## API Design

### Endpoint

```
POST /api/v1/orgs/$org/checks/discover
```

### Request

```json
{
  "url": "https://metabase.example.com"
}
```

### Response

```json
{
  "proposals": [{
    "name": "Metabase",
    "config": {
      "url": "https://metabase.example.com/api/health",
      "expected_status_code": 200
    },
    "relevancy": {
      "score": 0.8,
      "reason": "Title contains 'Metabase'"
    }
  }]
}
```

If no application is detected:

```json
{
  "proposals": []
}
```


### Multi-Protocol Discovery

When the input URL has no protocol specified (e.g., `metabase.example.com`), the API should concurrently probe multiple protocols and return proposals for all that respond:

- `https://` (preferred, check first)
- `http://`

This allows the user to choose the appropriate protocol. The response may contain multiple proposals with different protocols:

```json
{
  "proposals": [
    {
      "name": "Metabase (HTTPS)",
      "config": {
        "url": "https://metabase.example.com/api/health",
        "expected_status_code": 200
      },
      "relevancy": {
        "score": 0.9,
        "reason": "Title contains 'Metabase', HTTPS available"
      }
    },
    {
      "name": "Metabase (HTTP)",
      "config": {
        "url": "http://metabase.example.com/api/health",
        "expected_status_code": 200
      },
      "relevancy": {
        "score": 0.7,
        "reason": "Title contains 'Metabase', HTTP only"
      }
    }
  ]
}
```

HTTPS proposals should have a higher relevancy score than HTTP.

### Frontend Integration

1. When user enters a URL in the check creation form, debounce and call the discover API
2. If proposals are returned, show a notification/banner: "Detected Metabase instance. Apply suggested settings?"
3. User can accept all proposals, cherry-pick individual ones, or dismiss
4. Proposals should not auto-apply; always require user confirmation

## Implementation Notes

- Detection should be non-intrusive (single GET request to the provided URL)
- The healthcheck suggestion should be optional; the user can choose to ignore it
- Store detection rules in a configurable format (JSON) for easy updates
- Consider caching detection results to avoid repeated checks
- Some applications may require authentication for their health endpoints; document this
- Rate-limit the discover endpoint to prevent abuse
