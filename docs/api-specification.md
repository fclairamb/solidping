# Complete API Specification

## Base URL Pattern
All APIs follow the pattern: `/api/v1/orgs/{org}/*` where `{org}` maps to `organizations.code`

## Authentication APIs

### POST /api/v1/orgs/{org}/auth/login
Login with email/password
```json
Request:
{
  "email": "user@example.com", 
  "password": "password123"
}

Response:
{
  "token": "jwt-token",
  "user": {
    "uid": "uuid",
    "email": "user@example.com",
    "role": "admin"
  }
}
```

### POST /api/v1/orgs/{org}/auth/logout
Logout current session
```json
Response: 204 No Content
```

### POST /api/v1/orgs/{org}/auth/tokens
Create API token
```json
{
  "name": "My API Token",
  "expires_at": "2024-12-31T23:59:59Z" // optional
}
---
Response:
{
  "token": "pat_xyz123",
  "name": "My API Token",
  "created_at": "2024-01-01T00:00:00Z"
}
```

### GET /api/v1/orgs/{org}/auth/tokens
List user's tokens
```json
Response:
{
  "data": [
    {
      "uid": "uuid",
      "name": "My API Token", 
      "created_at": "2024-01-01T00:00:00Z",
      "last_used_at": "2024-01-15T10:30:00Z"
    }
  ]
}
```

### DELETE /api/v1/orgs/{org}/auth/tokens/{token_uid}
Revoke API token
```json
Response: 204 No Content
```

## Organization Management APIs

### GET /api/v1/orgs/{org}
Get organization details
```json
Response:
{
  "uid": "uuid",
  "code": "my-org",
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

### PUT /api/v1/orgs/{org}
Update organization (admin only)
```json
Request:
{
  "code": "new-org-code" // optional, must be unique
}

Response:
{
  "uid": "uuid", 
  "code": "new-org-code",
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-15T12:00:00Z"
}
```

### DELETE /api/v1/orgs/{org}
Soft delete organization (admin only)
```json
Response: 204 No Content
```

## User Management APIs

### GET /api/v1/orgs/{org}/users
List organization users
```json
Response:
{
  "data":[{
    "uid": "uuid",
    "email": "user@example.com",
    "role": "admin",
    "auth_provider": {
      "uid": "uuid",
      "type": "email",
      "code": "email-auth"
    },
    "created_at": "2024-01-01T00:00:00Z"
  }]
}
```

### POST /api/v1/orgs/{org}/users
Create new user (admin only)
```json
Request:
{
  "email": "newuser@example.com",
  "role": "user",
  "password": "temp-password", // optional, for password auth
  "auth_provider_uid": "uuid" // optional, for OAuth
}

