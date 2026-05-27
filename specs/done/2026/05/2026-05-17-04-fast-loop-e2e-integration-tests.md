# Fast-loop end-to-end integration tests

## Context

The incident pipeline has six stages: check-result ingestion → incident state-machine →
escalation policy scheduling → on-call resolution → notification fan-out → outbound
delivery. Each stage is covered by unit and handler tests in isolation, but no test
exercises the full pipeline from a check going down to a webhook leaving the process.

Manual validation today means spinning up `make dev-test`, sending a heartbeat POST,
watching logs, and checking whether a webhook arrived — slow, non-repeatable, and catches
nothing in CI.

The codebase already has two primitives that make this approachable without a separate
external process:

1. **Heartbeat push ingestion.** `POST /api/v1/heartbeat/:org/:identifier?token=...&status=down`
   calls `incidentSvc.ProcessCheckResult` synchronously
   ([server/internal/handlers/heartbeat/service.go:80](server/internal/handlers/heartbeat/service.go))
   — no worker scheduling, no 1-minute poll loop. The incident state machine runs
   immediately.

2. **Outbound webhook.** `WebhookSender.Send` in
   [server/internal/notifications/webhook.go:29](server/internal/notifications/webhook.go)
   POSTs a JSON payload to the channel URL. An in-test `httptest.NewServer` is all that is
   needed on the receiving end.

The main infrastructure gap is that the existing integration test harness
(`server/test/integration/testhelper.go`) boots the server against SQLite in-memory. The
job worker polls every 1 minute on SQLite; on Postgres, `LISTEN/NOTIFY` wakes it the
moment a job is inserted
([server/internal/jobs/jobsvc/service.go:218](server/internal/jobs/jobsvc/service.go)).
Fast tests therefore need Postgres — this spec introduces a `NewPostgresScenario(t)` harness
built on testcontainers.

One real ceiling remains: escalation step delays are stored as integer minutes
(`DelayMinutes int` in [server/internal/db/models/escalation_policy.go:72](server/internal/db/models/escalation_policy.go)).
Setting `DelayMinutes: 0` makes a step fire immediately, which is enough for the
single-step open/close/multi-channel scenarios below. Multi-step and repeat-cycle
scenarios require [2026-05-17-05-sub-minute-escalation-step-delays.md](2026-05-17-05-sub-minute-escalation-step-delays.md)
to land first; those tests are included here behind a build tag or skipped with
`t.Skip` until that spec ships.

## Goal

A Go integration-test package `server/test/integration/scenario/` that:

- Runs the full check-down → incident → escalation → webhook → recovery pipeline.
- Completes every scenario in under 10 seconds on a dev laptop with Docker available.
- Requires no manual setup: `go test ./server/test/integration/scenario/...` with Docker
  running is sufficient.
- Fails fast with a clear assertion message, not a timeout, when the pipeline breaks.

### Honest opinion (recorded at planning time)

The obvious alternative — a separate driver process that manages its own HTTP listener and
calls the solidping API externally — adds two-process coordination, CI orchestration
overhead, and duplicates setup code. The heartbeat endpoint already gives you "inject a
failure from outside the server process"; `httptest.NewServer` gives you "receive a
webhook from outside the server process". There is nothing an external driver can verify
that the in-process test cannot. Save the external driver for staging/canary validation —
that is a different spec ([2026-05-17-06-scenario-driver-cli.md](2026-05-17-06-scenario-driver-cli.md)).

The second temptation is a `/api/v1/test/notifications` inspection endpoint (add a
server-side ledger, query it after each scenario). This would be simpler to assert against
but embeds a test seam in production code. An `httptest.NewServer` inside the test costs
six lines and is trivially self-documenting — prefer it.

## Non-goals

- UI / Playwright testing (stays in `web/dash0/e2e/`).
- Active-check scenarios (HTTP/TCP probe to a real target) — those tests would need the
  check worker poll path; they belong to a separate load/smoke test suite.
- Email or Slack notification delivery (requires external credentials; these channels are
  tested by their unit senders). Webhook is the only channel that can be self-hosted in a
  test.
- Mocking any internal dependency — the point of this suite is to exercise real
  integrations.

## Design

### Test harness

