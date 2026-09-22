---
model: opus
effort: high
---

# A check that fails intermittently, or answers ten times slower than usual, is reported nowhere

## Problem

A production HTTP check, 1-minute period, single region, 113 probes. Measured
from `results` in production:

| | |
|---|---|
| Probes | 113 |
| Failures (`down` / `timeout`) | 7 |
| Availability | 93.81% |
| Baseline p95 before the episode | 453 ms |
| Peak duration | 11.1 s, on a probe that **succeeded** |
| Incidents opened | 0 |
| `flap_count` afterwards | 0 |

The episode runs 14:35 to 15:28. It opens with three consecutive slow-but-
successful probes (2565 ms, 1362 ms, 5534 ms) at 14:35-14:37. Failures land at
14:42, 14:55, 14:58, 15:09, 15:10, 15:18, 15:22 and stop. SolidPing reported
nothing: no incident, no notification, no status-page entry, no history line.

Two things are invisible there, and they need different detectors.

### Isolated failures never reach the confirmation period

The check runs `confirmation_period_seconds = 120` against a 60 s period. Every
failure is followed by a success before that elapses, so `pickStatus`
([service.go:901](server/internal/handlers/incidents/service.go:901)) goes
`validating` then `up`, and no incident opens.

The confirmation period is doing its job (suppressing a single blip). Lowering
it is the wrong fix: it turns one silent episode into seven separate pages.
What is missing is a second, statistical detector beside it.

The existing flapping layer cannot fill that role. `bumpFlap`
([service.go:1074](server/internal/handlers/incidents/service.go:1074))
increments at **incident onset** only; zero incidents means `flap_count` stayed
at 0 (verified on the row). It counts outages. We need to count failed probes.

### Latency is not a signal

Nothing user-facing reads response time. `SlowThresholdMs` in
[scheduling.go:78](server/internal/checkworker/scheduling/scheduling.go:78)
is lane placement. The HTTP checker has no latency assertion. A target
answering every probe in 11 s is 100% available and unusable.

### What exists and does not close the gap

- `CheckStatusWarning` ([check.go:34](server/internal/db/models/check.go:34))
  is per-probe (TLS expiry, domain expiry), never statistical.
- SLO burn-rate alerting (spec 2026-08-21-08) is the right math but requires an
  SLO object plus a policy that ships disabled. Nobody does that for an
  ordinary check.

## Proposal

### One rule primitive

    fires when: M of the last N countable probes match

applied to two populations on the same check:

| Population | Match | Default M of N |
|---|---|---|
| failures | `status NOT IN (up, warning)` | 5 of 60 |
| slow | `status IN (up, warning) AND duration > slow_threshold_ms` | 3 of 6 |

Both are evaluated by one periodic sweep modelled on
[job_slo_burn_eval.go](server/internal/jobs/jobtypes/job_slo_burn_eval.go):
same batch-on-an-interval shape, same bounded batch size. "Slow" is computed
in the evaluator's query from `results.duration` (milliseconds,
[worker.go:1435](server/internal/checkworker/worker.go:1435)). **The worker and
the result status are not touched.** The chart already draws an 11 s probe at
11 s; recoloring it adds nothing, and leaving `ResultStatus` alone keeps
`PageStatus` and `ComputeGroupStatus` out of this change entirely.

Rules of the primitive:

- **Countable** = `status IN (3,4,5,6,8)`, not `maintenance`. Abandoned and
  maintenance rows are skipped, not counted as a slot.
- **Max age**: a probe older than `2 x N x period` does not count. "The last 6
  probes" of a check that was paused for two days must not include Tuesday.
- **Suppressed while a `kind = "check"` incident is open** on the check. This
  one rule is what makes the feature shippable (see calibration: 114/24h
  without it, 25 with it). Do not weaken it.
- **Resolution**: the condition has been false for N consecutive countable
  probes. When both populations fired, the larger N governs.
- **Multi-region** is one stream: the incident state machine is per check
  ([service.go:643](server/internal/handlers/incidents/service.go:643)), so a
  3-region check with one dead region has 1 failure in every 3 probes and
  trips 5-of-60 on its own. Consistent with how outages behave today; per-region
  degradation is a non-goal.

Why probes and not seconds: fixed denominator, so no `min_samples` floor; the
meaning survives a period change; and alert latency scales with the sampling
the operator already chose. The trade is that a 5-minute-period check gets a
5 h failure window. Accepted.

Why 5-of-60 and 3-of-6 and not one window: three slow probes in an hour is
nothing; three in a row is the leading edge of the motivating episode (14:37,
five minutes before the first failure, 33 minutes before any failure rule).

### Modelling: a third incident kind

`incidents.kind` already discriminates and `slo_burn` is the precedent
([incident.go:28-41](server/internal/db/models/incident.go:28)). Add
`IncidentKindDegraded = "degraded"`. It inherits history, timeline, escalation,
notifications, status pages, MCP, ack/snooze for free.

**No in-place promotion.** When a `check` incident opens on a check with an
open degraded incident, resolve the degraded one with
`resolution_type = "escalated"` and set the outage's `caused_by_incident_uid`
to it (the column exists). When the outage resolves, the evaluator simply runs
again; if the check is still degraded a new degraded incident opens. No kind
mutation, nothing keyed on kind has to cope with a change mid-life.

**No live `CheckStatusDegraded` in v1.** The open incident and the chart band
carry the information. Promoting the status would touch `pickStatus`, three
rollup functions, the `?status=` filter, status pages and six locales for a dot
color. Follow-up if wanted.

**Audit every `Kinds` filter site** ([incident.go:181](server/internal/db/models/incident.go:181)).
A degraded incident leaking into an availability, SLO or uptime-report
denominator is the most likely regression in this spec.

