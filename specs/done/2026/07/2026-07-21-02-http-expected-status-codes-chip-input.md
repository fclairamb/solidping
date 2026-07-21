---
model: sonnet
effort: medium
kind: fix
---

# HTTP check form: accept multiple expected status codes with wildcards (200, 4XX) via a chip input

> **Change type: fix.** This is a bug fix, not a feature — the backend already
> supports multiple status codes with wildcards; the UI failing to expose it is
> the defect. Use `fix:` conventional commits and a `fix/` branch so
> release-please files it under Bug Fixes.

## Problem

The HTTP check form only accepts a **single numeric** expected status code:
[`web/dash0/src/components/checks/form/types/http.tsx`](web/dash0/src/components/checks/form/types/http.tsx)
renders one `<Input type="number">` (line ~138) and `toConfig` writes a single
`expectedStatus` int (line 68–69). There is no way from the UI to say "200 or
201", let alone "any 2XX plus 401".

**The backend already supports exactly what's being asked for** — this is a
frontend-exposure gap, not a feature build:

- `HTTPConfig.ExpectedStatusCodes []string`
  ([config.go:65](server/internal/checkers/checkhttp/config.go:65)) accepts a
  list of patterns; `MatchStatusCode`
  ([config.go:36](server/internal/checkers/checkhttp/config.go:36)) matches
  exact codes (`"200"`) and wildcards (`"4XX"` → 400–499, case-insensitive,
  first digit 1–5).
- Execution prefers the list when present
  ([checker.go:533](server/internal/checkers/checkhttp/checker.go:533)) and
  falls back to the single `ExpectedStatus` otherwise.
- `Validate` already checks every pattern per element via
  `validateStatusPattern` and returns a field-scoped config error
  ([checker.go:136](server/internal/checkers/checkhttp/checker.go:136)).
  The single-int field is explicitly commented "deprecated, but still
  supported" ([checker.go:131](server/internal/checkers/checkhttp/checker.go:131)).
- Both camelCase (`expectedStatusCodes`) and snake_case keys decode
  ([config.go:152](server/internal/checkers/checkhttp/config.go:152)).

The "automatic text blocks" the user refers to is the chip/tag input built for
the email integration's multiple recipients
([`web/dash0/src/components/shared/recipients-input.tsx`](web/dash0/src/components/shared/recipients-input.tsx),
from spec `2026-07-14-06-email-integration-multiple-addresses.md`): typing a
separator or Enter commits the current token to a removable chip, invalid
tokens render as destructive chips, Backspace on empty input pops the last
chip.

## Proposal (frontend-only)

### 1. Generalize the chip input

Extract the generic behavior of `RecipientsInput` into a reusable
`TokenChipsInput` (e.g. `web/dash0/src/components/shared/token-chips-input.tsx`)
parameterized by:

- `validate: (token: string) => boolean` (invalid chips render
  `Badge variant="destructive"` with a `title` explaining why),
- `normalize?: (token: string) => string` (applied on commit),
- `placeholder`, `data-testid`, `value: string[]`, `onChange`.

Re-base `RecipientsInput` on it (unchanged behavior, email validator) so there
is one chip implementation, not two. Keep the interaction contract from the
email spec: commit on Enter / separator keystroke (space, comma, semicolon) /
blur; multi-token paste splits into multiple chips; `X` icon dismiss (not
`Trash2` — removing an unsaved token is not a resource deletion); wrapping
flex layout, touch-friendly.

If refactoring `RecipientsInput` proves riskier than expected, an acceptable
fallback is a sibling `StatusCodesInput` modeled on it — but prefer the shared
extraction.

### 2. Status-code chip input in the HTTP form

Replace the single numeric "Expected Status" input in `http.tsx` with the chip
input configured for status patterns:

- **Validator**: `/^[1-5]([0-9]{2}|XX)$/i` — mirrors the backend's
  `validateStatusPattern` (exact 100–599 or `NXX` wildcard, N in 1–5).
