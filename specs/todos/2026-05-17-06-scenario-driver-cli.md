# Scenario driver CLI

## Context

The fast-loop integration tests in
[2026-05-17-04-fast-loop-e2e-integration-tests.md](2026-05-17-04-fast-loop-e2e-integration-tests.md)
run entirely in-process using testcontainers. They are the right tool for CI on the main
branch. But they cannot answer a different question: "does the currently deployed staging
environment actually fire notifications end-to-end?"

Today the only way to validate a staging deployment is to log in, manually configure a
test check and escalation policy, trigger a failure, and watch logs or a webhook listener
in another terminal. This is slow, non-repeatable, and leaves test data in the
environment.

A scenario driver CLI solves this: it talks to a live solidping instance purely over its
public API, runs a defined scenario (seed → inject failure → assert notification received →
teardown), and exits with a clear success/failure code. It can run in CI as a post-deploy
gate, or by hand when debugging a staging regression.

This is deliberately a **separate tool from the integration tests** — different audience
(ops/deploy gates vs. developers), different lifecycle (always targets a live server vs.
always self-contained), and different failure modes (network flakiness vs. code bugs).

## Goal

- A standalone Go binary `solidping-scenario` (`cmd/scenariodriver/`) that runs named
  scenarios against any live solidping instance.
- Scenarios are defined in YAML files — readable, versionable, shareable.
- The binary starts its own local webhook receiver (a real HTTP listener, not httptest) so
  it can verify outbound notification delivery.
- Output is human-readable on stdout and JUnit XML to a file — CI-friendly.
- Teardown removes every resource the scenario created, even on failure.

### Honest opinion (recorded at planning time)

An alternative implementation would use the Playwright CLI as the driver (driving the
browser UI to configure and trigger scenarios). That tests the UI as well, but it is much
slower, harder to run in low-resource environments, and overkill for a "does the backend
pipeline work?" check. The API-only driver is the right tool here.

The YAML scenario format described below is intentionally minimal — it is not a
general-purpose test framework. If a scenario needs conditional logic or complex
assertions, the right answer is to write a Go test in the integration suite, not to extend
the YAML DSL.

## Non-goals

- Load or soak testing (this is a single-threaded sequential scenario runner).
- UI testing (covered by Playwright E2E in `web/dash0/e2e/`).
- Replacing the in-process integration tests.
- A plug-in system or extensible YAML DSL — keep it simple.

## Design

### Binary and directory layout

```
cmd/scenariodriver/
  main.go            # CLI entry point (urfave/cli v2, same as the main binary)
  runner.go          # Scenario execution engine
  receiver.go        # Embedded webhook receiver (net/http listener)
  assertions.go      # Assertion helpers (assertWebhook, assertNoWebhook)
  seed.go            # API helpers for seeding and tearing down fixtures
  output.go          # Human-readable + JUnit XML output
  scenarios/
    incident-open-close.yaml
    oncall-rotation.yaml
    multi-step-repeat.yaml    # Requires spec 2026-05-17-05
  README.md
```

The generated OpenAPI client at `server/pkg/client/` is reused for all API calls so the
driver stays in sync with the server API surface automatically.

### CLI flags

```
solidping-scenario run \
  --server    https://solidping.example.com \
  --org       default \
  --token     pat_xxxxxxx \
  --scenario  scenarios/incident-open-close.yaml \
  --listen    :9876 \                        # webhook receiver listen address
  --public-url https://driver.internal:9876 \ # URL the server will POST to
  --timeout   30s \
  --junit     results/scenario.xml
```

`--public-url` is required when `--server` is not localhost — the running solidping
instance must be able to reach the driver's webhook listener over the network.

`solidping-scenario list` prints available built-in scenario files.

### YAML scenario format

```yaml
# scenarios/incident-open-close.yaml
name: incident-open-close
description: "Heartbeat goes down → webhook received → heartbeat recovers → resolved webhook received"
timeout: 20s

steps:
  - kind: create_heartbeat_check
    id: my-check
    slug: scenario-test-heartbeat
    confirmation_period_seconds: 0
    recovery_period_seconds: 0
    escalation_threshold: 1

  - kind: create_webhook_channel
    id: my-channel
    url: "{{.ReceiverURL}}/hook/my-channel"    # template fills in driver's listen addr

  - kind: create_escalation_policy
    id: my-policy
    slug: scenario-test-policy
    steps:
      - delay_seconds: 0
        targets:
          - type: connection
            channel_id: my-channel

  - kind: assign_policy_to_check
    check_id: my-check
    policy_id: my-policy

  - kind: send_heartbeat
    check_id: my-check
    status: down

  - kind: expect_webhook
    channel_id: my-channel
    timeout: 10s
    assert:
      event_type: incident.created

  - kind: send_heartbeat
    check_id: my-check
    status: up

  - kind: expect_webhook
    channel_id: my-channel
    timeout: 10s
    assert:
      event_type: incident.resolved
      resolved_at_present: true

cleanup: true   # remove all seeded resources on exit (even on failure)
```

Template variables available to YAML:
- `{{.ReceiverURL}}` — base URL of the driver's webhook listener.
- `{{.OrgSlug}}` — value of `--org`.
- `{{.Timestamp}}` — Unix timestamp (for unique slug generation).

### Execution engine

`runner.go` executes steps sequentially. Each `kind` maps to a handler:

