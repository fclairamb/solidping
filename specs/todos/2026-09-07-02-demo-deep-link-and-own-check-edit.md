---
model: opus
effort: high
---

# The live demo's deep link only works from one URL, loses to an existing session, and refuses edits of the visitor's own checks

## Problem

The shared public live demo ([2026-09-06-02](../done/2026/09/2026-09-06-02-public-live-demo-account.md))
works, but three rough edges make the first minute worse than it should be.

### 1. `?demo=true` only works on the org-scoped login page

The autologin flag is parsed and honoured by `/orgs/$org/login` only
([login.tsx:72-88](../../web/dash0/src/routes/orgs/$org/login.tsx:72),
[login.tsx:455-468](../../web/dash0/src/routes/orgs/$org/login.tsx:455)). It already
accepts `true`, `"true"`, `"1"` and `1`, so `/dash0/orgs/default/login?demo=true` signs the
visitor in. Every other natural entry point drops the flag:

- `/dash0/login?demo=true` — the root login route forwards only `returnTo` in its redirect
  ([login.tsx:9-11](../../web/dash0/src/routes/login.tsx:9),
  [login.tsx:30-35](../../web/dash0/src/routes/login.tsx:30)). The visitor lands on an
  ordinary login form.
- `/dash0/?demo=true` — the index route redirects to `/orgs/$org` with no search at all
  ([index.tsx:8-17](../../web/dash0/src/routes/index.tsx:8)).
- `/dash0/orgs/demo?demo=true` (or any org) — the org layout bounces an unauthenticated
  visitor to the login page with the *whole* URL folded into `returnTo`
  ([$org.tsx:104-111](../../web/dash0/src/routes/orgs/$org.tsx:104)), so `demo` is buried
  inside `returnTo` and never reaches the login page's own search params.

The marketing site cannot reasonably be told "link to `/dash0/orgs/default/login?demo=1`,
and only that". The link that reads naturally — `solidping.io/dash0/login?demo=true` — is the
one that does not work. (Nothing in `web/docs/`, the changelog or this repo publishes the
deep link yet, so there is no existing URL to preserve.)

### 2. An existing session wins over the demo flag

On the org login page the "redirect if already authenticated" effect
([login.tsx:351-355](../../web/dash0/src/routes/orgs/$org/login.tsx:351)) fires before the
autologin effect. A visitor who already holds a session — in their own org `acme`, or even in
the demo itself — and follows a `?demo=true` link is sent to `/orgs/<url org>`:

- session in `acme`, link `/orgs/default/login?demo=true` → lands in `/orgs/default`, never
  in the demo;
- session already in the demo, link `/orgs/default/login?demo=true` → lands on
  `/orgs/default`, which the demo user is not a member of (permission-denied page).

`enterDemo` needs no sign-out step to fix this: it calls the ordinary `login()`
([AuthContext.tsx:426-436](../../web/dash0/src/contexts/AuthContext.tsx:426)), whose
`applyLoginResponse` replaces the stored session and org outright. It simply has to run
*instead of* the authenticated redirect when the flag is present. The unauthenticated
cross-org case ("entering from `acme`'s login page lands in the demo org") already works and
is covered by `e2e/demo-account.spec.ts` (lines 111-122); this spec is about the
authenticated case.

### 3. Editing a check the visitor just created is refused with `DEMO_READ_ONLY`

Reproduce: enter the demo, create an `http` check, open it, click Edit, change the name,
Save. The toast says *"This is the shared read-only live demo. Creating and editing your own
checks is allowed; everything else is not…"*, the form stays open — and the name **was**
changed.

Cause: the edit page saves in two steps
([checks.$checkUid.edit.tsx:123-146](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx:123)).
The `PATCH /checks/{checkUid}` is allowlisted
([demo_guard.go:75](../../server/internal/handlers/auth/demo_guard.go:75)) and passes the
ownership rule ([demo.go:116-127](../../server/internal/handlers/checks/demo.go:116)), so it
lands. Then, unconditionally:

```ts
if (data.connectionUids !== undefined) {
  await setConnections.mutateAsync(data.connectionUids);
}
```

