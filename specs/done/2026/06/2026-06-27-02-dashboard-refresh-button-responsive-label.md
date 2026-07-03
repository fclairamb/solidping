# Dashboard refresh buttons: show a "Refresh" label on desktop, icon-only on mobile

## Context

Almost every dash0 list/detail page has a header (or toolbar) **refresh button** — an
`outline` `Button` with `size="icon"` wrapping a `RefreshCw` icon that `refetch()`es the
page's data. Across the dashboard these are inconsistent: most are **icon-only** (just the
spinning arrows, no text), while a handful already render the icon **plus a "Refresh" label
that is hidden on small screens**.

The desired, already-shipped pattern lives on the discovery pages
([`web/dash0/src/routes/orgs/$org/discovery.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx)
and
[`discovery.$jobUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx)):

```tsx
<Button variant="outline" onClick={() => void refetch()} disabled={isRefetching}
        aria-label={t("refresh")}>
  <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
  <span className="hidden sm:inline">{t("refresh")}</span>
</Button>
```

i.e. **text "Refresh" from the `sm` breakpoint up, icon-only below it** (mobile). The icon
gets `sm:mr-2` so the gap only appears when the label is visible, and `size="icon"` is
dropped so the button sizes to its content.

The user asked that **every** dashboard refresh button behave this way — label on desktop,
collapsing to icon-only on mobile — for visual consistency. (This change keeps getting wiped
when applied live because a parallel process resets this repo's working tree, so it is being
captured as a spec to apply in one clean pass later.)

### Current state (committed baseline)

**Already conform — do not touch:**

