---
model: opus
effort: high
---

# Import checks from Gatus, Better Stack, and Uptime Kuma

## Problem

Users migrating to SolidPing from another monitoring tool have to recreate
every check by hand. The three most requested sources are:

- **Gatus** — self-hosted, config-as-YAML (`endpoints:` list with conditions).
- **Better Stack (Uptime)** — SaaS, monitors behind a REST API.
- **Uptime Kuma** — self-hosted, socket.io app with a JSON backup export.

We already have the entire back half of an import pipeline: the export/import
feature (specs/done/2026/03/2026-03-22-check-export-import.md) gives us
`ExportDocument` ([service.go:2621](server/internal/handlers/checks/service.go:2621)),
manifest parsing with JSON+YAML support
([apply.go:486](server/internal/handlers/checks/apply.go:486)), a dry-run plan
(`computeApplyPlan`, [apply.go:106](server/internal/handlers/checks/apply.go:106)),
slug-based upsert (`ApplyChecks`,
[apply.go:345](server/internal/handlers/checks/apply.go:345)), per-check error
collection, and the dash0 preview flow. What is missing is only the **front
half**: converters that turn each tool's native format into an
`ExportDocument`.

## Decision: one spec, converters share one framework

Deliberately a single spec rather than three. The shared plumbing (converter
interface, API endpoint, wizard UI, warning reporting) dominates the work; the
three converters are pure functions over fixtures and would conflict if
implemented as separate specs touching the same new package. Adding a fourth
source later (e.g. Pingdom, Checkly) should be a small, independent spec.

## Decision: input mode per source (not one-size-fits-all)

"URL+token vs JSON" is answered per source by what each tool can actually
provide:

| Source | Input | Why |
|---|---|---|
| Gatus | paste/upload the `config.yaml` | Gatus has no config-export API; its runtime API returns statuses, not full endpoint config (conditions, intervals are absent). The YAML file is the source of truth. |
| Uptime Kuma | upload the backup JSON | Kuma's API is socket.io with session auth — not practically fetchable server-side. Kuma 1.x has Settings → Backup → Export JSON. |
| Better Stack | API token → we fetch server-side | Clean REST API (`GET https://uptime.betterstack.com/api/v2/monitors`, Bearer token, paginated). Asking users to hand-assemble paginated JSON would be worse UX. |

The Better Stack fetch happens **server-side** (avoids CORS, keeps the token
out of browser storage). The token is used transiently for the fetch and is
**never persisted**.

## Proposal

### Backend

New package `server/internal/handlers/checks/importers/` (or `convert/`):

```go
type Converter interface {
    Source() string                      // "gatus" | "betterstack" | "uptime-kuma"
    Convert(input []byte) (*ConversionResult, error)
}

type ConversionResult struct {
    Document *checks.ExportDocument
    Warnings []ConversionWarning // per-source-item: dropped fields, unsupported types
}
```

Converters are **pure functions**: bytes in, `ExportDocument` + warnings out.
No DB access — validation, group auto-creation, and upsert are already handled
by `ApplyChecks`.

**Endpoint** (mirrors the existing import route conventions):

- `POST /api/v1/orgs/:org/checks/import/convert?source=gatus|betterstack|uptime-kuma&dryRun=true|false`
  - `gatus` / `uptime-kuma`: request body is the raw YAML / backup JSON.
  - `betterstack`: request body is `{ "token": "..." }`; the server fetches
    all pages of `/api/v2/monitors` (base URL overridable for tests via a
    request field or system parameter).
  - Response: the existing apply/dry-run result shape, **plus** a `warnings`
    array so users see exactly what didn't map.
  - Internally: convert → feed the resulting `ExportDocument` through the
    existing `ApplyChecks` path. No second import pipeline.

**Mapping tables** (fidelity notes; the implementer should keep a per-source
`mapping.md` or table in the package doc):

- *Gatus*: `endpoints[].url` scheme decides check type (`http(s)://` → http,
  `tcp://` → tcp, `icmp://` → icmp, `dns` conditions → dns, `ssh://` → ssh...).
  `interval` → `period`; `group` → `group`; conditions map onto the http
  config: `[STATUS] == 200` → `expected_status`, `[BODY] == ...` /
  `pat(...)` → `body_expect` / `body_pattern`, `[CERTIFICATE_EXPIRATION]` →
  an ssl check, jsonpath conditions → `json_path_assertions`
  ([checkhttp/config.go:62](server/internal/checkers/checkhttp/config.go:62),
  [jsonpath.go](server/internal/checkers/checkhttp/jsonpath.go)). Unmappable
  conditions (e.g. `[RESPONSE_TIME]` thresholds) → warning, check still
  imported.
