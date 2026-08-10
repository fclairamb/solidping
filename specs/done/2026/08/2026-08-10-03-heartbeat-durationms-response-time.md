---
model: sonnet
effort: medium
---

# Heartbeat pings cannot report a run duration — the response-time chart is stuck at 0

## Problem

Heartbeat results always persist `duration = 0`: the ingest service hardcodes it
(`server/internal/handlers/heartbeat/service.go:179` — `durationMs := float32(0)`).
The dashboard's response-time chart plots exactly that column
(`web/dash0/src/components/checks/response-time-chart.tsx`, `durationMs` series),
so every heartbeat check shows a flat zero line forever.

Since the structured-body feature, a ping's JSON body is accepted and stored —
`message` becomes the display message, every other key lands under
`output.data` (`server/internal/handlers/heartbeat/handler.go:63`,
`decodeHeartbeatBody`). But that is display/forensics data only; nothing feeds
the `duration` column. Verified live on solidping.k8xp.com (v0.11.0): a POST
with `"durationSec": 42` in the body persists under `output.data` while the
result's duration stays 0.

Cron/CI jobs know exactly how long their run took, and that number is a real
monitoring signal — a nightly backup that suddenly takes 3× longer is a
degradation you want to see on a chart before it becomes a timeout.

## Proposal

Recognize a top-level `durationMs` key in the heartbeat JSON body and store it
as the result's duration, giving passive checks a real response-time chart.

### Ingest (backend)

- In `decodeHeartbeatBody` (`server/internal/handlers/heartbeat/handler.go`),
  give `durationMs` the same consume-and-delete treatment as `message`: when it
  is a JSON number that is finite, ≥ 0, and ≤ 604 800 000 (7 days in ms — sanity
  cap, also keeps the float32 conversion safe), extract it as the ping's
  duration in milliseconds and delete it from the body so it is not duplicated
  under `output.data`.
- Invalid values (non-numeric, negative, NaN/Inf, over the cap) are ignored for
  duration purposes and left in `output.data` as ordinary caller data — the
  caller can then see what they actually sent when debugging an empty chart.
  A bad `durationMs` must never fail the ping, consistent with the existing
  lenient body handling.
- Thread the value through `Service.ReceiveHeartbeat`
  (`server/internal/handlers/heartbeat/service.go`) and persist it as the
  result's `Duration` instead of the hardcoded 0. The parameter list is already
  long (10 params); folding the per-ping fields into a small struct is a
  welcome cleanup but implementer's choice.
- Applies to every accepted status, including `running` (unusual to send one on
  a run-start ping, but if present it is stored — no special casing).
- Everything else about the body contract is unchanged: 8 KiB cap
  (`maxHeartbeatBodyBytes`), malformed-JSON leniency, `data` nesting,
  `Content-Type: application/json` requirement.

### Chart (frontend)

- No new plumbing expected: `response-time-chart.tsx` already plots
  `durationMs` from raw results, and aggregation already rolls `duration` into
  min/max/p95 buckets (`server/internal/jobs/jobtypes/job_aggregation.go`), so
  rolled-up heartbeat rows chart for free.
- **Verify** the check-detail response-time chart is not gated off for
  heartbeat/passive check types; if it is hidden for heartbeats, un-hide it so
  reported durations actually render.

### Docs

- Update the **Structured body** section of
  `web/docs/docs/features/check-types.md` (~line 755) to document `durationMs`
  next to `message`, with a one-line curl example (e.g. a backup job reporting
  `{"message": "backup done", "durationMs": 42000}`).

### Tests

- Handler: valid `durationMs` is consumed (present as the stored duration,
  absent from `output.data`); string/negative/NaN/over-cap values are ignored
  for duration and left in `output.data`; boundary at the cap; body without the
  key keeps duration 0.
- Service: the threaded value is persisted on the result row; default remains 0
  when absent.

### Out of scope

- A `?durationMs=` query parameter for GET-only callers — body-only keeps one
  canonical path; can be added later if asked for.
- Documenting the heartbeat ingest endpoint in `openapi.yaml` (it is not there
  today; only token rotation is).
- Feeding caller data into `metrics` for suffix-convention aggregation — this
  spec only wires the single well-known duration field.
