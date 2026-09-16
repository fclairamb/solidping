# `kuma.db` fixture

A real Uptime Kuma **2.5.4** SQLite database, used by
`server/pkg/cli/kumadb`'s reader tests (`kumadb_test.go`) and by the
`--from uptime-kuma-db` end-to-end test in
`server/internal/handlers/checks/importers`.

## What it contains

- One monitor of every type `importers.UptimeKumaConverter` maps
  (`http`, `keyword`, `json-query`, `port`, `ping`, `dns`, `docker`, `push`,
  `grpc-keyword`, `mqtt`, `postgres`, `mysql`, `redis`, `sqlserver`,
  `mongodb`, `steam`, `real-browser`), plus one `radius` monitor (a type
  with **no** SolidPing counterpart) to exercise the "skipped" warning path.
- A `group` monitor ("Production") with two children (`Website`,
  `Docs keyword`), to exercise folder → check-group mapping.
- Two tags (`team`, `env`), each applied to one monitor via `monitor_tag`.
- Every hostname uses `acme.com` / `*.acme.com` or an RFC 5737 test address
  (repo rule: no real company names, no real infrastructure).

## What was scrubbed

Before this file was committed:

```sql
DELETE FROM heartbeat;
DELETE FROM stat_minutely;
DELETE FROM stat_hourly;
DELETE FROM stat_daily;
DELETE FROM notification;
DELETE FROM monitor_notification;
UPDATE user SET password = '', twofa_secret = NULL;
UPDATE setting SET value = '' WHERE key = 'jwtSecret';
VACUUM;
```

The last line is not in the spec's scrub list but was added after inspecting
the live database: Kuma also stores a bcrypt-derived `jwtSecret` in the
`setting` table, which the reader never touches but which has no reason to
sit in a public repo either. `notification` and `monitor_notification` end up
empty in the committed file, so the `monitor_notification`-row-count-only
behaviour (the "notifications are not imported" warning) is covered by a
separate synthetic database built inline in `kumadb_test.go`, not by this
fixture.

The admin account's password (`fixture-admin` / a throwaway string generated
for this container only, never reused anywhere) does not survive the scrub
either — `password` is blanked above.

## How it was generated

1. `docker pull louislam/uptime-kuma:2` (resolved to **2.5.4**) and
   `docker run -d --name kuma-fixture -p 13001:3001 louislam/uptime-kuma:2`.
2. Completed the setup wizard choosing **SQLite** (Kuma 2.x offers a database
   choice at first boot; 1.x always used SQLite) and created a throwaway
   admin account.
3. Created the `Production` group monitor through the actual web UI.
4. Driving the Vue SPA's "Add New Monitor" form for the remaining 17 monitor
   types via repeated automated browser control hit an unrelated UI bug
   (stacked confirmation modals from the accepted-status-codes / tag
   multiselect widgets swallowing subsequent clicks). Rather than fight that,
   the container was stopped, `kuma.db` was copied out with `docker cp`, and
   the remaining monitors/tags were inserted with plain `INSERT` statements
   against the **exact schema Kuma's own migrations created**
   (`sqlite3 kuma.db ".schema monitor"` etc. — reproduced in
   `kumadb_test.go`'s `TestReadColumnsMatchFixtureSchema`, which runs
   `PRAGMA table_info(monitor)` against this file and fails if a column the
   reader relies on goes missing). Every column used is one Kuma's own schema
   defines; no column was invented.
5. The seeded `kuma.db` was copied back into the container and the container
   restarted, to confirm the real Kuma server accepts the rows, runs checks
   against them, and serves them over its API without error (verified via
   `docker logs` — every seeded monitor executed a real check attempt; the
   only failures are the expected connection errors against non-existent
   `*.acme.com` hosts). This step is what makes the fixture "real Kuma data"
   rather than a hand-typed schema guess: the schema, defaults and
   constraints all came from the live app, and the live app validated the
   seeded rows by actually running them.
6. The container was stopped again and the scrub above was applied with the
   local `sqlite3` CLi, then the file was copied here.

## Regenerating

```bash
docker rm -f kuma-fixture 2>/dev/null
docker run -d --name kuma-fixture -p 13001:3001 louislam/uptime-kuma:2
# complete the setup wizard at http://localhost:13001 (choose SQLite),
# create the Production group monitor via the UI, then:
docker stop kuma-fixture
docker cp kuma-fixture:/app/data/kuma.db ./kuma.db
sqlite3 ./kuma.db < seed.sql   # see git history of this directory for seed.sql
docker cp ./kuma.db kuma-fixture:/app/data/kuma.db
docker start kuma-fixture      # sanity check: dashboard loads, no crash in `docker logs`
docker stop kuma-fixture
docker cp kuma-fixture:/app/data/kuma.db ./kuma.db
sqlite3 ./kuma.db <<'SQL'
DELETE FROM heartbeat;
DELETE FROM stat_minutely;
DELETE FROM stat_hourly;
DELETE FROM stat_daily;
DELETE FROM notification;
DELETE FROM monitor_notification;
UPDATE user SET password = '', twofa_secret = NULL;
UPDATE setting SET value = '' WHERE key = 'jwtSecret';
VACUUM;
SQL
docker rm -f kuma-fixture
```
