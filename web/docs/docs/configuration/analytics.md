---
sidebar_position: 6
title: Product Analytics
---

# Product Analytics (PostHog)

:::tip Analytics is **off** unless you configure it

A stock SolidPing install sends **no product analytics**. With no PostHog
project API key configured:

- the server constructs no analytics client — a genuine no-op is installed
  instead, so no buffer, no goroutine and no connection exist;
- the dashboard downloads **no analytics JavaScript at all** (`posthog-js` is
  behind a dynamic import that only runs after the server says the feature is
  on — there is no `<script>` tag in `index.html`);
- the browser makes **zero requests to any PostHog host**;
- `GET /api/v1/config` returns exactly `{"posthog": {"enabled": false}}` — the
  key and host fields are *absent*, not empty.

Nothing about your deployment, your users or your checks leaves your
infrastructure until you deliberately turn this on.
:::

## Enabling it

Analytics is active if and only if:

```
posthog.enabled == true  AND  posthog.project_api_key != ""
```

`posthog.enabled` defaults to `true`, but it is only a **kill switch** — it
never enables anything on its own. Supplying a project API key is what turns
the feature on; clearing `posthog.enabled` is how you turn it off again without
deleting the key.

The exact same rule is applied in three places, so they can never disagree: the
backend capture client, the public `GET /api/v1/config` endpoint, and the
dashboard.

## Configuration

### Environment variables

| Variable | Default | Secret | Description |
|----------|---------|--------|-------------|
| `SP_POSTHOG_PROJECT_API_KEY` | – | no | The public `phc_…` browser key. **Empty means analytics is entirely off.** Public by design: it is shipped to the dashboard, exactly like a Sentry DSN. |
| `SP_POSTHOG_HOST` | – (first-party proxy) | no | Ingestion endpoint. Leave it empty to capture through the built-in first-party proxy (see below). Set it to point the dashboard directly at a self-hosted PostHog or your own reverse proxy. |
| `SP_POSTHOG_PERSONAL_API_KEY` | – | **yes** | Optional server-side key. Stored as a secret, never returned by any API and never sent to the browser. When unset, the backend captures with the project key. |
| `SP_POSTHOG_ENABLED` | `true` | no | Kill switch. Set to `false` to disable analytics even while a key is configured. |

### Dashboard

Super-admins can configure the same four settings under
**Server Settings → Analytics** (`/orgs/<org>/server/analytics`). The page shows
the resolved effective state ("Analytics is active" / "Analytics is off") and
flags any field whose value is coming from an environment variable — environment
variables take precedence over the database, so without that flag an edit could
appear to be saved and then ignored.

Values saved from the dashboard live in the system `parameters` table under the
keys `posthog.enabled`, `posthog.project_api_key`, `posthog.host` and
`posthog.personal_api_key`. Resolution order is the standard
**environment variable → database → default**.

## First-party capture

By default the dashboard sends analytics to a first-party path on the SolidPing
origin, `/ingest`, instead of directly to `*.posthog.com`. The Go binary
reverse-proxies that path to PostHog. Many ad blockers drop third-party requests
to known analytics hosts, so first-party capture stops those blockers from
silently discarding events and keeps the web analytics numbers complete. This
needs no extra infrastructure: the same host that serves `/dash0` serves
`/ingest`.

The proxy forwards the visitor IP to PostHog, so geolocation stays accurate. It
sends `/ingest/static/*` requests (the toolbar and other posthog-js assets) to
the PostHog assets host and every other request to the ingestion host.

The dashboard also receives a `ui_host` (`https://eu.posthog.com`), so the
PostHog toolbar and "view in PostHog" links still resolve to the PostHog app
even though `api_host` is a local path.

Set `SP_POSTHOG_HOST` to opt out: the dashboard then captures directly against
that host and the built-in proxy is unused.

## Exactly what is sent

When (and only when) analytics is enabled, SolidPing captures a deliberately
small, fixed set of product events:

| Event | Fired when | Extra properties |
|-------|-----------|------------------|
| `org_created` | An organization is created | – |
| `user_signed_up` | A user account is created — by any signup path | `signupMethod` |
| `check_created` | A check is created | `checkType` (e.g. `http`, `dns`) |
| `integration_connected` | A notification/integration connection is created | `integrationType` (e.g. `slack`) |
| `status_page_published` | A status page becomes live and publicly visible | `visibility` |

**`user_signed_up`** covers every way an account can come into existence:
`password` (email registration, captured at confirmation — when the account
actually exists — not at the initial request), `invite`, and each SSO provider:
`google`, `github`, `gitlab`, `microsoft`, `discord`, `slack`, `oidc`, `saml`,
`ldap`. That provider family is the entire `signupMethod` value — never a
tenant name, issuer URL, directory DN or email domain. The bootstrap admin
account seeded on a fresh database is deliberately **not** counted: it is an
install artifact, not a person signing up.

**`status_page_published`** — SolidPing has no separate "publish" button, so
publishing is the state of being both enabled and publicly visible. The event
fires when a page is created already in that state (the default), and when an
existing private or disabled page *transitions* into it. It never fires on an
unrelated edit to a page that is already public, so it is neither
double-counted nor emitted on every save. Both captures live in the status-page
service rather than the REST layer, so pages created or published through the
MCP tools count identically.

Every event carries a **pseudonymous distinct id** built from UUIDs only:

```
org:<organization-uuid>/user:<user-uuid>
```

plus an `orgUid` property carrying the same organization UUID. The dashboard
uses the identical id scheme, so browser and server events for one session
stitch together without ever exchanging an identity.

From the browser, PostHog's autocapture is additionally configured
conservatively:

- input values and element attributes are masked;
- session recording is disabled;
- person profiles are only created for identified users;
- every captured URL and pathname is rewritten to a route template before it
  leaves the page — `/dash0/orgs/acme/checks/8f0e…` is sent as
  `/dash0/orgs/:org/checks/:uid`, and query strings and fragments are dropped —
  because SolidPing URLs embed organization slugs and resource UIDs.

## What is never sent

Regardless of configuration, SolidPing never sends:

- email addresses, user names or avatars;
- organization names or slugs;
- check names, slugs, descriptions, target hostnames, URLs, ports or any part
  of a check configuration;
- credentials, tokens, webhook URLs or any value stored as a secret — the
  personal API key itself is never exposed by any API;
- check results, response times, incident contents or notification payloads;
- any free text you or your users typed.

The `GET /api/v1/config` endpoint that the dashboard reads at boot is
unauthenticated and returns only non-secret, browser-safe values.

## Operational notes

- Capture is **asynchronous and buffered**: no HTTP request is ever blocked by
  analytics, and a failure to reach PostHog is logged at debug level and
  otherwise ignored.
- Pending events are flushed on graceful shutdown.
- A malformed configuration degrades to the no-op client with a warning; it can
  never prevent the server from starting or serving traffic.
