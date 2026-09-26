---
model: opus
effort: high
---

# A browser check's screenshots can only be found by opening its incidents one by one

## Problem

A browser check with `screenshot: true` (and a JS check calling `page.screenshot()`)
captures a WebP of the page when a run ends `down` / `timeout`. That capture reaches
exactly one place: `persistScreenshot` (`server/internal/handlers/incidents/service.go:456`)
stores it as an attachment on the incident the run opened or reopened, under the topic
`incidents/<uid>/screenshot` (`server/internal/handlers/attachments/topics.go:57`). The
only UI that shows it is `IncidentScreenshotCard` on the incident detail page
(`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1465`).

The check page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`) shows no
screenshot, not even a link to one. Seen on 2026-09-25: an operator added a browser
check on a public site to see what the site looks like from the probe, and there was no
way to get that answer from the check. Getting the image took a throwaway clone that
failed on purpose, with its notification channels detached, so it would open an incident
that carried the capture.

Three gaps, in the order they bite:

1. **Not discoverable.** A capture exists but lives one or more clicks away, on an
   incident the operator has to find first. Once a check has had a few incidents, the
   latest capture is buried.
2. **Captures that open no incident are thrown away.** A `down` run inside the
   confirmation period (`status: validating`) produces a capture that no code path
   persists. That covers the first failures of any check with `confirmationPeriodSeconds > 0`,
   and every blip that recovers before the period elapses. That is exactly the "what did
   the page look like during that blip?" question.
3. **A healthy check never produces one.** Capture is gated on a failing verdict
   (`capturableStatus`, `server/internal/checkers/checkbrowser/checker.go:331`), so the
   check page of a site that is up can never show what the probe sees.

## Proposal

### 1. Show the check's latest screenshots on the check page (required)

- New endpoint `GET /api/v1/orgs/{org}/checks/{checkUid}/screenshots?limit=N` returning
  `{ "data": [ { uid, mimeType, size, downloadUrl, capturedAt, region, trigger, incidentUid } ] }`,
  newest first, default `limit` 5. The source is the existing incident attachments
  whose `details.checkUid` matches. `files.details` already carries `checkUid`,
  `capturedAt`, `region` and `trigger`. The incident UID comes from the topic. Add an index
  or query path that does not scan every file in the org, and check the plan on Postgres
  and SQLite (`/sync-pg-to-sqlite`).
- `downloadUrl` is the same short-lived signed `/pub/files/...` URL the incident detail
  returns. Same authorization as reading the check. Operator-only, exactly like incident
  attachments: never exposed on a status page, badge or subscriber payload.
- dash0: a "Screenshots" card on the check overview, visible only for check types
  that can capture (`browser`, `js`). It shows the latest capture as a clickable thumbnail
  that opens it full size, plus `capturedAt` (relative time with absolute time on hover),
  region, and a link to the incident. Older captures sit in a compact strip underneath.
  Empty state: "No screenshot yet. Captures are taken when a run fails," plus a
  link to the edit form's screenshot toggle if it is off. Reuse the primitives from
  `web/dash0/src/routes/orgs/$org/design-reference.tsx`, and share the image and lightbox
  rendering with `IncidentScreenshotCard` rather than forking it. Must work at phone width.
- Locale keys in every shipped locale (`bun run test:unit` catches a missing one).

### 2. Keep captures from runs that open no incident (decide, then implement)

Persist the capture of a failing run that did not open or reopen an incident, under a
check-scoped topic (`checks/<uid>/screenshot`), and include it in the same listing.
Constraints to settle in the implementation plan:

- **Retention.** Keep the latest capture only (replace on write) or the last N per check.
  Captures are up to 4 MiB (`MaxScreenshotBytes`), and a flapping check on a 1-minute
  period must not fill storage. Proposal: latest one only, overwritten.
- **Agent path.** A private agent does not ship the bytes with the result; it is asked to
  upload them (`requestAgentScreenshot`, `incidents/service.go:514`). That handshake is
  wired to incident topics only. A new `checks` topic entity needs its own authorizer in
  the attachment rail (`topics.go` fails closed on an unknown entity).
- **Reaping.** Deleting a check must reap `checks/<uid>/` like deleting an incident reaps
  `incidents/<uid>/`.

### 3. A screenshot of a healthy page (open question, do not implement without a decision)

Two shapes, not mutually exclusive:

- **"Capture now" button** on the Screenshots card: runs the check once on demand, from
  one of its regions, with capture forced regardless of verdict, and stores the result
  under the check topic. Needs a run-now path (none exists today) and rate limiting, since
  a browser run is the most expensive check type.
- **`screenshot: "always"`** as a third config value next to `false` / on-failure: capture
  every run, keep the latest only. It costs a CDP round-trip and a few MiB of memory on every
  run (the reason `screenshot` defaults to off, see `checkbrowser/config/config.go:31`).

Pick one before starting part 3. The on-demand button is the cheaper default.

## Out of scope

- Changing when an incident attaches its screenshot.
- The capture budget bug that drops screenshots of `timeout` runs
  (`2026-09-25-35-browser-screenshot-lost-on-timeout.md`). It is independent of this
  spec, but without that fix, part 1 shows nothing for checks that fail by timing out.

## Tests

- Backend: listing returns captures from several incidents newest first, respects `limit`,
  excludes other checks' captures, returns `{ "data": [] }` for a check with none, and
  refuses a caller who cannot read the check. Postgres and SQLite.
- Part 2: a `down` run inside the confirmation period leaves exactly one check-scoped
  capture, and a second one replaces it. Deleting the check reaps it.
- dash0 Playwright: the card renders the latest capture with its region and incident link
  on a seeded check, the empty state on a check without captures, and nothing for an
  `http` check.

## Resolved open questions

- **Part 3, "A screenshot of a healthy page (open question, do not implement without a
  decision)": which shape?** Decision: implement the **"Capture now" button** only. Do not
  add `screenshot: "always"`. The button runs the check once on demand from one of its
  regions with capture forced regardless of verdict, stores the capture under the
  check-scoped topic, and is rate limited, because a browser run is the most expensive
  check type. Building the run-now path it needs is part of this spec.
- **Part 2, retention of captures from runs that open no incident.** Decision: keep the
  **last 5 captures per check**, not the latest only. When a sixth is written, delete the
  oldest. The storage bound is 5 × `MaxScreenshotBytes` per check. Captures from the
  "Capture now" button share this same check-scoped topic and the same cap of 5. Deleting
  the check reaps all of them.
  This overrides the Tests bullet "a second one replaces it": test instead that six
  check-scoped captures leave exactly five, and that the oldest is the one removed.

## Implementation Plan

### §1 Storage: the check-scoped topic
- `attachments/topics.go`: `EntityChecks = "checks"`, `CheckTopicPrefix(uid)` (`checks/<uid>/`,
  trailing slash load-bearing) and `CheckScreenshotTopic(uid)`. `MaxCheckScreenshots = 5`.
- New trigger values: `check-failure` (a failing run that opened/reopened no incident) and
  `capture-now` (the on-demand run). Agent uploads keep `agent-upload`.
- `attachments.Service.Put` stays replace-on-write for every topic EXCEPT `checks/<uid>/screenshot`,
  which is append-then-prune: write the new row, list the topic newest first, retire everything
  past the 5th. Pruned rows are soft-deleted AND their blob is removed from storage (new
  `FileStorage.DeleteFile` on localfs/s3fs, best-effort), otherwise "delete the oldest" would bound
  the rows but not the bill: soft-deleted blobs are never reclaimed today. A concurrent prune that
  finds the row already gone is not an error. `PutCheckScreenshot` is the in-process entry point;
  the agent upload goes through the same `Put`, so both paths share the cap.
- Agent upload authorizer for `checks`: `NewCheckAuthorizer` mirrors the incident one (check must
  exist, found through a new org-less `db.GetCheckAny`; an org agent only writes to its own org;
  the agent's region must serve the check). Registered next to `incidents`, so the rail still fails
  closed on anything else.
- The upload handler stamps `checkUid` into the details bag from the server's own data (the topic
  for `checks`, the incident row for `incidents`), never from the request. That also fixes agent-
  uploaded incident screenshots, which carried no `checkUid` and would be invisible to §2.
  The per-agent upload budget (20/min) is keyed per (agent, topic entity), so check-scoped uploads
  can never starve an incident's onset upload.

### §2 Listing
- `GET /api/v1/orgs/{org}/checks/{checkUid}/screenshots?limit=N` (read chain, viewers allowed,
  `checkUid` accepts a uid or slug like every check route). `limit` default 5, max 20.
  Response `{ "data": [ { uid, mimeType, size, downloadUrl, capturedAt, region, trigger,
  incidentUid } ] }`, newest first. `capturedAt` falls back to the row's `createdAt` (agent uploads
  carry no capture time). `incidentUid` comes from the topic, null for check-scoped captures.
  `downloadUrl` is the same 1 h signed relative `/pub/files/...` URL.
- New package `internal/handlers/checkscreenshots` (handler + service) so neither the checks nor
  the incidents service grows a files dependency. Never serialized on any public surface.
- Query path, one per engine, through `db.ListCheckScreenshotFiles(org, check, limit)`:
  `organization_uid = ? AND deleted_at IS NULL AND topic IS NOT NULL AND <checkUid expr> = ?
  AND (topic LIKE 'incidents/%/screenshot' OR topic = 'checks/<uid>/screenshot')
  ORDER BY created_at DESC LIMIT ?`, with `<checkUid expr>` = `details->>'checkUid'` (Postgres) /
  `json_extract(details, '$.checkUid')` (SQLite).
- Migration: a `-- SECTION: check-screenshots` appended to the unreleased `024_v0_33_0` on both
  engines: partial expression index `files_org_check_uid_idx (organization_uid, <checkUid expr>,
  created_at DESC) WHERE deleted_at IS NULL AND topic IS NOT NULL`, a backfill of `checkUid` onto
  live incident screenshots that lack it (from `incidents.check_uid`), and
  `check_jobs.capture_requested_at` (§4). Down migrations mirror it. Plan tests on both engines
  (EXPLAIN / EXPLAIN QUERY PLAN) assert the index is used, with a positive control.

### §3 Keeping captures of runs that open no incident
- `checkerdef.Screenshot` gains `OnDemand bool` (wire, `onDemand`) and `Attached bool` (`json:"-"`,
  server-side only). `persistScreenshot` sets `Attached` when it stored the capture on (or asked the
  agent to upload it to) an incident.
- `incidents.Service.ProcessCheckResult` runs the existing pipeline, then
  `persistCheckScreenshot`: when the result carries a capture that no incident took, it is written
  under `checks/<uid>/screenshot` (in-process bytes via `AttachmentStore.PutCheckScreenshot`; agent
  marker via `RequestScreenshotUpload` with the check topic). Trigger is `capture-now` when
  `OnDemand`, otherwise `check-failure`. This covers every failing run that neither opened nor
  reopened an incident: validating runs, blips that recover inside the confirmation period,
  regional (non-quorum) failures, failures during an already-open incident, and runs inside a
  maintenance window. Retention (§1) is the bound. A non-failing result is stored only when
  `OnDemand`.

### §4 "Capture now": the run-now path
- `POST /api/v1/orgs/{org}/checks/{checkUid}/screenshots/capture` (org write chain: viewers get
  403). Browser and js checks only (400 otherwise). 409 when the check has no scheduled job
  (disabled). 202 `{ region, requestedAt }` on success.
- It runs through the check's own scheduling, so it executes where the check executes: the shared
  cloud workers for a cloud region, the org's private agent for an `@private` region. The service
  picks one of the check's job rows (an unleased one first, ordered by region), sets
  `check_jobs.capture_requested_at = now` and pulls `scheduled_at`/`effective_scheduled_at` to now,
  then publishes the existing express hint (`check.created` channel, `{check_uid}` payload), which
  in-process workers and the agent WS relay already turn into `ClaimJobsForCheck`. Not the jobs
  system (`jobsvc`): a check run must be claimed by a worker in the check's region, which only
  `check_jobs` routes.
- Claim clears the flag in the same transaction as the lease (`updateSingleJobLease`) while the
  claimed struct keeps it; release (`ReleaseLease`, `ReleaseLeaseWithSchedulingState`) keeps a
  request that arrived mid-lease due now instead of pushing it to the next tick. The next tick is
  phase-locked (`NextAligned` from now), so the forced run does not shift the schedule.
- `AgentJob` carries `captureRequestedAt` both ways. The worker wraps the checker context with
  `checkerdef.WithForcedCapture` and stamps `OnDemand` on the capture. Browser checker: forced
  means capture regardless of verdict and of the `screenshot` toggle (the session budget grows by
  the screenshot allowance). JS checker: forced keeps the script's last `page.screenshot()`
  regardless of verdict (a script that never shoots yields nothing). The run is a real run: its
  result is saved and goes through the incident pipeline like any other.
- Rate limit, DB-backed in the service (fixed windows in `state_entries`, org-scoped keys), so it
  holds across API replicas and keys on the check rather than on an IP (an in-memory
  `RateLimitRoute` would multiply by the replica count): **1 per check per 60 s** and
  **20 per org per hour**. 429 `RATE_LIMITED` with `Retry-After`. Validation (type, jobs) runs
  before the limiter, so a refused request spends no budget.

### §5 Reaping
- `checks.Service.DeleteCheck` (every delete path: API, MCP, chat commands, apply prune, demo
  cleanup) soft-deletes `checks/<uid>/` by prefix.
- The state-cleanup orphan sweep also walks `checks/` and reaps attachments whose check is gone
  (covers org deletion and direct DB deletes), same grace and batch as incidents.

### §6 dash0
- `ScreenshotImageLink` (new shared primitive, `components/shared/screenshot-image.tsx`, added to
  the design reference): the image as a link to its full-size self, the existing no-lightbox
  pattern. `IncidentScreenshotCard` switches to it.
- `CheckScreenshotsCard` on the check overview for `browser` and `js` only: latest capture as a
  thumbnail, `TimeAgo` (absolute on hover), region, trigger label, incident link; older captures
  as a compact thumbnail strip; empty state "No screenshot yet. Captures are taken when a run
  fails." plus a link to the edit form's screenshot toggle when a browser check has it off. The
  "Capture now" button POSTs, then polls the listing every 5 s for up to 3 min until a capture
  newer than `requestedAt` lands; 429/403/409 surface as toasts with the server message.
  Mobile-first layout. Strings in en/fr/de/es.

### §7 Docs, API reference
- `openapi.yaml` and `wiki/api-specification/checks.md` for both endpoints; browser section of
  `web/docs/docs/features/check-types.md` and the JS screenshots note describe the card and
  "Capture now". No MCP tool.

### Tests
- attachments: six check-scoped captures leave exactly five and the oldest (row and blob) is the
  one removed; the check authorizer's refusals; upload stamps `checkUid`; per-entity budgets.
- DB (SQLite + Postgres): listing newest first, `limit`, other checks excluded, empty; plan tests.
- checkscreenshots handler: list shapes, 404 for another org's check, capture-now 202 / 400 / 409
  / 429 (per check, per org), job flagged and pulled forward.
- incidents: a `down` run inside the confirmation period leaves exactly one check-scoped capture;
  the run that opens the incident does not also write one; an on-demand `up` capture is stored.
- checkjobsvc: claim clears the flag and returns it; release keeps a mid-lease request due now.
- checkers: forced capture on an `up` browser run and a JS run; unforced `up` still captures nothing.
- checks: deleting the check reaps `checks/<uid>/`; orphan sweep reaps a deleted check's captures.
- dash0 Playwright: card with latest capture, region and incident link on the seeded check;
  empty state on a fresh browser check; nothing for an http check; Capture now happy path and the
  rate-limit message (API-level: the side-car has no browser engine, so the capture itself is not
  produced there).
