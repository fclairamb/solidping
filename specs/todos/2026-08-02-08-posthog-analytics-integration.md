---
model: opus
effort: high
---

# No product analytics: PostHog is not wired into either the backend or the dashboard

## Problem

SolidPing has no product-analytics instrumentation at all.

- **Frontend**: `web/dash0/index.html` carries only the favicon/manifest/theme
  bootstrap; `web/dash0/src/routes/__root.tsx:25` renders `<Outlet /> + <Toaster />`
  and nothing else. A repo-wide grep for `posthog|plausible|umami|segment|gtag`
  across `web/dash0` returns zero hits.
- **Backend**: the only telemetry is operator-facing — OTel
  (`server/internal/config/config.go:172`) and Sentry (`:238`). There is no
  product-event pipeline (signup, check created, integration connected, …).

Two structural gaps make this non-trivial:

1. **There is no public config endpoint the SPA can read at boot.** System
   parameters are served by `GET /api/v1/system/parameters`
   (`server/internal/app/server.go:1028`), which is super-admin gated. The only
   unauthenticated startup reads today are `/api/v1/auth/providers`
   (`server.go:610`), `/api/v1/check-types`, `/api/v1/regions` and
   `/api/mgmt/version`. The dashboard needs the PostHog project key *before*
   login to be useful, so something public has to expose it.
2. **Self-hosted operators must not be opted in silently.** SolidPing ships as a
   self-hostable binary; sending product events from someone else's deployment
   without them configuring it is unacceptable. The feature must be strictly
   off unless credentials are present.

## Proposal

Add a PostHog integration, backend **and** frontend, that is **entirely inert
unless credentials are configured**. No credentials → no client instantiated, no
script loaded, no network call, no behavioural change.

### 1. Configuration surface

Register the keys in the canonical parameter registry —
`server/internal/systemconfig/systemconfig.go` (key constants `:28-160`,
`getKnownParameters()` `:190`) — so they inherit the existing
**env > db > defaults** overlay (`Service.Initialize` `:1165`) and are covered by
the unknown-`SP_*` warning in `server/main.go:104`.

| Parameter key | Env var | Secret | Notes |
|---|---|---|---|
| `posthog.project_api_key` | `SP_POSTHOG_PROJECT_API_KEY` | no | The `phc_…` client key. Public by design — it is shipped to the browser. |
| `posthog.host` | `SP_POSTHOG_HOST` | no | Default `https://eu.i.posthog.com`. Supports self-hosted / reverse-proxy. |
| `posthog.personal_api_key` | `SP_POSTHOG_PERSONAL_API_KEY` | **yes** | Optional; only needed for backend capture with a distinct key. If unset the backend uses the project key. |
| `posthog.enabled` | `SP_POSTHOG_ENABLED` | no | Default `true`. A kill switch that works *even when a key is set* — it does not enable anything on its own. |

Add the env vars to `RecognizedEnvVars` in
`server/internal/config/envvars.go:28` (or let `koanfReachableEnvVars` pick them
up if a config struct field is added).

**Enablement rule, applied identically on both sides:**
`enabled == true && project_api_key != ""`. Everything else is off.

### 2. Public config endpoint

Add `GET /api/v1/config` — unauthenticated, alongside `/api/v1/auth/providers`
in `server/internal/app/server.go` (~`:610`). It returns only non-secret,
browser-safe values:

```json
{ "posthog": { "enabled": true, "projectApiKey": "phc_…", "host": "https://eu.i.posthog.com" } }
```

When PostHog is off, return `{ "posthog": { "enabled": false } }` — the key and
host must be **absent**, not empty strings. The endpoint must never surface
`posthog.personal_api_key` or any parameter marked `Secret`. Shape it as a
general-purpose public-config blob so future public flags can join it rather
than each minting an endpoint.

### 3. Backend capture

- Add a small `server/internal/analytics` package wrapping `posthog-go` with a
  no-op implementation selected when disabled, so call sites never branch.
- Instantiate once at startup from `systemconfig`; wire into the app the way
  Sentry/OTel already are. No client is constructed when disabled.
- Capture a **small, deliberate** set of server-side product events, keyed by a
  stable pseudonymous distinct-id (org UID + user UID — never email, never
  check target hosts/URLs). Suggested initial set: org created, user signed up,
  check created, integration connected, status page published. Keep it short;
  it is easier to add events than to purge them.
- Never block a request on capture: the client must be async/buffered, and must
  flush on graceful shutdown.
- Never send check results, incident payloads, target hostnames, or any
  customer-supplied free text.

### 4. Frontend capture

- Add `posthog-js` to `web/dash0`.
- Fetch `/api/v1/config` at boot and initialize PostHog only when
  `posthog.enabled` is true, from `web/dash0/src/routes/__root.tsx:25` (or a
  small provider it mounts). No `<script>` tag in `index.html` — nothing may load
  when the feature is off.
- Identify the user on login and reset on logout, using the same pseudonymous
  id scheme as the backend so sessions stitch. Hook into
  `web/dash0/src/contexts/AuthContext.tsx`.
- Configure autocapture conservatively and mask input values; SolidPing URLs
  embed org slugs and check UIDs, so scrub or allowlist the captured pathname
  rather than shipping raw URLs.

### 5. Dashboard settings tab

Add an **Analytics** tab to the server settings section:

- Append to the `tabs` array in
  `web/dash0/src/routes/orgs/$org/server.tsx:16-26` and add the
  `tabs.analytics` label to the `server` i18n namespace.
- New route `web/dash0/src/routes/orgs/$org/server.analytics.tsx`, following the
  canonical pattern of `server.web.tsx` (`:19` route, `:37-41` read by key,
  `:52-59` save with `secret: true` where applicable) and using
  `useSystemParameters` / `useSetSystemParameter`
  (`web/dash0/src/api/hooks.ts:2638`, `:2694`).
- Fields: enabled toggle, project API key, host, personal API key (secret).
  Show the resolved effective state, and make it visible when a value is coming
  from an env var rather than the DB, so an operator isn't confused by an
  edit that appears not to take.
- Per repo convention, build the page from the primitives on the design
  reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`); it must be
  usable on mobile.

### 6. Docs & tests

- Document the four env vars and the tab in `web/docs/`, including an explicit
  "analytics is off unless you configure it, and here is exactly what is sent"
  statement.
- Add the new endpoint to `server/internal/app/openapi/openapi.yaml` and
  `wiki/api-specification/`.
- Tests that prove the **negative**, not just the positive: with no credentials,
  `/api/v1/config` reports disabled and omits the key; no PostHog client is
  constructed; the dashboard loads no PostHog code and issues no request to a
  PostHog host. Plus a positive control asserting each of those *does* happen
  once a key is set. A Playwright test in `web/dash0/e2e/` should assert the
  no-credentials case makes zero third-party requests.

## Open questions

- Should the backend and frontend share one project key, or should backend
  events go to a separate PostHog project? The spec assumes one project with an
  optional distinct personal key.
- Should SaaS mode (`SP_DEPLOYMENT_MODE=saas`) default `posthog.enabled` on with
  a baked-in key, while self-hosted stays off? Left out of scope — the rule above
  is uniform, and a SaaS default can be layered on later in
  `server/internal/app/saas.go`.