### Configuration

Per check, code defaults, no org-level layer:

| Column | Default | Meaning |
|---|---|---|
| `degraded_failures` | 5 | M for failures; 0 disables |
| `degraded_failures_window` | 60 | N for failures |
| `degraded_slow` | 3 | M for slow; 0 disables |
| `degraded_slow_window` | 6 | N for slow |
| `slow_threshold_ms` | 0 | 0 = slow rule off; form suggests ~2x observed p95 |
| `degraded_enabled` | false on existing rows, true on new | opens incidents; see rollout |
| `degraded_would_fire_at` | null | stamped by the dry run, see below |

No `default:` clause on any of these bun tags. This codebase has been bitten
twice (`StatusPage.AutoPublishDelaySeconds`, `flappingWindowSeconds: 0`, spec
2026-08-30-04): with a tag default, `degraded_failures: 0` never reaches the
database and the rule cannot be turned off at creation.

No auto-baselined thresholds. The form suggests a value from `DurationP95`
([result.go:251](server/internal/db/models/result.go:251)) and the operator
commits to it. On the motivating check, 1000 ms detects at 14:37 and 1400 ms at
14:47.

### Rollout

Off for existing checks, on for new ones, per the rule already written at
[slo_alert_policy.go:64](server/internal/db/models/slo_alert_policy.go:64):
upgrading must never start paging on its own.

Adoption comes from a **dry run**: the evaluator runs for every check. When
`degraded_enabled` is false it opens nothing and only stamps
`degraded_would_fire_at`. The check page shows a banner ("this check would have
been flagged degraded at 14:37; enable?") and the checks list gets a
`wouldHaveFired` filter. One evaluator, one code path, no review page.

The look-back is 24 h, because that is the `raw` retention in production
(measured: ~24 h raw, 5 days hour, 5 weeks day). The rules need the per-probe
sequence, which only `raw` carries. Do not design a longer look-back.

### Reporting (v1)

- **Notification** wording must not resemble an outage or people mute both:
  "acme.com is degraded: 7 failures in the last 60 probes (93.8%). Currently
  up." with a deep link into the window (`graphFrom` / `graphTo` exist).
- **Chart**: shade the degraded span. Isolated red dots do not read as an
  event; a band does.
- **Status pages**: do not auto-publish degraded incidents. Per-page opt-in.

Follow-ups, not in scope: reliability line on the check card, amber filter in
the incident list, "worst intermittent" in the uptime report.

### Fleet calibration

Replayed against 24 h of production `raw` results: 159 enabled non-internal
checks with 30+ probes, 21 organizations, suppression applied. At the fleet's
dominant 1-minute period, "5 within 1 h" is "5 of 60".

| Rule | Degraded incidents / 24 h |
|---|---|
| failures 5 of 60 | 40 |
| failures 10 of 60 | 6 |
| slow 3 of 6 at 2x p95 | 23 |
| (for reference) failures 3 of 6, no suppression | 114 |
| (for reference) failures 3 of 6, suppressed | 25 |

49 of the 159 checks had at least one failure and zero incidents in that day.
That is the size of the problem.

Two things to decide with eyes open:

- **`5 of 60` is the noisiest rule and the only failure rule that catches the
  motivating episode** (7 failures in 40 minutes). `10 of 60` drops to 6/day
  and misses it. The spec ships 5. It is the number most likely to be retuned
  once the dry run shows operators what it produces.
- **The slow rule finds things nothing else reports**: 9 of the 11 checks it
  fired on had no real incident that day. It is the priority, not the add-on.

Upper bound with defaults: ~63 incidents/day fleet-wide before overlap. Enough
that default-on for existing checks would be a bad day; the rollout rule
already forbids it.

### Testing

- Table-driven evaluator tests: window edge, max age, maintenance skipped as a
  non-slot, resolution after N clean probes, suppression under an open outage.
- **Negative control**: replay the Problem shape (7 failures over 40 min, each
  followed by a success inside the confirmation period). A degraded incident
  opens; zero `check` incidents do.
- **Positive control**: a continuous outage opens a `check` incident and no
  degraded one.
- Slow rule fires at the 14:35-14:37 triple while the failure rule is still
  silent; the failure rule fires later with the slow rule unchanged.
- Escalation: degraded open, outage opens, degraded resolves as `escalated`,
  outage carries `caused_by_incident_uid`; outage resolves, evaluator reopens
  degraded.
- Dry run: `degraded_enabled = false` stamps `degraded_would_fire_at` and
  creates no incident row.
- Availability regression: degraded incidents enter no availability, SLO or
  uptime-report denominator.

### Non-goals

- Auto-baselined or anomaly-detected latency thresholds.
- Per-region degradation.
- Live `CheckStatusDegraded`, org-level defaults, 30-day look-back.
- Touching SLO burn-rate alerting.

### Open questions

1. Does a degraded incident participate in dependency cascade (spec
   2026-05-03-57)? Leaning no: a degraded ancestor suppressing a descendant's
   real outage is a bad trade.
2. Same escalation policy as the check's outages, or a quieter default?

## Resolved open questions

1. **Does a degraded incident participate in dependency cascade (spec
   2026-05-03-57)?** No. Degraded incidents do not participate in cascade
   suppression at all — a degraded ancestor must never suppress a
   descendant's real outage.
2. **Same escalation policy as the check's outages, or a quieter default?**
   Quieter default. The spec's own "Reporting" section requires degraded
   notification wording to read differently from an outage ("must not
   resemble an outage or people mute both") — reusing the outage escalation
   policy (which may page on-call) undercuts that. Default to notify-only,
   no paging escalation, for degraded incidents.
