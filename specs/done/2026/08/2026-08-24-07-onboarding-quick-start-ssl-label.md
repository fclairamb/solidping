---
model: sonnet
effort: medium
---

# The dashboard onboarding quick-start chip still says "SSL" after the check type was renamed to "TLS"

## Problem

Spec `2026-08-24-04` unified the `ssl` check type's user-facing label to **"TLS"**
across every surface that shows a *type label*: the check-creation picker
([check-form.tsx:83](web/dash0/src/components/shared/check-form.tsx#L83)), the
checks-list chip, the check detail page, and the new checks-list type filter.
`CHECK_TYPE_IDENTITY`
([check-type-identity.tsx](web/dash0/src/components/shared/check-type-identity.tsx))
is the canonical source, and
[check-type-identity.test.ts](web/dash0/src/components/shared/check-type-identity.test.ts)
now pins `check-form.tsx`'s picker labels to it so the two can't drift again
(commit `7f24a6857`).

One surface was deliberately left out as out-of-scope: the org dashboard's
empty-state **"quick start" onboarding chips** — the three one-input chips shown
when an org has zero checks
([empty-state-onboarding.tsx](web/dash0/src/components/dashboard/empty-state-onboarding.tsx)).
It still says "SSL" in three places:

| What | Where | Value |
|---|---|---|
| Chip label | `welcome.quick.ssl` in `locales/{en,fr,de,es}/dashboard.json` | `"SSL"` (all four) |
| Input label | `welcome.quickLabel.ssl` in the same four files | `"Domain for SSL check"` / `"Domaine pour la vérification SSL"` / `"Domain für SSL-Prüfung"` / `"Dominio para SSL"` |
| Sample check name seed | [empty-state-onboarding.tsx:49](web/dash0/src/components/dashboard/empty-state-onboarding.tsx#L49) `namePrefix: "SSL"` | `"SSL"` |

`namePrefix` is **not** a type label — it seeds the *name* of the check the user
is about to create: `` `${def.namePrefix} — ${displayHostFor(def.field, trimmed)}` ``
([empty-state-onboarding.tsx:79](web/dash0/src/components/dashboard/empty-state-onboarding.tsx#L79)),
so a user who types `acme.com` gets a check named `SSL — acme.com`. That is
persisted user data, not chrome.

### The key evidence: these chips already diverge from the canonical labels on purpose

The `icmp` chip is labelled **"Ping"** — both `namePrefix: "Ping"`
([empty-state-onboarding.tsx:42](web/dash0/src/components/dashboard/empty-state-onboarding.tsx#L42))
and `welcome.quick.icmp: "Ping"` — while `CHECK_TYPE_IDENTITY.icmp.label` is
**"ICMP"** ([check-type-identity.tsx:124](web/dash0/src/components/shared/check-type-identity.tsx#L124)).
The surrounding copy is friendlier throughout in the same way (`example.com`
placeholders, "Host to ping", "Domain for SSL check").

So the onboarding chips are an intentionally separate, beginner-facing
vocabulary, **not** a third label source that drifted. That reframes the
question: this is not "fix an inconsistency", it is "is `SSL` still the
friendlier beginner term (the way `Ping` is), or is it now merely stale?".

## Proposal

### Decision to make — delegated to the implementer, does NOT block implementation

Decide whether the three onboarding strings adopt "TLS". This is explicitly the
implementer's call: pick one, implement it deliberately, and record the reasoning
in the commit message. Do **not** escalate this as an unresolved open question,
and do not assume a rename is required.

Weigh at least these:

- **Argument for keeping "SSL"**: the `icmp`→"Ping" precedent shows this surface
  deliberately prefers the term a newcomer recognizes over the technically
  precise one. "SSL" remains the more widely recognized term among people who
  have not yet created their first check, which is exactly this component's
  audience.
- **Argument for moving to "TLS"**: every other surface now says TLS, so a user
  creates a check from a chip labelled "SSL" and immediately sees it listed as
  "TLS" — the inconsistency is visible within one flow, seconds apart.
- **`namePrefix` deserves its own answer.** It writes a persisted check name.
  Changing it does not rename existing checks, so both old (`SSL — acme.com`) and
  new (`TLS — acme.com`) names would coexist in the same list. That is a mild
  argument for leaving it alone even if the visible chip label changes, and it is
  legitimate to answer the label question and the `namePrefix` question
  differently.

A split outcome (e.g. chip label → "TLS", `namePrefix` unchanged) is acceptable
if reasoned; so is changing nothing, provided the reasoning is recorded.

### If the strings change

1. Update **all four locales** (`en`, `fr`, `de`, `es`) under
   `web/dash0/src/locales/*/dashboard.json`. Note `quickLabel.ssl` is a full
   sentence in each locale, not a bare token — translate it properly rather than
   swapping the acronym inside the English string.
2. Keep `welcome.quick.ssl` and `welcome.quickLabel.ssl` consistent with each
   other.

### Do NOT extend the pinning test to this surface

The existing `it.each` in
[check-type-identity.test.ts](web/dash0/src/components/shared/check-type-identity.test.ts)
pins `check-form.tsx`'s picker labels to `CHECK_TYPE_IDENTITY`. Extending it to
cover the onboarding chips would **force `icmp` to become "ICMP"** and destroy the
deliberate friendlier copy described above. Adding that guard would be a
regression, not an improvement.

If the implementer wants drift protection here, add a *separate*, narrower test
that asserts only what was actually decided — e.g. pinning `welcome.quick.ssl`
and `namePrefix` to each other, or to the chosen literal — and add a short comment
on `EMPTY_STATE_CHECK_DEFS` (or wherever the chip map lives) recording that this
surface intentionally does not follow `CHECK_TYPE_IDENTITY`, so the next person
doesn't "fix" it.

## QA

Frontend-only; no Go is touched, so skip the backend gate.

- `make build-docs` is not relevant here.
- Gate: `make build-dash0`, then `cd web/dash0 && bun run lint` and
  `bun run test:unit`.
- `bun run lint` is RED on base (~44 pre-existing `react-hooks` errors) — the bar
  is **no NEW errors in the files you touch**. Do not fix the pre-existing debt
  and do not relax the eslint config.
- Note that `test:unit` is a plain `vitest run` with **no** repo-wide locale
  key-set parity guard — if you add or rename a key, diff the key sets across all
  four `dashboard.json` files yourself rather than relying on the suite.
- If the visible chip label changes, extend the dashboard E2E coverage (or add a
  focused assertion) so the empty-state chip's rendered text is pinned.