`server/test/integration/scenario/harness.go` exports `Scenario` — a struct that owns
the Postgres testcontainer, the running `app.Server`, the in-test webhook receiver, and
all pre-seeded fixtures.

```go
type Scenario struct {
    T          *testing.T
    BaseURL    string   // e.g. "http://127.0.0.1:XXXXX"
    OrgSlug    string
    Token      string   // PAT for API calls

    WebhookURL string   // httptest server URL pointing at the receiver
    Webhooks   *WebhookCollector
}
```

`NewPostgresScenario(t)` does:

1. `testcontainers.RunContainer("postgres:17-alpine")` — same pattern used by the rest of
   the testcontainers usage referenced in `server/CLAUDE.md`. Registers `t.Cleanup` to
   terminate the container.
2. Applies migrations via `app.Server.Initialize` (same path as prod — honest test).
3. Boots `app.NewServer` pointed at the Postgres DSN and calls `SetupRoutes`.
4. Wraps in `httptest.NewServer`; registers `t.Cleanup`.
5. Creates an org (`test-scenario-<random-suffix>`), an admin user, and a PAT via the
   same `authedRequestWithBody` helper from
   [server/test/integration/escalation_policies_test.go](../escalation_policies_test.go).
6. Stands up the `WebhookCollector` — its own `httptest.NewServer` (per scenario, so
   tests run parallel without shared state).

`NewPostgresScenario` accepts a `ScenarioOptions` struct for threshold overrides (so each
test can set its own confirmation/recovery windows).

**`WebhookCollector`** is a thread-safe wrapper around a `[]json.RawMessage` slice plus a
`sync.Cond`. Its handler appends every POST body. `WaitForWebhook(ctx, predicate)` blocks
until `predicate(payload) == true` or the context is cancelled — deadline-bound, no busy
loop. Each test passes `context.WithTimeout(ctx, 5*time.Second)` as the deadline, so a
regression fails in 5s rather than timing out the full test run.

### Fixture helpers

Re-exported from the existing helpers in
[server/test/integration/escalation_policies_test.go](../escalation_policies_test.go) (move
to a non-`_test.go` file so they are importable from the sub-package):

- `CreateHeartbeatCheck(s *Scenario, slug string, opts CheckOptions) (uid, token string)` —
  POST `/api/v1/orgs/:org/checks` with
  `type: "heartbeat"`, `confirmationPeriodSeconds: 0`, `recoveryPeriodSeconds: 0`,
  `escalationThreshold: 1`. Returns the check's UID and heartbeat token.
- `CreateWebhookChannel(s *Scenario, receiverURL string) string` — POST
  `/api/v1/orgs/:org/channels` with `type: "webhook"` and `url: receiverURL`.
- `CreateEscalationPolicy(s *Scenario, policySlug string, steps []StepDef) string`.
- `AssignPolicyToCheck(s *Scenario, checkSlug, policySlug string)`.
- `SendHeartbeat(s *Scenario, checkSlug, token, status string)` — POST
  `/api/v1/heartbeat/:org/:slug?token=...&status=...`.

### Scenario: incident open and close

`server/test/integration/scenario/incident_open_close_test.go`

1. Create heartbeat check + webhook channel + escalation policy (step 0, `delayMinutes=0`,
   target = the webhook channel).
2. `SendHeartbeat(status="down")`.
3. `WaitForWebhook` asserting `eventType == "incident.created"` and `checkUid == uid`.
4. Assert webhook payload fields from
   [server/internal/notifications/webhook.go:81-114](../../../server/internal/notifications/webhook.go):
   `incidentUid` non-empty, `checkName` matches, `startedAt` non-zero, `resolvedAt` absent.
5. `SendHeartbeat(status="up")`.
6. `WaitForWebhook` asserting `eventType == "incident.resolved"` and `resolvedAt` non-zero.

### Scenario: multi-channel fanout

`server/test/integration/scenario/multi_channel_test.go`

1. Create two independent `WebhookCollector`s and two webhook channels pointing at them.
2. Assign both channels to the same escalation policy step.
3. `SendHeartbeat(status="down")`.
4. Assert both collectors receive `incident.created` within 5s.

