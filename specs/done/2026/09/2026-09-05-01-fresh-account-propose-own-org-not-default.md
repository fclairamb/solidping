---
model: opus
effort: high
---

# A freshly created account that no org auto-adopted is steered toward `default` instead of being offered its own organization

## Problem

When an account has just been created and no existing organization adopted it
automatically, the user lands on `/no-org`. Today that surface treats "start
your own org" and "ask an existing org to admit you" as equals, ships the
create form empty, and — on the federated path — actively points the newcomer
at the platform `default` organization. On SaaS, `default` is the operator's
own org (`server/internal/defaults/defaults.go:18`, bootstrapped by
`server/internal/jobs/jobtypes/job_startup.go:226`), so this both confuses the
newcomer and spams the operator with join requests.

Concretely, the three paths a brand-new account can take:

**1. Password registration.** `/login` hardcodes `default` as the org whose
login page every unknown visitor is sent to
(`web/dash0/src/routes/login.tsx:29`), so a SaaS visitor registers on
`/orgs/default/register`. After confirmation, `ConfirmRegistration` runs the
cross-org auto-join, finds no membership, and mints an org-less session with
`LoginActionNoOrg` (`server/internal/handlers/auth/service.go:2427-2443`);
the frontend routes to `/no-org` (`web/dash0/src/lib/confirm-registration-handoff.ts:61-65`).

**2. Federated sign-in (Google / GitHub / OIDC / …) from that same
`/orgs/default/login` page.** The callback resolves the org from the URL slug
(`server/internal/handlers/auth/google.go:38` and siblings) and runs the
admission rules in `JoinOrgViaLogin`. For a stranger, rules 0–5 all miss and
rule 6 fires (`server/internal/handlers/auth/join_policy.go:222-231`):
`ensureMembershipRequestForLogin` creates a `membership_requests` row against
`default` **and notifies its admins**
(`join_policy.go:554-572`), then the browser is redirected to
`/no-org?membershipPending=default` (`join_policy.go:600-611`). The page then
shows "Your sign-in to default succeeded, but that organization hasn't
admitted you yet. A join request was sent to its admins."
(`web/dash0/src/locales/en/auth.json`, `noOrg.membershipPending`). The user
never chose `default` — `/login` did — yet they are now told to wait for the
operator, and the operator's admins get a request from someone who just wanted
to try the product.

**3. `/no-org` itself.** The page renders `CreateOrgCard` and `JoinOrgCard`
side by side with equal weight (`web/dash0/src/routes/no-org.tsx:70-73`), under
the subtitle "Create a new organization or request to join one". The create
form starts empty — `name` and `slug` are both `""`
(`web/dash0/src/components/shared/create-org-card.tsx:50-51`) — and the
backend rejects a missing `slug` outright
(`server/internal/handlers/auth/handler.go:739-742`), so a newcomer has to
invent a company name and a URL slug before they can see a single check. For a
solo user evaluating the product, "Organization name / My Company" is the
wrong question.

Nothing today proposes "an organization just for you". The existing
random-slug machinery (`orgslug.GenerateUnique`,
`server/internal/orgslug/orgslug.go`) is only used by the connector flows
that auto-create orgs (Slack / Discord / OIDC), never by the human create path.

## Proposal

For an account that was **just created** and that no org adopted, the
experience becomes: "Here is an organization for you — click Create" with
`default` nowhere in sight.

### A. `/no-org` leads with a ready-to-create personal org

1. **Pre-fill the create form with a proposal.** `CreateOrgCard` gains an
   optional `suggestedName` prop. `/no-org` passes one computed on the client
   by a new pure helper, `web/dash0/src/lib/org-name-suggestion.ts`:
   - If `user.name` (from `useAuth()`,
     `web/dash0/src/contexts/AuthContext.tsx:73-79`) has a usable first
     token, propose a possessive form built from that first name, e.g.
     `Alice's organization` (i18n key with `{{firstName}}`; French/Spanish/
     German forms come from the locale, not string concatenation).
   - Otherwise (registration's `name` is optional —
     `web/dash0/src/routes/orgs/$org/register.tsx:41`), propose a random
     friendly two-word name from a small in-repo word list (adjective +
     animal/object, e.g. `Bright Falcon`). Keep the list short, neutral and
     brand-safe; no real company or person names.
   - The proposal is a *default value*, not a lock: the user can still edit
     the name, and the Advanced slug toggle keeps working.
   - The helper is unit-tested (`org-name-suggestion.test.ts`): first-name
     extraction (single token, multi-token, leading/trailing spaces, empty,
     non-Latin), and that the random fallback is deterministic when given a
     seed so tests are stable.

