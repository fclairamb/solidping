---
model: opus
effort: high
---

# The dead `web/dash` app is still built, embedded and served as the catch-all, while the real dashboard hides behind `/dash0/` and the status page behind `/status0/`

## Problem

Three single-page apps are compiled into the binary, but only two of them are
alive:

| App | Source | Embed dir | Mounted at | Status |
|---|---|---|---|---|
| dash (legacy) | `web/dash` (213 MB on disk incl. `node_modules`) | `server/internal/app/res` | `/*path` catch-all | dead — `CLAUDE.md:7` says "do not use `web/dash` for current development" |
| dash0 | `web/dash0` | `server/internal/app/dash0res` | `/dash0/` | the dashboard |
| status0 | `web/status0` | `server/internal/app/status0res` | `/status0/` | the public status page |

The legacy app costs a full build stage everywhere a build happens, and it is
what a visitor gets on any unmatched URL:

- **Go**: `//go:embed all:res` at `server/internal/app/server.go:172`;
  `mainGroup.GET("/*path", s.serveAppRoot)` at `server.go:2245` falls through to
  `serveAppStatic` (`server.go:2898`), which serves the legacy `res/index.html`
  for every path that is not `/`, not `/api/…`, and not one of the named
  mounts. A typo'd URL renders the *old* dashboard shell.
- **Makefile**: `build` (line 77) runs `build-dash` + `copy-dash` (lines
  108–118); `DASH_DIR`, `DASH_DIST`, `BACK_RES` (lines 47–56); `dev-dash`
  (line 404).
- **Dockerfile**: stage 1a `dash-builder` (lines 1–22) and
  `COPY --from=dash-builder … ./internal/app/res` (line 126).
- **CI**: the `dash` job (`.github/workflows/ci.yml:167–198`, lint + build +
  `dash-dist` artifact), consumed by `build` (line 322, download at 333–337)
  and gating `ci` (line 615); the `internal/app/res` placeholder at line 112.
- **docker-compose.yml:20–32**: a `dashboard` service mounting `./web/dashboard`,
  a directory that does not exist.
- `.gitignore:45` (`server/internal/app/res/`), `wiki/conventions/frontend-urls.md`
  (still documents `/dash/orgs/{orgSlug}/…`, indexed at `wiki/README.md:76`).

Meanwhile the two real apps carry a `0` suffix that only ever meant "the
rewrite, not the original". With the original gone the suffix is noise in every
URL a user sees, types, or pastes: `solidping.io/dash0/orgs/acme/checks`,
`status.acme.com` → `/status0/acme/status`. The prefixes are also spelled out
by hand in a lot of places rather than derived from one source of truth:

| Where | Count |
|---|---|
| Go, non-test (46 files: auth handlers, notifications, integrations, MCP, emails, jobs, uptime report, `pkg/client` generated code) | 110 |
| Go, tests | 222 |
| `web/dash0/src` (25 files, mostly tests of basepath-aware helpers) | 127 |
| `web/dash0/e2e` (34 files, e.g. `page.goto("/dash0/orgs/test/incidents")`) | 145 |
| `web/status0/e2e` + `src` | 72 |
| `web/docs/docs` | 27 |
| `wiki/` | 52 |

## Proposal

Remove the legacy app entirely, mount dash0 at **`/d/`** and status0 at
**`/s/`**, keep the old prefixes as permanent redirects, and make the two
prefixes a single constant on each side of the wire.

### 1. Delete `web/dash` and everything that builds or serves it

- `git rm -r web/dash`; drop `.gitignore:45`.
- Go: delete `resFiles` (`server.go:172–173`) and `serveAppStatic`
  (`server.go:2898–2945`). `serveAppRoot` (`server.go:2748`) keeps its
  `/api/` JSON-404 branch, its `/` → dashboard redirect and the
  config-driven `Server.Redirects` proxy loop, but the final fallback becomes
  a plain `404 Not Found` (`text/html`, no SPA shell). The config test cases
  that proxy prefix `/` (`server/internal/config/config_test.go:56`) still
  hold — the loop runs before the 404.
- Makefile: remove `DASH_DIR`/`DASH_DIST`/`BACK_RES`, `build-dash`,
  `copy-dash`, `dev-dash`, and drop the two from the `build` chain and the
  `.PHONY` line. `kill` (line 74) may stop killing `:5173` if nothing else uses it.
