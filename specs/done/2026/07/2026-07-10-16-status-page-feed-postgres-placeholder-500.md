# Status page Atom feed 500s on Postgres — `$1` placeholder never bound through bun

## Problem

The public status-page Atom feed is (still) broken on the Postgres deployment:

```
GET https://solidping.k8xp.com/api/v1/status-pages/acmetech/abyla-windows-vms/feed.xml
{"title":"Internal server error","code":"INTERNAL_ERROR","detail":"ERROR: there is no parameter $1 (SQLSTATE=42P02)"}
```

### Root cause

`ListPublicStatusUpdates` in the Postgres DB service
(`server/internal/db/postgres/status_update.go:118-129`) builds a raw query with a
Postgres-style `$1` placeholder and runs it through `s.db.QueryContext(...)` where
`s.db` is a `*bun.DB` (`server/internal/db/postgres/postgres.go:152`):

```go
rawQuery := fmt.Sprintf(
    `SELECT uid, section_uid, ... FROM status_updates
     WHERE status_page_uid = $1
       AND deleted_at IS NULL
       AND published_at >= NOW() - INTERVAL '%d days'
     ORDER BY published_at DESC
     LIMIT 100`, historyDays)

rows, err := s.db.QueryContext(ctx, rawQuery, statusPageUID)
```

`bun.DB.QueryContext` does **not** pass args through to the driver. It formats the
query itself, substituting args into bun-style `?` placeholders, and then sends the
resulting SQL to Postgres with **zero** bound parameters. Since the query contains
`$1` and no `?`, the `statusPageUID` arg is silently dropped and the literal `$1`
reaches Postgres → `SQLSTATE 42P02: there is no parameter $1`. This fails on every
request whenever the `status_updates` table exists (the "table does not exist"
graceful-degradation branch at `status_update.go:132` does not match this error).

The SQLite implementation is correct — it uses a `?` placeholder with
`s.db.NewRaw(rawQuery, statusPageUID).Scan(ctx, &rowResults)`
(`server/internal/db/sqlite/status_update.go:131-142`) — which is why local dev
(SQLite) works while the deployed Postgres environment 500s. This is the only `$n`
placeholder in `server/internal/db/postgres/`.

### Blast radius

Two call sites of `ListPublicStatusUpdates`:

1. **Atom feed** — `server/internal/handlers/statussubscribers/handler.go:301`:
   the error propagates via `WriteInternalError` → the public 500 above.
2. **Public status page JSON** — `server/internal/handlers/statuspages/service.go:927-928`:
   the error is silently swallowed (`if updErr == nil && len(updates) > 0`), so on
   Postgres the "recent status updates" timeline on public status pages has been
   silently empty. Same bug, masked.

## Proposal

1. Fix the Postgres implementation to bind the parameter the way bun expects —
   mirror the SQLite implementation: `?` placeholder +
   `s.db.NewRaw(rawQuery, statusPageUID).Scan(ctx, &rowResults)` (or keep
   `QueryContext` but with `?`). Keep the `fmt.Sprintf` interval interpolation
   (int, safe) or move `historyDays` into a bound arg too for consistency.
2. Add a Postgres integration test (testcontainers) that exercises
   `ListPublicStatusUpdates` with a real `status_updates` row — the current gap is
   that nothing runs this query against Postgres, so the placeholder mismatch
   shipped twice. Cover: rows returned within the window, rows excluded outside
   the window, soft-deleted rows excluded.
3. Consider surfacing (at least logging) the error at
   `statuspages/service.go:928` instead of silently dropping it, so a broken
   updates query can't hide behind an empty timeline again.

## Implementation Plan

### Step 1 — Fix the Postgres placeholder binding
`server/internal/db/postgres/status_update.go`, `ListPublicStatusUpdates`:
mirror the SQLite implementation exactly. Replace the `$1` placeholder with a
bun-style `?`, and swap `s.db.QueryContext(ctx, rawQuery, statusPageUID)` +
manual `rows.Scan` loop for `s.db.NewRaw(rawQuery, statusPageUID).Scan(ctx,
&rowResults)` scanning into a typed `rowResult` struct with `bun:"..."` tags,
then convert to `[]*db.PublicStatusUpdate`. Keep the `fmt.Sprintf` interval
interpolation of `historyDays` (int, safe) using Postgres `NOW() - INTERVAL
'%d days'`. Preserve graceful degradation for a missing table (`"does not
exist"` / `"undefined_table"`).

### Step 2 — Postgres integration test (embedded Postgres / testcontainers)
New file `server/internal/db/postgres/status_update_postgres_test.go`. Follow
the embedded-Postgres pattern used by `last_result_test.go` (unique port,
`New(ctx, &Config{Embedded: true, Port: …, RunMode: runModeTest})`,
`Initialize`, self-skip on `-short` / embedded-startup error). Seed an org, a
status page, and `status_updates` rows via `CreateStatusUpdate`, then assert
`ListPublicStatusUpdates` returns:
- a row published inside the history window (returned),
- a row published outside the window (excluded),
- a soft-deleted row inside the window (excluded).
This is the regression guard — nothing previously ran this query against
Postgres, which is why the `$1` mismatch shipped.

### Step 3 — Surface the swallowed error
`server/internal/handlers/statuspages/service.go` (~L927): when
`ListPublicStatusUpdates` returns a non-nil error, log it with
`slog.ErrorContext(ctx, …, "error", updErr, …)` before falling through to the
empty timeline, so a broken updates query can no longer hide behind an empty
"recent updates" list. Keep the graceful behavior (still render the page).
