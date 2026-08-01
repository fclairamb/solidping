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