`connectionUids` is *always* defined in edit mode: the form seeds it from the check's
existing bindings ([check-form.tsx:474-476](../../web/dash0/src/components/shared/check-form.tsx:474))
— an empty array for a demo-created check is still an array — and puts it in the submit
payload ([check-form.tsx:925](../../web/dash0/src/components/shared/check-form.tsx:925)). So
every save issues `PUT /api/v1/orgs/{org}/checks/{check}/channels`
([hooks.ts:5302-5311](../../web/dash0/src/api/hooks.ts:5302), route
[server.go:1134-1136](../../server/internal/app/server.go:1134)), which is *deliberately* not
on the demo allowlist (spec 2026-09-06-02 §2). The route guard
([middleware/auth.go:104-108](../../server/internal/middleware/auth.go:104)) answers 403
`DEMO_READ_ONLY`, `apiFetch` toasts it
([client.ts:403-404](../../web/dash0/src/api/client.ts:403)), the mutation throws, and the
`toast.success` + `navigate` at the end of `onSubmit`
([checks.$checkUid.edit.tsx:191-200](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx:191))
never run. Net effect: a *no-op* channel write, refused by design, masks a successful edit
and strands the visitor on the form.

The create page already solved the identical problem twice over — it seeds `[]` for a demo
session so the PUT never fires
([check-form.tsx:455-465](../../web/dash0/src/components/shared/check-form.tsx:455)) and
swallows `DEMO_READ_ONLY` from the secondary write so the primary create still completes
([checks.new.tsx:209-226](../../web/dash0/src/routes/orgs/$org/checks.new.tsx:209)). The edit
page got neither. The dependency mutations further down the same `onSubmit`
([checks.$checkUid.edit.tsx:157-188](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx:157))
are in the same position — not allowlisted — but only fire when the visitor actually changed
dependencies, so they are a smaller instance of the same bug rather than the one being hit.

Note what is **not** broken: the server-side ownership rule and `created_by` plumbing are
correct — the PATCH succeeds. Do not "fix" this by allowlisting `PUT …/channels`; the
original spec excludes it on purpose (binding a visitor's check to the org's notification
sinks is the spam vector the allowlist exists to close).

## Proposal

### A. `?demo=true` works from every entry point

Make the flag survive every redirect that leads to the org login page, so all of these enter
the demo on load (`1` and `true` both accepted, matching the existing coercion):

| URL | Today | After |
|---|---|---|
| `/dash0/orgs/<any>/login?demo=true` | works | works (unchanged) |
| `/dash0/login?demo=true` | plain login form | enters demo |
| `/dash0/?demo=true` | `/orgs/<stored org>` | enters demo |
| `/dash0/orgs/<any>?demo=true` | login form, flag buried in `returnTo` | enters demo |

Concretely:

- Root `/login` ([login.tsx](../../web/dash0/src/routes/login.tsx)): parse `demo` in
  `validateSearch` with the same coercion as the org route and forward it in the
  `Navigate` search.
- Index `/` ([index.tsx](../../web/dash0/src/routes/index.tsx)): parse `demo`; when set,
  redirect to `/orgs/$org/login` with `demo: true` (any org slug will do — the login page
  signs into the configured demo org regardless — use the stored/default org as the root
  login route does) *before* consulting `useAuth().org`.
- Org layout `beforeLoad` ([$org.tsx:104-111](../../web/dash0/src/routes/orgs/$org.tsx:104)):
  when the incoming search carries `demo`, redirect to the login page with `demo: true`
  and **no** `returnTo` — the destination of a demo entry is always the demo org's root, and
  a `returnTo` pointing at another org would be refused by `resolveDestination` anyway
  ([login-destination.ts:43-70](../../web/dash0/src/lib/login-destination.ts:43)). This must
  apply whether or not the visitor is authenticated, which is what makes part B reachable
  from these URLs.
- Document the canonical deep link in `web/docs/` wherever the demo is described (and in
  the changelog entry): `https://solidping.io/dash0/login?demo=true`. The marketing-site
  change itself stays in `solidping-website` (out of scope here, as in the original spec).

### B. The demo flag beats an existing session

In [login.tsx](../../web/dash0/src/routes/orgs/$org/login.tsx), when `demo` is set:

- The "redirect if already authenticated" effect
  ([:351-355](../../web/dash0/src/routes/orgs/$org/login.tsx:351)) must **not** run. The
  flag means "put me in the demo", not "put me wherever my token points".
