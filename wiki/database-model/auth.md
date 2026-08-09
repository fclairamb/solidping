# Authentication & User Management Tables

User accounts, credentials, external identity providers, org membership, and the
OAuth 2.1 client registry. See [README.md](README.md) for the full index.

### users
Global user accounts with authentication.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| email | text | Globally unique email (case-insensitive) |
| name | text | Display name |
| avatar_url | text | Profile picture URL |
| password_hash | text | Argon2id hash (NULL for SSO-only) |
| email_verified_at | timestamptz | Email verification timestamp |
| super_admin | boolean | Can access all organizations |
| last_active_at | timestamptz | Last login/activity |
| totp_secret | text | Base32-encoded TOTP secret for 2FA (NULL if not configured) |
| totp_enabled | boolean | Whether TOTP two-factor authentication is active |
| totp_recovery_codes | jsonb | JSON array of hashed one-time recovery codes for 2FA bypass |

**Foreign Keys**: None (global entity)

**Indexes**: unique on lower(email) where not deleted

---

### organization_providers
Maps organizations to external identity providers. One provider identity belongs to exactly one org.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| provider_type | text | Provider type: slack, google, github, gitlab, microsoft, discord, saml, oidc |
| provider_id | text | Unique identifier from the provider (e.g., Slack Team ID) |
| provider_name | text | Human-readable provider name (e.g., "Acme Corp Slack Workspace") |
| metadata | jsonb | Provider-specific metadata |
| metadata_private | text | AES-256-GCM envelope of the secret metadata fields |
| metadata_private_keys | text | Names of the keys held in `metadata_private` |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**:
- Unique on (provider_type, provider_id) where not deleted - for provider lookups
- Index on organization_uid where not deleted

**Purpose**: Defines which external identity provider identities belong to which organization. When a user authenticates via Slack OAuth, the `provider_id` (Slack team ID) determines which organization they belong to.

---

### user_providers
Links users to external auth providers (OAuth, SAML, OIDC, LDAP).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| user_uid | uuid | FK to users |
| provider_type | text | Provider: google, github, gitlab, microsoft, twitter, slack, discord, saml, oidc, ldap |
| provider_id | text | External identifier (e.g., OAuth sub claim) |
| metadata | jsonb | Provider-specific data (profile, tokens) |

**Foreign Keys**: `user_uid` → users(uid)

**Indexes**: unique on (provider_type, provider_id); index on user_uid

**Purpose**: Authentication identity - "How does this user authenticate to SolidPing?"

---

### user_passkeys
WebAuthn/FIDO2 credentials registered by a user for passwordless sign-in.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| user_uid | uuid | FK to users |
| name | text | User-chosen label for the authenticator |
| credential_id | bytea | WebAuthn credential ID |
| public_key | bytea | COSE-encoded public key |
| aaguid | text | Authenticator model identifier |
| sign_count | bigint | Signature counter for clone detection |
| transports | jsonb | Reported transports (usb, nfc, ble, internal, hybrid) |
| backup_eligible | boolean | Credential may be synced across devices |
| backup_state | boolean | Credential is currently backed up |
| user_verified | boolean | User verification was performed at registration |
| attestation_format | text | Attestation statement format |
| last_used_at | timestamptz | Last successful authentication |

**Foreign Keys**: `user_uid` → users(uid)

**Indexes**: unique on (user_uid, credential_id); index on credential_id; index on user_uid where not deleted

---

### user_tokens
Personal Access Tokens (PAT), session refresh tokens, and OAuth 2.1 refresh grants.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| user_uid | uuid | FK to users |
| organization_uid | uuid | FK to organizations (for PAT scope; NULL for global refresh tokens) |
| token | text | Hashed token value |
| type | text | Token type: pat, refresh, oauth_refresh |
| properties | jsonb | Token metadata (name, scopes; client_id/scope/resource for oauth_refresh) |
| expires_at | timestamptz | Token expiration (NULL = never) |
| last_active_at | timestamptz | Last usage |

**Foreign Keys**:
- `user_uid` → users(uid)
- `organization_uid` → organizations(uid)

**Indexes**: unique on (token) where not deleted; index on user_uid; index on expires_at

---

### user_contacts
Per-org contact endpoints for a user (email address, phone number, …) used as
notification destinations.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| user_uid | uuid | FK to users |
| organization_uid | uuid | FK to organizations |
| type | text | Contact type (e.g. email, sms, voice) |
| value | text | The address or number |
| label | text | Optional user-facing label (default empty) |
| verified_at | timestamptz | When the contact was verified (NULL = unverified) |

**Foreign Keys**:
- `user_uid` → users(uid)
- `organization_uid` → organizations(uid)

**Indexes**: unique on (user_uid, organization_uid, type, value); index on (user_uid, organization_uid) where not deleted

---

### organization_members
Links users to organizations with role-based access control.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| user_uid | uuid | FK to users |
| organization_uid | uuid | FK to organizations |
| role | text | Role: owner, admin, user, viewer (hierarchical: owner > admin > user > viewer) |
| invited_by_uid | uuid | FK to users (who invited) |
| invited_at | timestamptz | Invitation timestamp |
| joined_at | timestamptz | Acceptance timestamp (NULL = pending) |

**Foreign Keys**:
- `user_uid` → users(uid)
- `organization_uid` → organizations(uid)
- `invited_by_uid` → users(uid)

**Indexes**: unique on (user_uid, organization_uid) where not deleted

---

### membership_requests
Self-service requests from a user to join an organization, with the admin's
decision. At most one request row per (org, user).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| user_uid | uuid | FK to users |
| message | text | Optional note from the requester |
| status | text | pending, approved, rejected, canceled |
| decision_reason | text | Optional reason recorded with the decision |
| decided_at | timestamptz | When the request was decided |
| decided_by_uid | uuid | FK to users (the deciding admin) |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `user_uid` → users(uid)
- `decided_by_uid` → users(uid) (on delete set null)

**Indexes**: unique on (organization_uid, user_uid); indexes on (organization_uid, status) and (user_uid, status)

---

### oauth_clients
OAuth 2.1 clients registered against the MCP resource, either via RFC 7591
dynamic registration or as first-party clients. Global, not org-scoped.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| client_id | text | Public client identifier |
| secret_hash | text | Hashed client secret for confidential clients; NULL for public clients |
| client_name | text | Human-readable client name |
| redirect_uris | jsonb | Allowed redirect URIs |
| grant_types | jsonb | Allowed grant types |
| scopes | jsonb | Allowed scopes |
| is_public | boolean | True for public clients (PKCE + loopback redirects, no secret) |

**Foreign Keys**: None (global entity)

**Indexes**: unique on (client_id)

**Related storage**: authorization codes are single-use, 60s-lived rows in
`state_entries` (key `oauth_auth_code:<random>`); rotating refresh grants are
`user_tokens` rows of type `oauth_refresh`. See `server/internal/oauth/service.go`.
