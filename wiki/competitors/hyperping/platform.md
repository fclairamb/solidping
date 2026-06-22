# Hyperping — Platform Features

Heartbeats (Healthchecks), browser checks (Playwright), and status pages.

## Healthchecks (heartbeat / cron)

Endpoint: `https://hc.hyperping.io/{TOKEN_ID}` — accepts HEAD, GET, POST.

### Two scheduling modes

- **Simple**: `period_value` + `period_type` (`seconds` | `minutes` | `hours` | `days`).
- **Cron**: `cron` string + optional `timezone` (default UTC).

### Grace period
`grace_period_value` + `grace_period_type` — wait this much past expected ping time before alerting.

### `/start` endpoint
`https://hc.hyperping.io/{TOKEN_ID}/start` — call at job start. Service measures duration between `/start` and the completion ping. **Worth borrowing**: the simplest possible "is my job hung mid-run" primitive.

### What's missing
- **No exit-code support** documented.
- **No max-execution-time alert** as a direct field — only implicit via the duration measurement vs grace period.
- **No log/payload capture on failure** — the docs explicitly omit this. (BetterStack does capture the request body as the incident cause, which is better.)

### Rate limits
300 pings/min per IP, 10 pings/min per token.

### API
Full CRUD plus `pause` / `resume`.

## Browser checks (Playwright)

### Runtimes
- **Current ("2026.05")**: Node 24, Playwright 1.59.1, headless Chromium, TypeScript 5.6 compiler bundled (runs `.spec.ts` directly).
- **Legacy ("2023.04")**: Node 16 + Playwright 1.33 still selectable. Multi-version runtime support is unusual and useful.

### Assertions
Jest-style `expect`. Steps via `test.step()` — each step shown in the run report with status + duration.

### Web Vitals — automatic
**LCP, CLS, TBT, FCP, TTFB** captured automatically on each navigation. No script changes needed. This is the cleanest "free perf data on synthetic" we've seen and worth replicating.

### Timeouts
- Global per run: 2 minutes.
- Per individual test: 30 seconds (configurable via `test.setTimeout()`).

### Failure artifacts
Playwright **trace** + **video** + screenshot. Full replay capability.

### Region selection
Per-check region picking. **"Double Check"** (retry from a different region) is on by default — same multi-region confirmation philosophy as their HTTP monitors.

### Logs
Kept indefinitely.

### Quotas
3 / 10 / 25 browser checks per Essentials / Pro / Business.

## Status pages

Hosted at `{slug}.hyperping.app` or via custom domain (CNAME → `cname.hyperping.io`).

### Editor structure
Three-tab UI: **Settings**, **Sections**, **Subscribers**.

### Sections
Group monitors into components. Each component shows uptime and/or response-time graphs.

### Subscriber channels
- **Email** (with confirmation flow).
- **Slack channel** subscription.
- **SMS via Twilio** — BYO Twilio account (cost passed to the customer).

### Component-level subscriptions
Subscribers pick which components to follow. Hyperping case study claims unsubscribe rate dropped from 8% to <2% after launching this. Probably the highest-leverage subscriber-management feature to ship.

### Branding
Google Fonts, light/dark/system theme, accent color, favicon, logo, banner style. White-label footer is Business+.

### Access control
- Password protection.
- SSO SAML (Okta / Google / Azure).
- Google SSO with email-domain restriction.
- Search-engine-indexing toggle.

### Localization
Built-in `{ en, fr, de, ru, nl, pl, se }` for incident and maintenance titles and messages. Promoted to a first-class data-model concept — not just a UI translation layer. **Worth borrowing**: i18n at the JSON-payload level.

### Other features
- **TV mode**: 60 s auto-refresh, fullscreen — for office wallboards.
- Google Analytics integration.
- JSON export of status.
- Embeddable footer badge.
- Automatic incident reporting to status pages: yes — incidents API publishes to selected `statuspages[]`. Outages on monitors don't auto-publish unless explicitly wired.
- **Multi-tenancy**: marketing claims unlimited customer pages on Enterprise.

### Migration importer
`import-from-statuspage` — direct importer from Atlassian Statuspage. Hyperping has productized this as a sales tool ("switch from Statuspage in 5 minutes"). Worth considering for SolidPing.

## Sources

- https://hyperping.com/docs/monitoring/healthchecks
- https://hyperping.com/docs/api/healthchecks
- https://hyperping.com/docs/api/healthchecks/create
- https://hyperping.com/docs/monitoring/browser-checks
- https://hyperping.com/docs/status-page/create-status-page
- https://hyperping.com/blog/status-page-subscriber-management-guide
- https://hyperping.com/docs/status-page/import-from-statuspage