This exercises the fan-out loop in
[server/internal/jobs/jobtypes/job_escalation_step.go:85](server/internal/jobs/jobtypes/job_escalation_step.go).

### Scenario: on-call schedule target

`server/test/integration/scenario/oncall_target_test.go`

1. Create a weekly on-call schedule rostering the test user.
2. Create an escalation policy step with `targetType: "schedule"` targeting the schedule.
3. Give the test user a webhook notification preference (connection).
4. `SendHeartbeat(status="down")`.
5. Assert the user's webhook channel receives `incident.created`.

This exercises the on-call resolver at
[server/internal/handlers/oncallschedules/resolver.go:19](server/internal/handlers/oncallschedules/resolver.go)
through the escalation step job.

### Scenario: repeat-cycle (requires spec 2026-05-17-05)

`server/test/integration/scenario/repeat_cycle_test.go`

Skipped with `t.Skip("requires sub-minute step delays: 2026-05-17-05")` until that spec
lands.

When enabled:
1. Create policy with `repeatMax: 3`, `repeatAfterSeconds: 2` (introduced by that spec).
2. `SendHeartbeat(status="down")`.
3. Assert three consecutive `incident.paged` (or equivalent) webhooks arrive within 10s.

### Threading model

All tests call `t.Parallel()`. Each creates a fresh org with a random suffix, a fresh
Postgres container (or a fresh schema in a shared container — see implementation note
below), and a fresh `httptest.NewServer`. No global state.

**Implementation note — container reuse.** Spinning up a Postgres container per test
function adds ~2s overhead. Prefer a `TestMain` that boots one container and creates an
isolated schema (random `search_path`) per test. This is the same pattern used by
`testcontainers`' reaper/module framework and keeps the total suite under 10s.

## Files to change

### New files

- `server/test/integration/scenario/harness.go` — `Scenario`, `NewPostgresScenario`,
  `WebhookCollector`, `WaitForWebhook`.
- `server/test/integration/scenario/fixtures.go` — fixture helpers (moved and
  de-`_test.go`-ified).
- `server/test/integration/scenario/incident_open_close_test.go`
- `server/test/integration/scenario/multi_channel_test.go`
- `server/test/integration/scenario/oncall_target_test.go`
- `server/test/integration/scenario/repeat_cycle_test.go`

### Modified files

- `server/test/integration/testhelper.go` — extract server-boot logic into a reusable
  `bootServer(t, dbDSN string) *httptest.Server` so the scenario package can call it
  without copy-pasting.
- `server/test/integration/escalation_policies_test.go` (untracked) — move `authedRequestWithBody`,
  `createConnectionViaAPI`, `createEscalationPolicyViaAPI` etc. into
  `scenario/fixtures.go`; import from there.
- `Makefile` — add `test-scenario` target:
  `go test -v -timeout 120s ./server/test/integration/scenario/...`

## Verification

```bash
# Requires Docker
make test-scenario

# Or directly
cd server && go test -v -timeout 120s ./test/integration/scenario/...
```

Each `WaitForWebhook` call has a 5-second deadline; the full suite should complete in
under 30 seconds. A Postgres container is reused across sub-tests within a `TestMain` to
avoid per-test startup overhead.

Gate in CI: add `make test-scenario` to `.github/workflows/go.yml` alongside `make test`.
CI must have Docker-in-Docker or DIND available (it already does for the Playwright suite).

## Implementation Plan

1. Extract server-boot helper from `testhelper.go` into an unexported `bootServer(t, dbDSN)`.
2. Write `scenario/harness.go`: testcontainer setup, `NewPostgresScenario`, `WebhookCollector`.
3. Write `scenario/fixtures.go`: move shared helpers from `escalation_policies_test.go`,
   add `CreateHeartbeatCheck`, `SendHeartbeat`.
4. Write `incident_open_close_test.go` and run; iterate until green with `WaitForWebhook` timeout ≤ 3s.
5. Write `multi_channel_test.go`.
6. Write `oncall_target_test.go`; may surface on-call-resolver wiring issues in test mode.
7. Write `repeat_cycle_test.go` with `t.Skip`; remove skip when spec 05 lands.
8. Add `test-scenario` Makefile target and CI job.
9. `make lint` — fix any `t.Parallel()` / `prealloc` / `require` linter findings.
