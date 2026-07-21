# Core Infrastructure Tables

Tenancy root, configuration, and file storage. See [README.md](README.md) for
the full index.

### organizations
Multi-tenant root entity for isolating monitoring resources.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| slug | text | URL-friendly unique identifier (3-20 chars) |
| name | text | Human-readable display name |
| default_escalation_policy_uid | uuid | FK to escalation_policies. Org-wide fallback for checks that resolve to no policy (check > group > org default > none). NULL = no org default |
| created_at, updated_at, deleted_at | timestamptz | Timestamps with soft delete |

**Foreign Keys**: `default_escalation_policy_uid` → escalation_policies(uid) (on delete set null)

**Indexes**: unique on (slug) where not deleted

---

### parameters
Key-value configuration storage per organization or system-wide.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations (NULL = system-wide) |
| key | text | Configuration key, dot-separated (e.g. `smtp.host`) |
| value | jsonb | Configuration value |
| secret | boolean | Whether value is sensitive/encrypted |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**:
- Unique on (organization_uid, key) where not deleted and organization_uid is not null
- Unique on (key) where not deleted and organization_uid is null

---

### app_settings
Global process-level key-value settings. The only table that is neither
UUID-keyed nor organization-scoped.

| Column | Type | Description |
|--------|------|-------------|
| key | text PK | Setting name |
| value | text | Setting value |
| updated_at | timestamptz | Last write |

**Foreign Keys**: None (global entity)

---

### files
Stored file blobs and their metadata. The bytes live behind the storage backend
identified by the `file_uri` scheme (`file://`, `s3://`).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| name | text | Original file name |
| mime_type | text | MIME type |
| size | bigint | Size in bytes |
| file_uri | text | Backend URI of the stored blob (`file://`, `s3://`) |
| sha256 | text | Content hash (NULL if not computed) |
| created_by | uuid | FK to users (NULL after the uploader is deleted) |
| created_at, deleted_at | timestamptz | Creation timestamp with soft delete |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `created_by` → users(uid) (on delete set null)

**Indexes**: (organization_uid, created_at desc) where not deleted

See also `wiki/conventions/files.md`.
