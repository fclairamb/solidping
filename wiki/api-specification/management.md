# Management

Server-level introspection and support endpoints. These live under
`/api/mgmt` (not `/api/v1`), except for the feature-flag endpoint.

### GET /api/mgmt/health
Health check. Auth: public

### GET /api/mgmt/version
Returns server version, build hash, and build date. Auth: public

### GET /api/mgmt/limits
Introspection of the server's configured limits — the rate-limiting section
(window, burst, per-route overrides) and the concurrency section. Useful for
clients that want to pace themselves and for support when diagnosing 429s.
Auth: public

### GET /api/mgmt/memory
Runtime memory statistics for the server process (Go heap/alloc counters).
Auth: super-admin

### GET /api/mgmt/scheduling/cost-distribution
Distribution of scheduling cost across checks/lanes — used to diagnose an
unbalanced scheduler. Auth: super-admin

### GET /api/mgmt/email-preview
List every previewable email template, wrapped as `{ "data": [...] }`. Each row
carries `template` (the file name, which is the `:template` path segment
below), `subject` (rendered through the real formatter), `hasText` (whether the
template ships a plaintext part), `previewUrl`, and `error` when that template
failed to render with its fixture. Backs the dashboard's email catalog at
`/dash0/orgs/:org/test/emails`. Registered only when `SP_RUNMODE=test`.
Auth: public (test mode only)

### GET /api/mgmt/email-preview/:template
Render an email template with sample data so it can be reviewed in a browser
without sending anything. `?format=html` (default) or `?format=text`.
`?colorScheme=light` (default) or `?colorScheme=dark`; `dark` applies only to
the HTML format and rewrites the template's own
`@media (prefers-color-scheme: dark)` block to `@media all`, so an `<iframe>`
— which cannot be told to report a dark preference — shows the exact CSS a
dark-mode client applies. Without the param the response is the untouched
template. Any other value is a 400. See
[features/email-dark-mode.md](../features/email-dark-mode.md).
Registered only when `SP_RUNMODE=test`. Auth: public (test mode only)

### POST /api/mgmt/report
Submit an in-app bug report (multipart/form-data). Public endpoint, optional bearer token for user attribution. Body fields: `url` (required), `comment`, `org`, `annotations`, `context` (JSON), `screenshot` (file). Returns `{ uid }`. The screenshot is stored as a `File` (group `reports`) and a GitHub issue is created asynchronously when `app.github.*` is configured.

### GET /api/v1/features
Return the active feature flags for the frontend (e.g. `{ "bugReport": true }`). Auth required.
