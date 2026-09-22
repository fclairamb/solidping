# Degraded detection

Spec: `specs/.../2026-09-22-03-degraded-detection-intermittent-failures-and-latency.md`

A production HTTP check, 1-minute period, 113 probes: 7 failures, 93.81%
availability, one successful probe that took 11.1 s against a 453 ms p95 — and
**zero** incidents, zero notifications, zero history lines. Two different blind
spots produced that:

- **Isolated failures never reach the confirmation period.** With
  `confirmation_period_seconds = 120` against a 60 s period, every failure was
  followed by a success first, so `pickStatus` went `validating` → `up`. The
  confirmation period was doing its job; lowering it turns one silent episode
  into seven separate pages. The flapping layer cannot fill the gap either —
  `bumpFlap` increments at incident ONSET, and there were no incidents.
- **Latency was not a signal at all.** `SlowThresholdMs` in
  `checkworker/scheduling` is lane placement; the HTTP checker has no latency
  assertion. A target answering every probe in 11 s is 100% available and
  unusable.

## The rule primitive

One primitive — `internal/degraded/rule.go`, pure and table-driven-testable:

    fires when M of the last N countable probes match

applied to two populations on the same check:

| Population | Match | Default M of N |
|---|---|---|
| failures | `status NOT IN (up, warning)` | 5 of 60 |
| slow | `status IN (up, warning) AND duration > slow_threshold_ms` | 3 of 6 |

Rules of the primitive:

- **Countable** = status in (up, down, timeout, error, warning). A lifecycle
  marker, an abandoned row and a **maintenance-tagged probe are skipped, not
  counted as a slot** — planned work must neither trip a rule nor push a real
  failure out of the window.
- **Max age**: a probe older than `2 × N × period` does not count. "The last 6
  probes" of a check paused for two days must not include Tuesday.
- **Suppressed while a `kind = "check"` incident is open.** This one rule is what
  makes the feature shippable — measured on 24 h of production `raw` results:
  114 degraded incidents/day without it, 25 with it. Do not weaken it.
- **Resolution**: the condition has been false for N consecutive countable
  probes. When both populations fired, the larger N governs, and the governing N
  is recorded on the incident at open (`degraded_resolve_window`) so a config
  edit mid-episode cannot silently change the exit condition.
- **Multi-region is one stream.** The incident state machine is per check, so a
  3-region check with one dead region has 1 failure in every 3 probes and trips
  5-of-60 on its own. Per-region degradation is a non-goal.

Probes, not seconds: fixed denominator (no `min_samples` floor), the meaning
survives a period change, and alert latency scales with the sampling the operator
already chose. The trade — a 5-minute-period check gets a 5 h failure window — is
accepted.

## The evaluator

`internal/handlers/degradedeval` is a periodic sweep shaped exactly like
`handlers/sloalerts`: bounded batch (500), checks read **oldest-evaluated first**
(`checks.degraded_evaluated_at`) so a large install never starves its tail,
per-check failures logged and skipped. Driven by the self-rescheduling
`degraded_eval` job (`internal/jobs/jobtypes/job_degraded_eval.go`), once a
minute, seeded by the startup job and reached through
`services.DegradedEvaluator` (an interface, to break the
jobtypes → handlers/incidents import cycle).

The look-back is **24 h**, because that is the measured `raw` retention in
production and the rules need the per-probe sequence only `raw` carries. Do not
extend it.

**The worker, the result status and the check status are not touched.** The chart
already draws an 11 s probe at 11 s; leaving `ResultStatus` alone keeps
`PageStatus` and `ComputeGroupStatus` out of this entirely, and there is no live
`CheckStatusDegraded` in v1.

## The third incident kind

`IncidentKindDegraded = "degraded"` joins `check` and `slo_burn`, so history,
timeline, notifications, status pages, MCP and ack/snooze come for free. Three
deliberate differences from a check outage:

1. **No dependency cascade, in either direction.** A degraded ancestor must never
   suppress a descendant's real outage.
   `FindActiveIncidentsForChecksInWindow` already filters `kind = 'check'`, and
   `OpenDegradedIncident` never calls `applyRollup`.
