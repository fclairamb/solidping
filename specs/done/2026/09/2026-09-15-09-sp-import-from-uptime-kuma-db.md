---
model: sonnet
effort: high
---

# `sp checks import --from uptime-kuma-db kuma.db`: migrate from Uptime Kuma 2.x, which has no backup export

## Problem

The Uptime Kuma importer shipped in v0.7.0 only accepts the **1.x backup JSON**
(Settings → Backup → Export). Kuma 2.0 (October 2025) removed that screen, and
the converter refuses anything without a `monitorList`
([uptimekuma.go:106](server/internal/handlers/checks/importers/uptimekuma.go:106)).
The published guide tells 2.x users to "export from a 1.x instance"
([from-uptime-kuma.md:20](web/docs/docs/features/migrate/from-uptime-kuma.md:20)),
which nobody who is already on 2.x can do. Kuma is at 2.5.3 and has been on 2.x
for eleven months, so the importer is unusable for most of the people it exists
for.

What every Kuma install does have is its SQLite file, `data/kuma.db`. The 1.x
backup JSON was little more than a dump of that file's `monitor` table, so the
data the converter needs is all there.

Two constraints rule out the obvious "upload kuma.db in the dashboard" path:

- **Size.** The file is dominated by the `heartbeat` table. Hundreds of MB is a
  normal homelab; users report 1.4 GB (300 monitors), 6 GB (200 ping monitors,
  90 days) and 10.7 GB. The convert endpoint caps the body at 16 MB
  ([handler.go:18](server/internal/handlers/checks/importers/handler.go:18)),
  and reading the whole file in the browser (sql.js) fails for the same reason.
- **Secrets.** Beyond the monitor passwords the 1.x JSON already carried, the
  database holds the `notification` table (every Telegram token, Slack webhook,
  SMTP password), the `user` table (password hashes, 2FA secrets) and API key
  hashes. Asking a self-hoster to upload that to solidping.io is a hard sell,
  and we would be receiving a file we never want to hold.

The `sp` CLI runs on the user's machine, opens the file with random access, and
sends only the few KB that matter. That is the right shape.

## Proposal

### CLI

Add `--from <source>` to `sp checks import`
([commands.go:482](server/pkg/cli/commands.go:482)). Today the command reads a
SolidPing export document and posts it to `ImportChecks`
([apply.go:320](server/pkg/cli/apply.go:320)). With `--from uptime-kuma-db`,
the file argument is a Kuma SQLite database instead:

```bash
sp checks import --from uptime-kuma-db ./kuma.db            # preview only
sp checks import --from uptime-kuma-db ./kuma.db --apply    # create/update
```

Behaviour:

1. Open the file **read-only** with `modernc.org/sqlite` (pure Go; `sp` is
   built with `CGO_ENABLED=0` in
   [ci.yml:864](.github/workflows/ci.yml:864), so `mattn/go-sqlite3` is not
   an option). Use `mode=ro`, **not** `immutable=1`: Kuma runs in WAL mode, and
   `immutable` would ignore a `kuma.db-wal` sitting next to the file and drop
   monitors created since the last checkpoint. Refuse a file whose header is not
   SQLite, or that has no `monitor` table, with a message naming what was
   expected.
2. Read **only** `monitor`, `tag` and `monitor_tag`. Never touch `heartbeat`,
   `stat_*`, `notification`, `user`, `api_key`. Also read
   `monitor_notification` for the row count only, so the existing
   "notifications are not imported" warning reflects reality.
3. Rebuild the 1.x `monitorList` JSON that the server converter reads
   ([uptimekuma.go:33](server/internal/handlers/checks/importers/uptimekuma.go:33)).
   The mapping is the inverse of Kuma's `Monitor.toJSON(includeSensitiveData)`
   and is **not** a mechanical rename: the JSON keys mix camelCase and
   snake_case while the columns are all snake_case. Spell it out as a table in
   the code, one line per field the converter reads:

   | Column | JSON key |
   |---|---|
   | `id`, `name`, `description`, `type`, `active`, `parent` | same |
   | `url`, `hostname`, `port`, `method`, `body`, `headers` | same |
   | `interval`, `maxretries`, `timeout`, `keyword` | same |
   | `retry_interval` | `retryInterval` |
   | `invert_keyword` | `invertKeyword` |
   | `json_path` / `expected_value` | `jsonPath` / `expectedValue` |
   | `accepted_statuscodes_json` (TEXT holding a JSON array) | `accepted_statuscodes` (array) |
   | `ignore_tls` / `upside_down` | `ignoreTls` / `upsideDown` |
   | `dns_resolve_server`, `dns_resolve_type`, `docker_container` | same |
   | `mqtt_topic`, `mqtt_username`, `mqtt_password`, `mqtt_success_message` | `mqttTopic`, `mqttUsername`, `mqttPassword`, `mqttSuccessMessage` |
   | `database_connection_string` / `database_query` | `databaseConnectionString` / `databaseQuery` |
   | `grpc_url`, `grpc_service_name`, `grpc_enable_tls` | `grpcUrl`, `grpcServiceName`, `grpcEnableTls` |
   | `auth_method`, `basic_auth_user`, `basic_auth_pass` | `authMethod`, `basic_auth_user`, `basic_auth_pass` |
   | `tag` ⋈ `monitor_tag` | `tags: [{name, value}]` |

   Monitor folders are monitors with `type = 'group'` referenced through
   `parent`; the converter already handles that. Do **not** read Kuma's
   `group` / `monitor_group` tables, which are status-page sections, not
   folders. Password columns are passed through on purpose: the converter is
   what refuses to persist them and emits the warning, and the CLI must not
   grow a second policy.
