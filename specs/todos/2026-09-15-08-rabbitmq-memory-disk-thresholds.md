---
model: sonnet
effort: high
---

# RabbitMQ check: alert on memory and disk headroom before the broker's own watermarks trip

## Problem

The RabbitMQ management UI shows, per node, memory used against the **high
watermark** and disk free against the **low watermark** (e.g. "223 MiB / 1.9 GiB
high watermark", "49 GiB / 1.9 GiB low watermark"). Those two gauges are what an
operator watches: when either watermark is crossed, RabbitMQ raises a resource
alarm and **blocks every publisher**, which is already an outage.

The `rabbitmq` check in `management` mode only asks the broker whether that
alarm is *already* in effect — it calls `/api/health/checks/alarms` and maps
`200` → Up, anything else → Down
([checker.go:242](server/internal/checkers/checkrabbitmq/checker.go:242)). It
never reads the underlying numbers, so:

- There is no way to be paged *before* the watermark — "warn me at 80 % of the
  memory watermark" or "warn me when less than 10 GiB of disk is free" cannot
  be expressed. The check flips from green to red at the exact moment publishers
  are blocked.
- No metrics are recorded in management mode
  ([checker.go:255](server/internal/checkers/checkrabbitmq/checker.go:255)
  only ever fills `connection_time_ms` / `total_time_ms`), so the dashboard has
  no memory or disk history to look at, unlike the AMQP mode which records
  `queue_messages` / `queue_consumers`.
- `mode` and `managementPort` exist on the backend config
  ([config.go:34](server/internal/checkers/checkrabbitmq/config.go:34)) but the
  dash0 form never exposes them
  ([database.tsx:310](web/dash0/src/components/checks/form/types/database.tsx:310)
  owns `host/port/username/password/vhost/queue/tls` only), so management mode
  is API-only today. Any threshold feature is unusable from the UI without
  fixing that.

## Proposal

Add memory and disk thresholds to the `rabbitmq` check, evaluated in
`management` mode from the broker's own per-node figures, with the two-tier
warning / critical convention the `ssl`, `domain` and `prometheus` checks
already use
([checkprometheus/config.go:91](server/internal/checkers/checkprometheus/config.go:91),
[checkprometheus/checker.go:353](server/internal/checkers/checkprometheus/checker.go:353)).

### Config

Four new optional string keys on `RabbitMQConfig` (camelCase, per the REST
conventions):

| Key | Meaning | Accepted values |
|---|---|---|
| `memoryUsedWarning` | ceiling on memory used, Warning tier | `"80%"` (of the high watermark, i.e. `mem_used / mem_limit`) **or** a byte size (`"1.5GiB"`, `"1500MB"`, `"1610612736"`) |
| `memoryUsedCritical` | ceiling on memory used, Critical tier | same |
| `diskFreeWarning` | floor on free disk, Warning tier | byte size only (`"20GiB"`) |
| `diskFreeCritical` | floor on free disk, Critical tier | byte size only (`"5GiB"`) |

Each key is independent; setting one tier alone is fine. The value is stored
and echoed by `GetConfig()` as the string the user typed.

- **Memory** accepts a percentage or an absolute size. A percentage is relative
  to RabbitMQ's high watermark (`mem_limit`), which is what the management UI
  bar shows — `100%` is exactly the point where RabbitMQ blocks publishers.
- **Disk** is absolute only. The management API exposes `disk_free` and
  `disk_free_limit` per node but **no total disk size**, so "percent full"
  cannot be computed; a `%` value on a disk key is a `VALIDATION_ERROR` whose
  message says so. (If a relative form is ever wanted, the only meaningful
  denominator is the low watermark itself — out of scope here.)
- Parsing: `%` suffix → integer 1–100; otherwise
  `humanize.ParseBytes` (`github.com/dustin/go-humanize` is already an indirect
  dependency in [server/go.mod:154](server/go.mod:154); promote it to direct).
  Anything else is a config error naming the key.
- Ordering: when both tiers of a resource use the same unit kind, require
  `memoryUsedWarning < memoryUsedCritical` and
  `diskFreeWarning > diskFreeCritical`, mirroring
  [checkdomain/config.go:169](server/internal/checkers/checkdomain/config.go:169).
  Mixed units (`"70%"` vs `"1.8GiB"`) are accepted without cross-checking.
- Any threshold set while `mode` is not `management` is a config error
  (`memoryUsedWarning: requires mode "management"`). The AMQP protocol has no
  view of node resources.

### Execution (management mode)

1. Keep the existing `GET /api/health/checks/alarms` probe exactly as it is —
   it is the backward-compatible "is the broker already alarming" answer.
2. Additionally `GET /api/nodes` with the same credentials and timeout
   budget. For every node with `running: true`, read `name`, `mem_used`,
   `mem_limit`, `mem_alarm`, `disk_free`, `disk_free_limit`,
   `disk_free_alarm` (all bytes / booleans; confirm the field names against a
   live 3.x/4.x broker when writing the fixtures).
3. Evaluate thresholds per node; the **worst node wins** for a cluster:
   critical breached on any node → `StatusDown`; otherwise warning breached
   on any node → `StatusWarning`; otherwise `StatusUp`. `StatusWarning`
   counts as up and opens no incident, as documented at
   [checkerdef/types.go:46](server/internal/checkers/checkerdef/types.go:46).
4. Record flat numeric metrics (snake_case like the existing ones), taken from
   the worst node so single-node and cluster graphs read the same:
   `mem_used_bytes`, `mem_limit_bytes`, `mem_used_percent`, `disk_free_bytes`,
   `disk_free_limit_bytes`, `nodes_running`, `nodes_total`.
5. Output: `nodes` (per-node array of the raw fields above) and, on a breach,
   a human-readable `error` built with `humanize.IBytes`, e.g.
   `memory used 1.6 GiB is 84% of the 1.9 GiB high watermark on rabbit@node1 (critical threshold 80%)`.
6. Metrics are recorded on every management-mode execution, thresholds or
   not, so existing checks gain memory/disk history for free. If the
   `/api/nodes` call fails (network error, non-200 — e.g. a user without the
   `monitoring` tag): with any threshold configured the check is `StatusDown`
   with the error in output; with none configured the result is unchanged
   from today (alarms probe decides) and output carries `nodes_error`.
7. A node reporting `mem_limit` ≤ 0 cannot be evaluated against a percentage:
   skip that comparison for that node, mention it in output, never divide by
   zero.

### dash0 form

In the RabbitMQ module of
[database.tsx:299](web/dash0/src/components/checks/form/types/database.tsx:299):

- Add a **Mode** select (`amqp` / `management`) using the same `Select` pattern
  as the Prometheus module
  ([infra.tsx:797](web/dash0/src/components/checks/form/types/infra.tsx:797)),
  plus a **Management port** input (placeholder `15672`) shown in management
  mode. Add `mode`, `managementPort` and the four threshold keys to
  `ownedKeys` so clearing an input really deletes the key (see the
  passthrough rules at
  [common.ts:97](web/dash0/src/components/checks/form/types/common.ts:97)).
- In management mode show the four threshold inputs, two per resource, with
  help text stating the accepted forms ("80% or 1.5GiB" for memory, "10GiB"
  for disk). Hide the **Queue** input in management mode — the backend only
  inspects a queue over AMQP
  ([checker.go:166](server/internal/checkers/checkrabbitmq/checker.go:166)).
- Locale keys for the new labels/help text in **every** locale under
  `web/dash0/src/locales/` (de, en, es, fr), and a dash0 unit test for the
  module's `fromConfig` / `toConfig` round-trip.
- Check the design reference page first and reuse its primitives; add a
  data-testid per new input.

### Samples, docs, changelog

- Add a management-mode sample with thresholds to
  [samples.go:10](server/internal/checkers/checkrabbitmq/samples.go:10).
- Update the `rabbitmq` table in `wiki/conventions/checker-config.md` (it also
  claims `timeout` max is 60s while the code caps at 30s — fix that while
  there) and rewrite the RabbitMQ section of
  [check-types.md:755](web/docs/docs/features/check-types.md:755), which still
  documents a URL-string config the check has not used since it moved to
  host/port. Mention the two modes, that thresholds need management mode, and
  why disk has no percentage form.
- CHANGELOG entry per `wiki/conventions/changelog.md`.

### Tests

- Backend, `httptest.Server` faking `/api/health/checks/alarms` and
  `/api/nodes` (no live broker, deterministic): under threshold → Up with all
  metrics present; warning breach → Warning; critical breach → Down with the
  human-readable error; three-node cluster with one bad node → that node's
  values in metrics and the worst status; `/api/nodes` 403 without thresholds
  → Up plus `nodes_error`; with thresholds → Down; `mem_limit: 0` with a
  percent threshold → no panic. Config table tests: percent vs size parsing,
  invalid strings, `%` rejected on disk keys, tier ordering per unit kind,
  thresholds rejected outside management mode. Include a positive control
  proving a breach really flips the status, not just that fields parse.
- Playwright: create a management-mode check with thresholds through the
  form, reopen it, assert every value round-trips (model on
  `web/dash0/e2e/check-domain-thresholds-and-long-period.spec.ts`).

### Noticed, not in scope

Management mode dials with `http.DefaultClient`
([checker.go:257](server/internal/checkers/checkrabbitmq/checker.go:257)) and
ignores the tunnel dialer that AMQP mode honours, even though the type is
declared `SupportsTunnel: true`
([checkerdef/types.go:371](server/internal/checkers/checkerdef/types.go:371)).
A tunneled management-mode check goes direct today. Worth its own spec.
