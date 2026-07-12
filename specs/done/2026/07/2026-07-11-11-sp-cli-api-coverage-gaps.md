# sp CLI: cover the API features the CLI doesn't support yet

## Problem

The `sp` CLI (`server/cmd/sp/main.go`, commands registered in
`server/pkg/cli/commands.go:10-642`, urfave/cli/v3) exposes 11 command
groups — `auth`, `server`, `checks` (+`deps`/`export`/`import`), `apply`,
`results`, `incidents`, `events`, `tokens`, `members`, `jobs`, `system` —
but the API surface registered in `server/internal/app/server.go:413-1230`
is far larger. Whole resources have **zero** CLI coverage, and several
covered resources are missing operations:

**Partially covered (missing operations):**

- **Incidents** — read-only today (`list`/`get`/`events`,
  `server/pkg/cli/incidents.go`). No lifecycle actions: `ack`, `unack`,
  `snooze`, `unsnooze`, `resolve` (POST `/incidents/:uid/{action}`), and
  no per-incident notifications sub-resource.
- **Checks** — no `validate` (POST `/checks/validate`) and no `clone`
  (POST `/checks/:checkUid/clone`).
- **Check dependencies** — can add/remove/set but not PATCH a single edge
  (`/checks/:check/dependencies/:uid`), and no org-wide graph
  (GET `/orgs/:org/dependencies`).
- **Results** — org-wide `list` only (`server/pkg/cli/results.go:75`); no
  single-result get (`/checks/:check/results/:uid`) and no availability
  rollups (`/checks/:check/availability`).
- **Server/mgmt** — `health`/`version` only; no `/api/mgmt/limits`,
  `report`, `memory`, `scheduling/cost-distribution`.

**Zero coverage (org-facing resources):**

- Channels/integrations (notification destinations) — org-level CRUD +
  `rotate-secret` + `test` (`/orgs/:org/channels`), and per-check channel
  bindings (`/orgs/:org/checks/:check/channels`).
- Status pages (CRUD + sections + resources + subscribers) and status
  updates.
- Maintenance windows (CRUD + checks assignment).
- On-call schedules (CRUD + overrides + preview + ical-feed) and
  escalation policies (CRUD).
- Check groups, severities, labels, regions, check types
  (`/check-types`, `/check-types/samples`, `/orgs/:org/check-types`).
- Notifications (org-level, per-incident, per-user, `/me`) and the user's
  notification routes/contacts (`/orgs/:org/users/me/notification-routes`,
  `notification-contacts`, route `test`).
- Network discovery (scans, hosts, promote/dismiss) — notably already
  fully specified in `server/internal/app/openapi/openapi.yaml`, so it's
  the lowest-effort net-new group.
- Org management: create org (POST `/orgs`), org settings (GET/PATCH),
  invitations (list/create/delete), membership requests (both sides).
- Admin/ops: entitlements (get/put/patch/audits), files, email
  suppressions, jobs admin & scheduler introspection
  (`/orgs/:org/jobs/stats`, `/orgs/:org/admin/jobs*`,
  `/orgs/:org/check-jobs*`, `/system/jobs*`, `/system/check-jobs*`),
  system actions (`/system/test-email`, `/system/email-inbox/*`,
  `/system/activation`, `/system/scheduling/lane-load`).
- Check availability badges (`/checks/:check/badges/:components`) and
  heartbeat ingestion (`/heartbeat/:org/:identifier`).

**Structural blocker:** the CLI talks to the API through the
oapi-codegen client (`server/pkg/cli/apihelper/apihelper.go`), generated
from `server/internal/app/openapi/openapi.yaml` — and that spec documents
only a subset of the live routes (checks, deps, results, incidents,
events, members, jobs, discovery, system params, auth, mgmt). Every
uncovered resource that isn't in `openapi.yaml` (channels, status pages,
on-call, escalation, maintenance windows, notifications, severities,
check groups, entitlements, files, labels, regions, check types, …) needs
OpenAPI spec additions before a CLI command can call it through the
generated client.

## Proposal

Close the gap **completely** (all three phases below are in scope), one
command group per resource, following the existing conventions in
`server/pkg/cli/` (one file per group, registration in `GetCommands()`,
`--output json|table`, org from the persisted context).