4. Post the JSON to the existing
   `POST /orgs/:org/checks/import/convert?source=uptime-kuma&dryRun=…`
   ([handler.go:103](server/internal/handlers/checks/importers/handler.go:103)).
   The API client has `ImportChecks` and `ApplyChecks`
   ([client.go:806](server/pkg/client/client.go:806)) but nothing for the
   convert endpoint; add `ConvertChecks(ctx, org, source, body, dryRun)`
   beside them. The server-side converter stays the **single** mapping from
   Kuma to SolidPing; this spec adds a reader, not a second converter.
5. **Dry-run is the default** for `--from`, the opposite of the plain import.
   The plain import consumes a file the user wrote; this one consumes a file
   another product wrote, and the preview with its warnings is the point.
   `--apply` performs the write. Keep `--dry-run` accepted for symmetry (a
   no-op with `--from`). Print the same summary the dashboard preview shows:
   per-check outcome, the warnings list, the created/updated/skipped counts.
6. `--from` accepts only `uptime-kuma-db` for now; an unknown value lists the
   supported ones. Kuma 2.x on **MariaDB** has no `kuma.db` and is out of
   scope; say so in the error when the file is not SQLite and in the docs.

### Binary size

`sp` does not link SQLite today (14 MB). `modernc.org/sqlite` adds roughly
10 MB. Accept it: the module is already a dependency of the server, so there is
no new supply-chain surface, and a lighter read-only reader would mean either
a second SQLite implementation or hand-parsing the file format, including WAL.

### Tests

- A **real** `kuma.db` from a 2.5.x install as a fixture, not a hand-written
  one: run the `louislam/uptime-kuma:2` image, create one monitor of every
  type the converter maps plus a folder with children and two tags, stop the
  container, copy the file. Then shrink and scrub it before committing:
  `DELETE FROM heartbeat; DELETE FROM stat_minutely; DELETE FROM stat_hourly;
  DELETE FROM stat_daily; DELETE FROM notification; DELETE FROM
  monitor_notification; UPDATE user SET password = '', twofa_secret = NULL;
  VACUUM;`. Hostnames use `acme.com` (repo rule: no real company names), and
  the throwaway credentials typed into Kuma must not survive the scrub. Record
  the Kuma version and the exact creation steps in a `README.md` next to the
  fixture so it can be regenerated.
- A reader unit test asserting the emitted `monitorList` equals a golden JSON
  for that fixture, so a column rename in a future Kuma release is a visible
  diff rather than silently empty fields. Derive the column list from the
  fixture with `PRAGMA table_info(monitor)`, not from memory.
- An end-to-end test feeding the emitted JSON through the real
  `UptimeKumaConverter` and asserting the same check set the existing JSON
  fixture produces for equivalent monitors, so the two entry points cannot
  drift.
- Negative tests: not a SQLite file, SQLite file without a `monitor` table,
  `--from` with an unknown source, `--from` with a `-wal` file present next to
  the database (monitors only in the WAL must be seen).
- No dashboard change, so no Playwright.

### Docs and changelog

- [from-uptime-kuma.md](web/docs/docs/features/migrate/from-uptime-kuma.md):
  make the CLI the primary path for 2.x. Replace the "export from 1.x" warning
  with: where `kuma.db` lives (bare metal `data/`, Docker volume, `docker cp`
  with the note that `kuma.db-wal` and `kuma.db-shm` must come along or Kuma
  must be stopped first), the two commands above, and a sentence stating the
  file never leaves the machine and only three small tables are read. Keep the
  1.x JSON section for anyone still on 1.x. Add the MariaDB caveat.
- [cli.md](web/docs/docs/cli.md): the `--from` flag in the `sp checks import`
  row and a short paragraph under "Config as Code".
- Changelog entry as user-facing prose per
  [wiki/conventions/changelog.md](wiki/conventions/changelog.md), under
  `feat(cli)`: Kuma 2.x users can now migrate; name the command.

## Out of scope

- Any dashboard upload of the database (size and secrets, above).
- Importing Kuma notification channels. The `notification` table is nothing
  but tokens; the no-secrets rule stands. The preview warning already points to
  SolidPing integrations.
- Kuma on MariaDB.
- Reading `kuma.db` from inside the server by path. A server-side path read is
  a file-disclosure primitive, not a feature.

