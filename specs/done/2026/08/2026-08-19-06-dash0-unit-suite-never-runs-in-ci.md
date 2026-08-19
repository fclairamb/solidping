---
model: sonnet
effort: medium
---

# dash0's 411-test unit suite runs in no pipeline, so a red test sat on `main` unnoticed

## Problem

`web/dash0/src/components/dashboard/event-display.test.ts` fails on `main`
(`99bfafdbe`) and has done since the incident-comment fanout landed:

```
FAIL  EVENT_TYPE_REGISTRY pins the binding emoji per event type
      > INTENTIONALLY_UNMAPPED and EVENT_TYPE_REGISTRY never overlap
FAIL  EVENT_TYPE_REGISTRY pins the binding emoji per event type
      > has no registry entries outside the binding list above
```

### Root cause of the failure itself (investigated, resolved)

The duplicated event type is **`incident.comment`**. Two commits disagreed:

- **#224** (`54b97bd04`) added it to `INTENTIONALLY_UNMAPPED` in the test with
  the reason *"rendered by its own comment UI, not the event badge"* — correct
  at the time; [incidents.$incidentUid.tsx:1043](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1043)
  filters comments out of the timeline.
- **#226** (`a5ee7c439`), the incident-comment notification fanout, then added
  a real registry entry at [event-display.tsx:42](web/dash0/src/components/dashboard/event-display.tsx:42):
  `{ emoji: "💬", tone: TONE_BLUE }`.

Neither side reconciled with the other, and both are on `main` today.

**The registry is the correct side.** The backend declares the pairing as a
product-wide contract at [comment.go:5](server/internal/notifications/comment.go:5):

```go
// commentEmoji is the product-wide identity of `incident.comment`, shared with
// the dash0 event registry (web/dash0/src/components/dashboard/event-display.tsx).
// One emoji per event type product-wide — change both sides together.
const commentEmoji = "💬"
```

That 💬 ships through [msteamsbot.go:135](server/internal/notifications/msteamsbot.go:135),
`commentTitle()` for every card-style sender, and
[telegram/incidentview.go:453](server/internal/integrations/telegram/incidentview.go:453).
Comment events also genuinely reach a badge surface: the dashboard Recent
Activity feed calls `getEventIcon(event.eventType)` at
[dashboard-page.tsx:1080](web/dash0/src/components/dashboard/dashboard-page.tsx:1080)
without filtering comments out. So `incident.comment` is a binding pair like the
other eight, and the **test** was the stale side.

A fix is already applied in the working tree of `batch/2026-08-17` (uncommitted):
drop the stale `INTENTIONALLY_UNMAPPED` row, add `["incident.comment", "💬"]` to
`BINDING_PAIRS`, and point the header comment at `comment.go` as the backend half
of the pairing. Verified: 58/58 in the file, 411/411 across the suite, `tsc` and
`eslint` clean on the touched file, and mutation-checked — deleting the registry
entry fails 3 tests including the "every backend event type is accounted for"
guard, so moving the entry left no silent hole.

### The actual open problem

**Nothing runs `bun run test:unit` for dash0.** Not CI, not the Makefile:

- [ci.yml:120-139](.github/workflows/ci.yml:120) — the **Dash0 Build** job is
  `bun install` then `bun run build`. No lint step, no test step.
- By contrast **Status0 Build** ([ci.yml:182](.github/workflows/ci.yml:182)) does
  run `bun test ./src`, and **Dash Build** at least runs `bunx eslint .`.
- `make test-dash` ([Makefile:319](Makefile:319)) runs `cd $(DASH_DIR) && bun test`
  — that is the **legacy `web/dash`**, not `web/dash0`.

So dash0 — the primary operator UI, with 411 tests across 24 files — is the only
frontend app whose test suite is gated by nothing. That is why a red test lived on
`main` for days and surfaced only when someone ran it by hand. The overlap bug is
a one-line fix; the missing gate is what let it ship.

## Proposal

1. **Land the test fix.** The three-line diff to
   `web/dash0/src/components/dashboard/event-display.test.ts` is already in the
   working tree on `batch/2026-08-17`. It is unrelated to that branch's SMTP work,
   so it is a candidate for its own `fix/` branch — confirm with the user which
   they want before committing.

2. **Add a unit-test step to the Dash0 Build job**, mirroring Status0's, after
   `Install dependencies` at [ci.yml:136](.github/workflows/ci.yml:136):

   ```yaml
   - name: Run unit tests
     run: bun run test:unit
   ```

   Note dash0 uses the `test:unit` script (vitest); `bun test` is not equivalent
   here and will not pick the suite up correctly.

3. **Do *not* add `bun run lint` to the dash0 job in the same change.** `eslint .`
   is red on base for dash0 (~25 pre-existing `react-hooks` errors), so adding it
   turns CI red on unrelated debt. That cleanup is its own spec — file it
   separately rather than bundling it here and getting stuck.

4. **Consider a `make test-dash0` target** alongside `test-dash`, so the suite has
   a local entry point that matches CI. Optional, but `test-dash` pointing at the
   deprecated app is its own small trap.

## Open questions

- Should the dash0 unit suite gate merges (a required check), or just run
  informationally at first? Given it is green today apart from the fix in step 1,
  making it required immediately seems safe — worth confirming.

## Resolved open questions

Answered by Florent (2026-08-19). These are directives — implement exactly this.

**Q: Should the dash0 unit suite gate merges (a required check), or just run
informationally at first? Given it is green today apart from the fix in step 1, making
it required immediately seems safe — worth confirming.**

**Decision: required immediately.** Add `bun run test:unit` to the Dash0 Build job as an
ordinary step, so a failure fails the job and blocks the merge from day one. Do **not**
add `continue-on-error`, and do not stage it as an advisory check first. The whole reason
this spec exists is that a red test sat on `main` unnoticed — an advisory check nobody
watches would reproduce exactly that failure mode.

**Q (from Proposal step 1): the test fix "is a candidate for its own `fix/` branch —
confirm with the user which they want before committing."**

**Decision: already done, no action needed.** Florent had the fix committed directly to
`batch/2026-08-17` on 2026-08-19 as `test(dash0): pin incident.comment to its binding
emoji` (it drops the stale `INTENTIONALLY_UNMAPPED` row, adds `["incident.comment", "💬"]`
to `BINDING_PAIRS`, and points the header comment at `server/internal/notifications/comment.go`
as the backend half of the pairing). **Do not re-apply it, and do not create a `fix/`
branch.** Verify it is present and the suite is green, then move on to step 2 — the CI
step is the actual deliverable of this spec.