The OpenAPI drift is fixed as part of this spec: every live route in
`server/internal/app/server.go` that the CLI needs gets added to
`server/internal/app/openapi/openapi.yaml`, and the oapi-codegen client
is regenerated — the spec becomes an accurate description of the API
surface, not just the historical subset.

### Phase 1 — complete the partially-covered resources

No OpenAPI additions needed (all in `openapi.yaml` already):

- `sp incidents ack|unack|snooze|unsnooze|resolve <uid>` (snooze takes a
  duration flag).
- `sp checks validate` (file or stdin, mirrors `apply` input handling in
  `server/pkg/cli/apply.go`) and `sp checks clone <uid>`.
- `sp checks deps update <uid>` (PATCH edge kind/description) and
  `sp checks deps graph` (org-wide `/orgs/:org/dependencies`).
- `sp results get <check> <uid>` and `sp checks availability <check>`.
- `sp discovery …` (types, scans list/create/get/cancel, hosts
  list/promote/delete, checks list/promote/dismiss) — spec'd in
  `openapi.yaml`, zero-spec-work net-new group.

- `sp heartbeat send <identifier>` — sugar over
  POST `/heartbeat/:org/:identifier` for cron-job UX (also in
  `openapi`-free territory: it's a public ingestion route, call it
  directly, no auth client needed).

### Phase 2 — high-value net-new org resources

Extend `openapi.yaml` first (spec + regenerate client), then:

- `sp channels list|create|get|update|remove|rotate-secret|test` and
  `sp checks channels list|set|add|remove` for per-check bindings.
- `sp status-pages …` (CRUD; sections/resources/subscribers as
  subcommands) and `sp status-updates …` (CRUD).
- `sp maintenance-windows list|create|get|update|remove|checks`.
- `sp check-groups …`, `sp severities …`, `sp labels list`,
  `sp regions list`, `sp check-types list|samples`.
- `sp oncall …` (schedules CRUD + overrides + preview) and
  `sp escalation-policies …` — align with the outcome of spec
  2026-07-11-10 (slug removal on these resources) before writing flags.
- `sp orgs create`, `sp orgs settings get|set`,
  `sp invitations list|create|remove`, membership-request
  commands (request/list/cancel + org-side list/approve/reject).

### Phase 3 — admin/ops surface

- `sp entitlements get|set|patch|audits`, `sp files list|get|download|remove`,
  `sp email-suppressions list|remove`.
- `sp jobs stats`, `sp jobs admin list|get|chain`, `sp check-jobs …`,
  and system-scope equivalents.
- `sp server limits|report|memory` and `sp system test-email`,
  `sp system email-inbox …`, `sp system activation`, `sp system lane-load`.
- `sp notifications list|get` (org / me scopes) and
  `sp notification-routes list|update|test` +
  `sp notification-contacts add|remove`.
- Auth self-service: `sp auth register`, `sp auth confirm-registration`,
  `sp auth request-password-reset`, `sp auth reset-password`,
  `sp auth update` (PATCH `/auth/me`), `sp auth providers`,
  `sp auth invite <token>` (view) + `sp auth accept-invite`.

### Non-goals

- Worker-internal endpoints (`/workers/*`) — machine-to-machine only.
- Protocol endpoints: MCP, the OAuth2 server + `.well-known`, Slack app
  webhooks (`events`/`command`/`interaction`), browser OAuth login flows
  (browser login itself is spec 2026-07-11-08).
- WebAuthn/passkeys **and** 2FA setup/confirm/disable/verify/recovery —
  excluded by decision: token and web (browser) authentication are
  enough for the CLI. The 2FA/passkey routes stay dashboard-only and
  are not added to `openapi.yaml` as part of this spec.
- WebPush (`/orgs/:org/webpush/vapid-public-key`) — browser-oriented.
- Test-only endpoints (`/api/v1/test/*`, `/fake`).

### Tests

Each new command group gets the same treatment as existing ones: unit
tests against the CLI command wiring plus integration coverage exercising
the real handlers (the existing `sp` CLI tests are the template). The
OpenAPI additions in phase 2 must round-trip through codegen cleanly
(`make build` regenerates and compiles).

## Decisions

Resolved 2026-07-11:

1. **Scope**: full catalog — all three phases, including the
   `openapi.yaml` backfill for every resource the CLI gains.
2. **Auth self-service**: in scope (register, password reset, profile
   update, providers, invites).
3. **2FA/passkeys**: excluded — token/web authentication is considered
   enough for the CLI.
