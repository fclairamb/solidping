---
model: opus
effort: high
---

# A third of signups never create a check: the activation hero's chips clear the input, move no focus, and leave the submit disabled

## Problem

Measured on 2026-09-12 from PostHog over the preceding 30 days
(`solidping-business/scripts/adoption.py ladder`), staff accounts excluded:

| rung | count | rate |
|---|---|---|
| signed up | 14 | — |
| in an org (created or joined) | 13 | 93% of signups |
| **created a first check** | **9** | **64% of signups, 69% of orgs** |

The 9 who activate do it in a **median of 32 seconds** (fastest 14 s). So the
hero works: when it works, it works immediately. The problem is the other
**36%**, and four of the five stalls sit on this one component — the zero-check
dashboard hero
([`web/dash0/src/components/dashboard/empty-state-onboarding.tsx`](../../web/dash0/src/components/dashboard/empty-state-onboarding.tsx)).

### What the stalled sessions actually did

Pseudonymous user prefixes, from the server-side `user_signed_up` /
`org_created` / `check_created` events plus client autocapture:

- `2f83c23e` (Google, 2026-09-09, arrived from ChatGPT) — landed on `/dash0/no-org`,
  **six autocaptured clicks in 16 seconds**, created an org, two more clicks on
  the dashboard, gone. Never typed anything.
- `d60483d8` (Slack, 2026-08-13) — bounced between `/orgs/:org`, `/orgs/:org/checks`
  and `/orgs/:org/incidents` **eight times in 17 seconds**, then left. Clicking
  around an empty product looking for the thing to do.
- `3103ac67` (invite, 2026-08-16) — org dashboard, then `account/profile`, gone in 6 s.
- `8d3f2f55` (password, 2026-09-05) — org created 8 s after signup, then **zero**
  client events (ad-blocked, or closed the tab).

### The mechanism

Three quick-start chips (HTTP / Ping / SSL) sit above a single input and a
submit button. Reading the chip handler:

```tsx
onClick={() => {
  setTab(quick);
  setValue("");        // every chip click wipes what you typed
  setError(null);
}}
```

- **The chips look like the action and are only a type selector.** They carry
  the icons, they are the first thing in the hero, and clicking one changes
  nothing except which button is highlighted. A visitor who clicks all three —
  which is exactly what `2f83c23e` did — has performed the most prominent
  interaction on the screen and received no feedback that anything is expected
  of them next.
- **Nothing moves focus to the input.** There is no `autoFocus` and no
  `ref.focus()` in the handler, so the caret never lands in the one field that
  matters.
- **The submit is `disabled` until `value.trim()` is non-empty.** A disabled
  button cannot explain itself: clicking it is a no-op with no message. The
  screen's only affordances are therefore "chips that do nothing visible" and
  "a button that does nothing at all".
- **Switching chips destroys typed input.** `setValue("")` fires unconditionally,
  so someone who types a hostname and then reconsiders HTTP vs SSL loses it —
  even though a bare hostname is valid for both Ping and SSL.

## What to change

Implementation is the author's call; the three defects to close are:

1. **Focus the input on chip click** (and on mount). One line, and it converts
   "clicked three chips, nothing happened" into a blinking caret.
2. **Stop clearing the input when the type changes.** Keep the value and
   re-validate it for the new type; only clear when it cannot possibly apply
   (an `https://…` URL moving to Ping).
3. **Never present a dead disabled button as the primary action.** Either keep
   it enabled and surface the validation message on submit, or label the empty
   state on the button itself so the required step is legible before the click.

## How to know it worked

`scripts/adoption.py ladder 30` in `solidping-business` prints the rung-3 → rung-4
rate. It is **64%** on 2026-09-12 against 14 signups. The number is small enough
that a single week will not prove much on its own, so pair it with the absence of
the pattern above: a session with chip clicks, no input event, and no
`check_created`.

## Not in this spec