2. **Let the server pick the slug when the user did not.** Make `slug`
   optional in `POST /api/v1/orgs`
   (`server/internal/handlers/auth/service.go:2961-2964`, handler at
   `handler.go:739-742`): when empty, `CreateOrg` calls
   `orgslug.GenerateUnique(ctx, s.db, req.Name)` and uses the result. An
   explicitly supplied slug keeps today's strict validation and 409 on
   collision. `CreateOrgCard` sends `slug` only when the user opened Advanced
   and touched it (`slugTouched`); otherwise it omits it and the preview line
   reads "Will be reachable as `alice`" using the same base the server will
   start from (the numeric suffix, if any, is only visible after creation —
   the card already navigates to `result.slug`, `create-org-card.tsx:82`).
   Update `openapi.yaml` (`slug` no longer required on the create request)
   and the generated client.

3. **Reorder the page for the fresh-account case.** Put "Start a new
   organization" first and full-width as the primary action; move "Join an
   existing organization" below it in a collapsed / secondary treatment
   (a disclosure or an outlined card with a muted heading). Change the
   subtitle so it no longer offers the two as equal choices — something like
   "We've set up an organization for you. Create it, or join an existing
   one below." The Join card must not pre-fill or hint at any slug; its
   placeholder stays the generic `acme`.

   Check the design reference (`/dash0/orgs/default/design-reference`) for
   the disclosure / secondary-card pattern before adding one; add it there
   if it doesn't exist.

4. **Locales.** Every new key goes into all four locales (`de`, `en`, `es`,
   `fr`) and is covered by a `*-locales.test.ts` in the style of
   `web/dash0/src/lib/operator-notifications-locales.test.ts`, so a missing
   translation fails `bun run test:unit` rather than shipping as a raw key.

### B. Do not point a brand-new account at `default`

5. **Federated first sign-in from the platform default org, SaaS mode.** In
   `JoinOrgViaLogin` rule 6 (`join_policy.go:222-231`), when *all* of the
   following hold:
   - the deployment mode is SaaS (`config.DeploymentModeSaaS`,
     `server/internal/config/config.go:73`),
   - the org is the platform default (`org.Slug == defaults.Organization`),
   - the user was created by this very callback (the connector's
     find-or-create returned a fresh user — thread a flag through
     `LoginOption`, e.g. `WithNewlyCreatedUser()`),

   then **skip** `ensureMembershipRequestForLogin` (no `membership_requests`
   row, no admin notification) and return the "pending" outcome as a plain
   org-less session. `finishProviderCallback` / `pendingMembershipRedirect`
   must then send the browser to `/no-org` **without** `membershipPending`,
   so the alert that names `default` never renders. The user sees the
   pre-filled "your organization" card from section A instead.

   Self-hosted installs are deliberately untouched: there `default` is
   typically the one real org, and a colleague's Google sign-in should keep
   producing a join request for its admins. An existing account (not created
   in this callback) that deliberately signs in on `/orgs/default/login` also
   keeps today's behaviour.

6. **`/login`'s hardcoded `default` fallback stays**
   (`web/dash0/src/routes/login.tsx:29`). It is the host of the login page
   and is out of scope here — but the register page reached through it must
   not present `default` as the org the user is joining. Audit
   `web/dash0/src/routes/orgs/$org/register.tsx` for any copy that names the
   URL org and neutralise it (today it only uses `org` for links, which is
   fine).

### Tests

- **Go, `join_policy`**: table test for rule 6 — SaaS + `default` + newly
  created user → no request row, no notification, pending=true; each of the
  three conditions flipped individually → request row created as today
  (positive controls, so the guard cannot silently over-match).
