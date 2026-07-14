---
model: sonnet
effort: medium
---

# Email integration: allow adding more than one email address

## Problem

Users report that the Email integration "can't add more than one email address"
([#130](https://github.com/fclairamb/solidping/issues/130), labeled `bug`). The
request: accept multiple addresses separated by *any* reasonable character
(space, comma, semicolon, newline) and improve the surrounding UI.

## Findings — the open question is resolved

Reading the code before implementing (per the original open question) settles
the scope: **this is a frontend-only fix. The backend already fully supports a
list of recipients — no model, schema, or migration change is needed.**

- **Storage**: recipients are stored as a JSON array under the canonical `to`
  key in `integration_connections.settings` (JSONB). No single-address column
  exists; nothing to migrate.
- **Send path**: [`server/internal/notifications/email.go`](server/internal/notifications/email.go)
  already fans out to every recipient. `extractRecipients` (email.go:137)
  accepts both `[]any` (JSONB load) and `[]string`, filters non-strings/empties,
  and `sendPerRecipient` / the broadcast path both iterate the full list.
- **Backend tests already cover multi-recipient** in
  [`server/internal/notifications/email_test.go`](server/internal/notifications/email_test.go)
  ("to as []string resolves both recipients", transcript asserts
  `recipients: a@x.test, b@y.test`, partial-batch failure, etc.). Proposal item 5's
  backend requirement is therefore **already met** — no new backend test is required.
- **The API already accepts the list**: the create/edit handlers persist
  `settings.to` verbatim; there is **no** per-address validation on the save path
  (frontend or backend).

**Why it looks "blocked" today.** The email panel in
[`web/dash0/src/components/integrations/integration-form.tsx`](web/dash0/src/components/integrations/integration-form.tsx)
(the `case "email"` branch, ~line 319) is a `Textarea` labeled "Recipients (one
per line)" that splits **only on `\n`**:

```ts
e.target.value.split("\n").map((s) => s.trim()).filter(Boolean)
```

So newline-separated entry works, but it's non-obvious, and — matching the
reporter's exact suggestion ("separating them by space") — anyone who types
`ops@x.com oncall@x.com` (or comma/semicolon) on one line gets a **single glued
array element** `["ops@x.com oncall@x.com"]`. There is no validation feedback, so
it saves a malformed recipient that silently fails at send time. That is the bug.

## Proposal (frontend only)

### 1. Parse on any separator
Replace the newline-only split with a split on any run of separators —
whitespace, `,`, `;`, newlines: `value.split(/[\s,;]+/)`. Trim, drop empties, and
**de-duplicate** on the exact trimmed string (do not mutate case — email local
parts are technically case-sensitive). Handle **paste** too: pasting
`a@x.com, b@x.com` must yield two recipients.

### 2. Per-address validation with a clear per-address error
Add a small `isValidEmail` helper (pragmatic HTML5-style rule
`/^[^\s@]+@[^\s@]+\.[^\s@]+$/`) in `web/dash0/src/lib/` (e.g. `email.ts`), unit-
tested. Validate each parsed address. Invalid addresses must be shown distinctly
(see UX below) and must **block save** while any invalid entry remains — surface
an inline field error rather than silently dropping the entry or blocking the
whole form with no explanation. Valid entries are never lost.

### 3. UX — a chip/tag input (the stated ideal)
Build a reusable `RecipientsInput` component that renders each address as a
removable chip and keeps an input for typing/pasting the next one:
- **Reuse existing primitives** — the `Badge` (`@/components/ui/badge`, has a
  `destructive` variant) plus `Input`. Model the interaction on the existing
  chip-style multi-value control
  [`web/dash0/src/components/shared/check-multi-picker.tsx`](web/dash0/src/components/shared/check-multi-picker.tsx).
- Commit the current token to a chip on **Enter**, on any separator keystroke
  (space/comma/semicolon), and on **blur**; **Backspace** on an empty input
  removes the last chip.
- Valid chips render as normal `Badge`s; **invalid** chips render
  `variant="destructive"` (a sanctioned red *state*, not a red action) with a
  `title`/tooltip explaining why. Each chip has a small dismiss affordance — use
  the lucide **`X`** icon (the standard chip-dismiss control), **not** `Trash2`:
  removing an unsaved chip from an input is not a resource deletion, so the
  "delete is always a red trash bin" convention does not apply here.
- Keep it **responsive and touch-friendly** — chips wrap; the input grows;
  targets are large enough for touch.
- **Add it to the design reference** as a new section (register an entry in the
  section list and add a `RecipientsInputSection` render) in
  [`web/dash0/src/routes/orgs/$org/design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx),
  showing valid + invalid chips and the exact import line, so the catalog stays
  canonical (mandatory per project convention).
- Wire `RecipientsInput` into the `case "email"` branch of `integration-form.tsx`,
  reading/writing `settings.to` as a `string[]` exactly as today (so persisted
  data shape is unchanged and existing integrations keep working).

### 4. i18n
Update the `form.recipients` label (currently "Recipients (one per line)") and
add any new keys (placeholder, per-address error, hint) across **all four**
locale files: `web/dash0/src/locales/{en,fr,es,de}/integrations.json`.

### 5. Backend — verify only, no change
No model/migration/handler change. As a light verification step, confirm an
integration saved with two recipients still sends to both (the existing
`email_test.go` coverage already asserts this; re-run `make test` for the
notifications package). Do **not** add a backend migration or schema change.

## Tests

- **Frontend unit**: `isValidEmail` and the parse/de-dupe helper — separators
  (space/comma/semicolon/newline/mixed), trimming, empties, duplicates, invalid
  addresses. Colocate with the helper.
- **Playwright E2E** (new spec, model on `web/dash0/e2e/integrations.spec.ts` /
  `channels-webhook.spec.ts`): create an email integration, add **two** addresses
  via the chip input (exercise a non-newline separator and/or paste), save,
  reload the edit page, and assert both chips persist. Optionally assert an
  invalid address shows the error state and blocks save.
- **Backend**: none required — multi-recipient send is already covered by
  `server/internal/notifications/email_test.go`.

## Acceptance criteria

- [ ] Entering `a@x.com b@x.com` (space), `a@x.com,b@x.com` (comma),
      `a@x.com;b@x.com` (semicolon), newline-separated, or pasted — all produce
      two distinct recipients.
- [ ] Duplicates are collapsed; whitespace is trimmed; empties dropped.
- [ ] Each recipient is a removable chip; invalid ones are visibly flagged and
      block save with a clear message; valid ones are never dropped.
- [ ] Persisted shape is unchanged (`settings.to: string[]`); existing email
      integrations load and send correctly.
- [ ] The chip input is added to the design reference and reused (not a one-off).
- [ ] Fully usable on mobile.
- [ ] Frontend unit + Playwright E2E pass; `make lint` and `make test-dash` green.

## Implementation Plan

Frontend-only, per the Findings section (no Go/DB changes).

1. **Parse/validate helper** — `web/dash0/src/lib/email.ts`:
   - `isValidEmail(s: string): boolean` — pragmatic `/^[^\s@]+@[^\s@]+\.[^\s@]+$/`.
   - `parseEmailList(raw: string): string[]` — split on `/[\s,;]+/`, trim, drop
     empties, de-dupe on exact trimmed string (case preserved).
   - Colocated `email.test.ts` (vitest) covering separators (space/comma/
     semicolon/newline/mixed), trimming, empties, duplicates.

2. **`RecipientsInput` chip component** — new
   `web/dash0/src/components/shared/recipients-input.tsx`:
   - Props: `value: string[]`, `onChange: (v: string[]) => void`, optional
     `placeholder`/`data-testid`.
   - Modeled on `check-multi-picker.tsx`'s chip rendering: `Badge` (default
     variant for valid, `destructive` for invalid, using `isValidEmail`) +
     dismiss `X` icon button per chip (not `Trash2` — not a resource delete).
   - Free-text `Input` alongside the chips; commits the current token to a
     chip on Enter, on typing a separator char (space/comma/semicolon), and on
     blur; Backspace on an empty input pops the last chip. Paste is handled by
     the `onChange`/`onPaste` path splitting through `parseEmailList` so a
     multi-address paste yields multiple chips in one step.
   - Wrapping flex layout, large enough touch targets — responsive/mobile per
     project convention.
   - Invalid chips carry a `title` tooltip explaining the problem.

3. **Wire into `integration-form.tsx`**:
   - Replace the `case "email"` `Textarea` block with `RecipientsInput`,
     reading/writing `settings.to` as `string[]` (unchanged persisted shape —
     backward compatible with existing single/multi-address connections).
   - Track per-form validity (any invalid chip) and block Save — surface via
     the existing save-disabled wiring in the parent create/edit pages (same
     mechanism already used for name-required).

4. **Design reference** — add a `RecipientsInputSection` in
   `web/dash0/src/routes/orgs/$org/design-reference.tsx`: register in
   `SECTIONS`, render valid + invalid chip examples with the import line,
   call it from `DesignReferencePage`.

5. **i18n** — update `form.recipients` (drop "one per line" framing) and add
   `form.recipientsPlaceholder` / `form.recipientsInvalid` (or similar) keys
   across `en/fr/es/de` `integrations.json`.

6. **Tests**:
   - Unit: `email.test.ts` (step 1).
   - Playwright: new `describe` block in `web/dash0/e2e/integrations.spec.ts`
     — create an email integration, type `a@x.com b@x.com` (space-separated)
     into the chip input, save, reload the edit page, assert both chips
     persist; assert an invalid address renders a destructive chip and blocks
     save.
   - Backend: none — re-run `make test` on `server/internal/notifications`
     only to confirm existing multi-recipient coverage still passes
     unmodified.

7. **QA**: `make build-dash0`, `cd web/dash0 && bun run lint` (no new errors),
   `bun run test` (vitest) for the new unit test, Playwright run of the new
   E2E block if a local test-mode server is reachable.