- [`discovery.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx) — `t("refresh")`, `sm:` breakpoint.
- [`discovery.$jobUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx) — `t("refresh")`, `sm:` breakpoint.
- [`checks.$checkUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx) — **already responsive but at the `lg:` breakpoint** (`size="icon"` + `className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"`, label `hidden lg:inline`, key `t("checks:detail.refresh")`). It deliberately uses `lg` because its toolbar is crowded (Badges / Refresh / Edit / Delete). **Leave as-is** — changing it to `sm` would make its own toolbar inconsistent.

**Need the change (icon-only today) — 12 files:**

| File | `useTranslation` ns | Label key to use |
|---|---|---|
| [`checks.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.index.tsx) | `"checks"` | `t("common:refresh")` |
| [`incidents.index.tsx`](../../web/dash0/src/routes/orgs/$org/incidents.index.tsx) | `"incidents"` | `t("common:refresh")` |
| [`incidents.$incidentUid.tsx`](../../web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx) | `"incidents"` | `t("actions.refresh")` (existing key, already its `aria-label`) |
| [`events.tsx`](../../web/dash0/src/routes/orgs/$org/events.tsx) | `"events"` | `t("common:refresh")` |
| [`jobs.index.tsx`](../../web/dash0/src/routes/orgs/$org/jobs.index.tsx) | `"jobs"` | `t("refresh")` (existing top-level key, already its `aria-label`) |
| [`integrations.index.tsx`](../../web/dash0/src/routes/orgs/$org/integrations.index.tsx) | `"integrations"` | `t("common:refresh")` |
| [`dependencies.index.tsx`](../../web/dash0/src/routes/orgs/$org/dependencies.index.tsx) | `["dependencies","common"]` | `t("common:refresh")` |
| [`on-call.index.tsx`](../../web/dash0/src/routes/orgs/$org/on-call.index.tsx) | `["oncall","common"]` | `t("common:refresh")` |
| [`status-pages.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx) | `"statusPages"` | `t("common:refresh")` |
| [`status-updates.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx) | *(none — page has no i18n)* | hardcoded `"Refresh"` |
| [`escalation-policies.index.tsx`](../../web/dash0/src/routes/orgs/$org/escalation-policies.index.tsx) | `["escalation","common"]` | `t("common:refresh")` |
| [`account.tokens.tsx`](../../web/dash0/src/routes/orgs/$org/account.tokens.tsx) | `"account"` | `t("common:refresh")` |

## Decision

1. **One shared `refresh` string in `common`.** Add a `"refresh"` key to
   [`web/dash0/src/locales/{en,fr,de,es}/common.json`](../../web/dash0/src/locales/en/common.json)
   (it sits naturally beside `save`/`cancel`/`edit`/`search`). Reuse the translations already
   present in `discovery.json`: **en** `Refresh`, **fr** `Actualiser`, **de** `Aktualisieren`,
   **es** `Actualizar`.

2. **Reference `common:refresh` cross-namespace** from pages whose own namespace has no clean
   top-level refresh key. This works from single-namespace pages (e.g. `useTranslation("checks")`)
   because **all namespaces are statically bundled** in
   [`web/dash0/src/i18n.ts`](../../web/dash0/src/i18n.ts) (`defaultNS: "common"`); the explicit
   `common:` prefix resolves against the global store regardless of the hook's declared ns.
   This is an established pattern — e.g. `t("common:cancel")` is already used from the
   single-ns `checks.$checkUid.index.tsx` and `status-pages.index.tsx`. **No `useTranslation`
   call needs to change.**

3. **Reuse existing keys where they already fit:** `jobs.index` (`t("refresh")`) and
   `incidents.$incidentUid` (`t("actions.refresh")`) already pass those keys as the button's
   `aria-label`; reuse the same key for the visible label rather than switching to `common`.

4. **`status-updates.index.tsx` stays non-i18n.** That page uses no `useTranslation` and
   hardcodes English (its `aria-label="Refresh"`). Use a literal `"Refresh"` for the label too
   — do **not** introduce the i18n hook for one button (out of scope).

5. **Leave `checks.$checkUid.index.tsx` alone** (see Context — already responsive at `lg:`).

## Goals

- Every dashboard refresh button shows the word **Refresh** (localized) from the `sm`
  breakpoint up and collapses to an icon-only button below `sm`.
- Buttons keep `aria-label={…}` so the icon-only (mobile) state stays accessible.
- The spinning behaviour (`animate-spin` while refetching), `onClick`, `disabled`, and any
  `data-testid` on each button are preserved exactly.
- A single reusable `common:refresh` string exists in all four locales; no duplicated
  per-namespace key is added.
- `checks.$checkUid.index.tsx`, `discovery.index.tsx`, `discovery.$jobUid.index.tsx` are
  unchanged.

## Out of scope

- Adding i18n to `status-updates.index.tsx` beyond the single literal label.
- Touching contextual refresh-style buttons that already always show a label and are **not**
  the icon-only header pattern: the "refresh preview" button in
  [`badges.tsx`](../../web/dash0/src/routes/orgs/$org/badges.tsx) (`size="sm"`, already labelled)
  and the "Sync" button in
  [`server.email-inbox.tsx`](../../web/dash0/src/routes/orgs/$org/server.email-inbox.tsx).
- Normalizing `checks.$checkUid.index.tsx` from `lg:` to `sm:` (its crowded toolbar is a
  deliberate exception).
- Any change to the legacy `web/dash` dashboard or to `web/status0`.
- Broader header-action restyling (this spec only adds the responsive label to refresh).

## Implementation

1. **Locale key.** In each of
   [`en`](../../web/dash0/src/locales/en/common.json) /
   [`fr`](../../web/dash0/src/locales/fr/common.json) /
   [`de`](../../web/dash0/src/locales/de/common.json) /
   [`es`](../../web/dash0/src/locales/es/common.json) `common.json`, add `"refresh"` next to
   the `"search"` key:
   - en: `"refresh": "Refresh",`
   - fr: `"refresh": "Actualiser",`
   - de: `"refresh": "Aktualisieren",`
   - es: `"refresh": "Actualizar",`

2. **Per-button transform.** For each of the 12 files in the Context table, locate the header
   refresh `Button` (the `variant="outline"` button wrapping `RefreshCw` whose `onClick`
   calls the page's `refetch`/`refresh`) and apply:
   - **Remove** `size="icon"`.
   - **Add** `aria-label={<key>}` if the button doesn't already have one (keep the existing
     one for `jobs.index` / `incidents.$incidentUid`).
   - On the `RefreshCw` `className`, insert `sm:mr-2` (e.g. `` `h-4 w-4 sm:mr-2 ${… animate-spin …}` ``).
   - **Add** a label span as the button's last child:
     `<span className="hidden sm:inline">{<key>}</span>`.

   where `<key>` is the page's "Label key to use" from the Context table
   (`t("common:refresh")` for most; `t("refresh")` for `jobs.index`; `t("actions.refresh")`
   for `incidents.$incidentUid`; literal `"Refresh"` for `status-updates.index`).

   Canonical before/after (most pages):
   ```tsx
   // before
   <Button variant="outline" size="icon" onClick={() => refetch()} disabled={isRefetching}>
     <RefreshCw className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />
   </Button>
   // after
   <Button variant="outline" onClick={() => refetch()} disabled={isRefetching}
           aria-label={t("common:refresh")}>
     <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
     <span className="hidden sm:inline">{t("common:refresh")}</span>
   </Button>
   ```
   Preserve each button's own attributes verbatim — notably the `data-testid` on
   `integrations.index` (`integrations-refresh`), `dependencies.index`
   (`dependencies-refresh`), `on-call.index` (`oncall-refresh`), and
   `escalation-policies.index` (`policy-refresh`).

## Design reference

No new primitive. This reuses the responsive icon+label button shape already shipped on the
discovery pages and is consistent with the catalog at
[`web/dash0/src/routes/orgs/$org/design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx).
Optionally add this refresh button to the design reference as the canonical
"icon button that reveals its label at `sm`" example so future pages copy it; not required.

## Verification

With `make dev-test` running, spot-check a few pages (`/dash0/orgs/{org}/checks`,
`/incidents`, `/jobs`, `/integrations`, `/status-updates`):

1. At desktop width the refresh button reads **Refresh** (or the localized equivalent) with
   the icon to its left; clicking it still refetches and the icon spins while loading.
2. Narrow the viewport below `sm` (≈640px): the label disappears and the button is icon-only,
   with no leftover left-margin gap.
3. Switching language (fr/de/es) shows the translated label on every converted page
   (`Actualiser` / `Aktualisieren` / `Actualizar`); `status-updates` shows `Refresh`
   regardless (intentional).
4. `checks.$checkUid.index` still shows its label only at `lg` (unchanged).
5. No console warning about a missing i18n key (`common:refresh` resolves everywhere).

## Tests

- `bun run lint` and `bun run build` (tsc) in `web/dash0` pass.
- Where a refresh button has a stable `data-testid`, optionally assert in the relevant E2E
  spec that the accessible name is "Refresh"/localized and that clicking it triggers a
  refetch — but the primary guard is the existing suites continuing to pass, since the
  `onClick`/`disabled`/`data-testid` contract is unchanged. Run `make test-dash`.

## Files referenced

- [`web/dash0/src/locales/en/common.json`](../../web/dash0/src/locales/en/common.json) (+ `fr`/`de`/`es`) — add `refresh` key
- [`web/dash0/src/i18n.ts`](../../web/dash0/src/i18n.ts) — confirms all namespaces are bundled (`common:` prefix resolves anywhere)
- The 12 route files in the Context table — apply the per-button transform
- [`web/dash0/src/routes/orgs/$org/discovery.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx) / [`discovery.$jobUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx) — reference implementation (`sm:` pattern)
- [`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx) — already-responsive exception (`lg:`); do not change

## Implementation Plan

Pre-flight findings (committed baseline on `batch/2026-06-23`): none of the 12 target
buttons are responsive yet — each is `variant="outline" size="icon"` wrapping a `RefreshCw`
with `className={`h-4 w-4 ${… animate-spin …}`}` and no label span. The discovery pages and
`checks.$checkUid.index.tsx` already conform. `common.json` (en/fr/de/es) has a `"search"`
key but no `"refresh"` key. `status-updates.index.tsx` has zero `useTranslation` usage
(confirmed) — stays literal.

1. **Locale key.** Add `"refresh"` immediately after `"search"` in
   `web/dash0/src/locales/{en,fr,de,es}/common.json`:
   en `Refresh`, fr `Actualiser`, de `Aktualisieren`, es `Actualizar`.

2. **Per-button transform** (the 12 files; exact line of each `RefreshCw` confirmed):
   For each header refresh button: drop `size="icon"`; add `aria-label={<key>}` if absent
   (keep existing on `jobs.index`, `incidents.$incidentUid`, `status-updates.index`); add
   `sm:mr-2` to the `RefreshCw` className; append
   `<span className="hidden sm:inline">{<key>}</span>` as the button's last child. Preserve
   `onClick`, `disabled`, and every `data-testid` verbatim.

   | File | `<key>` for label & aria-label |
   |---|---|
   | `checks.index.tsx` (button L818, `onClick={handleRefresh}`) | `t("common:refresh")` |
   | `incidents.index.tsx` (L165) | `t("common:refresh")` |
   | `incidents.$incidentUid.tsx` (L657, already `aria-label={t("actions.refresh")}`) | `t("actions.refresh")` |
   | `events.tsx` (L97) | `t("common:refresh")` |
   | `jobs.index.tsx` (L232, already `aria-label={t("refresh")}`, `onClick={refresh}`, `stats.isRefetching`) | `t("refresh")` |
   | `integrations.index.tsx` (L152, `data-testid="integrations-refresh"`) | `t("common:refresh")` |
   | `dependencies.index.tsx` (L96, `data-testid="dependencies-refresh"`) | `t("common:refresh")` |
   | `on-call.index.tsx` (L94, `data-testid="oncall-refresh"`) | `t("common:refresh")` |
   | `status-pages.index.tsx` (L196) | `t("common:refresh")` |
   | `status-updates.index.tsx` (L354, already `aria-label="Refresh"`, no i18n) | literal `"Refresh"` |
   | `escalation-policies.index.tsx` (L127, `data-testid="policy-refresh"`) | `t("common:refresh")` |
   | `account.tokens.tsx` (L225) | `t("common:refresh")` |

3. **Design reference.** Add the responsive icon+label refresh button as the canonical
   "icon button that reveals its label at `sm`" example to `design-reference.tsx` so the
   catalog stays canonical.

4. **Playwright e2e.** Add a spec under `web/dash0/e2e/` asserting that on a converted page
   with a stable `data-testid` (e.g. `integrations-refresh`) the label text is hidden at a
   mobile viewport (<640px) and visible at a desktop viewport (≥640px), while the button
   stays accessible (aria-label present) in both.

5. **Untouched (verify):** `discovery.index.tsx`, `discovery.$jobUid.index.tsx`,
   `checks.$checkUid.index.tsx` (the `lg:` exception), `badges.tsx`, `server.email-inbox.tsx`.

6. **QA.** `make build-dash0 lint-dash` green; zero NEW dash0 eslint errors vs baseline.
