---
model: sonnet
effort: high
---

# Getting Started steps should offer one-click sensible defaults (magic wand)

## Problem

The "Getting started" checklist (`web/dash0/src/components/dashboard/onboarding-checklist.tsx`)
sends the user to a dedicated page for each incomplete step — and then leaves them alone in
front of a form. For a user who just wants "the sensible thing", every step still demands
decisions: which integration type, which recipients, which frequency, which checks on the
status page.

New orgs created through `CreateOrg` already get seeded defaults (email integration +
weekly report, `server/internal/handlers/auth/service.go:3073` `seedOrgDefaults`), so they
start at 3/5. But three audiences still land on empty pages with no shortcut:

- the bootstrap **default org** (self-hosted installs) and the test org, which bypass
  `CreateOrg` and get no seeding;
- orgs created **before** the seeding shipped (2026-08-28);
- users who deleted or disabled the seeded defaults.

Beyond onboarding, "create the obvious default in one click" is a good empty-state
affordance for these resource pages in general.

## Proposal

Add a **magic wand** action (`Wand2` icon from lucide, `variant="outline"`, mirroring the
existing auto-rebalance precedent at
`web/dash0/src/routes/orgs/$org/checks.scheduling.tsx:481`) on the dedicated page of each
step where a sensible default can actually be derived. Two flavors, chosen deliberately:

- **Direct create** for private, reversible resources whose default is exactly what the
  backend already seeds (alerts, report). One click → resource exists.
- **Prefill only** for the status page: it is a public-facing artifact with a slug, so the
  wand fills the form and the user still clicks Create once.

No new backend endpoints — the wand uses the existing `POST` APIs. No step-completion
storage changes — the checklist already derives completion from real resources
(`web/dash0/src/lib/onboarding-checklist.ts`), so a wand-created resource flips the step
to done through normal query invalidation.

### Step-by-step

**1. `alerts` — wand on `/orgs/$org/integrations` (index page)**

- Button next to the page's primary "New integration" action, label like
  *"Set up email alerts for me"*.
- Visible only when `!hasNotifiableIntegration(integrations)` (reuse the helper at
  `web/dash0/src/lib/onboarding-checklist.ts:58` — do not duplicate the logic).
- One click → `POST /api/v1/orgs/:org/integrations` with
  `{type: "email", name: <localized "Email alerts">, enabled: true, isDefault: true,
  settings: {to: [<current user's email from auth/me>]}}` — the same shape
  `seedOrgDefaults` writes. The visibility gate makes an `isDefault` collision a
  non-issue (the wand never shows when a notifiable integration already exists).
- On success: toast, invalidate the integrations query (checklist step flips), stay on
  the page so the new row is visible.

**2. `report` — wand on `/orgs/$org/organization/report-schedules` (index page)**

- Label like *"Create a weekly uptime report for me"*.
- Visible only when `!hasEnabledReportSchedule(schedules)` (helper at
  `web/dash0/src/lib/onboarding-checklist.ts:83`).
- One click → `POST /api/v1/orgs/:org/report-schedules` via `useCreateReportSchedule`
  (`web/dash0/src/api/hooks.ts:7530`) with `{name: <localized "Weekly uptime report">,
  frequency: "weekly", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  recipients: [<current user's email>], checkUids: [], checkGroupUids: [],
  includeSlos: true, enabled: true}`. Empty scopes = org-wide, matching the seeder;
  the browser timezone is an improvement over the seeder's UTC.
- Same success behavior: toast + invalidate.

**3. `statusPage` — wand inside `/orgs/$org/status-pages/new` (the form)**

- Wand button in the form header, label like *"Prefill for me"*: sets name = org display
  name, slug via the existing auto-`slugify` behavior
  (`web/dash0/src/components/shared/status-page-form.tsx:90`), and attaches **all**
  current checks. Everything stays editable; the user still clicks Create.
- The form currently carries at most one prefilled `checkUid`
  (`web/dash0/src/routes/orgs/$org/status-pages.new.tsx:14`); extend its state to a
  `checkUids: string[]` list (the backend already accepts the array —
  `server/internal/handlers/statuspages/service.go:879`) and render the attached checks
  as removable badges, generalizing the existing single-check badge at
  `status-page-form.tsx:209`.
- Wand hidden once the form already has a name or attached checks differing from empty
  (i.e. it only offers to fill a blank form), and on orgs that already have a status
  page keep it available — a second page is legitimate.

**4. `check` and `team` — deliberately no wand**

- The checklist only renders once the org has ≥1 check
  (`web/dash0/src/components/dashboard/dashboard-page.tsx:436`); the zero-check moment
  is owned by the empty-state hero's quick-create
  (`web/dash0/src/components/dashboard/empty-state-onboarding.tsx:70`). Nothing to wand.
- A teammate cannot be auto-generated: there is no sensible default for *who* to invite,
  and sending an invitation email is an outward-facing action that must stay explicit.

### Shared requirements

- **Permissions**: the wand inherits each page's existing gating — if the page hides or
  disables its "New …" action for the current role, the wand is hidden too. Never render
  a wand that would 403.
- **Idempotency by visibility**: the wand disappears as soon as the derivation helper
  says the step is satisfied; no server-side dedup is added.
