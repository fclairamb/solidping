---
model: sonnet
effort: medium
---

# An abandoned `status: created` result is never reaped and drives a nonsensical "Last checked" reading

## Problem

A raw result row is inserted with `status = 1` (`created`) at claim time and
updated to a terminal status (`up`/`down`/`timeout`/`error`) when the execution
finishes. If the process that claimed the job dies — a devloop restart mid-check,
a crashed worker, a lease that expires while the row is already written —
**nothing ever finalizes that row**. It stays `created` forever.

Observed on the local dev instance while investigating spec 2026-08-18-02: the
FTP check `ftp-test-rebex` carried a `created` raw row from **2026-07-28**,
three weeks stale, alongside perfectly healthy newer rows:

```
raw | 2026-07-28 07:44:41.928584+00 | created
```

That row is not inert. The check detail page derives "Last checked" from the
newest raw row regardless of status, so while the check was stalled the header
read **"Last checked 20d ago"** at the same time as **"Currently up for 2d 10h"**
and **"Pending first run…"** — three mutually contradictory statements on one
screen, all technically derived from the data.

Once check execution resumed the reading corrected itself, because a newer row
took over. The stale row is still there, and will surface again the moment the
check goes quiet — which is exactly when an operator is looking at the page.

## Proposal

Two parts, in order of importance:

1. **Reap abandoned `created` rows.** A raw result that is still `created` well
   past any plausible execution window (the check's period plus the worker
   lease timeout, with a generous multiplier) is not "in flight", it is
   orphaned. Decide — and the decision is the substance of this spec — whether
   it should be:
   - **finalized** as `error` with an output explaining the worker never
     reported (keeps the timeline honest: something *was* attempted and we do
     not know the outcome), or
   - **deleted** (keeps the availability maths clean: an attempt nobody
     observed is not evidence of anything).

   Lean towards finalizing as `error` unless it distorts availability
   percentages for checks whose worker restarts are routine. Whatever is
   chosen, it must be consistent with how an expired *lease* is already handled
   (`check_jobs.lease_expires_at` / `lease_starts`) — look there first, because
   the reaper may belong in that same sweep rather than in a new job.

2. **Make the read path defensive anyway.** Even with a reaper, "Last checked"
   should not be computed from a non-terminal row: a `created` row means "an
   attempt started", not "the check was checked". Whether that is fixed in the
   API (`lastResult`-style projections) or in the dash0 header is part of the
   investigation.

## Notes

- Split out of spec 2026-08-18-02 (`A rewritten migration 013 is silently
  skipped…`) by explicit decision: the reaper is unrelated machinery to the
  migration fix and deserves its own review.
- Reproduction is cheap: claim a job, kill the process before it writes the
  terminal status, then look at the check detail header.

## Open questions

- Finalize as `error` or delete? See above — this is the decision the spec
  exists to make.
- Does the existing lease-expiry sweep already have the information needed to
  reap the matching result row, making this a few lines inside it rather than a
  new job?
- Should a reaped attempt count against availability at all? If it is finalized
  as `error` it will, via `successful_checks` / `total_checks` in the hour
  rollup.

## Resolved open questions

> Finalize as `error` or delete? See above — this is the decision the spec
> exists to make.
>
> Should a reaped attempt count against availability at all? If it is finalized
> as `error` it will, via `successful_checks` / `total_checks` in the hour
> rollup.

**Decision: finalize as `error`, and EXCLUDE it from availability.** Both halves
are binding; implementing one without the other is not the decision.

Finalize rather than delete, so the timeline keeps the evidence that an attempt
happened — silently deleting the row erases the only symptom of a crashing or
restarting worker, which makes that class of bug much harder to diagnose. The
reaped row's output must say plainly that the worker never reported a result.

Exclude it from the availability maths, because an abandoned row means **our**
worker died, not that the monitored service was down. Counting it would
manufacture downtime for the customer out of our own infrastructure failure —
for a monitoring product that is the worst possible failure mode, and it would
make every devloop restart show up as a dip in someone's uptime percentage. So
the hour rollup must be able to tell a reaped attempt apart from a genuine
`error` result and leave it out of both `successful_checks` and `total_checks`.

This is the part of the work that carries real design weight: decide how the
distinction is represented (a dedicated status, a flag column, or a reserved
output marker) and make sure the rollup, the uptimebar union, and the
availability service all agree on it. Whatever is chosen, add a test proving a
reaped attempt does **not** move the availability percentage, with a positive
control showing a real `error` result in the same window does.

> Does the existing lease-expiry sweep already have the information needed to
> reap the matching result row, making this a few lines inside it rather than a
> new job?

**Not a question for the user — resolve it from the code.** Read the
`check_jobs.lease_expires_at` / `lease_starts` sweep first, as the spec's own
Proposal instructs. If that sweep can already identify the orphaned result row,
put the reaping there rather than adding a second job; if it cannot, a separate
sweep is fine. State which you found and why in the implementation.
