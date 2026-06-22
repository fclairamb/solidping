# File storage conventions

The `files` table is the source of truth for file metadata. The actual bytes
live behind a pluggable backend identified by the URI scheme stored in
`files.file_uri` (`file://` for the local FS, `s3://` for S3).

## Storage backends

Configured via `SP_FILESTORAGE_*` environment variables (or `filestorage.*` in
the YAML config):

| Setting          | Env var                       | Notes                              |
|------------------|-------------------------------|------------------------------------|
| `type`           | `SP_FILESTORAGE_TYPE`         | `local` (default) or `s3`          |
| `local_root`     | `SP_FILESTORAGE_LOCAL_ROOT`   | Filesystem root for local backend  |
| `s3_bucket`      | `SP_FILESTORAGE_S3_BUCKET`    | S3 bucket name                     |
| `s3_region`      | `SP_FILESTORAGE_S3_REGION`    | S3 region                          |
| `s3_prefix`      | `SP_FILESTORAGE_S3_PREFIX`    | Optional key prefix                |

AWS credentials come from the standard AWS SDK chain (env vars, IAM role,
shared config) — they are **never** stored in the database.

Layout under both backends is identical:

```
<root-or-bucket>/<orgUid>/<group>/<fileId>
```

`group` is one of the constants in `internal/handlers/filestorage`
(`reports`, etc.).

## Programmatic upload

Internal callers go through the files service:

```go
filesSvc := files.NewService(dbService, cfg)
file, err := filesSvc.CreateFile(
    ctx, orgUUID, filestorage.GroupTypeReports,
    "screenshot.png", "image/png", &userUID, body, sizeBytes,
)
```

The service writes the bytes to the configured backend, inserts the
`files` row, and returns the model. Maximum size is 25 MB; callers needing
more should bypass the service and use the storage backend directly.

There is no HTTP upload endpoint in v1 — the only producer today is the
bug-report service.

## Signed URLs

The `signedurl` package produces and verifies short-lived HMAC signatures:

```go
exp, sig := signedurl.Sign([]byte(cfg.Auth.JWTSecret), fileUID, 24*time.Hour)
url := signedurl.BuildURL(cfg.Server.BaseURL, fileUID, exp, sig)
```

Properties:

- HMAC-SHA256, first 128 bits hex-encoded.
- TTL clamped to 365 days.
- Verification uses constant-time compare and checks the signature **before**
  the expiry, so an invalid sig never reveals expiry timing.
- The HMAC key is the JWT secret. **Rotating the JWT secret invalidates every
  outstanding URL.** This is by design.

## Access modes

| Caller                     | Path                                          | Auth                |
|----------------------------|-----------------------------------------------|---------------------|
| Operator UI / API user     | `GET /api/v1/orgs/$org/files/$uid/content`    | JWT (org-scoped)    |
| Public link                | `GET /pub/files/$uid?exp=&sig=`               | Signed URL          |

Anything more elaborate (per-link revocation, audit log of fetches) is a
future feature, not v1.

## Soft-delete

`DELETE /api/v1/orgs/$org/files/$uid` flips `deleted_at` only — the blob in
storage is left in place. A future GC job can sweep orphan blobs.
