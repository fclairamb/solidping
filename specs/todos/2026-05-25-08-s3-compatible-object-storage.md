# S3-Compatible Object Storage (MinIO / R2 / Garage)

> Unblocks roadmap **P1.3** (screenshot-on-failure, `specs/ideas/2026-01-05-screenshots.md`)
> and `specs/ideas/2026-05-03-vortex-s3-compacted-results.md`.

## Context

The S3 backend is **not** a stub — it is fully implemented and wired at startup:

- `server/internal/handlers/filestorage/s3fs/s3fs.go` implements `WriteFile` /
  `ReadFile` / `ParseURI` against `aws-sdk-go-v2` (`PutObject` / `GetObject`).
- `server/internal/app/server.go:285-286` calls `localfs.Register()` and
  `s3fs.Register()` at bootstrap; `filestorage.NewFileStorage` selects by `cfg.Type`.
- Config keys exist: `filestorage.s3_bucket`, `s3_region`, `s3_prefix`
  (`server/internal/config/config.go:206-212`).

The "stubbed client" comment at `s3fs.go:31-32` refers only to the **test** seam (tests
inject a fake `*s3.Client` via `s3fs.New`), not to production.

## The actual gap

1. **No custom endpoint / path-style support.** `Register()` (`s3fs.go:43-61`) calls
   `awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.S3Region))` and
   `s3.NewFromConfig(awsCfg)` with **no** `BaseEndpoint` and **no** `UsePathStyle`. That
   means it only talks to **AWS S3** — it cannot target **MinIO, Cloudflare R2, Garage,
   Ceph/RGW, or Backblaze B2**. The screenshots roadmap item (P1.3) explicitly assumes
   "MinIO works for self-hosters" — today it does not. This is the real blocker for
   self-hosted screenshot storage.

2. **No static-credentials config path.** Credentials come only from the ambient AWS
   chain (env / IAM role / shared config) via `LoadDefaultConfig`. Self-hosters pointing
   at MinIO/Garage need explicit access key + secret in SolidPing's own config.

3. **The env loader can't reach multi-word filestorage keys.** koanf's env provider
   (`config.go:465-468`) collapses every underscore after the `SP_` prefix to a dot, so
   `SP_FILESTORAGE_S3_BUCKET` maps to `filestorage.s3.bucket`, which does **not** match
   the `koanf:"s3_bucket"` tag. **The existing `s3_bucket`/`s3_region`/`s3_prefix` keys
   are therefore settable via YAML only, not env** — there is no manual reader for them
   (cf. the `applyRateLimitingEnv` / `SP_SERVER_MAX_REQUEST_DURATION` workarounds at
   `config.go:507-523`). Any new multi-word key (`s3_endpoint`, `s3_use_path_style`,
   `s3_access_key`, `s3_secret_key`) hits the same wall. Self-hosters configure via env,
   so this must be fixed for the feature to be usable.

4. **Only `GroupTypeReports` exists** (`filestorage.go:27`). Screenshots (P1.3) need a
   new group.

## Goals

1. Make the S3 backend work against any S3-compatible store, not just AWS.
2. Let self-hosters configure endpoint + static credentials via **env** (and YAML).
3. Add a `screenshots` storage group so P1.3 can build on it.
4. Keep AWS-native behavior (ambient credential chain, no endpoint) as the default when
   the new keys are unset — no breaking change.

## Non-goals

- Per-org DB-encrypted credentials. The S3 secret is a **deployment config secret**
  (env / YAML / mounted file), loaded once at startup — not a user-facing, API-set
  secret. It does **not** go through `internal/crypto/credentials` (that path is for
  `checks.config` / `integration_connections.settings`, set via the API and stored in
  the DB). Just never log it in clear.
- Multi-bucket / per-org buckets. One configured backend, as today.
- Implementing screenshots themselves — that's P1.3. This only adds the group constant.

## Approach

### 1. Extend the config structs (`config.go`, `filestorage.go`)

Add to `FileStorageConfig` (`config.go:206-212`):

```go
S3Endpoint    string `koanf:"s3_endpoint"`        // custom endpoint, e.g. https://minio.local:9000
S3UsePathStyle bool  `koanf:"s3_use_path_style"`  // true for MinIO/Garage/Ceph
S3AccessKey   string `koanf:"s3_access_key"`      // optional static cred (else AWS chain)
S3SecretKey   string `koanf:"s3_secret_key"`      // optional static cred — never logged
```

Mirror the four new fields onto `filestorage.Config` (`filestorage.go:107-113`) and copy
them in the construction site at `config.go:411`.

### 2. Add the manual env reader (`config.go`)

The new keys are multi-word, so they need explicit `os.Getenv` reads like the existing
workarounds. Add an `applyFileStorageEnv(&cfg.FileStorage)` call next to
`applyRateLimitingEnv` (`config.go:523`), and define it near `applyRateLimitingEnv`
(~`config.go:556`):

```go
// applyFileStorageEnv reads SP_FILESTORAGE_S3_* into cfg. koanf's env loader
// collapses underscores in SP_*-prefixed names to dots, so it would map these
// to filestorage.s3.endpoint and miss the snake_case koanf tags.
func applyFileStorageEnv(cfg *FileStorageConfig) {
	if v := os.Getenv("SP_FILESTORAGE_S3_ENDPOINT"); v != "" {
		cfg.S3Endpoint = v
	}
	if v := os.Getenv("SP_FILESTORAGE_S3_USE_PATH_STYLE"); v == "true" || v == "1" {
		cfg.S3UsePathStyle = true
	}
	if v := os.Getenv("SP_FILESTORAGE_S3_ACCESS_KEY"); v != "" {
		cfg.S3AccessKey = v
	}
	if v := os.Getenv("SP_FILESTORAGE_S3_SECRET_KEY"); v != "" {
		cfg.S3SecretKey = v
	}
}
```