- Dockerfile: delete stage 1a and line 126.
- CI: delete the `dash` job, remove it from `build.needs` and `ci.needs`,
  drop the `dash-dist` download step and the `internal/app/res` placeholder.
- `docker-compose.yml`: delete the dead `dashboard` service and its
  `dashboard_node_modules` volume.
- `CLAUDE.md:7`: drop the "do not use `web/dash`" clause.
  `wiki/conventions/frontend-urls.md`: rewrite the `/dash/…` examples to
  `/d/…`; `README.md:276–277` already names only dash0/status0.

### 2. One constant per prefix, on each side

**Backend** — add to `server/internal/config` (or a tiny `server/internal/spapaths`
package if config would create an import cycle):

```go
// DashboardBasePath / StatusBasePath are the URL prefixes the two embedded
// SPAs are mounted at. Every URL the server hands to a human — emails, chat
// notifications, OAuth handoffs, MCP resources, the /demo shortcut — is built
// from these, never from a literal.
const (
    DashboardBasePath = "/d"
    StatusBasePath    = "/s"
)
```

Sweep the 46 non-test Go files listed by
`grep -rl -E '"/dash0|/dash0/|"/status0|/status0/' server --include='*.go' | grep -v _test.go`
to build from the constants. Notable anchors:

- `server.go:2214–2224` (mounts), `2723` (`demoShortcutLocation`), `2760`
  (root redirect), `2968`/`3030` (`TrimPrefix`), `2213` and `2222` comments.
- `custom_domain_routing.go:27–28` (`routeStatus0`, `routeDash0`) — see §4.
- `org_slug_redirect.go:12–21`: the positional indexes stay valid because
  `/d` and `/s` are still exactly one path segment (`/d/orgs/<slug>` → org at
  index 3, `/s/<org>/<page>` → org at index 2). Say so in the comment.
- `status0_meta.go:22` (`ogDefaultImagePath`).
- `mcp/handler.go:183` (`dashboardMCPPath`).
- `handlers/auth/{github,gitlab,google,microsoft,oidc,saml}.go:44` and
  `slack.go:90`, `device_service.go:47`, `join_policy.go:39,48`,
  `membership_requests.go:418`, `service.go:2403,2704`.
- `handlers/emailpreview/fixtures.go`, `notifications/*.go`,
  `integrations/{slack,discord,telegram}/…`, `jobs/jobtypes/job_escalation_step*.go`,
  `uptimereport/report.go`, `support/mirror.go`, `customdomain/alert.go`,
  `oauth/authorize.go`, `system/operator_notifications.go:240`,
  `members/provisioning.go:222`, `cmd/membench/scenario.go:213`.
- `server/internal/app/openapi/openapi.yaml` carries the prefixes in
  descriptions/examples (that is why `pkg/client/client_generated.go` matches);
  edit the YAML and re-run `go generate ./pkg/client/...`.

**Frontend** — both apps already read `import.meta.env.VITE_BASE_URL` /
`BASE_URL` for routing; only the *default* is hard-coded:

- `web/dash0/vite.config.ts:18` → `"/d/"`; `web/status0/vite.config.ts:15` → `"/s/"`.
- `web/dash0/public/manifest.webmanifest:5` (`start_url`), `web/dash0/index.html:7–8`
  comments, `web/dash0/src/main.tsx:162` (`register("/dash0/sw.js")` → build
  from `import.meta.env.BASE_URL`), `web/dash0/public/sw.js:17,21` (the
  default click URL and the icon path — derive from `self.registration.scope`
  so the file stays base-agnostic; it lives in `public/`, Vite does not
  rewrite it).
- The 25 `web/dash0/src` files: almost all are unit tests passing a literal
  basepath into basepath-aware helpers (`login-destination.test.ts`,
  `oauth-handoff.test.ts`, `analytics.test.ts`, `attribution.test.ts`, …) plus
  `design-reference.tsx`, `logo.tsx`, `step-target-row.tsx`, `login.tsx`,
  `checks.index.tsx`, two escalation routes. Mechanical, but read each — a
  test asserting a `/dash0`-anchored string is asserting the *shape*, not the
  literal, and should use a `BASE` constant after the sweep.
- Dev: Makefile `dev`/`dev-test`/`dev-saas` (lines 308, 314, 330) set
  `SP_REDIRECTS="/dash0:localhost:5174/dash0,/status0:localhost:5175/status0"`;
  becomes `/d:localhost:5174/d,/s:localhost:5175/s`. The Vite dev servers
  serve under their `base` so the proxy target path moves with it.

