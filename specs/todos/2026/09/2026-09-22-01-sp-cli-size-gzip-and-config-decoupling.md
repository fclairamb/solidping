# Shrink the `sp` CLI: gzip release assets now, decouple checker configs from implementations next

## Context

The `sp` CLI release assets went from ~30 MB (v0.31.0, shipped as
`sp_<version>_<os>_<arch>.tar.gz`) to ~99 MB (v0.31.1, shipped as bare
binaries with version-free names — #417). The naming change was deliberate
(stable `releases/latest/download/<name>` URLs); the size jump is almost
entirely the lost compression. The binary itself grew only ~6% between the
two releases (dependency bumps).

But the deeper finding is that a config-validation CLI should never have been
~93 MB to begin with. Measured marginal cost of packages linked into `sp`
(probe binaries built with the same `CGO_ENABLED=0 -ldflags "-s -w"` flags as
the release job, Go 1.27):

| component | marginal size |
|---|---|
| base CLI (`checkerdef`, `defaults`, modernc sqlite via `kumadb`) | ~20 MB |
| `internal/checkers/registry` (all ~40 checkers) on top of the base | +77 MB |
| — `checkkubernetes` (k8s client-go) | +18 MB |
| — `checkoracle` (go-ora) | +14 MB |
| — `checkjs` (goja) | +8 MB |
| — `checkgrpc` | +7 MB |
| — clickhouse, mongo, prometheus, docker, kafka, … | ~+10 MB |

The reason the whole zoo gets linked: `pkg/cli` (via `checks_more.go`)
imports `internal/handlers/checks` for `ParseManifest` and `ValidateDocument`,
and `validate_document.go` reaches into `internal/checkers/registry`, whose
`GetChecker`/`ParseConfig` reference the concrete `*Checker` and `*Config`
types of every checker package — and each checker package drags its execution
clients (client-go, go-ora, sarama, goja, grpc, modernc sqlite, …) into the
link, even though only the config half is needed offline.

Each checker already follows a clean internal split:

- `config.go` — the `XConfig` struct (json-tagged), `FromMap()`, `Validate()`,
  key constants: **light** (checkerdef, time, fmt)
- `checker.go` — the `XChecker`: `Validate(spec)` delegating to the config
  (plus `spec.Name`/`Slug` defaulting), `Execute` with the heavy client:
  **heavy**

The split exists inside each package but not across packages, and Go links
whole packages.

Two follow-up ideas were considered and rejected for the validation path:

- **Validating from the OpenAPI schema**: `config` is a free-form object in
  `openapi.yaml` ("fields accepted depend on type") — no per-type schema
  exists, so one would have to be authored. A hand-written schema is a second
  source of truth that drifts from the Go validators (the
  `strippedSecretComplaint` contract — "the server never demands a key it
  itself strips", carried by typed `ConfigError{Parameter}` errors — and the
  cross-field rules like sftp's password-or-private-key cannot be faithfully
  expressed), and `validate_document.go` documents a deliberate guarantee:
  "reusing the exact code path the live create/update/live-validate handlers
  use". The API-client half of `sp` is already OpenAPI-generated
  (`pkg/client` via oapi-codegen) and is not what bloats the binary.
- **Calling `POST /checks/validate` on a live server instead**: breaks the
  documented offline use case (validating a config-as-code manifest in CI
  before any instance exists) and adds credentials to a step that needs none.

## Part 1 — gzip twins of the release binaries (in flight)

PR #424 publishes a gzip twin next to every bare binary:
`sp_linux_amd64.gz`, … — same version-free name with `.gz` appended, hashed
into `sp-checksums.txt`, ~31 MB instead of ~99 MB. The bare files remain the
canonical, unpinned URLs; nothing is removed. Install docs updated. This is
the quick win that restores a reasonable download while the structural work
lands.

## Part 2 — decouple checker configs from checker implementations

### Design

1. **One light sub-package per checker**: `internal/checkers/check<type>/config/`
   (package `config`) receives the `XConfig` struct, `FromMap()`, `Validate()`,
   and the key constants. The heavy package keeps a type alias
   (`type KubernetesConfig = config.KubernetesConfig`), so the ~20 external
   call sites of `registry.GetChecker`/`ParseConfig` (checkworker,
   handlers/checks, jobtypes, credmigrate, …) and every test keep compiling
   untouched.

2. **Move the spec-defaulting block** (the `if spec.Name == "" { … }` /
   `if spec.Slug == "" { … }` logic inside each checker's `Validate`) into the
   config package, so it is reachable without linking the implementation.

3. **A light registry**: new `internal/checkers/configregistry` — the same
   switch shape as `registry`, but importing only the `config/` sub-packages;
   exposes `ParseConfig`, `IsKnownType`, and `ValidateSpec(type, spec)`. The
   existing (heavy) registry keeps `GetChecker` for the server and delegates
   its `ParseConfig` to the light one, so the two stay in lockstep by
   construction (one compiler-checked switch, not two hand-maintained ones).

4. **`validate_document.go` switches to the light registry** — `sp checks
   validate` (and the parsing side of `sp checks apply`) then links no
   execution client at all.

### Known wrinkles to resolve per checker

- `checkjs/config.go` imports **goja** for offline script syntax-checking
  (the `browserOpenRE` heuristic plus compilation); `checkbrowser` may be in
  the same situation. Two options, decided when reached: keep the dependency
  in the light layer (goja costs +8 MB — still leaves `sp` at ~30 MB), or
  inject the syntax check behind a hook, the way `checkkubernetes` already
  injects `ClientsetResolver`.
- `kumadb` legitimately needs modernc sqlite (it reads Uptime Kuma exports
  locally); that stays and accounts for most of the residual ~20 MB base.

### Expected outcome

`sp` drops from ~99 MB to ~25-30 MB (the residual being the CLI + generated
API client + sqlite-for-kumadb). The server binary is unchanged — the type
aliases keep everything it links today. Release downloads additionally get
the Part 1 `.gz` twins on top.

### Execution & verification

- Mechanical, checker-by-checker (or small groups), one green commit per
  group: `go build ./...` + `go test ./internal/checkers/... ./internal/handlers/checks/...`
  after each, full E2E before opening the PR.
- Progress meter: the size of a locally built, release-flagged `sp` after
  each group (99 → ~25-30 MB target); server binary size must not change.
- The two registries must never disagree: the heavy registry delegating
  `ParseConfig` to the light one is the enforcement mechanism; a registry
  test asserting every `GetChecker` type is `IsKnownType` in the light one
  (and vice versa) pins it.
- Follow-up (optional, cheap once the light layer exists): generate JSON
  Schemas from the same `XConfig` structs (e.g. invopop/jsonschema) and
  expose them in the OpenAPI spec or a `/checks/schema/{type}` route, for
  editor autocomplete and third-party tooling — as a *description*, never as
  the canonical validator.

## Decision

Do both, in this order: land Part 1 (PR #424) as the immediate download fix;
execute Part 2 as a dedicated batch on a `refactor/checker-config-split`
branch, keeping the Go validators as the single source of truth and the
CLI-on-server-code-path guarantee intact.