Response:
{
  "uid": "uuid",
  "email": "newuser@example.com", 
  "role": "user",
  "created_at": "2024-01-15T12:00:00Z"
}
```

### GET /api/v1/orgs/{org}/users/{user_uid}
Get user details
```json
Response:
{
  "uid": "uuid",
  "email": "user@example.com",
  "role": "admin",
  "auth_provider": {
    "uid": "uuid",
    "type": "google",
    "code": "google-sso"
  },
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

### PUT /api/v1/orgs/{org}/users/{user_uid}
Update user
```json
Request:
{
  "email": "updated@example.com", // optional
  "role": "viewer", // optional, admin only
  "password": "new-password" // optional
}

Response:
{
  "uid": "uuid",
  "email": "updated@example.com",
  "role": "viewer",
  "updated_at": "2024-01-15T12:00:00Z"
}
```

### DELETE /api/v1/orgs/{org}/users/{user_uid}
Soft delete user (admin only)
```json
Response: 204 No Content
```

## Auth Provider APIs

### GET /api/v1/orgs/{org}/auth-providers
List authentication providers
```json
Response:
{
  "data": [
    {
      "uid": "uuid",
      "code": "google-sso",
      "type": "google",
      "config": {
        "client_id": "google-client-id",
        "domain": "example.com"
      },
      "created_at": "2024-01-01T00:00:00Z"
    }
  ]
}
```

### POST /api/v1/orgs/{org}/auth-providers
Create auth provider (admin only)
```json
Request:
{
  "code": "github-auth",
  "type": "github", 
  "config": {
    "client_id": "github-client-id",
    "client_secret": "github-client-secret"
  }
}

Response:
{
  "uid": "uuid",
  "code": "github-auth",
  "type": "github",
  "config": {
    "client_id": "github-client-id"
  },
  "created_at": "2024-01-15T12:00:00Z"
}
```

### PUT /api/v1/orgs/{org}/auth-providers/{provider_uid}
Update auth provider (admin only)
```json
Request:
{
  "config": {
    "client_id": "updated-client-id"
  }
}

Response:
{
  "uid": "uuid",
  "code": "github-auth", 
  "type": "github",
  "config": {
    "client_id": "updated-client-id"
  },
  "updated_at": "2024-01-15T12:00:00Z"
}
```

### DELETE /api/v1/orgs/{org}/auth-providers/{provider_uid}
Soft delete auth provider (admin only)
```json
Response: 204 No Content
```

## Worker Management APIs

### GET /api/v1/orgs/{org}/workers
List workers
```json
Response:
{
  "data": [
    {
      "uid": "uuid",
      "code": "worker-eu-1",
      "name": "Europe Worker",
      "context": {
        "region": "eu-1",
        "datacenter": "amsterdam"
      },
      "last_active_at": "2024-01-15T12:00:00Z",
      "status": "active", // derived from last_active_at
      "created_at": "2024-01-01T00:00:00Z"
    }
  ]
}
```

### POST /api/v1/orgs/{org}/workers
Register new worker (admin only)
```json
Request:
{
  "code": "worker-us-1",
  "name": "US East Worker",
  "context": {
    "region": "us-east-1",
    "datacenter": "virginia"
  }
}

Response:
{
  "uid": "uuid",
  "code": "worker-us-1",
  "name": "US East Worker",
  "context": {
    "region": "us-east-1", 
    "datacenter": "virginia"
  },
  "created_at": "2024-01-15T12:00:00Z"
}
```

### GET /api/v1/orgs/{org}/workers/{worker_uid}
Get worker details
```json
Response:
{
  "uid": "uuid",
  "code": "worker-eu-1",
  "name": "Europe Worker",
  "context": {
    "region": "eu-1"
  },
  "last_active_at": "2024-01-15T12:00:00Z",
  "status": "active",
  "active_jobs": 5,
  "total_jobs_executed": 1250,
  "created_at": "2024-01-01T00:00:00Z"
}
```

## Check Management APIs

### GET /api/v1/orgs/{org}/checks
List monitoring checks
```json
Query parameters:
- enabled: boolean
- type: string
- limit: number (default: 50)
- offset: number (default: 0)

Response:
{
  "data": [
    {
      "uid": "uuid",
      "name": "Website Health Check",
      "type": "http",
      "config": {
        "url": "https://example.com",
        "method": "GET",
        "timeout": 30
      },
      "enabled": true,
      "period": "00:01:00", // 1 minute
      "status": {
        "last_result": "up",
        "last_checked": "2024-01-15T11:59:00Z",
        "uptime_24h": 99.5
      },
      "created_at": "2024-01-01T00:00:00Z"
    }
  ]
}
```

### POST /api/v1/orgs/{org}/checks
Create monitoring check
```json
Request:
{
  "name": "API Health Check",
  "type": "http",
  "config": {
    "url": "https://api.example.com/health",
    "method": "GET", 
    "expected_status": 200,
    "timeout": 10
  },
  "enabled": true,
  "period": "00:05:00" // 5 minutes
}

Response:
{
  "uid": "uuid",
  "name": "API Health Check",
  "type": "http",
  "config": {
    "url": "https://api.example.com/health",
    "method": "GET",
    "expected_status": 200,
    "timeout": 10
  },
  "enabled": true,
  "period": "00:05:00",
  "created_at": "2024-01-15T12:00:00Z"
}
```

### GET /api/v1/orgs/{org}/checks/{check_uid}
Get check details
```json
Response:
{
  "uid": "uuid",
  "name": "Website Health Check",
  "type": "http",
  "config": {
    "url": "https://example.com",
    "method": "GET",
    "timeout": 30
  },
  "enabled": true,
  "period": "00:01:00",
  "job": {
    "uid": "uuid",
    "scheduled_at": "2024-01-15T12:01:00Z",
    "lease_worker": {
      "uid": "uuid",
      "name": "Europe Worker"
    },
    "lease_expires_at": "2024-01-15T12:02:00Z"
  },
  "stats": {
    "total_runs": 1440,
    "success_rate": 99.2,
    "avg_duration_ms": 125,
    "uptime_24h": 99.5,
    "uptime_7d": 98.8,
    "uptime_30d": 99.1
  },
  "created_at": "2024-01-01T00:00:00Z"
}
```

### PUT /api/v1/orgs/{org}/checks/{check_uid}
Update monitoring check
```json
Request:
{
  "name": "Updated Check Name",
  "config": {
    "url": "https://newapi.example.com/health",
    "timeout": 15
  },
  "enabled": false,
  "period": "00:10:00"
}

Response:
{
  "uid": "uuid", 
  "name": "Updated Check Name",
  "type": "http",
  "config": {
    "url": "https://newapi.example.com/health",
    "method": "GET",
    "timeout": 15
  },
  "enabled": false,
  "period": "00:10:00",
  "updated_at": "2024-01-15T12:00:00Z"
}
```

### DELETE /api/v1/orgs/{org}/checks/{check_uid}
Soft delete check
```json
Response: 204 No Content
```

### POST /api/v1/orgs/{org}/checks/{check_uid}/run
Manually trigger check execution
```json
Request:
{
  "worker_uid": "uuid" // optional, specific worker
}

Response:
{
  "job_uid": "uuid",
  "scheduled_at": "2024-01-15T12:00:30Z",
  "status": "queued"
}
```

## Results & Monitoring APIs

### GET /api/v1/orgs/{org}/checks/{check_uid}/results
Get check execution results
```json
Query parameters:
- period: string (YYYY, YYYY-MM, YYYY-MM-DD)
- from: ISO datetime
- to: ISO datetime  
- status: number (1=up, 2=down, 3=timeout, 4=error)
- limit: number (default: 100)
- offset: number (default: 0)

Response:
{
  "data": [
    {
      "started_at": "2024-01-15T11:59:00Z",
      "status": 1, // up
      "duration_ms_avg": 125,
      "duration_ms_min": 120,
      "duration_ms_max": 130,
      "availability": 100.0,
      "output": {
        "response_code": 200,
        "response_time": 125,
        "response_size": 1024
      },
      "context": {
        "worker": "worker-eu-1",
        "ip": "203.0.113.1"
      }
    }
  ]
}
```

### GET /api/v1/orgs/{org}/checks/{check_uid}/stats
Get aggregated statistics
```json
Query parameters:
- period: string (hour, day, week, month)
- from: ISO datetime
- to: ISO datetime

Response:
{
  "period": "day",
  "from": "2024-01-14T00:00:00Z",
  "to": "2024-01-15T00:00:00Z",
  "total_checks": 1440,
  "uptime_percentage": 99.5,
  "avg_response_time": 125,
  "min_response_time": 95,
  "max_response_time": 250,
  "status_breakdown": {
    "up": 1433,
    "down": 5,
    "timeout": 2,
    "error": 0
  },
  "hourly_stats": [
    {
      "hour": "2024-01-14T00:00:00Z",
      "checks": 60,
      "uptime": 100.0,
      "avg_duration": 120
    }
  ]
}
```

### GET /api/v1/orgs/{org}/dashboard
Get organization dashboard data
```json
Response:
{
  "summary": {
    "total_checks": 15,
    "active_checks": 12,
    "overall_uptime": 99.2,
    "alerts_last_24h": 3
  },
  "recent_incidents": [
    {
      "check_uid": "uuid",
      "check_name": "API Health",
      "status": "down",
      "started_at": "2024-01-15T10:30:00Z",
      "duration": "00:05:00"
    }
  ],
  "top_checks": [
    {
      "check_uid": "uuid",
      "name": "Website Health",
      "uptime_24h": 99.8,
      "avg_response_time": 125,
      "status": "up"
    }
  ],
  "worker_status": [
    {
      "worker_uid": "uuid", 
      "name": "Europe Worker",
      "status": "active",
      "active_jobs": 5
    }
  ]
}
```

## Configuration Management APIs

### GET /api/v1/config
Get global configuration
```json
Query parameters:
- category: string (filter by category)

Response:
{
  "data": [
    {
      "uid": "uuid",
      "category": "system",
      "key": "maintenance-mode",
      "value": {
        "enabled": false,
        "message": "System maintenance in progress"
      },
      "created_at": "2024-01-01T00:00:00Z"
    },
    {
      "uid": "uuid", 
      "category": "defaults",
      "key": "check-timeout",
      "value": {
        "default_timeout": 30,
        "max_timeout": 300
      }
    }
  ]
}
```

### POST /api/v1/config
Set global configuration value (system admin only)
```json
Request:
{
  "category": "system",
  "key": "rate-limits", 
  "value": {
    "authenticated": 1000,
    "anonymous": 100
  }
}

Response:
{
  "uid": "uuid",
  "category": "system",
  "key": "rate-limits",
  "value": {
    "authenticated": 1000,
    "anonymous": 100
  },
  "created_at": "2024-01-15T12:00:00Z"
}
```

### PUT /api/v1/config/{config_uid}
Update global configuration value (system admin only)
```json
Request:
{
  "value": {
    "authenticated": 2000,
    "anonymous": 200
  }
}

Response:
{
  "uid": "uuid",
  "category": "system", 
  "key": "rate-limits",
  "value": {
    "authenticated": 2000,
    "anonymous": 200
  },
  "updated_at": "2024-01-15T12:00:00Z"
}
```

### DELETE /api/v1/config/{config_uid}
Delete global configuration (system admin only)
```json
Response: 204 No Content
```

### GET /api/v1/orgs/{org}/config
Get organization configuration
```json
Query parameters:
- category: string (filter by category)

Response:
{
  "data": [
    {
      "uid": "uuid",
      "category": "notifications",
      "key": "email-alerts",
      "value": {
        "enabled": true,
        "recipients": ["admin@example.com"]
      },
      "created_at": "2024-01-01T00:00:00Z"
    },
    {
      "uid": "uuid", 
      "category": "integrations",
      "key": "slack-webhook",
      "value": {
        "url": "https://hooks.slack.com/...",
        "channel": "#alerts"
      }
    }
  ]
}
```

### POST /api/v1/orgs/{org}/config
Set configuration value (admin only)
```json
Request:
{
  "category": "notifications",
  "key": "sms-alerts", 
  "value": {
    "enabled": false,
    "provider": "twilio"
  }
}

Response:
{
  "uid": "uuid",
  "category": "notifications",
  "key": "sms-alerts",
  "value": {
    "enabled": false,
    "provider": "twilio"
  },
  "created_at": "2024-01-15T12:00:00Z"
}
```

### PUT /api/v1/orgs/{org}/config/{config_uid}
Update configuration value (admin only)
```json
Request:
{
  "value": {
    "enabled": true,
    "provider": "aws-sns"
  }
}

Response:
{
  "uid": "uuid",
  "category": "notifications", 
  "key": "sms-alerts",
  "value": {
    "enabled": true,
    "provider": "aws-sns"
  },
  "updated_at": "2024-01-15T12:00:00Z"
}
```

### DELETE /api/v1/orgs/{org}/config/{key}
Delete configuration (admin only)
```json
Response: 204 No Content
```

## System Management APIs (existing)

### GET /api/v1/mgmt/version
Get system version
```json
Response:
{
  "version": "1.0.0",
  "build": "abc123",
  "build_date": "2024-01-15T12:00:00Z"
}
```

### GET /v1/api/mgmt/health
System health check
```json
Response:
{
  "status": "healthy",
  "database": "connected", 
  "workers": 3
}
```

## Error Responses

All APIs use standard HTTP status codes and return errors in this format:
```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Invalid input data",
    "details": {
      "field": "email",
      "reason": "Email format is invalid"
    }
  }
}
```

## Pagination

List endpoints support pagination:

Query parameters:
- limit: number (max 100, default 50)
- offset: number (default 0)

## Rate Limiting

- Authenticated requests: 1000/hour per user
- Public endpoints: 100/hour per IP
- Returns 429 Too Many Requests with Retry-After header