### 3. Old prefixes become permanent redirects

Links that already exist in the world must not break: emails sent last month,
bookmarks, Slack/Teams/Telegram messages, the marketing site, `llms.txt`,
search engines, and the OAuth handoff pages the SPA lands on.

- `GET /dash0`, `/dash0/*path` → `301` to `/d` + same path; `GET /status0`,
  `/status0/*path` → `301` to `/s` + same path. Query string preserved
  verbatim (`?demo=true`, `?returnTo=…`, `#bt=` fragments never reach the
  server so nothing to do there). `redirectRenamedOrgSPA` runs *after* the
  prefix redirect — one hop per concern, and the old-slug hop lands on the
  new prefix.
- `/` → `302 /d/` (was `/dash0/`).
- `/demo` → `302 /d/login?demo=true` (`server.go:2723`), and the docs' "Try
  the live demo" text that quotes the canonical link.
- Unaffected, and must stay byte-identical: `/embed/v1/widget.js`
  (`server.go:2218–2220`, frozen public contract), `/docs`, `/openapi`,
  `/llms.txt`, `/metrics`, the PostHog proxy path, `/api/**`.

### 4. Custom status-page domains

`handlerWithCustomDomains` runs ahead of the main router and switches on
prefixes (`custom_domain_routing.go:340+`):

- The `/status0` branch (asset-or-SPA-shell with the `sp-page` bootstrap tag)
  becomes the `/s` branch, byte-for-byte the same logic; `/status0/*` on a
  custom host `301`s to `/s/*` on the same host.
- The deny-list (`routeDash0`, `routeDemo`, `routeDocs`, `routeMetrics`) gains
  `/d` **and keeps `/dash0`**: a customer's status domain must never redirect
  a visitor into the SolidPing dashboard, not even via the legacy hop.
  `custom_domain_routing_test.go` gets both cases.

### 5. Web Push: the service worker scope moves

This is the one non-mechanical piece. Push subscriptions are bound to a
service-worker *registration*, and a registration is keyed by scope. Today
the SW is registered from `/dash0/sw.js` with scope `/dash0/`
(`main.tsx:162`); `useWebPushSubscription.ts:31` uses
`navigator.serviceWorker.ready`, which only resolves for a registration whose
scope covers the *current page*. A page at `/d/…` does not match scope
`/dash0/`, so after the move every existing push subscription is orphaned and
`ready` never resolves until a new registration exists.

Browsers also refuse a service-worker script that answers with a redirect, so
the `301` from §3 does not update the old worker — it silently keeps it.

Required behaviour:

1. On boot at `/d/`, before registering the new worker, enumerate
   `navigator.serviceWorker.getRegistrations()` and `unregister()` every one
   whose `scope` pathname is `/dash0/`.
2. Register `${BASE_URL}sw.js`.
3. If the server has a Web Push subscription on file for this browser/user
   but the new registration's `pushManager.getSubscription()` is `null`,
   re-subscribe and `POST` the new subscription (the existing endpoint
   `useWebPushSubscription` already calls) so the server-side row is
   replaced, not duplicated. Check how `notifications/webpush.go` keys stored
   subscriptions (by endpoint URL, presumably) and delete the stale one.
4. Playwright: a test that seeds a `/dash0/`-scoped registration, loads `/d/`,
   and asserts exactly one registration remains, scoped `/d/`.

### 6. Tests, docs, wiki

- **Go tests** (222 refs): sweep to the constants; `custom_domain_routing_test.go`,
  `org_slug_redirect` tests and the `server.go` route tests gain the
  `301`-preserves-path-and-query cases and the "`/dash0/sw.js` is not the
  live worker" case.
- **dash0 e2e** (145 refs / 34 files): `playwright.config.ts:41`
  `baseURL` → `http://localhost:4000/d/`. Specs use absolute paths
  (`page.goto("/dash0/orgs/test/…")`, 49 gotos), so `baseURL` alone changes
  nothing — export a `DASH_BASE` from the e2e fixtures and sweep. Same for
  `web/status0/e2e` (`${BASE}/status0/test` → `${BASE}/s/test`, 71 refs).
- **dash0 unit tests**: `bun run test:unit` — the locale/enum trap in the
  `/implement-todos` QA table applies here too; run it.
