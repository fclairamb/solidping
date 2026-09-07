# The public live demo

A real organization, user and check catalogue on a real instance, signed into with
one click (or one link) so an evaluator can see SolidPing work before installing
anything. Off by default (`SP_DEMO_ENABLED`); user-facing configuration is in
[web/docs → Public Live Demo](../../web/docs/docs/configuration/index.md).

Introduced by spec `2026-09-06-02-public-live-demo-account.md`; the deep-link and
own-check-edit fixes by `2026-09-07-02-demo-deep-link-and-own-check-edit.md`.

## The safety model is a positive allowlist

`server/internal/handlers/auth/demo_guard.go` lists the routes a demo session may
write; everything else answers `403 DEMO_READ_ONLY` from the `RequireAuth` chain
(`server/internal/middleware/auth.go`), matched on the **router pattern**, never
the raw path. `server/internal/handlers/checks/demo.go` adds the ownership rule:
a check with `created_by = NULL` (the seeded catalogue) is editable by nobody, so
immutability needs no "protected" flag.

`server/internal/app/demo_guard_route_table_test.go`
(`TestEveryNonGETRouteIsClosedToADemoSession`) walks every registered non-GET org
route and asserts the guard's own message — an unrelated 403 cannot mask a
regression, and the allowlist cannot grow silently.

**`PUT /orgs/:org/checks/:uid/channels` is excluded on purpose**: binding a
visitor's check to the organization's real notification sinks is the spam vector
the allowlist exists to close. Do not "fix" a refusal by adding it.

## Frontend rule: never offer what the guard refuses

`web/dash0/src/lib/demo.ts` and `components/shared/demo-read-only-note.tsx` are
**politeness, not security** — the server refuses these writes on its own. The
rule is simply that a control whose only possible outcome is a refusal toast is
replaced by the read-only note. That covers the check form's Notifications card
and its dependency editor, in create *and* edit mode.

The escalation-policy picker sits on the same card and is **not** wholesale
hidden — the check's `escalationPolicyUid` travels in the allowlisted PATCH
body, so choosing an existing policy works. One branch of it is the exception:
"No escalation (silent)" `POST`s a zero-step policy to
`/orgs/:org/escalation-policies` when the organization owns none, and the demo
org's single seeded policy has a step. That branch is withheld for a demo
session (`canOfferSilentEscalationShortcut` in `lib/demo.ts`, wired through
`EscalationSelect`'s `canCreatePolicy` prop); it is still offered whenever a
silent policy already exists, because reusing one is a plain selection. The
refusal was doubly invisible before: the picker swallows the create error, so
the selection simply snapped back.

The corollary bit us once: the check edit page issued
`PUT …/channels` on **every** save (the form always carries a `connectionUids`
array in edit mode), so a demo visitor renaming their own check got a
`DEMO_READ_ONLY` toast on a no-op write, the mutation threw, and the
`toast.success` + navigate never ran — while the PATCH had in fact succeeded.
Two independent guards now:

- `lib/connection-bindings.ts` — write the bindings only when they actually
  differ (correct for every user, not a demo special case);
- the create page's swallow rule, generalised as `isDemoReadOnlyError` and
  applied to every **secondary** write on the edit page (channels, dependency
  sync), so a refusal never discards a successful primary write.

## Entering the demo

`?demo=true` (or `?demo=1`) is parsed by one coercion, `parseDemoFlag`, on four
surfaces: `/`, `/login`, `/orgs/$org/login` and the `/orgs/$org` layout's
`beforeLoad`. The layout redirect drops `returnTo` deliberately — a demo entry
always ends at the demo org's root, and `resolveDestination` would refuse a
`returnTo` naming another org anyway. The flag also outranks an existing session:
the login page's redirect-if-authenticated effect stands down, and `enterDemo`
short-circuits when the current principal is already the demo user.

## Possible extension: dependency edges between two visitor-owned checks

Dependency writes (`POST/PATCH/DELETE …/dependencies`) are outside the allowlist,
so a demo visitor cannot link two checks they created themselves. Allowlisting
those **bounded by the same ownership rule the checks handlers already apply** —
both ends `created_by = <the demo visitor>` — would be a reasonable extension: it
adds no spam surface (no notification sink is involved) and it would let the demo
show off cascade rollup, which is one of the more convincing features. It is not
needed to fix anything today; the pickers are simply hidden.