- If the current session is already the demo principal (`user.isDemo` from
  `AuthContext`), skip the login round-trip and go straight to `/orgs/<demoConfig.orgSlug>`
  — re-entering the demo with a valid demo session must not mint a second session for
  nothing, and must never land on the URL's org.
- Otherwise run `enterDemo()` exactly as today; `applyLoginResponse` replaces the session.
  The visitor's previous session is simply gone — acceptable, since they followed a link
  that says "demo" and the demo banner makes the switch obvious. (Do **not** add a
  confirmation dialog: the whole point of the link is zero clicks.)
- The one-shot `demoAutoLoginStarted` latch stays; the public-config query resolving is
  still a re-render.
- Keep the existing behaviour for the flag-less page: none of the ordinary login paths (the
  form, OAuth, 2FA, org picker, `returnTo`) may change. The two effects racing on
  `isAuthenticated` were made to agree on a destination once already (see the comment above
  `:351`); the new rule is a *third* branch keyed on `demo`, not a rewrite.

### C. Editing your own check in the demo just works

Frontend only; the server stays as is.

1. **Never fire a no-op secondary write.** In the edit page, call `setConnections` only
   when the selected bindings differ from the check's existing bindings (order-insensitive
   set compare against `existingBindings`). This is correct for every user — a save that
   changed nothing about notifications should not `PUT` the bindings — and it is what makes
   the demo case disappear for a check whose bindings are `[]` and stay `[]`.
2. **Do not offer what the guard refuses.** For a demo session, the check form's
   Notifications card ([check-form.tsx:1478-1500](../../web/dash0/src/components/shared/check-form.tsx:1478))
   and the dependency editor render the existing read-only note
   (`components/shared/demo-read-only-note.tsx`) instead of the pickers, in **both** create
   and edit mode. This is the same "politeness, not a security control" principle as
   `lib/demo.ts` ([demo.ts:1-13](../../web/dash0/src/lib/demo.ts:1)): the button whose only
   outcome is a refusal toast is not shown. The escalation-policy select can stay if it
   only feeds the PATCH body; confirm before keeping it.
3. **Degrade the way the create page does.** Even so, wrap the secondary writes on the
   edit page (channels, dependency add/update/remove) the way
   [checks.new.tsx:214-226](../../web/dash0/src/routes/orgs/$org/checks.new.tsx:214) does:
   a `DEMO_READ_ONLY` from a secondary write is swallowed so that `toast.success` and the
   navigation to the detail page still happen after a successful PATCH. Any other error
   still surfaces. `apiFetch` already toasts the refusal itself, so the visitor is told
   *what* was refused without the form dead-ending.

Out of scope, but worth a line in the wiki: allowlisting dependency edges *between two
visitor-owned checks* would be a reasonable, ownership-bounded extension of the demo later.
It is not needed to fix this bug.

### Tests

- `web/dash0/e2e/demo-account.spec.ts`:
  - `?demo=true` on `/login` (root), on `/` and on `/orgs/test` (no `/login`) each land on
    the demo banner. Keep the existing `orgs/test/login?demo=1` case.
  - Sign in as the ordinary test user first, then visit `orgs/test/login?demo=true`: the
    demo banner appears and the URL is the demo org, not `/orgs/test`.
  - While already in the demo, visit `orgs/test/login?demo=true` again: lands in the demo
    org without a second login request (assert on the network, or on the absence of the
    login POST).
  - **The bug itself**: create a check, open Edit, rename it, Save → detail page shows the
    new name, no `DEMO_READ_ONLY` toast, and the notifications/dependency pickers were not
    rendered. Then delete it from the detail page → gone. This is the positive control the
    suite currently lacks: every existing demo test exercises a *refusal* or a *create*,
    none an *edit* of an owned check.
- Unit (`bun run test:unit`): the "bindings unchanged → no PUT" comparison, and the root
  `login.tsx` / `index.tsx` search coercion (`true`, `"true"`, `"1"`, `1`, absent).
- Backend: nothing new is required for correctness — `TestEveryNonGETRouteIsClosedToADemoSession`
  ([demo_guard_route_table_test.go](../../server/internal/app/demo_guard_route_table_test.go))
  must keep passing unchanged, which is the proof that the allowlist did not grow.

## Implementation Plan

### 1. One shared coercion for the `demo` flag (`web/dash0/src/lib/demo.ts`)

