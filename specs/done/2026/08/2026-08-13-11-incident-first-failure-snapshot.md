---
model: sonnet
effort: high
---

# Incidents don't record what caused them — snapshot the first failing result

## Problem

When an incident is opened we store *that* the check went down but nothing
about *why*. Investigating an incident after the fact means digging through
raw results — and often failing, because raw results are rolled up into
hour/day/month aggregates and **deleted** by the aggregation job, so for any
incident older than the raw-retention window the evidence is gone.

Concretely:

- `incidents.details` is a JSONB column
  ([incident.go:50](server/internal/db/models/incident.go:50)) that
  `NewIncident` initializes to an empty map
  ([incident.go:74](server/internal/db/models/incident.go:74)) and **nothing
  ever writes to**.
- `createIncident`
  ([service.go:690](server/internal/handlers/incidents/service.go:690)) has
  the failing `*models.Result` in hand and even records `resultUid` in the
  incident-created *event* — but that is a pointer to a row that retention
  will delete, not a snapshot, and the API never joins it back anyway.
- The Slack formatter already expects the missing data: `getFailureReason`
  ([notifications/slack.go:259-270](server/internal/notifications/slack.go:259))
  and its twin in
  [integrations/slack/interactions.go:541-544](server/internal/integrations/slack/interactions.go:541)
  read `incident.Details["failure_reason"]` / `Details["output"]` (both as
  strings). Since the keys are never populated, every Slack notification
  falls back to the literal "Check failed".
- The failing result carries good diagnostics: `status`, `duration`,
  `region`, and an `Output` JSONMap
  ([result.go:113](server/internal/db/models/result.go:113)) with a
  standardized error string under `checkerdef.OutputKeyError`
  ([checkerdef/types.go:197](server/internal/checkers/checkerdef/types.go:197))
  plus per-checker keys (HTTP status, DNS resolved values, cert errors…).
- `IncidentResponse`
  ([service.go:1606-1631](server/internal/handlers/incidents/service.go:1606))
  does not expose `details` at all, so the dashboard couldn't show a cause
  even if one were stored.

## Proposal

Snapshot the first failing result **into** `incident.Details` at incident
open (copy, not reference — retention makes references worthless), expose it
through the API, and render it in dash0. No migration needed; the column
exists.

### Backend — populate `Details`

Add a helper in the incidents service, e.g.
`failureDetails(check *models.Check, result *models.Result) models.JSONMap`,
producing:

- `failure_reason` (string) — `result.Output[checkerdef.OutputKeyError]` when
  present, else a status-derived fallback ("timeout", "error", "down"). This
  is the exact key the two existing Slack readers consume, so Slack
  notifications light up for free.
- `first_result` (object) — `{resultUid, status (string), region, duration,
  periodStart, output}` where `output` is the result's `Output` map,
  size-capped (marshal; if over ~8 KB drop the largest values or truncate
  long strings, keeping `error` intact).

Write it at every open/reopen path:

1. `createIncident`
   ([service.go:699](server/internal/handlers/incidents/service.go:699)) —
   set `incident.Details` before `CreateIncident`.
2. `createGroupIncident`
   ([service.go:1112](server/internal/handlers/incidents/service.go:1112)) —
   same snapshot; the triggering member's failure is the cause.
3. Reopen paths — `tryReopenIncident`
   ([service.go:818](server/internal/handlers/incidents/service.go:818)) and
   `reopenGroupIncident`: record the relapse's failing result under a
   `last_failure` key (same shape as `first_result`) via `IncidentUpdate.Details`,
   **preserving** the original `first_result` / `failure_reason`.

Secrets note: results are produced by checkers and never contain check-config
secrets (decrypt/merge happens at dispatch and the plaintext stays in the
worker), so this is a size concern, not a leak concern — but keep the size
cap.

### API

- Add `Details models.JSONMap \`json:"details,omitempty"\`` to
  `IncidentResponse` and populate it in the incident→response mapping (list +
  get + the incident payloads embedded in notifications/status endpoints that
  reuse it).
- Update `server/internal/app/openapi/openapi.yaml` accordingly.

### Dashboard (dash0)

On the incident detail page
([incidents.$incidentUid.tsx](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx)),
add a "First failure" card when `details.first_result` is present: the
`failure_reason` headline, then status / region / duration / time, and the
captured `output` as a compact key-value list (JSON fallback for nested
values). Show a "Latest relapse" variant when `last_failure` exists. Follow
the design-reference primitives; must work on mobile.

### Tests

- Service tests: open → `Details` populated (`failure_reason` from
  `Output["error"]`, fallback when absent); reopen → `last_failure` added and
  `first_result` untouched; group create path; size cap enforced.