- **Normalize**: trim + uppercase (`4xx` → `4XX`), matching the backend's
  `strings.ToUpper` before matching.
- De-duplicate on the normalized token.
- Invalid chips block save with an inline field error (wire through the
  existing `FieldErrors` mechanism in `toConfig`, like the URL-required error).

State/serialization in `httpModule`:

- `fromConfig`: seed the chip list from `expectedStatusCodes` when present;
  else from legacy `expectedStatus` (as one exact chip) when set; else default
  to `["200"]`.
- `toConfig`: when the list is exactly `["200"]` (the default) or empty, omit
  both keys (preserves today's "200 is implicit" behavior). Otherwise write
  `expectedStatusCodes: string[]` and never write the deprecated
  `expectedStatus`. `toConfig` rebuilds the config object, so the legacy key
  drops off a re-saved check automatically — verify an existing
  `expectedStatus: 201` check round-trips to `expectedStatusCodes: ["201"]`
  on next save without changing check behavior.
- Keep the `?expectedStatus=` prefill search param on `checks.new.tsx`
  working (seed it as a single chip).

### 3. Design reference

Add the generic chip input to
[`web/dash0/src/routes/orgs/$org/design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx)
(or update the existing recipients-input section to present it as the generic
`TokenChipsInput` with the email and status-code configurations as examples),
with the exact import line — mandatory per project convention.

### 4. Copy / hints

Under the field, show a short muted hint like "Exact codes or ranges: 200,
201, 4XX". Follow the existing style of `http.tsx` for labels (the file mixes
hardcoded labels and the `checks` i18n namespace — if adding new keys, add
them to all four locales `en/fr/es/de`).

## Out of scope

- Backend changes — matching, validation, and both config keys already exist
  and are covered by `config_test.go` / `checker_test.go`.
- Other check types (only HTTP has expected status).
- MCP tooling (`validate_check` flows through the same backend validation).
  Optionally refresh `checkhttp/samples.go` to showcase
  `expectedStatusCodes` if samples currently only show the legacy key.

## Tests

- **Frontend unit** (vitest): the status-pattern validator/normalizer
  (valid: `200`, `404`, `4XX`, `4xx`→`4XX`; invalid: `6XX`, `0XX`, `99`,
  `20X`, `XXX`, `600`), de-dupe, and `fromConfig`/`toConfig` mapping
  (legacy int seed, `["200"]` omission, list serialization).
- **Playwright E2E** (extend the checks form spec in `web/dash0/e2e/`):
  create an HTTP check, enter `200 4XX` (space-separated) producing two
  chips, save, reload the edit page, assert both chips persist and the saved
  config carries `expectedStatusCodes: ["200","4XX"]`; assert an invalid
  token (`6XX`) renders a destructive chip and blocks save.
- Existing email-integration E2E must stay green after the `RecipientsInput`
  re-base.

## Acceptance criteria

- [ ] Typing `200, 4XX` (any separator: space/comma/semicolon/Enter/paste)
      yields two chips; saved config is `expectedStatusCodes: ["200","4XX"]`.
- [ ] `4xx` normalizes to `4XX`; duplicates collapse; invalid patterns show a
      destructive chip and block save with a clear message.
- [ ] Default stays implicit: a plain-200 check saves with neither status key.
- [ ] Existing checks with legacy `expectedStatus` load correctly and migrate
      to `expectedStatusCodes` on next save with identical behavior.
- [ ] One shared chip component serves both email recipients and status codes;
      it appears in the design reference.
- [ ] Fully usable on mobile.
- [ ] `make lint` (no new errors), frontend unit tests, and `make test-dash`
      green.

## Implementation Plan

Frontend-only, `web/dash0`. Staying on the current batch branch throughout —
no `fix/` branch, `fix:` commit prefixes per commit.

1. **Generic chip input** — extract `web/dash0/src/components/shared/token-chips-input.tsx`
   (`TokenChipsInput`) out of `recipients-input.tsx`'s existing behavior:
   `value`/`onChange`, `validate`, optional `normalize`, `placeholder`, `id`,
   `data-testid`, plus `invalidTitle`/`getRemoveLabel` so callers can supply
   their own (translated) chip tooltip/aria text. Internal token
   splitting/dedup logic is generalized (dedupe key = normalized token when
   `normalize` is given, else the trimmed token — preserves email's
   case-sensitive dedup unchanged). Re-base `RecipientsInput` as a thin
   email-flavored wrapper (`validate={isValidEmail}`, no `normalize`,
   `integrations` i18n strings) — same `data-testid`s, so existing
   `email-recipients-*` E2E keeps working unmodified.
2. **Status-code helpers** — new `web/dash0/src/lib/http-status.ts`:
   `isValidStatusPattern` (mirrors backend `validateStatusPattern`:
   `/^[1-5]([0-9]{2}|XX)$/i`), `normalizeStatusPattern` (trim + uppercase),
   `dedupeStatusPatterns` (normalize + de-dupe, first-seen order). Unit tested
   directly.
3. **`http.tsx` rewiring** — `HttpState.expectedStatus: string` becomes
   `expectedStatusCodes: string[]`. `fromConfig` seeds from
   `expectedStatusCodes` (deduped/normalized) when present and non-empty,
   else the legacy `expectedStatus` int as one chip, else `["200"]`.
   `toConfig` omits both keys when the list is `["200"]` or empty, otherwise
   writes `expectedStatusCodes` and never the legacy key (toConfig rebuilds
   the config object from scratch every save, so a legacy key drops off
   automatically). `toConfig`'s returned `errors` gains an
   `expectedStatusCodes` field error when any chip fails
   `isValidStatusPattern`, blocking submit through the existing
   `blockingErrors` path in `check-form.tsx` (same mechanism as the
   URL-required error) — no backend change needed. The `Fields` render swaps
   the numeric `<Input>` for `<TokenChipsInput>` plus a muted hint line and an
   inline destructive message when any current chip is invalid.
   `checks.new.tsx`'s `?expectedStatus=` prefill needs no change — it already
   seeds `config.expectedStatus`, which `fromConfig`'s legacy branch picks up
   as a single chip.
4. **Design reference** — update the existing "Recipients input" section into
   a "Token chips input" section presenting `TokenChipsInput` generically,
   with the email (`RecipientsInput`) and HTTP status-code configurations as
   two side-by-side examples and the exact import lines for both.
5. **i18n** — add `expectedStatusCodesHint`, `statusCodeInvalid`,
   `statusCodeInvalidSummary`, `removeStatusCode` to the `form` section of
   `checks.json` in all four locales (en/fr/es/de).
6. **Tests**
   - `web/dash0/src/lib/http-status.test.ts` — validator/normalizer/dedupe
     per the spec's valid/invalid lists.
   - `web/dash0/src/components/checks/form/types/http.test.ts` —
     `fromConfig`/`toConfig` mapping: default `["200"]` seed/omission, legacy
     int seed and round-trip to `expectedStatusCodes`, list serialization,
     precedence when both keys are present, invalid-pattern field error,
     URL-required still works.
   - New Playwright spec `web/dash0/e2e/check-http-expected-status-codes.spec.ts`
     (modeled on `check-http-basic-auth.spec.ts`): create an HTTP check,
     space-separated `200 4XX` produces two chips, save, reload the edit
     page, assert both chips persist and the stored config carries
     `expectedStatusCodes: ["200","4XX"]`; assert a `6XX` chip renders
     destructive and blocks save (form stays put, no navigation, error
     surfaces).
   - Re-run `integrations.spec.ts`'s "Email integration recipients" describe
     block (or the full suite, if the local server can run in test mode) to
     confirm the `RecipientsInput` re-base didn't regress it.
7. **QA** — `make build-dash0`, `bun run lint` scoped to touched files (no
   new errors against the pre-existing ~45 `react-hooks` baseline),
   `bun run test:unit`, Playwright E2E (author-and-run if the local devloop
   is in test mode, else author-only and note it in the report).
