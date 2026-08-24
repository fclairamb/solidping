---
model: sonnet
effort: medium
---

# The incident timeline shows UTC inline; it should show local time first, with UTC on hover

## Problem

On the incident detail page the timeline (and the comment / status-update
timestamps that use the same component) renders the **inline** `<TimeAgo>`
variant, whose absolute part is built by `formatInlineAbsolute`
([web/dash0/src/lib/time-ago.ts:57](web/dash0/src/lib/time-ago.ts:57)):

```ts
const time = `${date.toISOString().slice(11, 19)} UTC`;
return isToday ? time : `${date.toLocaleDateString()} ${time}`;
```

So the always-visible text reads `09:31:07 UTC · 46m ago`. That is the wrong
default for the primary reader of this page: an on-call operator comparing an
incident against their own wall clock, their calendar, and whatever their
colleagues are saying in chat. They have to do the offset arithmetic in their
head on every single row.

UTC still matters — it is the unambiguous form you paste into a log query or
share across timezones — but it belongs one step away (hover), not in the
front row.

Two secondary defects in the same function:

1. **The day check and the clock disagree on timezone.** `isToday` compares
   `date.toDateString()` (local calendar day) but the rendered clock is UTC.
   Near midnight this prints a bare UTC time that belongs to a *different*
   calendar day than the one the "today" check asserted — e.g. for a user at
   UTC+2 at 00:30 local, a timestamp of `2026-08-14T22:30:00Z` renders as
   `22:30:00 UTC` with no date, while it is in fact "yesterday" in UTC and
   "today" locally. Whatever timezone the display settles on, the day check
   must use the same one.
2. **`toLocaleDateString()` is locale-order-dependent** (`MM/DD/YYYY` vs
   `DD/MM/YYYY`), which the sibling `formatLocalDateTime` deliberately avoids
   by hand-building an ISO-ordered `YYYY-MM-DD`. The date prefix here should be
   consistent with it.

The hover tooltip (`formatTooltipText`,
[web/dash0/src/lib/time-ago.ts:68](web/dash0/src/lib/time-ago.ts:68)) already
shows `local (local) · UTC ISO`, and click-to-copy already writes
`formatUtcIso` — ISO 8601 UTC (`2026-08-14T09:31:07Z`). **The copy behaviour is
correct as-is and must stay UTC**; this spec's job there is to lock it in with a
test, not to change it.

## Proposal

1. **`formatInlineAbsolute` renders local time.** Change it to build the clock
   from the local getters (matching `formatLocalDateTime`'s padding), suffixed
   with a short local-zone marker so the reader can tell at a glance it is not
   UTC. Prefer the runtime's own short zone name via
   `Intl.DateTimeFormat(undefined, { timeZoneName: "short" })` (e.g.
   `11:31:07 CEST`), falling back to a UTC-offset label (`11:31:07 UTC+2`) if
   the short name is unavailable. Never render local time with no marker at
   all — an unlabeled clock next to a page that also talks in UTC is exactly
   the ambiguity this change is meant to remove.

2. **Fix the day check to match.** With a local clock, `isToday` compares local
   calendar days — which `date.toDateString() === now.toDateString()` already
   does correctly. Keep it, and make the timezone agreement explicit in a
   comment so the pair does not drift apart again.

3. **ISO-ordered date prefix.** When the timestamp is not today, prefix with
   the hand-built local `YYYY-MM-DD` (reuse/extract the date half of
   `formatLocalDateTime`) instead of `toLocaleDateString()`.

4. **Tooltip keeps UTC, and makes it prominent.** `formatTooltipText` already
   contains both; keep local first, UTC second. Since the inline text is now
   local, the tooltip's UTC ISO is the "on hover" UTC the user asked for — no
   behaviour change needed beyond confirming it renders for the `inline`
   variant too (it does; both variants wrap in the same `Tooltip`).

5. **Copy stays UTC.** No change to `formatUtcIso` or the `handleActivate`
   clipboard write in
   [web/dash0/src/components/ui/time-ago.tsx](web/dash0/src/components/ui/time-ago.tsx).
   Add/keep an explicit assertion that the clipboard payload matches
   `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$` even though the visible text is now
   local — that divergence is the whole point and deserves a regression test.

6. **Unit tests** (`web/dash0/src/lib/time-ago.test.ts`): the existing
   `formatInlineAbsolute` cases at
   [web/dash0/src/lib/time-ago.test.ts:62](web/dash0/src/lib/time-ago.test.ts:62)
   assert the UTC string and must be rewritten. Pin `process.env.TZ` (or use a
   fixed-offset zone via vitest config / `vi.stubEnv`) so the expectations are
   deterministic in CI, and cover:
   - same-local-day → clock + zone marker, no date prefix;
   - different local day → `YYYY-MM-DD HH:MM:SS ZONE`;
   - a timestamp whose **UTC** day differs from its **local** day (the
     midnight-boundary case from Problem #1) → asserts the date prefix decision
     follows the *local* day, i.e. a positive control that the old
     UTC-clock/local-day mismatch cannot come back;
   - `formatUtcIso` unchanged (guards the copy contract).

7. **E2E** (`web/dash0/e2e/incident-timestamps.spec.ts:60-73`): the assertion
   `await expect(timelineTime).toContainText(/UTC/)` now describes the *old*
   behaviour and will pass accidentally for any user whose browser sits in
   `UTC` — the Playwright default. Set an explicit non-UTC `timezoneId` for
   this test (e.g. `Europe/Paris`) so local and UTC genuinely differ, then
   assert:
   - the inline text does **not** read as the UTC clock (compare against the
     incident's known UTC timestamp, or assert the rendered zone marker is not
     `UTC`);
   - hovering reveals a tooltip containing the `…Z` UTC ISO;
   - clicking still copies the UTC ISO.

8. **Design reference.** Update the `TimeAgo` inline-variant entry at
   [web/dash0/src/routes/orgs/$org/design-reference.tsx:1930](web/dash0/src/routes/orgs/$org/design-reference.tsx:1930)
   — its surrounding copy describes the inline variant as showing UTC. It is
   the canonical catalog, so the description must match the new behaviour.

9. **Scope.** Only the `inline` variant's absolute text changes. The `tooltip`
   variant (dense lists: incidents index, jobs) already shows relative text with
   local+UTC on hover and is untouched. Maintenance-window recurrence patterns
   stay UTC-labelled — they are UTC-*defined* values
   (`web/dash0/src/lib/maintenance-window-schedule.ts:14`), not observations of
   a moment, and must keep reading back identically to what the form sets.

## Out of scope

- A per-user timezone preference. The browser's zone is the source of truth
  here; a stored preference is a separate, larger change.
- Status page (`status0`) timestamps.