`9a0a9079` (Microsoft, 2026-08-24) is the fifth stall and a different problem:
they landed on `/dash0/no-org` with a pending membership request, **came back
four days later and were still on `/dash0/no-org`**. `notifyAdminsOfMembershipRequest`
([`server/internal/handlers/auth/membership_requests.go:358`](../../server/internal/handlers/auth/membership_requests.go))
does email the org's admins, so the code path exists — whether that mail was
delivered and simply never acted on is unverified, and worth its own look given
operator mail is known to bounce for at least one org.

## Implementation Plan

All changes are in the zero-check hero
(`web/dash0/src/components/dashboard/empty-state-onboarding.tsx`) plus one new
pure helper module and its unit test. No backend change.

### 1. Focus the input (defect 1)

- `useRef<HTMLInputElement>` on the quick-create input.
- **Chip click** focuses it unconditionally — it is the direct consequence of a
  deliberate user action, so there is no a11y objection to moving focus there.
- **Mount** focus is *conditional*: only when `matchMedia("(pointer: fine)")`
  matches, and with `{ preventScroll: true }`. Auto-focusing on a touch device
  raises the on-screen keyboard and scrolls the hero out of view before the user
  has read it; on a pointer device a caret is free feedback and costs nothing.
  Screen-reader users on a pointer device land on the field that is the page's
  entire purpose in its zero-check state, which is the same bargain a login page
  makes.

### 2. Keep the typed value across a chip switch (defect 2)

New `web/dash0/src/lib/quick-check-target.ts`, pure and unit-tested:

- `targetAppliesTo(type, raw)` — a bare hostname applies to **all three** types,
  so the common case survives every chip switch. A value carrying a scheme, a
  path, a query, a fragment or whitespace cannot be a `host`/`domain`, so it does
  **not** apply to `icmp`/`ssl` (the spec's `https://…` → Ping case). Everything
  applies to `http`.
- `normalizeTarget(type, raw)` — for `http`, a scheme-less value gets `https://`
  so the hostname the user typed reaches a backend that requires the scheme
  (`checkhttp.Validate`: "must start with http:// or https://").
- `validateTarget(type, raw)` — `"empty" | "invalid" | null`, driving the message.

The chip handler becomes `setValue(v => targetAppliesTo(quick, v) ? v : "")`.

### 3. No dead disabled button (defect 3)

- The submit is enabled whenever a create is not in flight; the only disabled
  state left is "creating…", which explains itself.
- Submitting an empty or inapplicable value renders the localized message in the
  existing `<Alert variant="destructive">` (which already carries `role="alert"`,
  so it is announced), wires `aria-invalid` + `aria-describedby` on the input per
  the design reference's inline-error pattern, and returns focus to the input.
- The input drops `type="url"` / `required`: native constraint validation shows
  an unlocalized, unannounced browser bubble and silently blocks submit — the
  design reference already flags that trap on the image-URL field. `inputMode`
  keeps the right mobile keyboard.
- New keys `welcome.validation.empty.{http,icmp,ssl}` and
  `welcome.validation.invalid.{http,icmp,ssl}` in all four locales.

### 4. Incidental, and load-bearing for defect 3: the SSL chip could never create

`QUICK_DEFS.ssl` posts `config: { domain: … }`, but `checkssl.SSLConfig.FromMap`
reads only `host` and `Validate` then fails `host is required`. One of the three
chips therefore ends in a 400 for everyone who uses it. Fixed to `host`, and
covered by an e2e that actually creates an SSL check — otherwise "surface the
validation message on submit" would just be surfacing a server error nobody can
act on.

### Tests

- `src/lib/quick-check-target.test.ts` — the matrix, including the negative case.
- `e2e/empty-state-onboarding.spec.ts` — focus on mount and on chip click; a
  hostname surviving both chip switches; `https://acme.com/health` being cleared
  by the Ping chip; the enabled submit producing an announced message; an SSL
  check created end to end on a fresh org.
