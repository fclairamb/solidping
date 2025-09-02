# Credentials Storage and Sharing

## Problem Statement

How can we perform the storage and sharing of credentials between jobs?

Use cases:
- API keys for authenticated HTTP checks
- Basic auth credentials
- Bearer tokens
- TLS client certificates
- Database connection strings
- Webhook signing secrets

## Current Foundation

The schema already has a `parameters` table designed for this:

```sql
create table parameters (
    uid               uuid primary key,
    organization_uid  uuid not null references organizations(uid),
    key               text not null check (key ~ '^[a-z0-9_\.]+$'),
    value             jsonb not null,
    secret            boolean,  -- <-- Flag for sensitive values
    ...
);
```

And `check_jobs.config` is described as "merged config of the job and the parameters".

## Proposed Solution: Parameter References with Secret Handling

### 1. Credential Storage in Parameters

Store credentials as parameters with `secret: true`:

```json
// POST /api/v1/orgs/$org/parameters
{
  "key": "credentials.api.production",
  "value": {
    "api_key": "sk-prod-abc123...",
    "api_secret": "secret..."
  },
  "secret": true
}
```

**Naming convention**: Use dot-notation namespaces:
- `credentials.` - for secrets
- `config.` - for non-sensitive configuration
- `env.` - for environment-specific values

### 2. Reference Syntax in Check Config

Checks reference parameters using `$param{key}` syntax:

```json
// Check configuration
{
  "slug": "api_health",
  "type": "http",
  "config": {
    "url": "https://api.example.com/health",
    "headers": {
      "Authorization": "Bearer $param{credentials.api.production.api_key}",
      "X-Custom-Header": "$param{config.custom_header}"
    }
  }
}
```

### 3. Resolution Flow

```
┌─────────────────────────────────────────────────────────────────┐
│ checks                                                          │
│ config: {headers: {Authorization: "Bearer $param{creds.api}"}} │
└─────────────────────────────────────────────────────────────────┘
                              │
                              │ on enable/update
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│ check_jobs                                                      │
│ config: {headers: {Authorization: "Bearer sk-prod-abc123..."}} │
│ parameter_refs: ["credentials.api.production"]                  │
└─────────────────────────────────────────────────────────────────┘
                              │
                              │ worker executes
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│ results                                                         │
│ output: {request: {headers: {Authorization: "[REDACTED]"}}}    │
└─────────────────────────────────────────────────────────────────┘
```

### 4. Schema Additions

```sql
-- Track which parameters a job depends on (for cache invalidation)
alter table check_jobs add column parameter_refs text[];

comment on column check_jobs.parameter_refs is
  'List of parameter keys this job depends on. Used for invalidation when parameters change.';

-- Add encryption key reference for at-rest encryption
alter table parameters add column encrypted boolean not null default false;

comment on column parameters.encrypted is
  'Whether the value is encrypted at rest. Requires server-side decryption key.';
```

### 5. Security Measures

#### At Rest
- **Encryption**: Secret parameters encrypted using organization-specific keys
- **Key management**: Master key in environment variable, per-org keys derived

```go
// Encryption flow
func encryptParameter(orgUID uuid.UUID, value []byte) []byte {
    orgKey := deriveKey(masterKey, orgUID)
    return aesGCMEncrypt(orgKey, value)
}
```

#### In Transit
- Parameters resolved server-side before sending to workers
- Workers receive resolved config, never parameter references
- TLS for all worker communication

#### In Logs/Results
- **Redaction**: Any value from a secret parameter is redacted in:
  - Result output
  - Error messages
  - Audit logs
- **Pattern matching**: Redact known secret patterns (Bearer tokens, API keys)

```go
// Redaction in results
func redactSecrets(output map[string]any, secretKeys []string) map[string]any {
    // Replace secret values with "[REDACTED]"
    // Also pattern-match common secret formats
}
```

#### Access Control
- Only `admin` role can create/update/delete secret parameters
- `user` role can reference secrets in checks but not view values
- `viewer` role cannot see secret parameters at all

### 6. API Design