Add `parseDemoFlag(value: unknown): true | undefined` — the exact coercion the org
login route already inlines (`true`, `"true"`, `"1"`, `1`, everything else `undefined`),
and `demoFlagFromLocation(search, searchStr)` for the org layout's `beforeLoad`, which
sees the raw parsed location rather than a route-validated search object. Also add
`isDemoReadOnlyError(err)` so the create page's swallow rule and the new edit-page one
cannot drift apart. Unit-tested in `lib/demo.test.ts`.

Rewire `/orgs/$org/login`'s `validateSearch` to call `parseDemoFlag` (behaviour
unchanged, one definition).

### 2. `?demo=true` survives every redirect into the login page

- `routes/login.tsx` — parse `demo` in `validateSearch`, forward it in the `Navigate`
  search alongside `returnTo`.
- `routes/index.tsx` — parse `demo` in `validateSearch`; when set, `Navigate` to
  `/orgs/$org/login` with `demo: true` (stored org, else `default`) **before** reading
  `useAuth().org`, so an existing session cannot steer it.
- `routes/orgs/$org.tsx` `beforeLoad` — after the public-route check (so the login page
  itself never loops), when the incoming location carries `demo`, `redirect` to
  `/orgs/$org/login` with `demo: true` and **no** `returnTo`, whether or not the visitor
  is authenticated.

### 3. The flag beats an existing session (`routes/orgs/$org/login.tsx`)

- Guard the "redirect if already authenticated" effect with `!demoAutoLogin`.
- The auto-login effect waits for `auth.isLoading` to settle, then:
  - `user?.isDemo` → navigate straight to `/orgs/<demoConfig.orgSlug>`, no login POST;
  - otherwise → `enterDemo()` as today (`applyLoginResponse` replaces the session).
  - The one-shot `demoAutoLoginStarted` ref stays and is set in both branches.
- Nothing about the form, OAuth, 2FA, org picker or `returnTo` paths changes.

### 4. Editing your own check in the demo (frontend only)

- `lib/connection-bindings.ts`: `connectionBindingsChanged(selected, existing)` — an
  order-insensitive, duplicate-tolerant set compare. Unit-tested.
- `routes/orgs/$org/checks.$checkUid.edit.tsx`:
  - read the check's current bindings (`useCheckConnections`) and call `setConnections`
    only when `connectionBindingsChanged` says so — correct for every user, and the
    reason the demo case disappears for a `[] -> []` save;
  - wrap the channels write and the whole dependency-sync block so a `DEMO_READ_ONLY`
    is swallowed and the `toast.success` + navigate still run; any other error surfaces.
- `components/shared/check-form.tsx`: for a demo session render `DemoReadOnlyNote` in
  place of `NotifyViaSection` and in place of `DependsOnFormSection`, in **both** create
  and edit mode. `EscalationSelect` stays — it only feeds the PATCH body, which is
  allowlisted, so it is a working control rather than one that ends in a refusal toast.
- `routes/orgs/$org/checks.new.tsx`: reuse `isDemoReadOnlyError` (no behaviour change).

### 5. Docs + changelog

- `web/docs/docs/intro.md`: a short "Live demo" section publishing the canonical deep
  link `https://solidping.io/dash0/login?demo=true`.
- `web/docs/docs/configuration/index.md`: the `SP_DEMO_*` variables, with the same deep
  link and the note that it is off by default.
- `CHANGELOG.md`: an `## Unreleased` → `### Bug Fixes` entry.

### 6. Tests

- `lib/demo.test.ts` — `parseDemoFlag` over `true`/`"true"`/`"1"`/`1`/absent/garbage,
  `demoFlagFromLocation`, `isDemoReadOnlyError`.
- `lib/connection-bindings.test.ts` — unchanged (any order) → no PUT; added, removed,
  duplicates, empty-vs-empty.
- `e2e/demo-account.spec.ts` — `?demo=true` from `/login`, `/` and `/orgs/test`; the
  authenticated-visitor case; the already-in-the-demo case (no second login POST); and
  the positive control: create → edit → rename → save → detail page shows the new name
  with no `DEMO_READ_ONLY` toast and no notification/dependency pickers → delete.
- Backend: nothing changes; `TestEveryNonGETRouteIsClosedToADemoSession` must stay green.