- **Go, `CreateOrg`**: empty slug → slug generated from name via
  `GenerateUnique`, unique across a collision (second create with the same
  name yields `alice2`); explicit invalid slug still 422; explicit taken slug
  still 409; empty name still 422.
- **dash0 unit**: `org-name-suggestion.test.ts` and the locales test above.
- **Playwright** (extend `web/dash0/e2e/create-org.spec.ts`, which already
  mints a zero-org identity via `POST /api/v1/test/users`): a fresh user with
  a name lands on `/no-org` with the name field pre-filled with the
  possessive form, the Join card is secondary, the page contains no text
  `default`, and clicking Create with nothing typed lands on a working
  dashboard. A second spec with an unnamed user asserts a non-empty random
  proposal. Keep `confirm-registration-no-org.spec.ts`,
  `zero-org-session.spec.ts`, `membership-requests.spec.ts` and
  `account-organizations-create.spec.ts` green — the account-section create
  flow (`/orgs/$org/account/organizations/new`) must **not** get a
  pre-filled name; only `/no-org` passes `suggestedName`.
- Run the **full** e2e suite as the gate, not just the touched files.

### Open questions / decisions for the implementer

- Should the email local part be a middle fallback before the random name
  (`alice.smith@acme.com` → `Alice's organization`)? Default: **no** —
  first name from `user.name`, else random. Keep it simple unless the
  random names read badly in practice.
- Whether the Join card should be hidden entirely (rather than collapsed)
  when the user has no pending requests. Default: collapsed but present —
  invited colleagues still need it.
- The federated gate in B.5 is the one behavioural change with a blast
  radius outside the newcomer flow. If the implementer finds a cleaner
  signal than "user created in this callback" (e.g. the callback was started
  from `/login`'s fallback rather than a deliberate `/orgs/default/login`
  link), prefer it — but the SaaS-only and default-org-only conditions are
  non-negotiable.

## Implementation Plan

### 1. Backend — optional slug on `POST /api/v1/orgs`

- `server/internal/handlers/auth/service.go` `CreateOrg`: when `req.Slug == ""`,
  derive it with `orgslug.GenerateUnique(ctx, s.db, req.Name)` (already collision-safe)
  and skip the availability check for that generated value. A non-empty slug keeps
  today's `orgslug.IsValid` → `ErrInvalidOrgSlug` (422) and `GetOrganizationBySlug`
  → `ErrOrgSlugTaken` (409) path untouched.
- `server/internal/handlers/auth/handler.go` `CreateOrg`: drop the
  "Slug is required" validation; `Name` stays required (422).
- `server/internal/app/openapi/openapi.yaml`: `CreateOrgRequest.required` becomes
  `[name]`; document that an omitted slug is derived from the name. Regenerate
  `server/pkg/client/client_generated.go` via `go generate ./pkg/client/...`.
- Tests (`server/internal/handlers/auth/`): empty slug → generated from name;
  a second create with the same name yields the `…2` variant; explicit invalid
  slug still 422; explicit taken slug still 409; empty name still 422.

### 2. Backend — do not open a join request against the platform default for a brand-new SaaS account

- `join_policy.go`: new `LoginOption` `WithNewlyCreatedUser()` setting
  `loginOptions.newlyCreatedUser`.
- New predicate `suppressDefaultOrgJoinRequest(org, opts)` — true only when all
  three hold: `s.fullCfg.Deployment.Mode == config.DeploymentModeSaaS`,
  `org.Slug == defaults.Organization`, and `opts.newlyCreatedUser`.
- Rule 6 consults it: when true, log and return `(nil, true, nil)` **without**
  calling `ensureMembershipRequestForLogin` (so no `membership_requests` row and
  no admin notification). Otherwise unchanged.
- `ProviderLoginResult` gains `PendingOrgSlug string` — the org to name on the
  no-org screen, empty when the pending outcome must stay anonymous.
  `CompleteOrgLogin` sets it to `org.Slug` normally and `""` when suppressed.
- `pendingMembershipRedirect` omits the `membershipPending` query param when the
  slug is empty; `finishProviderCallback` call sites pass `result.PendingOrgSlug`.