2. **No escalation policy** — notify-only. The wording requirement ("must not
   resemble an outage or people mute both") is undone the moment the same
   incident wakes on-call, so `queueLifecycleNotifications` skips the escalation
   branch for this kind. Channel fan-out still runs.
3. **No auto-publish** to a status page unless that page set
   `status_pages.publish_degraded` (false everywhere, including new pages).

**No in-place promotion.** When a real outage opens on a degraded check, the
degraded incident resolves with `resolution_type = "escalated"` and the outage's
`caused_by_incident_uid` points back at it — provenance, not suppression
(`paging_suppressed` stays false, and the pointer is only written when rollup has
not already claimed that column for a real parent). Nothing keyed on `kind` ever
has to cope with a kind changing mid-life. When the outage resolves, the
evaluator simply runs again.

## Configuration

Per check, code defaults, no org-level layer:

| Column | Default | Meaning |
|---|---|---|
| `degraded_failures` | 5 | M for failures; 0 disables |
| `degraded_failures_window` | 60 | N for failures |
| `degraded_slow` | 3 | M for slow |
| `degraded_slow_window` | 6 | N for slow |
| `slow_threshold_ms` | 0 | 0 = slow rule off; the form suggests ~2× the observed p95 |
| `degraded_enabled` | false on existing rows, true on new | opens incidents |
| `degraded_would_fire_at` | null | stamped by the dry run |
| `degraded_evaluated_at` | null | evaluator rotation state, not configuration |

**None of these carries a bun `default:` clause**, even though every column has
one. With `default:5` on the tag, `degraded_failures: 0` never reaches the
database and the rule cannot be turned off at creation (the
`StatusPage.AutoPublishDelaySeconds` / `flappingWindowSeconds: 0` trap, spec
2026-08-30-04). `models.NewCheck` supplies the defaults instead, and
`internal/db/models/default_tag_guard_test.go` enforces it.

No auto-baselined thresholds: the operator commits to a number. On the motivating
check, 1000 ms detects at 14:37 and 1400 ms at 14:47.

All six knobs are editable in the check form's own "Degraded detection" section
(`web/dash0/src/components/shared/check-form.tsx`). The threshold field shows a
suggestion of about 2x the check's observed p95 — read from the hour rollups'
`DurationP95` via `lib/slow-threshold-suggestion.ts`, which needs at least three
samples and otherwise suggests nothing — behind a one-click "use suggestion". It
is never auto-applied: auto-baselining is the non-goal, and the click IS the
operator committing. `buildDegradedPayload` is the form's 0-vs-blank guard: a
typed 0 reaches the server (that is how a rule is turned off), a blank field is
omitted so the code default stands.

The form's payload reaches the API through `lib/check-request.ts`. That module is
here because of this feature: both check routes used to hand-pick which keys of
the payload to forward, so the six fields rendered, saved without error and never
reached the database — the same way `confirmationPeriodSeconds` was lost in spec
2026-07-15-04, under a comment warning about precisely that. It is now a
DENY-list (channel bindings, dependency edges and the diff baseline, all of which
have their own endpoints), so a new form field reaches the server by default and
dropping one has to be deliberate. `lib/check-request.test.ts` asserts the
request BODY, not the form's internal state — that distinction is the whole bug.

## Rollout: the dry run

Off for existing checks, on for new ones — upgrading must never start notifying
on its own. Adoption comes from a dry run: the evaluator runs for **every** check
and, when `degraded_enabled` is false, opens nothing and only stamps
`degraded_would_fire_at` (earliest wins — the banner is past tense). One
evaluator, one code path, no review page.

- The check page shows a banner ("this check would have been flagged degraded at
  …; enable?") with a deep link into the window —
  `components/checks/degraded-dry-run-banner.tsx`.
- The checks list gets `?wouldHaveFired=true`.
- Enabling the feature retires the stamp, so "would have fired" and "is allowed
  to fire" can never both look true.

## Reporting

- **Notification wording** must not resemble an outage: "acme.com is degraded: 7
  failures in the last 60 probes (93.8%). Currently up." plus a `graphFrom` /
  `graphTo` deep link into the window. `internal/notifications/degraded.go`,
  wired into the Slack sender (amber, not red) and two dedicated email templates.
- **Chart**: an amber `ReferenceArea` over the episode's span. Isolated red dots
  do not read as an event; a band does.
- **Status pages**: per-page opt-in, never automatic.

## Fleet calibration (24 h of production `raw`, 159 checks, 21 orgs)

| Rule | Degraded incidents / 24 h |
|---|---|
| failures 5 of 60 | 40 |
| failures 10 of 60 | 6 |
| slow 3 of 6 at 2× p95 | 23 |
| (reference) failures 3 of 6, no suppression | 114 |
| (reference) failures 3 of 6, suppressed | 25 |

49 of the 159 checks had at least one failure and zero incidents that day — that
is the size of the problem. `5 of 60` is the noisiest failure rule and the only
one that catches the motivating episode, so it ships; it is the number most likely
to be retuned once the dry run shows operators what it produces. The slow rule is
the priority, not the add-on: 9 of the 11 checks it fired on had no real incident
that day.

## The regression to watch

**A degraded incident leaking into an availability, SLO or uptime-report
denominator.** Every filter that derives downtime from incidents must pass
`Kinds: []string{models.IncidentKindCheck}`:

- `internal/uptimereport/report.go`
- `internal/handlers/availability/service.go`
- `internal/handlers/slos/service.go`
- `internal/handlers/incidentpublications/group.go`

`TestEveryDowntimeFilterRestrictsKind` parses those four files and fails if a
`ListIncidentsFilter` literal in any of them omits `Kinds` — a source guard,
because the real risk is a NEW call site added by somebody who never read this
page.

## Non-goals

Auto-baselined or anomaly-detected latency thresholds; per-region degradation;
live `CheckStatusDegraded`; org-level defaults; a 30-day look-back; touching SLO
burn-rate alerting.