**Create secret parameter:**
```http
POST /api/v1/orgs/$org/parameters
{
  "key": "credentials.api.production",
  "value": {"api_key": "sk-...", "api_secret": "..."},
  "secret": true
}
```

**List parameters (secrets redacted):**
```http
GET /api/v1/orgs/$org/parameters

{
  "data": [
    {
      "uid": "...",
      "key": "credentials.api.production",
      "value": "[REDACTED]",  // Never expose secret values
      "secret": true
    },
    {
      "uid": "...",
      "key": "config.timeout",
      "value": {"default": 30},
      "secret": false
    }
  ]
}
```

**Check which checks use a parameter:**
```http
GET /api/v1/orgs/$org/parameters/$uid/usage

{
  "data": {
    "checks": [
      {"uid": "...", "slug": "api_health", "name": "API Health Check"}
    ]
  }
}
```

### 7. Parameter Update Propagation

When a parameter is updated:

1. Find all `check_jobs` where `parameter_refs` contains the key
2. Re-resolve the config for each affected job
3. Update `check_jobs.config` with new resolved values

```go
func (s *ParameterService) Update(ctx context.Context, uid uuid.UUID, value any) error {
    // Update parameter
    param, err := s.updateParameter(ctx, uid, value)

    // Find affected jobs
    jobs, err := s.db.NewSelect().
        Model((*CheckJob)(nil)).
        Where("? = ANY(parameter_refs)", param.Key).
        Scan(ctx)

    // Re-resolve each job's config
    for _, job := range jobs {
        s.resolveJobConfig(ctx, job)
    }
}
```

### 8. Credential Types

Support structured credential types for validation and UI:

```json
{
  "key": "credentials.api.production",
  "type": "bearer_token",  // Optional type hint
  "value": {
    "token": "sk-..."
  },
  "secret": true
}
```

**Supported types:**
- `bearer_token` - Single token value
- `basic_auth` - Username/password pair
- `api_key` - Key/secret pair
- `tls_cert` - Certificate + private key (PEM)
- `oauth2_client` - Client ID/secret + token URL
- `custom` - Arbitrary JSON (default)

### 9. Implementation Steps

1. **Phase 1: Basic secret storage**
   - Add `encrypted` column to parameters
   - Implement encryption/decryption in parameter service
   - Add `parameter_refs` to check_jobs
   - Implement `$param{key}` resolution

2. **Phase 2: Security hardening**
   - Result/log redaction
   - Access control by role
   - Audit logging for secret access

3. **Phase 3: Enhanced features**
   - Credential types with validation
   - Parameter usage tracking
   - Secret rotation support
   - Expiration dates for credentials

### 10. Example: Full Flow

```json
// 1. Create a secret parameter
POST /api/v1/orgs/acme/parameters
{
  "key": "credentials.stripe.live",
  "value": {"api_key": "sk_live_..."},
  "secret": true
}

// 2. Create a check using the parameter
POST /api/v1/orgs/acme/checks
{
  "slug": "stripe_api",
  "type": "http",
  "config": {
    "url": "https://api.stripe.com/v1/balance",
    "headers": {
      "Authorization": "Bearer $param{credentials.stripe.live.api_key}"
    }
  }
}

// 3. System creates check_job with resolved config
// check_jobs.config = {
//   "url": "https://api.stripe.com/v1/balance",
//   "headers": {"Authorization": "Bearer sk_live_..."}
// }
// check_jobs.parameter_refs = ["credentials.stripe.live"]

// 4. Worker executes, result stored with redaction
// results.output = {
//   "status_code": 200,
//   "request": {"headers": {"Authorization": "[REDACTED]"}}
// }
```

### 11. Alternatives Considered

**External secret manager (Vault, AWS Secrets Manager)**
- Pros: Industry-standard, advanced features (rotation, audit)
- Cons: Additional dependency, latency, complexity
- Decision: Support as optional integration in future, not required

**Secrets in environment variables**
- Pros: Simple, follows 12-factor
- Cons: Not multi-tenant, hard to manage per-check
- Decision: Use for master encryption key only

**Per-check encrypted fields**
- Pros: Simpler model
- Cons: No sharing between checks, duplication
- Decision: Parameters table enables sharing and centralized management
