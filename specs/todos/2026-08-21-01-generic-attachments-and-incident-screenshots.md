---
model: opus
effort: high
---

# Generic file attachments (`files.topic` + `details`) with an agent upload endpoint, and incident screenshots riding on them

## Problem

The screenshot idea ([specs/ideas/2026-01-05-screenshots.md](../ideas/2026-01-05-screenshots.md))
— capture what a failing page looked like and show it on the incident — is
still unimplemented, but its prerequisites have all quietly shipped:

- **Chrome for real (Phase 0)**: `chromedp/headless-shell` sidecar in
  [docker-compose.yml:38](../../docker-compose.yml) plus
  `SP_CHECKERS_BROWSER_CDP_URL` → `chromedp.NewRemoteAllocator` with a CDP
  pre-flight probe and per-run isolated incognito contexts
  ([checkbrowser/checker.go:189](../../server/internal/checkers/checkbrowser/checker.go)).
- **Storage**: the `files` service with local-FS and S3 backends, signed
  download URLs, and a `GroupTypeScreenshots` group declared with zero callers
  ([filestorage.go:28](../../server/internal/handlers/filestorage/filestorage.go)).
- **Eager-capture / lazy-persist plumbing**: `Result.Diagnostics` travels with
  a result in memory only (`bun:"-" json:"-"`,
  [models/result.go:159](../../server/internal/db/models/result.go)) and the
  incident pipeline copies it into `incidents.details` on open/reopen
  ([failure_details.go:74](../../server/internal/handlers/incidents/failure_details.go))
  — built for `failureResponse` (spec 2026-08-20-01), directly reusable.

Three things are missing, and the first two are more general than screenshots:

1. **A `files` row cannot say what it is attached to.** There is no queryable
   link from a file to its owning entity, so there is no "list attachments of
   this incident", and no GC story — the idea spec's Phase 3 warning (blobs
   with no reaper become a billing surprise) applies to any future attachment
   kind, not just screenshots.
2. **A deported agent has no way to upload binary bytes.** The WS result frame
   is JSON ([agents/protocol.go:92](../../server/internal/agents/protocol.go)
   already carries `Diagnostics` inline); a PNG would be base64 through the
   control channel, which the idea spec's §6 explicitly rejects. Agents hold no
   S3 credentials by design.
3. The actual feature: no capture in `checkbrowser`, no persistence on the
   incident, no rendering on the incident page.

**Design decision (from review):** do not build a screenshot-specific upload
path. Build a *generic attachments* rail — `files.topic` + `files.details`
columns and one agent-facing `POST /api/v1/agent/attachments` — and make
incident screenshots its first consumer. Future captures (HTTP-check
screenshots, HAR files, packet captures, "after" shots) ride the same rail with
a new topic string instead of a new endpoint.

## Proposal

### 1. Schema — `files.topic` and `files.details`

Add to `files` (created in `001_v0_1_0.up.sql:680`), both dialects:

- `topic` TEXT NULL — a path-like attachment key, `<entity>/<uid>/<kind>`,
  e.g. `incidents/9a1eb273-0a95-4d6b-b967-9af076c1f8e8/screenshot`. NULL for
  files that are not attachments (org logos keep working untouched).
- `details` JSONB NULL (TEXT in SQLite, like the other jsonb columns) — free
  metadata bag for future needs; for screenshots:
  `{"capturedAt": "...", "region": "...", "checkUid": "...", "trigger": "incident-open"}`.
- Partial index `files_org_topic_idx ON files (organization_uid, topic)`
  `WHERE deleted_at IS NULL AND topic IS NOT NULL`.

Migration mechanics: v0.17.0 is unreleased, so the statements belong in the
existing consolidated `014_v0_17_0.up.sql` / `.down.sql` (both dialects, both
directions). Because 014 is already applied on dev databases, appending trips
the migration guard (dev runs `SP_DB_MIGRATION_GUARD_MODE=warn`) and the new
statements will NOT run there — apply them by hand or reset the dev DB, then
`solidping migrate repair`. Say so in the PR description.

### 2. Files service — attachment support

[`files.Service`](../../server/internal/handlers/files/service.go):

