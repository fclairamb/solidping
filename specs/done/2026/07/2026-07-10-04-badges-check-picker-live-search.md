# Badges page: replace the capped check dropdown with the live-search CheckPicker

## Problem

The badge generator page (`/orgs/$org/badges`) selects its check through a plain
Radix `Select` fed by a single list fetch capped at the endpoint maximum:

- [web/dash0/src/routes/orgs/$org/badges.tsx:336-337](../../web/dash0/src/routes/orgs/$org/badges.tsx) —
  `useChecks(org, { limit: 100 })`, with a comment admitting the workaround
  ("Raise to the endpoint max so the dropdown covers up to 100 checks").
- [web/dash0/src/routes/orgs/$org/badges.tsx:464-469](../../web/dash0/src/routes/orgs/$org/badges.tsx) —
  the `Select` itself (`data-testid="badge-check-select"`).

On an org with more than 100 checks — e.g. `acmetech` on the k8xp dev deploy
(`https://solidping.k8xp.com/dash0/orgs/acmetech/badges`) — this breaks down:

1. **Checks beyond the first 100 cannot be selected at all.** They simply are
   not in the dropdown.
2. **Even under 100, there is no way to search** — the user scrolls a huge
   dropdown to find one check by eye.
3. The page carries extra machinery just to paper over the gap: a direct
   `useCheck` fetch so deep links to an unlisted check still resolve
   ([badges.tsx:338-343](../../web/dash0/src/routes/orgs/$org/badges.tsx)),
   plus injecting that resolved check into `checkOptions` so the Select
   trigger can display it ([badges.tsx:371-376](../../web/dash0/src/routes/orgs/$org/badges.tsx)).

Meanwhile the codebase already has a shared live-search picker solving exactly
this: [`CheckPicker`](../../web/dash0/src/components/shared/check-picker.tsx)
does debounced (150 ms) server-side search via the checks endpoint's `q` param
with a 25-item result limit, and is already catalogued in the design reference
and used by the check form (dependencies), status pages, and the multi-picker.

## Proposal

Swap the `Select` on the badges page for the shared `CheckPicker` so check
selection is a live server-side search.

- Replace the `Select`/`SelectTrigger`/`SelectItem` block with `CheckPicker`,
  keeping the existing URL contract: `onChange` still writes the check **slug**
  (fallback uid) to the `?check=` search param via `handleCheckChange`
  ([badges.tsx:398-401](../../web/dash0/src/routes/orgs/$org/badges.tsx)).
- Keep the direct `useCheck` resolution for deep links; pass the resolved
  check's name as `selectedLabel` so the trigger shows it (CheckPicker leaves
  trigger labelling to the caller). Keep the `checkNotFound` stale-bookmark
  message.
- Drop the page-level `useChecks(org, { limit: 100 })` list fetch and the
  `checkOptions` merge — CheckPicker fetches its own matches. The loading
  skeleton / `loadFailed` error states move to (or are covered by) the picker.
- Pass a badges-namespace placeholder (CheckPicker's built-in default text
  lives in the `dependencies` i18n namespace).
- Keep `data-testid="badge-check-select"` on the picker trigger, or update
  [web/dash0/e2e/badges.spec.ts](../../web/dash0/e2e/badges.spec.ts) (and any
  other spec driving that testid) to drive the combobox: open, type a query,
  pick a result.
- E2E: extend `badges.spec.ts` with a live-search scenario — type part of a
  check name, assert the filtered result appears, select it, and assert the
  badge preview/URL updates and `?check=<slug>` lands in the URL.

Out of scope: changing the checks API (the `q` + `limit` params already
exist), and the other capped dropdowns elsewhere in dash0 (e.g. maintenance
windows) — same pattern, separate spec if wanted.

## Implementation Plan

1. **CheckPicker: optional trigger testid** — add a `triggerTestId?: string` prop to
   `web/dash0/src/components/shared/check-picker.tsx` and put it on the trigger `Button`,
   so the badges page can keep `data-testid="badge-check-select"` (spec's preferred
   option). Backward-compatible: existing callers pass nothing.

2. **Badges page swap** (`web/dash0/src/routes/orgs/$org/badges.tsx`):
   - Drop `useChecks(org, { limit: 100 })`, the `checkOptions` merge, and the
     Select-specific loading skeleton / `loadFailed` error branch (the picker fetches
     its own matches).
   - Keep the direct `useCheck(org, search.check)` resolution for deep links;
     `selectedCheck` becomes just `directCheck`.
   - Replace the `Select`/`SelectTrigger`/`SelectItem` block with `CheckPicker`:
     `value={selectedCheck?.uid}`, `selectedLabel` from the resolved check's
     name/slug, `placeholder={t("selectCheck")}` (badges namespace),
     `triggerTestId="badge-check-select"`, `onChange` writing `check.slug || uid`
     (or clearing the param) via `updateSearch` — URL contract unchanged.
   - Keep the `checkNotFound` stale-bookmark alert and the preview-pane skeleton
     while the direct fetch resolves.
   - Remove now-unused imports (`useChecks`, Select primitives; `loadFailed` i18n
     key may stay in locale files — harmless).

3. **E2E** (`web/dash0/e2e/badges.spec.ts`):
   - Update the one interaction that drove the Radix Select ("should select a check…")
     to drive the combobox: click the `badge-check-select` trigger, type into the
     search input, click `check-picker-option-<slug>`.
   - Add a live-search scenario: create a target check plus filler checks so the
     target is outside the first result page, open the picker, type a distinctive
     substring of the target's name, assert the filtered option appears, select it,
     and assert the badge preview renders, the embed URL points at the target, and
     `?check=<slug>` lands in the page URL.
   - Trigger-visibility and `toContainText(name)` assertions keep working since the
     testid stays on the picker trigger and `selectedLabel` shows the resolved name.

4. **QA** — `make build-dash0`, `cd web/dash0 && bun run lint` (no NEW errors in
   touched files), `make build-backend`; run `e2e/badges.spec.ts` against a
   test-mode server if one is available, otherwise report authored-but-not-run.