- Slack formatter test proving `getFailureReason` now returns the real reason
  end-to-end from a created incident (this is the existing dead path coming
  alive — assert the negative today fails without the write).
- API test: `GET /incidents/:uid` returns `details`.
- dash0 E2E: incident detail shows the failure reason panel.

### Non-goals

- Backfilling existing incidents (their raw results are mostly gone already).
- Storing every failing result on the incident — `failureCount` plus
  first/last snapshots is enough; the full series stays in `results` while
  retention lasts.
- Changing the incident-created event payload (keep `resultUid` there).

## Implementation Plan

1. **`server/internal/handlers/incidents/failure_details.go`** (new file) —
   `failureDetails(result)`, `lastFailureDetails(existing, result)`,
   `failureReasonFromResult(result)`, `resultSnapshot(result)`,
   `cappedOutput(output)`. Keys: `failure_reason` / `first_result` /
   `last_failure` at the top level (matching what
   `notifications/slack.go:getFailureReason` and
   `integrations/slack/interactions.go:getFailureReason` already read); each
   snapshot is `{resultUid, status, region, duration, periodStart, output}`.
   Size cap: marshal, and if over `maxFailureSnapshotBytes` (8 KB), truncate
   long output strings first, then drop the largest remaining output keys
   (deterministic, largest-first) until it fits — `checkerdef.OutputKeyError`
   is never dropped/truncated away from the map (may itself be truncated if
   it alone is enormous, per "truncate long strings, keeping error intact").
2. **`service.go` — write paths**:
   - `createIncident` (~:699): `incident.Details = failureDetails(result)`
     before `s.applyRollup`/`CreateIncident`.
   - `createGroupIncident` (~:1111): same, right after `NewIncident`.
   - `reopenIncident` (called from `tryReopenIncident`, ~:867): add
     `newDetails := lastFailureDetails(incident.Details, result)`, set it on
     `IncidentUpdate.Details`, and mirror it onto the in-memory `incident`
     alongside the existing `PagingSuppressed`/`CausedByIncidentUID` mirrors.
   - `reopenGroupIncident` (~:1174): same pattern.
3. **API** — add `Details models.JSONMap` to `IncidentResponse`
   (service.go:~1631) and populate it in `incidentToResponse` (the single
   mapping function used by list, get, and every embedded payload — MCP
   tools, notifications — that reuses `IncidentResponse`). Add `details` to
   `IncidentDetail` in `openapi.yaml` and regenerate
   `server/pkg/client/client_generated.go` via `go generate ./pkg/client/...`
   (repo already commits generated client code).
4. **Dashboard** — `incidents.$incidentUid.tsx`: add a `FailureDetailsCard`
   (new self-contained component, following the `BlastRadiusCard` pattern)
   rendered when `incident.details?.first_result` is present: `failure_reason`
   headline, status/region/duration/periodStart, and the output map as a
   compact key-value list (JSON fallback for nested values). A second
   "Latest relapse" block renders when `details.last_failure` is present.
   Add translation keys under `detail.failureDetails.*` in
   `src/locales/{en,fr,es,de}/incidents.json`. Regenerate the TS API client
   types (dash0 consumes `server/pkg/client` types via its own generated
   client — check `web/dash0` codegen step) so `IncidentDetail.details` is
   typed.
5. **Tests**:
   - `failure_details_test.go` (package `incidents`, in-package so it can
     call the unexported helpers directly): `failureReasonFromResult` (error
     present / fallback per status), `cappedOutput` (under cap passthrough,
     over cap truncates then drops, error key survives).
   - `failure_details_service_test.go` (package `incidents_test`, sqlite,
     mirroring `resolve_test.go`'s setup style) using the new
     `CreateIncidentForTest` / `CreateOrReopenIncidentForTest` /
     `CreateGroupIncidentForTest` / `CreateOrReopenGroupIncidentForTest`
     export hooks: open → Details populated; reopen → `last_failure` added,
     `first_result`/`failure_reason` untouched; group create path; size cap
     enforced end-to-end through `createIncident`.
   - Slack formatter test in `internal/notifications` (or alongside) proving
     `getFailureReason` returns the real reason from a `*Service`-created
     incident (not a hand-built `models.Incident{Details: ...}`) — must fail
     without the write.
   - API test: `GET /incidents/:uid` returns `details` (handler_test.go).
   - dash0 Playwright: extend `server/test/testdata/testdata.go`'s seeded
     incident (`00000000-0000-0000-0000-000000000013`) with a `Details`
     snapshot, add an E2E asserting the failure-reason panel renders for
     that deterministic incident.