- Extend `CreateFile` (line 201) to accept topic + details — prefer a
  `CreateFileOptions`/functional-options shape over two more positional args,
  since existing callers (feedback, org logos) pass neither.
- `ListAttachments(ctx, orgUID, topic)` — exact-topic list, newest first.
- `DeleteAttachments(ctx, orgUID, topicPrefix)` — soft-delete by prefix, for
  entity-deletion reaping and for replace-on-reopen.
- Model + JSON: `topic` / `details` on `models.File`, camelCase in responses.

### 3. Browser-check capture (in-process path)

- `BrowserConfig` ([checkbrowser/config.go](../../server/internal/checkers/checkbrowser/config.go)):
  add opt-in `screenshot` bool (default false). Validation: nothing else needed
  now; a delay knob is future scope.
- On a **failing** execution with the flag set, capture
  `chromedp.FullScreenshot` into a buffer *before* the browser context is
  disposed, and hang it on the result as a new
  `Diagnostics.Screenshot` field (`[]byte` PNG + `CapturedAt`), next to
  `FailureResponse` in
  [checkerdef/diagnostics.go](../../server/internal/checkers/checkerdef/diagnostics.go).
- The capture must never change the check's outcome: time-box it (a second or
  two), and on any capture error log and return the result without it.
- Cap the PNG (reuse/mirror `files.MaxFileSize`, but a tighter ~4 MiB local cap
  is fine); an over-cap capture is dropped with a log line, not truncated.
- Note: `Diagnostics` is serialized onto the agent WS result frame
  ([protocol.go:92](../../server/internal/agents/protocol.go)). The new
  `Screenshot` bytes field must be **excluded from that frame** (`json:"-"`) so
  an agent-side capture can never smuggle megabytes through the control channel
  — the agent path uses §6 instead.

### 4. Incident persistence — on transitions only

In the incident pipeline (`ProcessCheckResult`,
[incidents/service.go:220](../../server/internal/handlers/incidents/service.go)):

- On `createIncident` (line 731) and on reopen, if the triggering result
  carries `Diagnostics.Screenshot`: write it via the files service into
  `GroupTypeScreenshots` with topic `incidents/<incidentUid>/screenshot` and
  the details bag from §1. On reopen, soft-delete the previous screenshot
  attachment first (the new onset is the evidence — mirrors the
  `failureResponse` overwrite rule).
- Every other failing run's capture is dropped on the floor — it only ever
  existed in memory. This keeps the storage math at "a handful of blobs per
  incident", not 2,880/day for a flapping 30 s check.
- No link needs to be stored in `incidents.details`: the topic *is* the link.

### 5. Incident API + dashboard

- Incident detail response: an `attachments` array (`{ "data": [...] }`-style
  nesting not needed inside an object; camelCase fields) with kind, mime,
  size, capture details, and a **signed download URL** via the existing
  [signedurl](../../server/internal/handlers/files/signedurl/signedurl.go)
  machinery. Update `server/internal/app/openapi/openapi.yaml` and run
  `make generate`.
- dash0 incident page: render the screenshot next to the "What the probe saw"
  block, caption carrying capture timestamp + region, honestly labelled as
  "shortly after failure detection" — never as the failure frame. Follow the
  design reference; usable on mobile.
- **Never public.** Attachments are org-operational evidence like
  `failureResponse`: extend the never-public audit
  ([details_never_public_test.go](../../server/internal/handlers/statuspages/details_never_public_test.go))
  so no status-page or subscriber payload can ever carry an attachment or its
  signed URL.

### 6. Agent upload — `POST /api/v1/agent/attachments`

Sibling of the WS route (`api.GET("/agent/ws", ...)`,
[server.go:1099](../../server/internal/app/server.go)):

- **Auth**: same Ed25519 request signature as the WS upgrade —
  `agentcrypto.VerifySignature` over (method, path, timestamp, nonce)
  ([crypto.go:90](../../server/internal/agents/crypto.go),
  [agentws/handler.go:393](../../server/internal/handlers/agentws/handler.go)),
  ±5 min skew, replay-guarded. No bearer tokens, no S3 credentials.