- *Better Stack*: `monitor_type` → check type (`status`/`expected_status_code`
  → http, `tcp` → tcp, `ping` → icmp, `pop`/`imap`/`smtp` → matching
  checkers); `check_frequency` (seconds) → `period`; `request_headers`,
  `required_keyword` → `body_expect`; `verify_ssl`, `follow_redirects` where
  supported. `paused` → `enabled: false`. Additionally fetch
  `GET /api/v2/heartbeats` (same token, paginated) and map each heartbeat to a
  SolidPing heartbeat check (`server/internal/checkers/checkheartbeat/`):
  `period` → expected interval, `grace` → grace period, `paused` →
  `enabled: false`. The new heartbeat push URLs differ from Better Stack's, so
  the import preview and the migration docs must tell users to repoint their
  cron jobs/agents at the new URLs after import.
- *Uptime Kuma*: backup JSON `monitorList`; `type` maps across our large
  checker set (http, keyword → http+`body_expect`, json-query →
  `json_path_assertions`, port → tcp, ping → icmp, dns, docker, grpc-keyword,
  mqtt, postgres, mysql, redis, mssql — most Kuma types have a SolidPing
  counterpart in `server/internal/checkers/`); `interval` → `period`;
  `maxretries` → `incidentThreshold`. Kuma groups → check groups. Notification
  bindings are **not** imported (out of scope, same as native import). Note:
  Kuma 2.x dropped the JSON backup export; document that 2.x users must use a
  1.x-compatible export or we add DB-file parsing later (out of scope).

Slugs are derived from source names (slugified, deduped with numeric
suffixes), which keeps re-imports idempotent via the existing slug upsert.

### Dashboard (dash0)

Extend the existing Import flow on the checks list page with a source picker:
**SolidPing JSON/YAML** (current behavior) plus the three new sources. Per
source: file upload or textarea paste (Gatus/Kuma), or a token field
(Better Stack) with copy explaining the token is only used for this import and
not stored. Then the **existing** dry-run preview dialog (created / updated /
errors), now also listing conversion warnings, → confirm → apply. Follow the
design reference; mobile-usable.

### Docs

One page per source under `web/docs/` ("Migrate from ..."), each with where to
find the export/token in the source tool. These pages are also SEO surface for
migration searches.

### Testing

- Golden-file fixtures per source: real-shaped sample configs (Gatus YAML with
  varied conditions, Kuma 1.x backup JSON with many monitor types, Better
  Stack paginated API responses via `httptest` server) → snapshot the
  resulting `ExportDocument` + warnings.
- One integration test per source through the real endpoint: convert →
  dryRun preview → apply → checks exist; re-import is idempotent.
- Better Stack pagination + error paths (401 bad token, network failure →
  clean `VALIDATION_ERROR`-family response, token never logged), and the
  monitors + heartbeats combined fetch.

## Decisions

- **Better Stack heartbeats are in scope for v1**: the converter fetches both
  `/api/v2/monitors` and `/api/v2/heartbeats` and imports both.
- **Unmappable source items never block the import**: import what maps,
  surface the rest as warnings (consistent with existing per-check error
  handling).

## Implementation Plan

### Package placement (import-cycle note)

`ConversionResult.Document` is a `*checks.ExportDocument`, so the converter
package **imports** `handlers/checks` and therefore `handlers/checks` can never
import it. The convert **handler** lives in the same new package
(`server/internal/handlers/checks/importers/`) since it needs both the
converters and `*checks.Service`; routes are wired in `internal/app/server.go`
next to the existing export/import/apply routes, under the same admin group.

### Steps (one commit each)

1. **Shared framework** — `importers/converter.go`:
   `Converter` interface (`Source()`, `Convert([]byte) (*ConversionResult, error)`),
   `ConversionResult{Document, Warnings}`, `ConversionWarning{Item, Field, Message}`,
   a source registry, and shared helpers: `slugify` (mirrors
   `checks.sanitizeSlug` rules: `^[a-z][a-z0-9-]{2,49}$`), a slug deduper with
   numeric suffixes, seconds→duration-string, and a warning collector.
   Package doc + `mapping.md` table.