While here, also read `SP_FILESTORAGE_S3_BUCKET` / `_REGION` / `_PREFIX` in the same
helper so the whole S3 backend is env-configurable (today they are YAML-only — a latent
gap that blocks any containerized deployment).

### 3. Extract a testable option-builder and wire it (`s3fs.go`)

`Register()` currently builds the client inline, which can't be unit-tested without a
live AWS call. Extract a pure helper that returns the load options + the `s3.Options`
mutator, then assert on it in tests:

```go
// buildClientOptions returns the LoadDefaultConfig options and the s3.Options
// mutators implied by cfg. Pure — no network — so it is unit-testable.
func buildClientOptions(cfg *filestorage.Config) ([]func(*awsconfig.LoadOptions) error, []func(*s3.Options)) {
	loadOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.S3Region)}
	if cfg.S3AccessKey != "" && cfg.S3SecretKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		))
	}

	var s3Opts []func(*s3.Options)
	if cfg.S3Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
			o.UsePathStyle = cfg.S3UsePathStyle
		})
	}
	return loadOpts, s3Opts
}
```

In the `Register()` factory:

```go
loadOpts, s3Opts := buildClientOptions(cfg)
awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
if err != nil {
	return nil, fmt.Errorf("s3fs: load aws config: %w", err)
}
client := s3.NewFromConfig(awsCfg, s3Opts...)
```

New import: `github.com/aws/aws-sdk-go-v2/credentials`. Update the package doc comment at
`s3fs.go:1-3` (it currently claims credentials come only from the standard chain).

### 4. Add the screenshots group (`filestorage.go`)

```go
const (
	GroupTypeReports     GroupType = "reports"
	GroupTypeScreenshots GroupType = "screenshots"
)
```

## Files to edit

| File | Change |
|---|---|
| `server/internal/config/config.go` | 4 new `FileStorageConfig` fields; copy at L411; `applyFileStorageEnv` helper + call |
| `server/internal/handlers/filestorage/filestorage.go` | 4 new `Config` fields; `GroupTypeScreenshots` |
| `server/internal/handlers/filestorage/s3fs/s3fs.go` | `buildClientOptions`; wire `BaseEndpoint`/`UsePathStyle`/static creds; update doc comment; new import |

## Tests

### `server/internal/handlers/filestorage/s3fs/s3fs_test.go` (extend)

Table-driven, `t.Parallel()`, `testify/require`. Test `buildClientOptions` (export it
for test via the existing `s3fs_test` external package, or add an internal `export_test.go`
seam):

```go
func TestBuildClientOptions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// AWS default: no endpoint, no static creds → only region option.
	_, s3Opts := s3fs.BuildClientOptions(&filestorage.Config{S3Region: "us-east-1"})
	r.Empty(s3Opts)

	// MinIO: endpoint set → BaseEndpoint + UsePathStyle applied.
	_, s3Opts = s3fs.BuildClientOptions(&filestorage.Config{
		S3Endpoint: "https://minio.local:9000", S3UsePathStyle: true,
	})
	r.Len(s3Opts, 1)
	var o s3.Options
	s3Opts[0](&o)
	r.Equal("https://minio.local:9000", *o.BaseEndpoint)
	r.True(o.UsePathStyle)
}
```

### `server/internal/config/config_test.go` (extend)

`TestApplyFileStorageEnv`: set `SP_FILESTORAGE_S3_ENDPOINT` + `_USE_PATH_STYLE=true` +
`_ACCESS_KEY` + `_SECRET_KEY` via `t.Setenv`, load config, assert the four fields land on
`cfg.FileStorage`. (Confirms the koanf quirk is bypassed.)

## Verification

1. `make build` — backend compiles.
2. `make lint` — zero violations.
3. `make test` — all backend tests pass incl. the two new ones.
4. Manual MinIO round-trip:
   ```bash
   docker run -d -p 9000:9000 -e MINIO_ROOT_USER=minio -e MINIO_ROOT_PASSWORD=minio123 \
     minio/minio server /data
   # create a bucket "solidping" with mc, then:
   SP_FILESTORAGE_TYPE=s3 \
   SP_FILESTORAGE_S3_BUCKET=solidping \
   SP_FILESTORAGE_S3_REGION=us-east-1 \
   SP_FILESTORAGE_S3_ENDPOINT=http://localhost:9000 \
   SP_FILESTORAGE_S3_USE_PATH_STYLE=true \
   SP_FILESTORAGE_S3_ACCESS_KEY=minio \
   SP_FILESTORAGE_S3_SECRET_KEY=minio123 \
   make dev
   ```
   Upload + read back a blob through the files service; confirm the object lands in the
   MinIO bucket.
5. Regression: with all `SP_FILESTORAGE_S3_*` unset and `SP_FILESTORAGE_TYPE=s3`,
   the backend still loads via the ambient AWS chain (no endpoint).

## Implementation plan

1. **Config** — add the four fields to `FileStorageConfig` + `filestorage.Config`; copy
   at L411; write `applyFileStorageEnv` (incl. bucket/region/prefix) and call it; write
   `TestApplyFileStorageEnv`. `make test && make lint`.
2. **Backend wiring** — extract `buildClientOptions`, wire endpoint/path-style/static
   creds in `Register()`, add `credentials` import, update doc comment, add
   `GroupTypeScreenshots`. Write `TestBuildClientOptions`. `make test && make lint`.
3. **Manual MinIO verification** per above.
4. **Archive** — move spec to
   `specs/done/2026/05/2026-05-25-08-s3-compatible-object-storage.md`.

## Priority

Prerequisite for P1.3 (screenshots) and the vortex-S3 results spec. Small, contained.
