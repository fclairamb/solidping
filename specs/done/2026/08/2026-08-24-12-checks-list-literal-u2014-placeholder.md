---
model: sonnet
effort: low
---

# Checks list shows the literal text `—` instead of an em dash when a check has no duration

## Problem

On the org checks list, the "duration" column renders the literal seven characters
`—` instead of an em dash (—) whenever the check has no duration to show — i.e.
it has never been executed yet, or its latest result carries no `durationMs`
(observed on a DNSBL check that was `Up` but had not yet produced a timing).

The cause is a JSX text node containing a JavaScript escape sequence. JSX text is
not a JavaScript string literal, so `—` is emitted verbatim rather than
decoded:

- [`web/dash0/src/routes/orgs/$org/checks.index.tsx:540`](web/dash0/src/routes/orgs/$org/checks.index.tsx:540)
  ```tsx
  <span className="text-muted-foreground text-xs font-mono">—</span>
  ```

Every other placeholder dash in dash0 is written as a literal `—` character, so
this one line is also inconsistent with the established convention:

- [`web/dash0/src/components/dashboard/dashboard-page.tsx:919`](web/dash0/src/components/dashboard/dashboard-page.tsx:919)
- [`web/dash0/src/components/notifications/member-coverage.tsx:48`](web/dash0/src/components/notifications/member-coverage.tsx:48)
- [`web/dash0/src/routes/orgs/$org/integrations.index.tsx:324`](web/dash0/src/routes/orgs/$org/integrations.index.tsx:324)
- [`web/dash0/src/routes/orgs/$org/escalation-policies.index.tsx:373`](web/dash0/src/routes/orgs/$org/escalation-policies.index.tsx:373)
- [`web/dash0/src/routes/orgs/$org/notifications.$notificationUid.tsx:155`](web/dash0/src/routes/orgs/$org/notifications.$notificationUid.tsx:155)

A repo-wide scan of `web/dash0/src`, `web/dash0/e2e` and `web/status0/src` for
`\uXXXX` escapes outside string literals returns exactly this one hit, so the bug
is a single site, not a class of sites.

## Proposal

1. **Fix the render.** Replace the escape sequence with the literal em dash used
   everywhere else in dash0:
   ```tsx
   <span className="text-muted-foreground text-xs font-mono">—</span>
   ```
   Keep the existing classes and the surrounding `durationMs != null ? … : …`
   ternary untouched — only the text node changes.

2. **Regression guard.** Add a dash0 Playwright assertion covering the empty
   duration cell on the checks list: a check whose latest result has no
   `durationMs` (or that has never run) must render `—` and must **not** render
   the substring `—`. Asserting the *absence* of the literal escape is the
   part that actually proves the fix — asserting only that a dash is present
   would still pass on the broken string. Extend an existing checks-list E2E
   spec in `web/dash0/e2e/` rather than adding a new file if one already
   exercises that table.

3. **Optional cheap sweep.** If it lands without friction, add a grep-based
   guard (lint script or a unit test) that fails on `\uXXXX` sequences appearing
   in JSX text nodes under `web/dash0/src` and `web/status0/src`, so a future
   copy-paste of an escaped character cannot ship the same way. Skip this if it
   proves noisy against legitimate escapes inside string literals — the fix in
   step 1 plus the E2E in step 2 is the required scope.

## Out of scope

- Why some results have no `durationMs` — the placeholder is the correct render
  for that state; only its text is wrong.
- Restyling or relabelling the duration column.