- **docs** (`web/docs/docs`, 27 refs): rewrite the paths;
  `web/docs/docs/changelog.md` is generated from `CHANGELOG.md` — leave history
  alone, only new entries use `/d` and `/s`. Add a one-line note in the
  docs that `/dash0` and `/status0` keep redirecting.
- **wiki** (52 refs): sweep, and rewrite `wiki/conventions/frontend-urls.md`.
- **CHANGELOG**: a `feat` entry for the new paths + redirects and a `chore`
  for the legacy removal (see `wiki/conventions/changelog.md`).

### Out of scope

- Renaming the source directories (`web/dash0`, `web/status0`), the embed
  directories (`dash0res`, `status0res`), the Makefile targets
  (`build-dash0`, …) or the CI jobs. The user-visible URL is the deliverable;
  internal names can follow in a separate mechanical spec.
- The k8xp ingress and the `solidping-website` repo link to `/dash0/`; the
  redirect in §3 covers them until they are updated on their own schedule.
- Custom-domain routing semantics beyond the prefix rename.

### Open questions

- **Route-segment collisions**: `/d` vs `/docs`/`/demo` and `/s` vs anything
  are distinct segments for the router (`/d` and `/d/*path` are registered,
  not a prefix match), but confirm no existing top-level route already owns
  the bare `d` or `s` segment before mounting.
- **Should `/status0` on a *custom host* redirect or silently alias?** §4
  proposes a `301` for consistency; aliasing (serve both, no hop) is also
  defensible since the canonical URL there is `/`.
- **Retention of the `301`s**: permanent, no sunset — the cost is two route
  registrations. Revisit only if the prefixes ever need to be reused.

## Resolved open questions

> **Route-segment collisions**: `/d` vs `/docs`/`/demo` and `/s` vs anything are
> distinct segments for the router (`/d` and `/d/*path` are registered, not a
> prefix match), but confirm no existing top-level route already owns the bare
> `d` or `s` segment before mounting.

**Resolved — verified, no collision. Mount `/d` and `/s` as specified.** The
complete set of top-level segments registered on `mainGroup`
(`server/internal/app/server.go:2195–2245`) is: `api` (via the `/api/v1` group,
which every other group hangs off), `health`, `metrics`, `ingest`
(`config.PostHogProxyPath`), `docs`, `demo`, `dash0`, `status0`, `embed`,
`openapi`, `openapi.yaml`, `llms.txt`, `llms-full.txt`, `unsubscribe`. Nothing
owns the bare `d` or `s` segment. The custom-host deny-list is segment-safe too
— `isCustomHostForbidden` (`custom_domain_routing.go:437–443`) matches
`reqPath == route || strings.HasPrefix(reqPath, route+"/")`, so adding `/d`
there cannot swallow `/docs` or `/demo`. **Keep that exact-or-slash idiom when
adding the new prefixes** — a bare `strings.HasPrefix(reqPath, "/d")` would
deny `/docs` and `/demo` on custom hosts and is the one way this goes wrong.
Add a regression test asserting `/docs` and `/demo` still route as before once
`/d` is in the deny-list.

> **Should `/status0` on a *custom host* redirect or silently alias?** §4
> proposes a `301` for consistency; aliasing (serve both, no hop) is also
> defensible since the canonical URL there is `/`.

**Resolved — `301` redirect, as §4 proposes.** On a resolved custom host,
`/status0` and `/status0/*` answer `301` to `/s` / `/s/*` on the **same host**,
preserving path and query verbatim. There is exactly one code path serving the
status SPA on a custom host (the `routeStatus0` branch in `serveCustomHost`
becomes the `/s` branch, byte-for-byte); do **not** keep the branch matching
both prefixes. Rationale: consistency with §3's redirect on the installation's
own host, no duplicated asset-exists/`sp-page`-bootstrap logic to maintain on
two prefixes forever, and no two URLs serving identical content on the
customer's domain. `custom_domain_routing_test.go` covers both the redirect
(path + query preserved, same host) and the deny-list case from §4 (`/dash0`
and `/d` stay 404 on a custom host, never a redirect into the dashboard).

> **Retention of the `301`s**: permanent, no sunset — the cost is two route
> registrations. Revisit only if the prefixes ever need to be reused.

**Resolved — already settled in the question itself: permanent, no sunset.**
Do not add a deprecation date, a feature flag, or a config toggle for the
legacy-prefix redirects.
