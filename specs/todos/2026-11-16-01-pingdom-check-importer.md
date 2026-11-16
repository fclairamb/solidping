---
model: sonnet
effort: medium
---

# Import checks from Pingdom

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/87

## Source

Business request: Pingdom is the next "migrate from" page of the SEO plan, after
Better Stack, Gatus, Uptime Kuma and UptimeRobot, which all have an importer. A
migrate page without an importer would only be a manual guide. Ask: a `pingdom`
source for `POST /api/v1/orgs/:org/checks/import/convert?source=pingdom` and in the
import UI, fed by the Pingdom API 3.1 (`GET /checks?include_tags=true`, then
`GET /checks/{id}` for the type-specific settings, `Authorization: Bearer <read-only
token>`), with the token used for the fetch only and never stored.

## Problem

There is no Pingdom converter:

- `server/internal/handlers/checks/importers/handler.go:87-99` (`converterFor`) knows
  `gatus`, `uptime-kuma`, `uptimerobot` and `betterstack` only;
  `SupportedSources()` (`converter.go:424`) lists the same four.
- `handler_test.go:262-269` (`TestConvertEndpointRejectsUnknownSource`) uses
  `pingdom` as its example of an unknown source: it must switch to another name.
- The import dialog (`web/dash0/src/routes/orgs/$org/checks.index.tsx:177-190`) lists
  the four sources; only `betterstack` sends `{token}` instead of the pasted text
  (`:1588`).
- `mapping.md` has no Pingdom section.

## Why a spec

- New behaviour on an API endpoint (a new accepted `source`, a new request body shape)
  and an outbound integration.
- Well over ~4 files / ~150 lines (converter, fixtures, golden test, UI, i18n x4,
  mapping doc).
- Credential policy decision (basic auth on HTTP checks).

## Proposal

### Backend: `PingdomConverter` (`importers/pingdom.go`)

Model it on `BetterStackConverter` (outbound fetch, `ContextConverter`) **and** accept a
pasted payload, so it is testable and works without handing over a token:

- Request body JSON: `{"token": "..."}` → fetch; anything else that parses as a
  Pingdom `GET /checks` response (`{"checks":[...]}`) or an array of `GET /checks/{id}`
  `check` objects → convert directly.
- Fetch: `GET {base}/api/3.1/checks?include_tags=true`, then `GET /checks/{id}` per
  check, Bearer token, with the same guarded HTTP client, timeouts, size limits and
  error mapping as Better Stack (`ErrPingdomTokenRequired`, `…Unauthorized` on 401/403,
  `…API`, `…Unreachable`). Base URL fixed in prod, overridable only through a
  `WithPingdomBaseURL` handler option for tests (as `WithBetterStackBaseURL`), never
  from the request body. Bound concurrency of the per-check calls (e.g. 4) and
  respect `429` with a clear error.
- `Source()` = `pingdom`; add `SourcePingdom` to `converterFor` and `SupportedSources`;
  managed label `solidping-managed=pingdom` through the document's `organization`.

Mapping (to confirm against the API docs and the fixture):

| Pingdom | SolidPing |
|---|---|
| `type.http` (`encryption` → https) | `http`: url from `hostname` + `url` + `port`, `shouldcontain` / `shouldnotcontain` → body keyword assertions, `requestheaders` → headers (credential-looking names → encrypted `secretHeaders`, as Better Stack), `postdata` → POST body |
| `type.httpcustom` | `http` on the URL + warning (custom XML response not supported) |
| `type.tcp` | `tcp` (`stringtosend` / `stringtoexpect` if `checktcp` supports them, else warning) |
| `type.ping` | `icmp` |
| `type.dns` | `dns` (hostname, `expectedip`, `nameserver`) |
| `type.udp` | `udp` |
| `type.smtp` / `pop3` / `imap` | `smtp` / `pop3` / `imap` checkers (they exist in `server/internal/checkers/`) |
| Transaction (TMS) checks | skipped + warning |
| `resolution` (min) | period, clamped by `normalizeChecks` |
| `sendnotificationwhendown` | `confirmationPeriodSeconds` = n × period |
| `status: paused` | imported disabled |
| `tags[].name` | labels |
| `probe_filters` | ignored + warning |
| `auth` (basic) | see open question 1 |
| alert contacts, integrations, maintenance | not imported (as the others) |

### Tests

- `testdata/pingdom-checks.json` (list) and `pingdom-check-details.json` with every
  type above, `acme` names only; `pingdom.golden.json` through `golden_test.go`.
- Fetch test against an `httptest` server: token sent as Bearer, 401 → unauthorized
  error, token never in the result or logs.
- Re-import idempotent (same slugs, `solidping-managed=pingdom`).
- `TestConvertEndpointRejectsUnknownSource` switches to another unknown source name.

### Frontend

- Add `pingdom` to `IMPORT_SOURCES`; a token field (like Better Stack) plus the
  "paste JSON" option, with a short hint: "My Pingdom > Settings > Pingdom API >
  Add API token, read access". `data-testid` on the new inputs; i18n in en/fr/de/es.
- Extend the import E2E (mock the convert endpoint) to cover the Pingdom path.

### Docs

- `mapping.md`: a Pingdom section (types, fields, credential policy).
- The user docs page listing importers, if any, gets Pingdom.

## Open questions

1. **Basic auth on HTTP checks** (`auth` field in the check details): drop it with a
   warning like Gatus / Better Stack / Kuma, or import it like UptimeRobot?
   *Recommended: drop + warning.* It matches the general policy in `mapping.md`; the
   UptimeRobot exception stays an exception. Trade-off: users with basic-auth checks
   re-enter the credential by hand.
2. **Input modes**: fetch with a token only, or fetch + pasted JSON?
   *Recommended: both*, as above. Trade-off: two code paths in the converter, but the
   pasted path is what the golden test exercises and it avoids sharing a token.

## Closing the issue

This spec closes fclairamb/solidping-business#87. The implementing PR body **must**
carry a `Closes fclairamb/solidping-business#87` line. When the spec is archived to
`specs/done/`, verify the issue is closed and, if the merge did not close it, close it
by hand:

    gh issue close 87 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
