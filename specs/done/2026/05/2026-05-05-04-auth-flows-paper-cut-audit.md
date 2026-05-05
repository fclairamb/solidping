# Auth flows paper-cut audit — single-PR sweep

## Context

`2026-05-05-02` removes the redundant email field from `/invite/$token`. That
fix exposed a likelihood: the rest of the auth surface (`forgot-password`,
`reset-password`, `confirm-registration`, `login`, `no-org`) accumulated
similar small frictions over time. Each one in isolation is too small to
justify a spec; together they're a meaningful onboarding tax.

This spec is one focused pass through every auth route in `web/dash0/src/routes/`,
in one PR, fixing the small things and listing for follow-up the things that
turn out to be larger.

## Routes under audit

Listed as observed in `web/dash0/src/routes/`:

```
auth.slack.complete.tsx
confirm-registration.$token.tsx
forgot-password.tsx
invite.$token.tsx               # covered by 2026-05-05-02
login.tsx                       # redirect shim — see below
no-org.tsx
reset-password.$token.tsx
orgs/$org/login                 # actual login form
```

## Scope — per-route findings

### `login.tsx` — broken redirect default

```tsx
return (
  <Navigate
    to="/orgs/$org/login"
    params={{ org: "test" }}                         // ← suspicious
    search={{ session_expired: false, returnTo: undefined }}
  />
);
```

Hardcodes `org: "test"` as the redirect target. That's the test-mode org slug.
On a non-test deployment, anyone hitting the bare `/login` URL is redirected
to a possibly-nonexistent `test` org login. Should either:
- Pull the default org from a session cookie / last-visited slug in
  localStorage, falling back to a configured default; or
- Redirect to `/orgs/$default/login` where `$default` is `default` in
  prod-mode and `test` in test-mode (read from `useFeatureFlags()` or a
  similar runtime config).

Verify whether this is actually broken in prod before fixing — may be that
nothing links to `/login` anymore and this is dead code, in which case delete.

### `forgot-password.tsx` — pre-fill the email if we know it

The form asks for `email`. If the user got here via an "email expired, please
re-authenticate" flow, we *just* knew their email and lost it. Carry it
forward via query param or session-expiry context so the field can be
pre-filled. If empty, fall back to current behavior. Not a regression risk —
the field stays editable.

Anti-enumeration is the server's job (already documented in the file's
comments) — that's correct, leave it.

### `reset-password.$token.tsx` — covered by spec 05

Has a `password` + `confirmPassword` pair. Removed by `2026-05-05-05`
(confirm-password / show-hide spec). This audit only flags it as touched by
the sibling spec; do not duplicate the change here.

Check during impl: does this route validate the token client-side before
showing the form? If not, a user with a stale link types a password and only
sees the failure on submit. Cheap fix: a `useQuery` against a token-info
endpoint (or treat the existing reset-password mutation as the validator and
swap the form for an "expired" card on its specific error code).

### `confirm-registration.$token.tsx` — already minimal

Zero form fields. Nothing to cut. Verify that the redirect on success goes to
`/orgs/$org` if the user has an org, `/no-org` otherwise. (It does today.)

### `no-org.tsx` — slug field is internal plumbing leaking into onboarding

`CreateOrgCard` shows both **Org name** and **Slug** as required fields, with
the slug auto-derived from the name unless the user has touched it
(`slugTouched`). The slug being visible is honest but adds a field of
friction for users who don't care.

Proposed change:
- Hide the slug behind a small "advanced" disclosure (collapsed by default).
- Show a tiny inline preview of the auto-derived slug under the name field
  ("Will be reachable at solidping.io/orgs/<slug>").
- Keep the manual edit affordance for power users.

`JoinOrgCard` is fine.

### `auth.slack.complete.tsx` — verify, don't presume

OAuth-completion callback. Should be no-form-field, redirect-only. Verify
during audit; if not, list separately for a focused fix.

### `orgs/$org/login` — the actual login form

Audit checklist for the real login page (file path: discover during impl —
likely `routes/orgs/$org/login.tsx` or similar):
- Is the org slug visible and editable, or is it accepted as URL-only? Per
  the project's own CLAUDE.md ("org optional in body" for `POST
  /api/v1/auth/login`), the field should not be required. If the form has an
  org input, demote or hide.
- Does the email field auto-focus on load? If not, fix.
- Are "remember me" and "forgot password" links present and reachable in one
  tab? Standard expectation.
- Social login buttons (if any) — are they prominent enough? Below or above
  the password field?

## Out of scope

- Confirm-password removal — `2026-05-05-05`.
- Magic-link / passwordless flows — rejected per security review (long-lived
  invites can't be safely converted to session-grant magic links).
- 2FA-flow review — separate concern, separate spec when relevant.
- Backend auth changes — none of the above require backend work, *except* the
  pre-fill of forgot-password email which can use existing query-param plumbing.

## Test plan

- [ ] Walk every route listed above as a fresh user. For each, write one line
      about (a) what fields are required, (b) which are pre-fillable, (c)
      what the next CTA is. Reject any field that fails to pull weight.
- [ ] Verify `/login` redirect lands somewhere useful in both prod-mode and
      test-mode runtimes.
- [ ] e2e regression: existing auth tests still pass after the audit cleanup.
- [ ] Manual: hidden slug field on `no-org` doesn't break power users who
      want a custom slug — disclosure expands cleanly, slug edits stick.

## Why bundle these instead of one-spec-per-cut

Each individual cut is below the threshold for an independent PR — reviewing
five tiny PRs is more total work than reviewing one focused sweep with a
clear scope. A single PR also makes it cheap to verify the i18n keys and
auth-CSS stay coherent across all six routes at once.