2. **Gatus converter** — `importers/gatus.go`. YAML `endpoints[]`. Type from URL
   scheme (`http(s)`→http, `tcp`/`tls`/`starttls`→tcp, `icmp`/`ping`→icmp,
   `ssh`→ssh, `udp`/`sctp`→udp, `ws(s)`/`websocket`→websocket, `dns:` block→dns).
   Conditions → http config (`[STATUS] ==` → `expected_status`,
   `[BODY] ==` → `body_expect`, `pat(...)` → `body_pattern`,
   `[BODY].x <op> v` → `json_path_assertions`, `has(...)` → `exists`,
   `[CERTIFICATE_EXPIRATION]` → an extra `ssl` check,
   `[DOMAIN_EXPIRATION]` → an extra `domain` check). `interval`→period,
   `group`→group, `enabled`→enabled, `client.timeout`→timeout. Unmappable
   conditions (`[RESPONSE_TIME]`, `[IP]`, …), `alerts`, `external-endpoints`,
   `client.insecure` → warnings, check still imported.
3. **Uptime Kuma converter** — `importers/uptimekuma.go`. Backup JSON
   `monitorList`. Types: http / keyword (+`body_expect`, invert→`body_reject`) /
   json-query (`json_path_assertions`) / port→tcp / ping→icmp / dns / docker /
   grpc-keyword→grpc / mqtt / postgres→postgresql / mysql / redis / sqlserver→mssql /
   mongodb / push→heartbeat / steam→a2s / real-browser→browser; `group` monitors
   become check groups via `parent`. `interval`→period,
   `maxretries`×`retryInterval`→`confirmationPeriodSeconds` (the spec's
   `incidentThreshold` was renamed to `confirmationPeriodSeconds` in the current
   `ExportCheck`), `active`→enabled, `accepted_statuscodes`→`expected_status_codes`.
   Notifications, `ignoreTls`, unsupported types (gamedig, radius,
   tailscale-ping) → warnings. Kuma 2.x has no JSON backup → documented.
4. **Better Stack converter + API client** — `importers/betterstack.go`.
   Body `{"token": "...", "baseUrl": "..."}`; the token is read into a local
   variable, used only for the `Authorization` header, and never stored,
   logged, or echoed in an error. Paginated `GET /api/v2/monitors` **and**
   `GET /api/v2/heartbeats` (follow `pagination.next`, bounded page count).
   `monitor_type`→type, `check_frequency`→period, `request_headers`→headers,
   `required_keyword`→`body_expect`/`body_reject`, `expected_status_codes`,
   `paused`→`enabled:false`. `auth_username`/`auth_password`,
   `verify_ssl:false`, `follow_redirects:false` → warnings (credentials are
   deliberately NOT imported). Heartbeats → `heartbeat` checks
   (`period`→period, `grace`→`confirmationPeriodSeconds`) plus a warning that
   the push URLs change and cron jobs must be repointed.
5. **Endpoint wiring** — `importers/handler.go`:
   `POST /api/v1/orgs/:org/checks/import/convert?source=…&dryRun=…`. Converts,
   then feeds the resulting document straight into the existing
   `checks.Service.ApplyChecks` (Prune off). `doc.Organization` is set to the
   source name so the managed-label scope is per-source and re-imports are
   idempotent. Response = the apply/dry-run shape plus `source`, `converted`
   and a `warnings` array (apply's own string warnings are folded in).
   Registered on the existing admin-only `orgChecksAdmin` group.
6. **dash0 UI** — source picker on the checks list Import flow (SolidPing
   JSON/YAML = current behavior, plus the 3 sources), file upload + textarea
   paste for Gatus/Kuma, a token field for Better Stack with "not stored" copy,
   feeding the **existing** preview dialog which now also renders conversion
   warnings. i18n keys in all four locales. Design-reference primitives only.
7. **Docs** — `web/docs/docs/features/migrate-from-{gatus,better-stack,uptime-kuma}.md`
   with where to find the export/token and what does not map.
8. **Tests** — golden-file fixtures per source under `importers/testdata/`
   (`-update` flag to regenerate), one endpoint integration test per source
   (convert → dryRun → apply → checks exist → re-import idempotent),
   Better Stack pagination / 401 / network-failure / token-never-leaked tests,
   plus a Playwright E2E for the source picker → paste → preview → apply flow.