- **Pending/failure states**: disable with a spinner while the mutation is in flight;
  surface failures via the standard error toast, honestly (no optimistic "done").
- **Localization**: all wand labels, toasts, and the generated resource names
  ("Email alerts", "Weekly uptime report") in all four locales
  (`web/dash0/src/locales/{en,fr,de,es}`); parity is enforced by `bun run test:unit`.
- **Design reference**: add the wand-button pattern (icon + outline + conditional
  visibility) to `web/dash0/src/routes/orgs/$org/design-reference.tsx` per repo
  convention.
- **Mobile**: buttons wrap correctly on small screens (the index-page headers and the
  status-page form header must not overflow).
- Test ids: `wand-create-email-alerts`, `wand-create-weekly-report`,
  `wand-prefill-status-page`.

### QA

- Unit tests for any new pure helpers (e.g. default-payload builders), colocated like
  `onboarding-checklist.test.ts`.
- Playwright E2E (`web/dash0/e2e/`):
  - integrations page of an org without notifiable integrations shows the wand; click
    creates the email integration addressed to the signed-in user; wand disappears; the
    Getting Started `alerts` step shows done.
  - same flow for the weekly report wand.
  - status-page new form: wand prefills org name + all checks as badges; submit creates
    the page with one resource per check (assert via the API or the page detail).
  - wand absent when the step is already satisfied.
- Existing suites must stay green — in particular `onboarding-checklist.spec.ts` and
  `status-page-from-check.spec.ts` (the single-`checkUid` prefill entry from the check
  detail page must keep working on top of the new list-based form state).

### Non-goals

- No wand inside the checklist card rows themselves — the card's CTAs keep navigating;
  the wand lives where the resource is managed.
- No new backend endpoint, no AI/LLM generation, no retro-seeding of existing orgs.
- No change to `seedOrgDefaults` or to the checklist derivation logic.

## Implementation Plan

1. **Pure helpers** — `web/dash0/src/lib/onboarding-wand.ts`: `buildEmailAlertsWandPayload`,
   `buildWeeklyReportWandPayload`, `buildStatusPageWandPrefill`. Colocated unit tests
   (`onboarding-wand.test.ts`) cover the payload shapes and, per locale bundle, that every
   wand-generated resource name resolves to real text (not a raw i18n key).

2. **Locales** — add a `wand` block to `integrations.json` (all four locales),
   `slos.json`'s existing `reports` object, and `statusPages.json` (`wand` +
   `form.removeCheck`), with real FR/DE/ES translations, not copies of English.

3. **Alerts wand** (`routes/orgs/$org/integrations.index.tsx`) — a second `Button` next to
   "New integration" inside the same `PageHeader.actions`, visible only when
   `!hasNotifiableIntegration(integrations)` (and not still loading). Click calls
   `useCreateIntegration(org).mutate(buildEmailAlertsWandPayload(t, user.email))`;
   `useCreateIntegration` already invalidates `["integrations", org]`, which is the same
   query key the checklist reads, so the step flips without extra plumbing. Disabled +
   spinner while pending; toast on success/failure. `data-testid="wand-create-email-alerts"`.

4. **Report wand** (`routes/orgs/$org/organization.report-schedules.index.tsx`) — same
   pattern: visible only when `!hasEnabledReportSchedule(schedules)`, calls
   `useCreateReportSchedule(org).mutate(buildWeeklyReportWandPayload(t, user.email,
   Intl.DateTimeFormat().resolvedOptions().timeZone))`. `data-testid="wand-create-weekly-report"`.
   `PageHeader` gets `className="flex-wrap"` (matches the integrations page) since it now
   carries two buttons.

5. **Status-page prefill wand** (`components/shared/status-page-form.tsx` +
   `routes/orgs/$org/status-pages.new.tsx`) — the form's `checkUid`-only prop becomes
   internal `checkUids: string[]` state (seeded from a new `initialCheckUids` prop),
   rendered as removable badges (generalizing the single read-only "will include" badge).
   The route fetches the org's full check list via `useInfiniteChecks` (auto-paging exactly
   like `checks.scheduling.tsx`'s rebalance) and passes it down as `allChecks` +
   `allChecksLoaded`, plus `orgName` for the wand to seed the Name field. Wand button lives
   in the form header, visible only in create mode while the form is still blank
   (`name === "" && checkUids.length === 0`); disabled with a spinner until `allChecksLoaded`.
   `data-testid="wand-prefill-status-page"`.

6. **Design reference** — import `Wand2`, add a `magic-wand` entry to `SECTIONS` and a
   `MagicWandSection` component (rendered in `DesignReferencePage`) showing the button
   pattern (icon + outline + conditional visibility) with its import line, mirroring
   `OnboardingChecklistSection`'s structure.

7. **QA** — cheap loop (`tsc`, `eslint` on changed files) after each step; unit tests for the
   new helpers; new Playwright spec(s) covering: wand visible/hidden by derivation state for
   all three flows, alerts + report wand create-and-flip-the-checklist-step, status-page wand
   prefill + submit creates one resource per check; full existing suite green, in particular
   `onboarding-checklist.spec.ts` and `status-page-from-check.spec.ts`. Precondition note: a
   fresh org now seeds a default email integration + weekly report
   (`seedOrgDefaults`), so wand-visibility tests must delete the seeded resource first rather
   than assume an empty org.
