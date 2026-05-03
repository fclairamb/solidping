# Files storage foundation (`/files`)

## Context

We need a generic file-storage seam in the backend before we can ship the
bug-report feature (screenshots), and it will be reused later for:

- Historical status-page screenshots (BetterStack-style proof of past state)
- Possibly result compaction (parquet / vortex blobs on S3)

This spec only delivers the foundation: a `files` table, a pluggable
`filestorage` package (local FS + S3), authenticated read/list/delete, and a
signed-URL public read endpoint. **No callers are wired up here** — the
bug-report spec (#49) is the first consumer.

## Honest opinion

1. **Build the seam now, but only the seam.** Bug reports alone don't justify
   S3 + signed URLs + a file table. The justification is the next two use
   cases (status-page snapshots, parquet rollups), and retrofitting a generic
   layer into a one-off `/mgmt/report` upload path later is more painful than
   adding it once now while there's exactly one caller.
2. **Don't pre-build features.** No thumbnails. No checksum verification on
   read. No metadata sidecar files. No HTTP-FS backend. No multipart-resumable
   uploads. We can add any of these the first time we need them — by then
   we'll know the actual shape of the requirement.
3. **The `files` table is dead simple.** No "target entity" foreign key, no
   "flags", no `properties` jsonb. A file is `(uid, org, name, mime, size,
   uri, created_by, created_at)`. If a caller needs to associate a file with
   something else, that's the caller's job (FK in their table).
4. **S3 support is cheap because the AWS SDK already does the work.** ~80 LOC.
   Don't skip it — once the API is shipped, swapping the storage backend in
   prod is the kind of change that should be a config flip, not a migration.
5. **Public access is signed-URL-only, no "share token" model.** Two access
   modes total: authenticated owner read (org-scoped) and signed URL.
   Anything more complex (per-link revocation, audit log of fetches) is a
   future spec, not a v1 dependency.
6. **The `/pub/` prefix matters.** Keep public routes under `/pub/...` so an
   operator can firewall, log, or rate-limit them separately from `/api/...`.

## Scope

**In**

- `files` table + migration.
- `File` model with a minimal field set.
- `internal/handlers/filestorage/` package: interface + local FS backend +
  S3 backend, registered via factory by URI prefix (`file://`, `s3://`).
- Configuration (`SP_FILESTORAGE_TYPE`, `SP_FILESTORAGE_LOCAL_ROOT`,
  `SP_FILESTORAGE_S3_BUCKET`, `SP_FILESTORAGE_S3_REGION`, etc.).
- `internal/handlers/files/` handler+service: list / get-metadata / get-content
  / delete, scoped to the authenticated org. Programmatic create only — no
  generic upload endpoint until a UI needs one.
- `internal/handlers/files/signedurl/` package: HMAC-signed,
  time-limited URLs (max 365 days), constant-time verification.
- Public route `GET /pub/files/{fileUid}?exp=...&sig=...` — no auth
  middleware, returns the bytes with proper `Content-Type` /
  `Content-Disposition: inline`.

**Out**

- Upload endpoint for end users (no UI today; bug-report spec #49 uploads
  programmatically through the service).
- Ownership/share permissions beyond org-scoping.
- Thumbnail generation, virus scanning, deduplication.
- Audit log of public fetches.
- Rate limiting on `/pub/files/...` (operator can do this at the LB/WAF).
- Versioning, soft-delete, retention policies.

## Design

### 1. Database

New migration `files` table:

```sql
CREATE TABLE files (
    id              BIGSERIAL PRIMARY KEY,
    uid             UUID NOT NULL UNIQUE,
    organization_uid UUID NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    mime_type       TEXT NOT NULL,
    size            BIGINT NOT NULL,
    file_uri        TEXT NOT NULL,        -- "file://orgUid/group/fileId" or "s3://orgUid/group/fileId"
    sha256          TEXT,                  -- hex; nullable to keep insert path simple
    created_by      BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX files_org_created_idx ON files(organization_uid, created_at DESC) WHERE deleted_at IS NULL;
```

SQLite migration mirrors the same shape (UUID stored as text, timestamps as
text). The migration goes through the existing Flyway-style migration runner
in `server/internal/migrations/`.

### 2. `internal/handlers/filestorage/` package

Single interface for read/write:

```go
type GroupType string

const (
    GroupTypeReports GroupType = "reports" // bug-report screenshots
    // future: GroupTypeStatusSnapshots, GroupTypeResultRollups, ...
)

type FileMetadata struct {
    Filename string
    MimeType string
    Size     int64
}

type FileStorage interface {
    WriteFile(ctx context.Context, orgUID uuid.UUID, group GroupType, fileID string,
        r io.Reader, meta FileMetadata) (uri string, err error)
    ReadFile(ctx context.Context, orgUID uuid.UUID, group GroupType, fileID string) (io.ReadCloser, *FileMetadata, error)
    ParseURI(uri string) (orgUID uuid.UUID, group GroupType, fileID string, err error)
}
```

Factory registration so backends self-register:

```go
type StorageFactory func(cfg config.FileStorageConfig) (FileStorage, error)
func RegisterStorageFactory(prefix string, factory StorageFactory)
func NewFileStorage(cfg config.FileStorageConfig) (FileStorage, error)
func GetStorageForURI(uri string, cfg config.FileStorageConfig) (FileStorage, error)
```

Backends:

- `filestorage/localfs/`: writes to `<root>/<orgUid>/<group>/<fileId>`,
  reads via `os.Open`, no metadata sidecar (the `files` row is the truth).
- `filestorage/s3fs/`: writes via PutObject with `Content-Type` set from
  metadata; reads via GetObject; uses `aws-sdk-go-v2`. Region + bucket from
  config; credentials picked up from the standard AWS chain.

Reads use `GetStorageForURI(file.FileURI, cfg)` so the right backend handles
each blob even after a config flip.

### 3. Configuration

Add to `config.Config`:

```go
type FileStorageConfig struct {
    Type      string `koanf:"type"`         // "local" (default) or "s3"
    LocalRoot string `koanf:"local_root"`   // for local: filesystem root
    S3Bucket  string `koanf:"s3_bucket"`
    S3Region  string `koanf:"s3_region"`
    S3Prefix  string `koanf:"s3_prefix"`    // optional key prefix
}
```

Defaults: `Type: "local"`, `LocalRoot: "./data/files"`. Env vars follow the
existing `SP_*` convention. Add the keys to `systemconfig` so they can be
inspected (and `s3_*` set) from the system parameters API. **Do not** persist
AWS credentials in the database — they come from the AWS SDK chain
(env / IAM role / shared config).

### 4. `internal/handlers/files/` handler+service

Routes (under the standard auth/org middleware, mounted in `server.go`):

| Method | Path                                     | Purpose                              |
|--------|------------------------------------------|--------------------------------------|
| GET    | `/api/v1/orgs/$org/files`                | List files (paginated, q= search)    |
| GET    | `/api/v1/orgs/$org/files/$uid`           | File metadata                        |
| GET    | `/api/v1/orgs/$org/files/$uid/content`   | Stream the bytes (auth, org-scoped)  |
| DELETE | `/api/v1/orgs/$org/files/$uid`           | Soft-delete (sets `deleted_at`)      |

Service exposes a programmatic `CreateFile(ctx, orgUID, group, name, mime,
reader)` for internal callers (bug-report service in spec #49). No HTTP
upload route in v1.

Conventions:

- Response shape: `{ "data": [...], "page": ..., "total": ... }` (matches
  existing endpoints).
- Errors: `FILE_NOT_FOUND`, `FILE_TOO_LARGE`, `FORBIDDEN` (cross-org access).
- DELETE flips `deleted_at` only; the blob in storage is left in place. A
  future "garbage collect orphan blobs" job can sweep them — explicitly out
  of scope here.

### 5. Signed URLs (`internal/handlers/files/signedurl/`)

Single source of truth for sign/verify. Same shape as a JWT-secret-keyed
HMAC, kept tiny:

```go
const MaxSignedURLTTL = 365 * 24 * time.Hour

var (
    ErrSignedURLExpired      = errors.New("signed URL expired")
    ErrSignedURLBadSignature = errors.New("signed URL has a bad signature")
)

func Sign(secret []byte, fileUID uuid.UUID, ttl time.Duration) (exp int64, sig string)
func Verify(secret []byte, fileUID uuid.UUID, exp int64, sig string, now time.Time) error
func BuildURL(baseURL string, fileUID uuid.UUID, exp int64, sig string) string
```

- `sig` = first 32 hex chars of `HMAC-SHA256(secret, "<fileUID>.<exp>")`.
- `Verify` uses `hmac.Equal` (constant time). Expiry checked after signature
  match so an invalid sig never reveals expiry timing.
- `Sign` clamps `ttl` to `MaxSignedURLTTL` (and logs at warn).
- HMAC key is `cfg.Auth.JWTSecret`. Rotating the JWT secret invalidates
  every outstanding URL — acceptable, documented.

### 6. Public route

```
GET /pub/files/{fileUid}?exp=<unixSeconds>&sig=<hex>
```

- Mounted **outside** the auth middleware group (`/pub/...` prefix).
- Looks up the file by UID (404 if unknown or soft-deleted).
- Calls `signedurl.Verify` → translates errors:
  - `ErrSignedURLBadSignature` → 403
  - `ErrSignedURLExpired`      → 410 Gone
- Streams via `GetStorageForURI(file.FileURI, cfg)`, sets `Content-Type` from
  the row and `Content-Disposition: inline; filename="..."`.
- No range-request support in v1 (small images / short videos only). If a
  later use case needs it, add it then.

## Files affected

| File / dir                                                     | Change                                                              |
|----------------------------------------------------------------|---------------------------------------------------------------------|
| `server/internal/migrations/NNN_files.sql`                     | New migration creating `files` table (PG + SQLite variants)         |
| `server/internal/models/file.go`                               | New `File` Bun model                                                |
| `server/internal/config/config.go`                             | Add `FileStorageConfig`, defaults, env loading                      |
| `server/internal/systemconfig/systemconfig.go`                 | Add `filestorage.*` parameter keys (non-secret)                     |
| `server/internal/handlers/filestorage/filestorage.go`          | Interface + `FileMetadata` + factory registry                       |
| `server/internal/handlers/filestorage/router.go`               | `NewFileStorage` / `GetStorageForURI`                               |
| `server/internal/handlers/filestorage/localfs/localfs.go`      | Local FS backend                                                    |
| `server/internal/handlers/filestorage/s3fs/s3fs.go`            | S3 backend (aws-sdk-go-v2)                                          |
| `server/internal/handlers/files/handler.go`                    | List/get/delete handlers                                            |
| `server/internal/handlers/files/service.go`                    | Service, including programmatic `CreateFile`                        |
| `server/internal/handlers/files/signedurl/signedurl.go`        | Sign/Verify/BuildURL + tests                                        |
| `server/internal/handlers/files/public_handler.go`             | `GET /pub/files/{uid}` handler (signed-URL verified)                |
| `server/internal/app/server.go`                                | Register routes (auth + public), wire service into registry         |
| `server/internal/app/services/services.go`                     | Add files service to `ServicesList`                                 |
| `docs/api-specification.md`                                    | Add `/files` and `/pub/files` to the endpoint list                  |
| `docs/conventions/files.md` (new)                              | Short doc: storage backends, signed URLs, group conventions         |

## Tests

- **`signedurl`** — sign/verify round-trip, tampered sig, expired exp,
  TTL clamp at 365 d, constant-time compare smoke test.
- **`localfs`** — write+read round-trip, ParseURI validity, missing file →
  error, size enforcement.
- **`s3fs`** — round-trip against `localstack` testcontainer (already used
  elsewhere) or, if that's heavy, a mock; gate behind a build tag if needed.
- **`files` service** — list scoping by org, delete sets `deleted_at`,
  cross-org read returns 404, create returns expected URI scheme.
- **public handler** — 200 with valid params, 410 expired, 403 bad sig,
  404 unknown UID, correct `Content-Type` / `Content-Disposition`.

## Verification

1. `make test` — all new tests pass.
2. `make dev` — start with `SP_FILESTORAGE_TYPE=local` (default), POST a
   File via service from a unit-test harness, GET it back from
   `/api/v1/orgs/default/files`, then sign a URL and `curl /pub/files/...`
   and confirm it returns the bytes.
3. **Negative path** — tamper the `sig` query param: 403; bump `exp` to 0:
   410.
4. With `SP_FILESTORAGE_TYPE=s3` against a local LocalStack, confirm
   PutObject lands in the bucket and the same `/pub/files/...` URL fetches
   through `s3fs`.
