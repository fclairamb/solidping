---
model: sonnet
effort: medium
---

# `/demo` should enter the live demo when demo mode is enabled

## Problem

The shared public live demo ([2026-09-06-02](../done/2026/09/2026-09-06-02-public-live-demo-account.md))
is entered through a query-string flag. After
[2026-09-07-02](../done/2026/09/2026-09-07-02-demo-deep-link-and-own-check-edit.md)
the flag is honoured on every address a human would naturally write under
`/dash0`, and the canonical published link is
`https://solidping.io/dash0/login?demo=true`
([`web/docs/docs/intro.md:65`](../../web/docs/docs/intro.md),
[`web/docs/docs/configuration/index.md:214`](../../web/docs/docs/configuration/index.md)).

That link is fine to click but poor to *say*, *print* or *type*: it carries an
SPA prefix, a route name and a query string, and one wrong character (`demo=1`
vs `demo=true` is fine, `/dash0/login/?demo` is not) drops the visitor on an
ordinary login form. Every product with a public demo exposes it as a
one-word path — `example.com/demo`. SolidPing has no such path: today `/demo`
is nothing. It misses every explicit mount in `mainGroup`
([`server/internal/app/server.go:2199-2209`](../../server/internal/app/server.go))
and falls through `serveAppRoot`
([`server.go:2717`](../../server/internal/app/server.go)) to the SPA
catch-all, which answers 200 with a shell that has no idea what `/demo` means.

The requirement, as stated: **if the instance has demo mode enabled, `/demo`
must work the same as `/dash0/login?demo=true`.** A redirect is acceptable;
something cleaner is welcome.

## Proposal

### A server-side redirect, gated on demo mode

Add a root-path route `/demo` (and `/demo/`) to `mainGroup`, next to the other
root conveniences (`/llms.txt`, `/docs`,
[`server.go:2195-2205`](../../server/internal/app/server.go)):

- **Demo enabled** (`s.config.Demo.Enabled`,
  [`internal/config/config.go:825`](../../server/internal/config/config.go)):
  answer **`302 Found`** with `Location: /dash0/login?demo=true`. Not a 301 —
  browsers cache permanent redirects, and the shortcut must stop working the
  moment an operator turns the demo off. Emit exactly that target; do not
  forward the incoming query string (a stray `?returnTo=` on a demo link is
  ignored by the auto-login anyway, see the comment in
  [`web/dash0/src/routes/login.tsx:47-51`](../../web/dash0/src/routes/login.tsx)).
- **Demo disabled**: behave exactly as today — delegate to `serveAppRoot` so a
  self-hosted instance without a demo sees no change at all. (A plain 404 is an
  acceptable alternative if delegating is awkward; what must not happen is a
  redirect into a login page that then shows an ordinary form.)

Why a redirect rather than a dash0 route: the flag's whole handling —
`parseDemoFlag`, the three `validateSearch` sites, the org layout's
`beforeLoad`, the session-outranking rule — already lives in one place on the
client ([`web/dash0/src/lib/demo.ts`](../../web/dash0/src/lib/demo.ts)). A 302
reuses all of it unchanged, keeps one canonical entry URL for the SPA, works for
non-browser clients (link previews, `curl -I`), and is testable with a plain
`httptest` recorder without booting the SPA. There is nothing "cleaner" to
gain from a second entry point; the clean part is that `/demo` has no logic of
its own.

Implementation notes:

- The gate is a **request-time** check on the config, not a registration-time
  one, so it needs no special-casing in the router-building path and the
  `Demo.Enabled` toggle stays the single source of truth.
- `handlerWithDocsHost` ([`server.go:2548`](../../server/internal/app/server.go))
  rewrites every non-`/docs` path on the docs host into `/docs/...`, so
  `docs.solidping.io/demo` becomes a docs 404. That is fine and out of scope:
  the shortcut is for the main host.
- Custom status-page hosts serve only status0 paths and 404 the operator
  surfaces ([`custom_domain_routing.go:365`](../../server/internal/app/custom_domain_routing.go),
  `isCustomHostForbidden`). `/demo` on `status.acme.com` currently renders the
  status page in place (the `default:` branch). Add `/demo` to
  `isCustomHostForbidden` so a customer's domain never redirects into the
  SolidPing dashboard — the same reasoning that forbids `/dash0` there.

### Documentation

- [`web/docs/docs/configuration/index.md`](../../web/docs/docs/configuration/index.md)
  "Deep-linking into the demo" table: add `https://solidping.io/demo` as the
  **canonical** row ("use this in marketing copy, docs and emails") and demote
  the `/dash0/login?demo=true` row to "the address the shortcut redirects to".
  Note that the shortcut only exists while `demo.enabled` is on.
- [`web/docs/docs/intro.md:65`](../../web/docs/docs/intro.md) "Try the live
  demo": point the bold link at `https://solidping.io/demo`.
- Do not hand-edit `CHANGELOG.md` — release-please generates it from the PR.

### Tests

- **Backend** — `server/internal/app/demo_shortcut_test.go`, modelled on
  [`llms_txt_test.go`](../../server/internal/app/llms_txt_test.go): with
  `Demo.Enabled` true, `GET /demo` and `GET /demo/` answer 302 with the exact
  `Location`; with it false, the response is **not** a redirect (positive
  control: assert the status is whatever `serveAppRoot` gives an unknown path
  today, so a future regression that starts redirecting unconditionally is
  caught). A custom-host case: `/demo` on a resolved custom domain is 404.
- **E2E** — extend the deep-link table in
  [`web/dash0/e2e/demo-account.spec.ts:115-119`](../../web/dash0/e2e/demo-account.spec.ts)
  with a row for `/demo` (an absolute path; the suite's `baseURL` is
  `/dash0/`, and Playwright resolves `/demo` against the origin). It must land
  on the demo org with the `demo-banner` visible, exactly like the other three
  rows. The suite already runs with the demo enabled in test mode.

## Out of scope

- Any change to the flag's client-side handling; the redirect target is the
  existing canonical link, unchanged.
- A `/demo` on the docs host or on custom domains (both intentionally do
  nothing / 404, see above).
- The marketing site (`solidping-website` repo) linking to the new path — a
  one-line follow-up there once this ships.
