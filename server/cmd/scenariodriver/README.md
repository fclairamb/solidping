# solidping-scenario

A standalone CLI tool for running end-to-end scenarios against a live SolidPing
instance. It seeds test fixtures via the public API, injects failures, asserts
that notifications are delivered, and tears down all created resources — even on
failure.

## When to use this

- **Post-deploy gate**: verify that a staging or production deployment actually
  fires notifications end-to-end.
- **Debugging regressions**: pinpoint where in the pipeline (check → incident →
  escalation → webhook) things break on a live server.
- **Ops smoke test**: confirm the deployed stack is healthy before closing a
  maintenance window.

For in-process CI tests that run in isolation without a live server, use the
Go integration tests in `server/test/integration/scenario/` instead.

## Quick start

```bash
# Build the binary
make build-scenario

# Run the smoke test against a local dev server (make dev-test)
make scenario-test
```

## Manual usage

```bash
./bin/solidping-scenario run \
  --server   https://solidping.example.com \
  --org      default \
  --token    pat_xxxxxxx \
  --scenario server/cmd/scenariodriver/scenarios/incident-open-close.yaml \
  --listen   :9876 \
  --public-url https://driver.internal:9876 \
  --timeout  30s \
  --junit    /tmp/scenario.xml
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `--server` | `http://localhost:4000` | SolidPing server URL |
| `--org` | `default` | Organization slug |
| `--token` | _(required)_ | Personal Access Token |
| `--scenario` | _(required)_ | Path to scenario YAML file |
| `--listen` | `:9876` | Webhook receiver listen address |
| `--public-url` | `http://localhost:9876` | URL the server will POST notifications to |
| `--timeout` | scenario's `timeout` | Override the global scenario timeout |
| `--junit` | _(none)_ | Write JUnit XML to this path |

`--public-url` must be reachable from the SolidPing server. When running
locally against `localhost:4000`, the default `http://localhost:9876` works.
When running against a remote staging server, expose the driver's port and
set `--public-url` accordingly.

## List built-in scenarios

```bash
./bin/solidping-scenario list
```

## Scenario YAML format

```yaml
name: my-scenario
description: "What this tests"
timeout: 30s    # global timeout; override with --timeout
cleanup: true   # delete all seeded resources on exit (even on failure)

steps:
  - kind: create_heartbeat_check
    id: my-check           # referenced by later steps
    slug: test-heartbeat   # slug on the server (template variables allowed)
    confirmation_period_seconds: 0
    recovery_period_seconds: 0
    escalation_threshold: 1

  - kind: create_webhook_channel
    id: my-channel
    url: "{{.ReceiverURL}}/hook/my-channel"   # driver's listener

  - kind: create_escalation_policy
    id: my-policy
    slug: test-policy
    steps:
      - delay_seconds: 0
        targets:
          - type: connection
            channel_id: my-channel   # references step id above

  - kind: assign_policy_to_check
    check_id: my-check
    policy_id: my-policy

  - kind: send_heartbeat
    check_id: my-check
    status: down           # "up" | "down" | "error"

  - kind: expect_webhook
    channel_id: my-channel
    timeout: 10s
    assert:
      event_type: incident.created

  - kind: wait
    duration: 2s           # sleep between steps
```

### Step kinds

| kind | description |
|---|---|
| `create_heartbeat_check` | Create a heartbeat check and register its token |
| `create_webhook_channel` | Create a webhook notification channel |
| `create_escalation_policy` | Create an escalation policy with steps |
| `assign_policy_to_check` | Assign an escalation policy UID to a check |
| `send_heartbeat` | POST a heartbeat status ping |
| `expect_webhook` | Block until a matching webhook arrives (or timeout) |
| `assert_no_webhook` | Wait `duration` and assert nothing arrived |
| `assert_incident_open` | Assert at least one open incident exists for the check |
| `wait` | Sleep for `duration` |

### Template variables

| variable | value |
|---|---|
| `{{.ReceiverURL}}` | Base URL of the driver's webhook listener |
| `{{.OrgSlug}}` | Value of `--org` |
| `{{.Timestamp}}` | Unix timestamp (useful for unique slug suffixes) |

### Assertions in `expect_webhook`

| key | type | meaning |
|---|---|---|
| `event_type` | string | Exact match on `eventType` field |
| `resolved_at_present` | bool | `true` = `resolvedAt` must be non-empty |
| _(any other key)_ | any | Exact equality on that JSON field |

## Output

Success:
```
✓ incident-open-close  12.4s
  ✓ create_heartbeat_check my-check            0.1s
  ✓ create_webhook_channel my-channel          0.1s
  ✓ create_escalation_policy my-policy         0.1s
  ✓ assign_policy_to_check                     0.1s
  ✓ send_heartbeat (down)                      0.1s
  ✓ expect_webhook (incident.created)          2.3s
  ✓ send_heartbeat (up)                        0.1s
  ✓ expect_webhook (incident.resolved)         3.1s
  ✓ cleanup: delete policy ...                 0.1s
  ✓ cleanup: delete channel ...                0.1s
  ✓ cleanup: delete check ...                  0.1s
```

Failure:
```
✗ incident-open-close  15.0s  FAILED
  ...
  ✗ expect_webhook (incident.created)         15.0s  no webhook received within 15s: incident.created
```

Exit code is 0 on success, non-zero on failure.

## Troubleshooting

**Webhook not received**

1. Check `--public-url` is reachable from the server (use `curl <public-url>/hook/test`).
2. Check the escalation worker is running (`LOG_LEVEL=debug ./solidping serve`).
3. Verify the PAT token has admin rights in the org.

**`delete policy … in use`**

A policy cannot be deleted while an open incident references it. The cleanup
step will fail with a 409. Resolve the incident manually or wait for it to
auto-close, then re-run cleanup by hand if needed.

**Authentication failures**

Ensure `--token` starts with `pat_` (Personal Access Token) and is scoped to
the `--org` you specified.