- Thread "this callback created the user" through every federated connector:
  `findOrCreateUser` returns `(user, created, err)` in google / github / gitlab /
  discord / microsoft / oidc / saml / slack, and each `HandleCallback` appends
  `WithNewlyCreatedUser()` when created. Each provider result struct carries the
  new `PendingOrgSlug`. LDAP calls `JoinOrgViaLogin` with no options, so it is
  inert there by construction.
- Tests: a table over (SaaS?, default org?, newly created?) — only the all-three
  case suppresses; each condition flipped individually is a positive control that
  still creates the request row. Plus a redirect test that the suppressed case
  produces a `/dash0/no-org` URL with no `membershipPending`.

### 3. dash0 — `org-name-suggestion.ts`

- New pure helper `web/dash0/src/lib/org-name-suggestion.ts`:
  - `firstNameOf(name)` — trim, collapse whitespace, first token, `null` when the
    input is empty/whitespace or the token holds no letter or digit. Unicode-aware
    so non-Latin names work.
  - a short, neutral, brand-safe adjective + noun word list.
  - `randomOrgName(seed?)` — deterministic for a given numeric seed.
  - `suggestOrgName(userName, seed?)` returns a discriminated union
    `{ kind: "personal", firstName }` | `{ kind: "random", name }`, so the caller
    (not the lib) owns i18n.
- `org-name-suggestion.test.ts`: single token, multi-token, leading/trailing
  spaces, empty/undefined, punctuation-only, non-Latin; random fallback is
  deterministic per seed, varies across seeds, and always slugifies to a valid
  org slug base.

### 4. dash0 — pre-filled create card

- `CreateOrgCard` gains `suggestedName?: string`: `name` state initialises to it
  and `slug` to `slugify(suggestedName)`. Only `/no-org` passes it; the
  account-section route keeps an empty form.
- Submit sends `slug` **only** when `slugTouched`; otherwise the field is omitted
  and the server derives it. `useCreateOrg`'s mutation input becomes
  `{ name: string; slug?: string }`.
- The preview line keeps showing `slugify(name)`, which is exactly the base
  `orgslug.Slugify` will start from server-side, so preview and reality agree.

### 5. dash0 — `/no-org` reordering

- New `web/dash0/src/components/ui/secondary-disclosure-card.tsx`: an outlined
  card with a muted, always-rendered `<h2>` heading plus a description, and a
  ghost expand/collapse trigger driving a Radix `Collapsible`. The heading stays
  outside the trigger so it remains a real heading for assistive tech (and for
  the existing e2e role queries).
- Added to the design reference (`design-reference.tsx`) with its import line and
  a nav entry, so the catalog stays canonical.
- `/no-org` renders `CreateOrgCard` full width first, then the join form inside
  `SecondaryDisclosureCard`, collapsed by default. Subtitle copy changes to
  "We've set up an organization for you…". The join slug placeholder stays `acme`.

### 6. Locales + locales test

- New/changed keys in all four locales: `auth:noOrg.subtitle` (reworded),
  `auth:noOrg.joinExpand`, `auth:noOrg.joinCollapse`,
  `auth:createOrg.suggestedPersonal` (`{{firstName}}` interpolated, possessive
  form written per language, never concatenated).
- `web/dash0/src/lib/org-name-suggestion-locales.test.ts` in the style of
  `operator-notifications-locales.test.ts`: every key present and non-empty in
  `de`/`en`/`es`/`fr`, `suggestedPersonal` carries `{{firstName}}` in each, and
  the non-English bundles are actually translated.

### 7. E2E

- Extend `web/dash0/e2e/create-org.spec.ts` with two tests: a named fresh user
  sees the possessive proposal pre-filled, the join card collapsed/secondary, no
  occurrence of `default` on the page, and Create-with-nothing-typed lands on a
  working dashboard; an unnamed fresh user sees a non-empty random proposal.
- `confirm-registration-no-org.spec.ts`, `zero-org-session.spec.ts`,
  `membership-requests.spec.ts` and `account-organizations-create.spec.ts` keep
  their heading assertions working by construction (see §5).