- **Request**: raw body (`image/png` first; allowlist, sniff the magic bytes,
  reject mismatches), `topic` + declared kind via query/header. Per-file size
  cap and a per-agent rate limit.
- **Authorization is the hard part**: never trust the topic. The org comes
  from the agent's region binding, never from the request; for an
  `incidents/<uid>/...` topic the server verifies the incident exists, belongs
  to that org, and its check is one this agent's region serves. A generic
  topic-authorizer registry (topic prefix → validator) keeps this extensible.
- **Response**: `{ "fileUid": "..." }` — the agent references it from its
  result/diagnostics instead of carrying bytes.
- **Flow for agent-side screenshots** (browser checks on private agents):
  eager capture happens agent-side, but at capture time no incident exists yet
  and most failures never open one. So the agent advertises
  `screenshotAvailable` (a small marker + capture id) in the result frame's
  diagnostics; when the incident pipeline opens/reopens an incident from that
  result, the server sends an upload-request frame (new WS frame type) naming
  the capture id and the topic; the agent then POSTs it here. The agent keeps a
  small LRU of recent captures (a few entries, few MiB). If the agent-side
  flow proves too large for this spec, the endpoint + authorizer land now
  (with tests exercising them directly) and the WS request-frame wiring may be
  split into a follow-up spec — but the endpoint contract must be settled
  here.

### 7. Retention & GC — not optional

- Incident deletion (and check deletion cascading incidents) reaps its
  attachments via `DeleteAttachments(org, "incidents/<uid>/")`.
- Extend the existing files GC job (the one that already catches
  bytes-without-row orphans, per the note on `CreateFile`) to also sweep:
  attachment rows whose `incidents/<uid>/...` topic points at a missing
  incident, and agent-uploaded blobs never referenced within a few hours.

### Non-goals

- Screenshots for HTTP checks (a Chrome *re-visit* of the URL after failure —
  a different mechanism; it will ride this same rail later).
- "After" screenshots on resolve; periodic/visual-regression captures.
- Org-level storage quota in `org_entitlements` — with capture-on-transition
  and per-file caps the write volume is bounded; the quota becomes necessary
  when HTTP-check screenshots or user-driven attachments arrive.
- Fixing `requires:chrome` capability dispatch
  ([checkerdef/types.go:242](../../server/internal/checkers/checkerdef/types.go)
  is still read by nothing) — related, separate spec.

## Tests

- Migration: both dialects apply/rollback; existing `files` callers untouched.
- Files service: topic/details round-trip, `ListAttachments`,
  `DeleteAttachments` prefix semantics, camelCase JSON.
- Checker: with the flag set, a failing run carries `Diagnostics.Screenshot`
  (use the existing `c.session` test hook — no real Chrome in CI); a passing
  run doesn't; capture failure doesn't alter status; the WS frame never
  serializes the bytes (positive control: `FailureResponse` still does).
- Incident pipeline: screenshot persisted on open and reopen (old one
  soft-deleted), dropped on non-transition failures; proves the negative with
  a flapping sequence.
- Agent endpoint: valid signature + valid topic → 201; bad signature, replay,
  foreign incident topic, oversize body, mime mismatch → rejected (each case).
- Never-public audit extended to attachments.
- E2E (dash0): incident with a seeded screenshot attachment renders the image
  with timestamp + region caption.

## Implementation Plan

Ordered so every step lands on a green tree. Steps 1–2 are the generic rail;
3–5 are the screenshot consumer; 6–7 are the agent + GC halves; 8 is the UI.

### Step 1 — Schema + model + db layer (`files.topic`, `files.details`)

- New `SECTION: generic-attachments` appended to `014_v0_17_0.up.sql` in BOTH
  dialects, with the mirrored teardown prepended (reverse order) to both
  `.down.sql` files. `topic` TEXT NULL, `details` jsonb/TEXT NULL, partial
  index `files_org_topic_idx (organization_uid, topic)
  WHERE deleted_at IS NULL AND topic IS NOT NULL`.
- `models.File`: `Topic *string`, `Details JSONMap` (`type:jsonb,nullzero`),
  `ListFilesFilter.Topic` / `.TopicPrefix`.
