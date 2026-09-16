---
model: sonnet
effort: medium
---

# The activation hero starts empty for everyone; pre-fill it with the user's own email domain so a work-email signup gets a real first check in one click

## Problem

The zero-check dashboard hero
([`web/dash0/src/components/dashboard/empty-state-onboarding.tsx`](../../web/dash0/src/components/dashboard/empty-state-onboarding.tsx))
is one input and one button. It already does most of what an activation flow
should: three quick types (HTTP / Ping / SSL), a bare hostname is promoted to
`https://` on submit (`normalizeTarget`, `lib/quick-check-target.ts`), the
input is focused on mount and on chip click, and the value survives a chip
switch (spec 2026-09-12-04). The 64 % of signups who activate do it in a median
of 32 seconds.

What it still asks of every user is to know, and type, the thing to monitor.
For a large share of signups we already know the most likely answer: someone
signing up as `alice@acme.com` almost certainly wants to watch `acme.com`. The
field is empty anyway, with `https://example.com` as a placeholder.

An outside suggestion was to pre-fill a fixed public URL (google.com) so the
button works with zero typing. That was rejected: a google.com check is a junk
check nobody wants, it counts against `maxChecks` on SaaS, it inflates
`check_created` without moving activation, and it costs the majority who type
their own URL a select-all first. Pre-filling the user's **own** domain keeps the
zero-typing path and drops every one of those costs, because the pre-filled
check is one they would have created anyway.

Free-webmail signups (`gmail.com`, `outlook.com`, …) get nothing useful from
their domain, so they keep today's empty field. The backend already maintains
exactly that denylist for auto-join patterns
([`server/internal/handlers/auth/autojoin_regex.go`](../../server/internal/handlers/auth/autojoin_regex.go),
`freeMailDomains`).

## Proposal

### 1. A pure helper: `suggestQuickTarget(email)`

New file `web/dash0/src/lib/quick-target-suggestion.ts`, next to
`quick-check-target.ts`, exporting:

```ts
/** The target to pre-fill the hero with for this user, or null for none. */
export function suggestQuickTarget(email: string | null | undefined): string | null
```

Rules:

- No `@`, empty, or whitespace-only input → `null`.
- Take everything after the **last** `@`, trim, lowercase. Empty → `null`.
- Domain on the free-webmail denylist → `null`. Mirror the Go
  `freeMailDomains` list verbatim in this file, with a comment pointing at
  `autojoin_regex.go` as the source of truth. Duplicating twenty strings was
  chosen over an API change: this is a UX heuristic where a miss costs one
  select-all, so drift between the two lists is harmless, and `/auth/me`
  should not grow a field for it.
- Domain that is not a plausible host (contains `/`, `:`, whitespace, or a
  leading / trailing dot; is a bare IP literal is fine and passes through) →
  `null`. Reuse `validateTarget("icmp", …)` from `quick-check-target.ts` for
  the shape check rather than writing a second parser.
- Otherwise return the **bare domain** (`acme.com`), not `https://acme.com`.
  A bare hostname is valid for all three chips and survives a chip switch
  (`carryOverTarget` clears an `https://…` URL when moving to Ping / SSL,
  which would make the suggestion vanish on the first chip click), and
  `normalizeTarget` already promotes it to `https://` on an HTTP submit.
- Use the exact email domain. `alice@mail.acme.com` suggests `mail.acme.com`.
  Guessing the registrable domain needs a public-suffix list and is not worth
  it for a value the user can overwrite in one keystroke.

Unit tests in `quick-target-suggestion.test.ts` covering every bullet, both
directions: a work domain is suggested (positive control), each of the
denylisted domains yields `null`, mixed case is lowercased, `@` in the local
part (`"a@b"@acme.com` is not worth supporting, but `alice+tag@acme.com` must
work), and malformed input yields `null`.

### 2. Wire it into the hero

In `EmptyStateOnboarding`:

- Read the signed-in user with `useAuth()` (`user?.email`, see
  `onboarding-checklist.tsx:91` for the existing pattern) and compute
  `const suggestion = suggestQuickTarget(user?.email)`.
- Initialise the input state from it: `useState(suggestion ?? "")`. Do not
  re-apply it later — if the user clears the field, it stays cleared.
- Track whether the value is still the untouched suggestion:
  `const untouched = suggestion !== null && value === suggestion`. No extra
  state; derive it.