| kind | action |
|---|---|
| `create_heartbeat_check` | POST `/api/v1/orgs/:org/checks` with `type: heartbeat` |
| `create_webhook_channel` | POST `/api/v1/orgs/:org/channels` |
| `create_escalation_policy` | POST `/api/v1/orgs/:org/escalation-policies` + PATCH steps |
| `assign_policy_to_check` | PATCH `/api/v1/orgs/:org/checks/:slug` |
| `send_heartbeat` | POST `/api/v1/heartbeat/:org/:slug?token=...&status=...` |
| `expect_webhook` | block on receiver channel until timeout |
| `assert_incident_open` | GET `/api/v1/orgs/:org/incidents?checkUid=...&status=open` |
| `assert_no_webhook` | wait for the specified duration and assert nothing arrived |
| `wait` | sleep for a fixed duration |

The `cleanup` flag triggers a deferred teardown that deletes created checks, channels,
policies (in reverse order) via DELETE API calls regardless of pass/fail.

### Webhook receiver

`receiver.go` listens on `--listen` (default `:9876`). Each path under `/hook/:id`
corresponds to a channel (matched to `channel_id` in `expect_webhook` steps). Incoming
POST bodies are appended to a per-channel `[]json.RawMessage`; a `chan struct{}` notifies
any waiting `expect_webhook` goroutine.

The receiver starts before scenario execution and shuts down after teardown.

### Output

On success:
```
✓ incident-open-close  12.4s
  ✓ create_heartbeat_check     0.1s
  ✓ create_webhook_channel     0.1s
  ...
  ✓ expect_webhook (incident.created)   2.3s
  ✓ expect_webhook (incident.resolved)  3.1s
  ✓ cleanup                    0.3s
```

On failure:
```
✗ incident-open-close  10.0s  TIMEOUT
  ...
  ✗ expect_webhook (incident.created)  10.0s  TIMEOUT: no webhook received within 10s
```

JUnit XML is written to `--junit` if specified — compatible with GitHub Actions test
reporter and Jenkins.

### Canned scenarios

**`scenarios/incident-open-close.yaml`** — described above. Single-step policy, one
channel. The fundamental smoke test.

**`scenarios/oncall-rotation.yaml`** — creates a weekly on-call schedule with the test
user rostered, an escalation policy with a `schedule` target, triggers a failure, asserts
the test user's webhook connection receives a notification. Verifies the on-call resolver
path.

**`scenarios/multi-step-repeat.yaml`** — requires
[2026-05-17-05-sub-minute-escalation-step-delays.md](2026-05-17-05-sub-minute-escalation-step-delays.md).
Policy with two steps (`delay_seconds: 0` and `delay_seconds: 3`) and `repeat_after_seconds: 5`.
Asserts three webhook events arrive in the correct order and within 20s.

### Makefile integration

```makefile
scenario-test: build
	./solidping-scenario run \
	  --server http://localhost:4000 \
	  --org test \
	  --token pat_test \
	  --listen :9876 \
	  --public-url http://localhost:9876 \
	  --scenario cmd/scenariodriver/scenarios/incident-open-close.yaml \
	  --junit /tmp/scenario-results.xml
```

CI integration: add a post-deploy job in `.github/workflows/deploy.yml` that runs
`solidping-scenario` against staging after a successful deployment, using a staging PAT
stored in GitHub secrets.

## Files to change

### New files

- `cmd/scenariodriver/main.go`
- `cmd/scenariodriver/runner.go`
- `cmd/scenariodriver/receiver.go`
- `cmd/scenariodriver/assertions.go`
- `cmd/scenariodriver/seed.go`
- `cmd/scenariodriver/output.go`
- `cmd/scenariodriver/scenarios/incident-open-close.yaml`
- `cmd/scenariodriver/scenarios/oncall-rotation.yaml`
- `cmd/scenariodriver/scenarios/multi-step-repeat.yaml`
- `cmd/scenariodriver/README.md`

### Modified files

- `Makefile` — add `scenario-test` target; wire `solidping-scenario` into `make build`.
- `server/cmd/` or root Go module — add `cmd/scenariodriver` as a buildable binary in the
  module (update `Makefile`'s build target to also compile `./cmd/scenariodriver`).
- `.github/workflows/deploy.yml` (or equivalent) — post-deploy gate step.

## Verification

### Local

```bash
# Start the dev server with test mode
make dev-test &

# Run the scenario driver against it
make scenario-test
```

Expected output: `✓ incident-open-close  ~5s`. Exit code 0.

### CI

Post-deploy job in staging pipeline:
```yaml
- name: Run scenario tests against staging
  run: |
    ./solidping-scenario run \
      --server $STAGING_URL \
      --org $STAGING_ORG \
      --token ${{ secrets.STAGING_PAT }} \
      --listen :9876 \
      --public-url $DRIVER_PUBLIC_URL \
      --scenario cmd/scenariodriver/scenarios/incident-open-close.yaml \
      --junit results/scenario.xml
  env:
    STAGING_URL: https://solidping.k8xp.com
```

### Failure mode

Kill the notification worker mid-test; assert that the driver prints a clear timeout
message (`expect_webhook: no event received within 10s`) and exits non-zero.

## Implementation Plan

1. Scaffold `cmd/scenariodriver/main.go` with urfave/cli subcommands `run` and `list`.
2. Implement `receiver.go` — `net/http` listener with per-channel queues and `WaitForEvent`.
3. Implement `seed.go` — API helper wrappers using `server/pkg/client`.
4. Implement `runner.go` — step dispatch table, step context with resolved IDs, teardown
   deferred stack.
5. Implement `assertions.go` — `assertWebhook(ctx, collector, timeout, predicate)`.
6. Implement `output.go` — pretty stdout + JUnit XML writer.
7. Write `scenarios/incident-open-close.yaml` and run end-to-end against `make dev-test`.
8. Write `scenarios/oncall-rotation.yaml`; debug on-call resolver path.
9. Write `scenarios/multi-step-repeat.yaml` (skip if spec 05 not yet landed).
10. Add `README.md` with usage example and troubleshooting section.
11. Wire `make scenario-test` and CI deploy job.
