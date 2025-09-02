# System Parameters

## Overview

This spec formalizes the system parameters pattern and introduces the `base_url` parameter. System parameters are configuration values that:
1. Can be set via environment variables (highest priority)
2. Can be stored in the database (medium priority)
3. Fall back to hardcoded defaults (lowest priority)

## Requirements

### Parameter Loading Priority

Parameters shall be loaded with the following precedence (highest to lowest):
1. **Environment variables** (`SP_*` prefix) - Always takes precedence
2. **Database** (system parameters in `parameters` table where `organization_uid IS NULL`)
3. **Hardcoded defaults**

### New Parameter: `base_url`

| Property | Value |
|----------|-------|
| Key | `base_url` |
| Environment Variable | `SP_BASE_URL` |
| Default | `http://localhost:4000` |
| Secret | `false` |
| Description | The public URL where SolidPing is accessible |

**Use cases for `base_url`:**
- Email notifications with links back to the dashboard
- Slack messages with incident links
- OAuth callback URLs
- Webhook verification

### Auto-Generated Parameters

Some parameters shall be auto-generated at startup if not explicitly set:

| Parameter | Condition | Generation |
|-----------|-----------|------------|
| `jwt_secret` | Not set via env var AND default value unchanged AND not in database | Generate 32-byte random secret |

**Auto-generation behavior:**
1. Check if env var is set → skip generation
2. Check if config differs from default → skip generation
3. Check if value exists in database → load it
4. Otherwise → generate secure random value and save to database

This ensures:
- Explicit configuration is always respected
- Secrets are not regenerated on restart (persisted in DB)
- Each installation gets a unique secret automatically

## Configuration Examples

### Environment Variables

```bash
# Override base_url via environment
SP_BASE_URL=https://status.example.com

# Override jwt_secret via environment
SP_AUTH_JWT_SECRET=my-secure-secret-key
```

### YAML Configuration

```yaml
server:
  base_url: "https://status.example.com"

auth:
  jwt_secret: "my-secure-secret-key"
```

## Implementation

### 1. Add `base_url` to Config struct

In `back/internal/config/config.go`:

```go
type ServerConfig struct {
    Listen          string        `koanf:"listen"`
    BaseURL         string        `koanf:"base_url"`
    ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
    // ...
}
```

Default:
```go
Server: ServerConfig{
    Listen:  ":4000",
    BaseURL: "http://localhost:4000",
    // ...
}
```

### 2. Register `base_url` in systemconfig

In `back/internal/systemconfig/systemconfig.go`:

```go
const (
    KeyBaseURL   ParameterKey = "base_url"
    // ...
)

// Add to knownParameters
{
    Key:    KeyBaseURL,
    EnvVar: "SP_BASE_URL",
    Secret: false,
    ApplyFunc: func(cfg *config.Config, value any) {
        if v, ok := value.(string); ok && v != "" {
            cfg.Server.BaseURL = v
        }
    },
},
```

### 3. Ensure proper initialization order

At startup:
1. Load config from defaults + YAML + env vars (koanf)
2. Initialize database connection
3. Call `systemconfig.Service.ApplyDatabaseParameters()` to load DB overrides
4. Call `systemconfig.Service.EnsureRequiredParameters()` to auto-generate missing secrets

## Database Storage

System parameters are stored in the existing `parameters` table with `organization_uid IS NULL`:

```sql
-- Example: base_url stored in database
INSERT INTO parameters (key, value, secret, organization_uid)
VALUES ('base_url', '"https://status.example.com"', false, NULL);

-- Example: auto-generated jwt_secret
INSERT INTO parameters (key, value, secret, organization_uid)
VALUES ('jwt_secret', '"<generated-secret>"', true, NULL);
```

## Testing

- [ ] `base_url` loads from environment variable
- [ ] `base_url` loads from database when env var not set
- [ ] `base_url` defaults to `http://localhost:4000`
- [ ] `jwt_secret` auto-generates when not configured
- [ ] `jwt_secret` persists across restarts (stored in DB)
- [ ] `jwt_secret` env var takes precedence over DB value
- [ ] Secret parameters are not exposed in logs or API responses

## Summary

**Key decisions:**
- Environment variables always override database values
- `jwt_secret` is auto-generated only once and persisted in DB
- `base_url` is a non-secret parameter used for external links
- The existing `parameters` table and `systemconfig` service are reused