- **Select the text whenever the input receives focus while `untouched`.**
  Add an `onFocus` handler that calls `e.currentTarget.select()` under that
  condition. This makes typing replace the suggestion instead of appending
  to it, and it covers all three ways focus arrives: the conditional mount
  focus (`shouldFocusOnMount`, fine pointer only), the unconditional chip-click
  focus, and a tap on touch devices where there is no mount focus at all. Once
  the user has typed or edited, `untouched` is false and focus behaves
  normally (caret where they clicked, no surprise select-all).
- Chip switches keep working through `carryOverTarget` unchanged; a bare
  domain applies to every chip, so the suggestion stays put.
- Submit validates the suggestion exactly like a typed value
  (`validateTarget`), no special case. The generated name
  (`HTTP — acme.com`, via `displayHostFor`) needs no change.
- No new locale strings. The user's own domain in the field is
  self-explanatory; a "suggested from your email" hint was considered and
  dropped as noise.

### 3. Make the effect measurable

The hero exists to move the signup → first-check rung, and spec 2026-09-12-04
is only days old, so the two changes must be separable in the data. On a
successful quick-create, capture a client analytics event through the
existing wrapper (`web/dash0/src/lib/analytics.ts`, which already scrubs URLs
and never ships typed values):

- event `quick_check_created`
- properties: `checkType` (`http` / `icmp` / `ssl`) and
  `targetSource: "suggested" | "typed"` — `"suggested"` when the submitted
  value equals the suggestion, `"typed"` otherwise. **Never include the target
  itself**, it is a customer hostname.

The server-side `check_created` event
([`server/internal/handlers/checks/handler.go:436`](../../server/internal/handlers/checks/handler.go))
stays as it is; it cannot know where the value came from and does not need to.

### 4. Tests

- **Unit**: the helper, as in §1. Run with `bun run test:unit` in `web/dash0`.
- **E2E** (`web/dash0/e2e/empty-state-onboarding.spec.ts`): the Playwright user
  is `test@test.com`, so `test.com` is not on the denylist and the input will
  now be pre-filled with `test.com` in every existing hero test. Two current
  tests assert an initially empty field and will fail as written:
  - "a hostname survives every chip switch…" (starts with `fill`, so only the
    implicit assumption changes — verify it still passes, adjust if not);
  - "the submit is never a dead disabled button…" asserts
    `toHaveValue("")` before submitting empty. Change it to assert the
    pre-filled `test.com`, then `fill("")`, then continue as today.

  Add:
  - the input is pre-filled with `test.com` and, after mount focus, the whole
    value is selected (`selectionStart === 0`, `selectionEnd === value.length`
    via `evaluate`); pressing keys types **over** it, so `keyboard.type("acme.com")`
    yields `acme.com`, not `test.comacme.com`;
  - clicking the Ping and SSL chips keeps `test.com` in the field and
    re-selects it;
  - after the user edits the value, refocusing the input does **not**
    select-all (the negative for the selection rule);
  - in the "real empty org" block, submitting the untouched suggestion creates
    `HTTP — test.com` and lands on the check page (the one-click path, end to
    end).
  - a free-webmail user gets an empty field: sign up a `@gmail.com` user
    through the existing test-mode registration API the "real empty org" block
    already uses for orgs, if that path accepts an arbitrary email; if it does
    not, say so in the PR and rely on the helper's unit coverage for this
    branch. Do not skip it silently.

Existing hero tests (mobile width, MCP CTA, full-editor link) must stay green.

### How to know it worked

`scripts/adoption.py ladder 30` in `solidping-business`, plus the new
`targetSource` split: the share of activations tagged `suggested`, and whether
the signup → first-check rate for work-email signups moves relative to
free-webmail signups (who are the control group, since they see no change).

## Not in this spec

- **A "try it with example.com" demo link** under the form was considered and
  dropped for the same reason as google.com: it creates a check nobody wants.
  Typing `example.com` and pressing enter already does that for anyone who
  really wants a throwaway.
- **Immediate first run.** Spec 2026-05-05-03 asked for a one-shot immediate
  run after the first check is created. Verified while writing this: it was
  not implemented — `CreateCheck` places the first job at the next
  period-aligned slot (`scheduling.NextAligned`,
  [`service.go:2808`](../../server/internal/handlers/checks/service.go)), so
  the first result is up to one period away (60 s by default). Time to first
  result is a bigger activation lever than anything on this form and deserves
  its own spec.