4. **Heartbeat**: `sp heartbeat send` is in scope (phase 1).

## Implementation Plan

This is a large, multi-phase spec. This plan sequences the work; a single
pass delivers **Phase 1** (the self-contained, high-value core that closes the
partially-covered resources and adds the zero-spec-work discovery + heartbeat
groups). Phases 2 and 3 (dozens of net-new org/admin resources, each needing
`openapi.yaml` schema+path additions, client regen, a CLI file, and tests) are
tracked as follow-up work.

### Codegen workflow (verified)
- The generated client lives in `server/pkg/client/client_generated.go`, built
  from `server/internal/app/openapi/openapi.yaml` via oapi-codegen.
- `go generate ./...` fails locally (a transitive dep won't compile under the
  pinned toolchain), so regenerate with the prebuilt binary:
  `cd server/pkg/client && oapi-codegen --config=oapi-codegen.yaml ../../internal/app/openapi/openapi.yaml`
  (writes `client_generated.go` in place per the config's `output:` key).
- The CLI talks to the API via `client.SolidPingClient` (embeds the generated
  `*ClientWithResponses`, plus hand-written `rawRequest` wrappers in
  `client.go` for endpoints not worth typing).

### Phase 1 — this pass

1. **OpenAPI additions** to `openapi.yaml`, then regenerate the client:
   - Incident lifecycle: `acknowledgeIncident`, `unacknowledgeIncident`,
     `snoozeIncident`, `unsnoozeIncident`, `resolveIncident`
     (POST `/incidents/{uid}/{action}`) → all return `IncidentDetail`.
     New request schemas `IncidentAckRequest` (`note`), `IncidentSnoozeRequest`
     (`until`/`duration`/`reason`).
   - `validateCheck` (POST `/checks/validate`) with `ValidateCheckRequest`
     (`type`,`slug`,`config`,`dependsOn`) → `ValidateCheckResponse`
     (`valid`,`fields`).
   - `cloneCheck` (POST `/checks/{checkUid}/clone`) with `CloneCheckRequest` →
     `Check` (201).
   - `getOrgDependencyGraph` (GET `/orgs/{org}/dependencies`) →
     `OrgDependencyGraphResponse` (`data:{nodes[],edges[]}`).
   - (`getOrgResult`, `getCheckAvailability`, `updateCheckDependency`, and the
     whole discovery group are already in the spec — just regenerate so the
     stale client picks up `getOrgResult`.)
2. **`sp incidents ack|unack|snooze|unsnooze|resolve <uid>`** — new actions in
   `incidents.go`; `snooze` takes `--duration`/`--until`/`--reason`; ack/resolve
   take `--note`. Register in `commands.go`.
3. **`sp checks validate`** (`--file` or stdin, JSON/YAML, mirroring
   `apply.go` input handling) and **`sp checks clone <uid|slug>`**
   (`--name`/`--slug`/`--description`/`--group`/`--enabled`) in `checks.go`.
4. **`sp checks deps update <child> <parent>`** (PATCH edge kind/description via
   `UpdateCheckDependencyWithResponse`) and **`sp checks deps graph`** (org-wide
   nodes/edges) in `checks_deps.go`.
5. **`sp results get <check> <uid>`** in `results.go` (via `GetOrgResult`).
6. **`sp checks availability <uid|slug>`** in `checks_detail.go` (via
   `GetCheckAvailability`, `--period`/`--window` flags).
7. **`sp discovery`** new group `discovery.go`: `types`, `scans
   list|create|get|cancel`, `hosts`/`checks list|promote|dismiss` mapped to the
   discovered-checks + promote/dismiss client ops.
8. **`sp heartbeat send <identifier>`** new group `heartbeat.go` + a
   `SendHeartbeat` wrapper in `client.go` (public ingestion route,
   `--token`/`--status`/`--message`).
9. **Tests**: integration tests in `server/test/integration/` exercising each
   new generated-client op against a `NewTestServer`, plus CLI-action smoke
   tests where practical. Regen must round-trip cleanly (`make build-backend`).

### Phase 2 & 3 — follow-up (not this pass)
Net-new org resources (channels, status pages, maintenance windows, on-call,
escalation, check-groups, severities, labels, regions, check-types, orgs,
invitations, membership requests) and the admin/ops + auth-self-service surface.
Each needs `openapi.yaml` additions, client regen, a CLI group, and tests.