- `db.Service`: `ListFilesByTopic(ctx, orgUID, topic)`,
  `DeleteFilesByTopicPrefix(ctx, orgUID, prefix) (int, error)`, and
  `ListOrphanIncidentAttachments(ctx, olderThan, limit)` for the GC sweep —
  implemented in both `postgres.go` and `sqlite.go`.

### Step 2 — `files.Service` attachment API

- `CreateFile` grows a variadic `...CreateFileOption` tail (`WithTopic`,
  `WithDetails`) so the two existing callers (feedback, org logos) are
  untouched.
- `ListAttachments(ctx, orgUID, topic)` (newest first),
  `DeleteAttachments(ctx, orgUID, topicPrefix) (int, error)`.
- `FileResponse` gains `topic` / `details` (camelCase, `omitempty`).

### Step 3 — `Diagnostics.Screenshot` + browser capture

- `checkerdef.Screenshot{PNG []byte `json:"-"`, CapturedAt, Width, Height}`
  hung on `Diagnostics.Screenshot`. The BYTES field is `json:"-"` so the agent
  WS result frame can never carry them; `FailureResponse` stays serialized as
  the positive control.
- `BrowserConfig.Screenshot bool` (`screenshot`, default false), round-tripped
  through `FromMap`/`GetConfig`.
- `checkbrowser`: on a FAILING outcome with the flag set, `chromedp.FullScreenshot`
  into a buffer before the browser context is disposed, time-boxed
  (`screenshotTimeout`), capped at `MaxScreenshotBytes` (4 MiB) — over-cap or
  errored capture is logged and dropped, never changes the status.

### Step 4 — Incident persistence on transitions only

- `incidents.AttachmentStore` (small interface, mirrors `PublicationHook`) with
  `CreateAttachment` / `DeleteAttachments`; wired in `server.go` from the files
  service via an adapter. Nil-safe.
- `createIncident`: after the row is created, persist the screenshot under
  topic `incidents/<uid>/screenshot`.
- `reopenIncident`: soft-delete `incidents/<uid>/` first, then persist the new
  onset's capture (mirrors the `failureResponse` overwrite rule). No capture on
  the relapse ⇒ the stale one is still dropped.
- Every non-transition failing run drops its capture on the floor.

### Step 5 — Incident API + never-public audit

- `IncidentResponse.Attachments []IncidentAttachment` (uid, kind, name,
  mimeType, size, capturedAt, region, downloadUrl — signed via `signedurl`).
  Populated by `GetIncident` only (not the list endpoint).
- `openapi.yaml` + `make generate`.
- `details_never_public_test.go`: forbid `attachments`, `downloadurl`,
  `screenshot` as public field names, and add a value-level control.

### Step 6 — `POST /api/v1/agent/attachments`

- Sibling of `api.GET("/agent/ws", …)`, same Ed25519 signed-header auth
  (`agentcrypto.VerifySignature`, ±5 min skew, DB-backed nonce replay guard).
- Raw body, `image/png` only (magic-byte sniff), `topic` + `kind` from query,
  per-file cap, per-agent rate limit.
- `attachtopics` — a topic-prefix → authorizer registry. The
  `incidents/<uid>/` authorizer resolves the incident, requires it to belong to
  the agent's org, and requires the incident's check to be served by a region
  this agent serves. Org NEVER comes from the request.
- Response `{"fileUid": "..."}`.
- **Escape hatch (spec §6):** the WS upload-request frame + agent-side LRU are
  DEFERRED to a follow-up spec; the endpoint + authorizer land here with direct
  tests, and a follow-up spec file is created in `specs/todos/`.

### Step 7 — GC

- `DeleteAttachments` is the reaper primitive.
- The periodic state-cleanup job gains an attachment sweep: attachment rows
  whose `incidents/<uid>/…` topic points at an incident that no longer exists
  (and which are older than a grace window) are soft-deleted.

### Step 8 — dash0 + E2E

- `IncidentAttachment` type in `api/hooks.ts`; `IncidentScreenshotCard`
  rendered next to `ProbeResponseCard`, caption = captured-at + region,
  labelled "shortly after failure detection". Responsive, i18n in all four
  locales.
- Playwright spec in `web/dash0/e2e/` asserting the image + caption render.