## Implementation Plan

1. **New `server/pkg/cli/kumadb` package** — the pure-Go SQLite reader, kept
   next to the CLI it serves (not under `internal/`, so it stays free to
   import `modernc.org/sqlite` and does not reach into the server's private
   `internal/handlers/checks/importers` types). Exposes
   `Read(path string) (json []byte, err error)`:
   - Opens `path` with `modernc.org/sqlite` using `mode=ro` (not `immutable`).
   - Verifies the file is SQLite (header check) and has a `monitor` table;
     named sentinel errors (`ErrNotSQLite`, `ErrNoMonitorTable`) so the CLI
     can print a message naming what was expected.
   - `SELECT`s the exact column list from the spec's mapping table off
     `monitor`, `tag ⋈ monitor_tag`, and counts `monitor_notification` rows
     per monitor id.
   - Rebuilds the 1.x `monitorList` JSON shape (own struct with the same
     json tags `uptimekuma.go`'s `kumaMonitor` reads — duplicated
     deliberately so this package has no dependency on `internal/`), setting
     a placeholder `notificationIDList` (`{"1": true}`-shaped, keyed by
     nothing meaningful) when a monitor has at least one
     `monitor_notification` row, so the existing
     "notification bindings are not imported" warning fires correctly
     without ever reading the `notification` table's contents.
   - Never touches `heartbeat`, `stat_*`, `notification`, `user`, `api_key`.
2. **Real fixture** — `louislam/uptime-kuma:2` via Docker (2.5.4), one
   monitor of every mapped type + a `radius` (unmapped) monitor + a group
   with two children + two tags, scrubbed per the spec (plus clearing the
   `setting.jwtSecret` row, found during scrubbing). Committed to
   `server/internal/handlers/checks/importers/testdata/uptime-kuma-sqlite/`
   with a `README.md` documenting the exact steps. See that README for why
   the monitors were seeded via SQL against the live schema rather than
   through the web form.
3. **Reader tests** (`server/pkg/cli/kumadb/kumadb_test.go`):
   - Golden test: `Read` against the fixture equals a committed golden
     `monitorList` JSON.
   - `PRAGMA table_info(monitor)` schema-guard test: every column the
     reader's `SELECT` references still exists on the fixture, so a Kuma
     column rename fails loudly.
   - Synthetic in-memory databases (built with plain `CREATE TABLE`/`INSERT`
     via `database/sql` + `modernc.org/sqlite`, not the fixture) for:
     not-a-SQLite-file, SQLite-but-no-monitor-table, and the
     `monitor_notification`-count-only warning trigger.
   - A `-wal` sibling file test: monitors present only in a `kuma.db-wal`
     file (not yet checkpointed into `kuma.db`) must be visible — proves
     `mode=ro` was used, not `immutable=1`.
4. **End-to-end test** (in `server/internal/handlers/checks/importers`,
   which can freely import `server/pkg/cli/kumadb` since importers is a
   leaf): feed `kumadb.Read`'s output for the fixture through
   `UptimeKumaConverter.Convert` and assert against the same golden
   `ExportDocument` shape `golden_test.go` already checks for the existing
   1.x JSON fixture, so the two entry points cannot drift silently.
5. **`ConvertChecks` API client method** (`server/pkg/client/client.go`,
   beside `ImportChecks`/`ApplyChecks`) — posts arbitrary bytes + `source` to
   `POST /orgs/:org/checks/import/convert?source=…&dryRun=…` and returns the
   raw `ConvertResult` JSON.
6. **CLI wiring** (`server/pkg/cli/commands.go`, `apply.go`):
   - Add `--from <source>` to the `checks import` command's flags.
   - New `checksImportFromAction` (or a branch in the existing action) that,
     when `--from` is set: validates the source is `uptime-kuma-db`, calls
     `kumadb.Read`, posts via `ConvertChecks` with `dryRun` **true by
     default**, `false` only when `--apply` is passed (`--dry-run` accepted
     as a no-op alias for symmetry). Renders the same
     created/updated/skipped/warnings summary shape the dashboard preview
     and the plain import already use.
   - Unknown `--from` value → error listing supported sources
     (`uptime-kuma-db` today).
7. **CLI tests** — table-driven test of the summary-formatting function
   (mirrors `formatImportSummary`'s existing test) plus an
   `apply_import_test.go`-style test hitting a `httptest.Server` standing in
   for the convert endpoint, exercising `--from` / `--apply` /
   `--dry-run` / unknown-source.
8. **Docs**: rewrite `from-uptime-kuma.md`'s "2.x" warning into the CLI
   instructions (where `kuma.db` lives, the `docker cp` + `-wal`/`-shm`
   caveat, the two `sp checks import --from uptime-kuma-db` commands, the
   never-leaves-the-machine / three-tables sentence, the MariaDB caveat);
   add the `--from` flag + a short paragraph to `cli.md`.
9. **Changelog**: `feat(cli): …` entry per `wiki/conventions/changelog.md`.
