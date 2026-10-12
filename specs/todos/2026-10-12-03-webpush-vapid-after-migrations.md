---
model: sonnet
effort: medium
---

# The VAPID keypair is created after the migrations, persisted on the first boot and reused on every restart

## Problem
On a fresh database the first boot logs three warnings before the migrations even start. Reproduced on 2026-10-12 with `docker run ghcr.io/fclairamb/solidping:0.38.3` and an empty data dir (SQLite, the default type):

```
level=WARN msg="SQL query failed" operation=SELECT error="SQL logic error: no such table: app_settings (1)"
level=WARN msg="SQL query failed" operation=INSERT error="SQL logic error: no such table: app_settings (1)"
level=WARN msg="webpush: failed to persist VAPID public key" err="set app setting \"webpush.vapid_public_key\": SQL logic error: no such table: app_settings (1)"
level=INFO msg="Running database migrations..." type=sqlite
```

The same lines were reported on DietPi/Debian installs and in a TrueCharts test. It is the first thing a new self-hoster reads in the logs. It is not only noise: the key served during the first run is never stored.

**The boot order.** `prepareServer` (`server/main.go:309`) calls `app.NewServer` (`:310`), then `server.Initialize` (`:324`, the migrations), then `InitializeSystemConfig` (`:333`), `seedStartupData`, `SetupRoutes` (`:353`). `cmd_mcp.go:131` goes through the same `prepareServer`. `NewServer` (`server/internal/app/server.go:269`) builds the DB service (`:287-342`) and, near its end, initializes the VAPID keys (`:537-555`) with `webpushpkg.GetOrCreateVAPIDKeys`. So the keys are read and written before `app_settings` exists. The table is created by the first migration (`server/internal/db/sqlite/migrations/001_v0_1_0.up.sql:807`, `server/internal/db/postgres/migrations/001_v0_1_0.up.sql:1035`). `Server.Initialize` (`server.go:4216`) only calls `s.dbService.Initialize`.

**What `GetOrCreateVAPIDKeys` does on a missing table** (`server/internal/webpush/vapid.go:36`):
1. No env keys (`SP_WEBPUSH_VAPID_PUBLIC_KEY` / `SP_WEBPUSH_VAPID_PRIVATE_KEY`), so it reads the DB (`:43`). `readVAPIDFromDB` (`:76-86`) returns a wrapped error for anything but `sql.ErrNoRows`. The caller ignores that error: `:46` only checks `pubErr == nil && pub != ""`, and falls through.
2. It generates a fresh pair (`:55`).
3. `SetAppSetting` for the public key fails (`:60`). It logs the WARN at `:61` and **returns the generated keys anyway** (`:62`).
4. `server.go:546-554` stores them in `cfg.WebPush` and `svcList.WebPushOptions`. The process serves that public key at `GET /api/v1/orgs/:org/webpush/vapid-public-key` (`server/internal/handlers/webpush/handler.go:36`, route `server.go:1865`) and signs with that private key (`server/internal/notifications/webpush.go:58`, `server/internal/jobs/jobtypes/job_escalation_step.go:693`, `server/internal/opsnotifywire/wire.go:249`, `server/internal/handlers/integrations/service.go:1762`).

The two "SQL query failed" lines come from the SQL log hook (`server/internal/db/sloghook/hook.go:113`).

**Consequence: the first-run key is lost.** On the restart, the table exists and has no row, so a new pair is generated and persisted. Reproduced: after `docker restart`, the log shows `Generated VAPID keys, persisted to app_settings`. Any browser that subscribed during the first run holds a subscription bound to the old public key:
- Pushes are now signed with a key the push service does not accept for that subscription. `webpush.Send` (`server/internal/webpush/sender.go:64-70`) only maps 404/410 to `ErrSubscriptionGone`. Any other status is `ErrPushServiceError`, logged as `webpush: send failed; skipping subscription` (`server/internal/notifications/webpush.go:83-91`) on every alert, and never pruned.
- Re-enabling from the UI calls `pushManager.subscribe` with the new key (`web/dash0/src/hooks/useWebPushSubscription.ts:32-35`). It does not unsubscribe the existing subscription first.

The first run is exactly when a new user tries the features, so this is a correctness bug on the first restart, not only log noise. Installs that set both env keys are not affected (`vapid.go:38-40` returns before touching the DB).

**Postgres.** Same order and same code path (`server.go:287-316` for `postgres` and `postgres-embedded`). The error text is `relation "app_settings" does not exist` instead. Two more Postgres-specific points:
- With split node roles (`api` and `jobs`, `server/internal/config/config.go:29-31`), each process runs `NewServer` and generates its **own** ephemeral pair on a fresh database. The api process hands out key A, the jobs process signs with key B. Every push fails until both restart.
- Even with the order fixed, two processes booting together on a fresh database both read "no row", both generate, and `SetAppSetting` is an upsert (`ON CONFLICT (key) DO UPDATE`, `server/internal/db/postgres/app_settings.go:36-41`, same in `server/internal/db/sqlite/app_settings.go:36-41`). Last writer wins, and the loser keeps a key that is not in the database. The two keys are written in two separate statements (`vapid.go:60,65`), so a race can also leave a public key from one process next to a private key from the other.
- In `test`/`demo` run mode with `Reset`, `postgres.Service.Initialize` drops every table (`server/internal/db/postgres/postgres.go:329-333`). Today the pair persisted by `NewServer` on an existing database is dropped right after, so the demo serves a key that is not in the database. Moving the init after the migrations fixes that too.

**Why the tests miss it.** `newTestDB` in `server/internal/webpush/vapid_test.go:13-25` runs `svc.Initialize` before every call. No test calls `GetOrCreateVAPIDKeys` on an unmigrated database, and no test boots the server twice on the same database.

**Other startup components.** `GetAppSetting` / `SetAppSetting` have exactly two callers in production code. VAPID is the only one at startup. The SAML SP keypair (`server/internal/handlers/auth/saml_service.go:179-221`) is lazy (first SAML request) and already returns non-`ErrNoRows` read errors instead of generating. It is the model to copy. The full first-boot log on 0.38.3 has no other "no such table" line. One adjacent ordering problem is not about `app_settings` and stays out of scope here (see To verify): the OVH delivery callback is registered in `NewServer` (`server.go:520-526`) from `cfg.Server.BaseURL` and `ovhsmscb.ResolveToken(cfg)` before `InitializeSystemConfig` overlays `server.base_url` and `auth.jwt_secret`. The route handler resolves the token after the overlay (`server.go:2305`).

## Proposal
1. **Move the VAPID init after the migrations.** Extract `server.go:537-555` from `NewServer` into a method, e.g. `(s *Server) initWebPush(ctx context.Context)`, that sets `s.config.WebPush.VAPIDPublicKey/PrivateKey` and `s.services.WebPushOptions` exactly as today. Call it from `Server.Initialize` (`server.go:4216`) right after `s.dbService.Initialize(ctx)` succeeds. Update the `Initialize` doc comment ("runs migrations, then the startup state that lives in the database"). Every path that runs migrations already calls `Initialize` before `SetupRoutes` and `Start`: `main.go:324`, `server/test/integration/testhelper.go:83`, `server/test/integration/scenario/harness.go:195`, and the `server/internal/app/*_test.go` helpers. Nothing reads `WebPushOptions` or `cfg.WebPush.VAPID*` between `NewServer` and `Initialize` (the readers are wired in `SetupRoutes`, `server.go:1613,1863`, and in the job worker).
2. **Cover the one caller that skips `Initialize`.** `scenario/harness.go:246` calls `NewServer` on a database migrated by `TestMain` and goes straight to `SetupRoutes`. Export the step (e.g. `InitializeWebPush`) and call it there, so scenario servers keep a key as they do today.
3. **Never serve a key that is not stored.** In `GetOrCreateVAPIDKeys` (`vapid.go`):
   - Return the read error when `readVAPIDFromDB` or the private-key read fails with anything but `sql.ErrNoRows`. Do not generate.
   - Return an error when persisting fails. Do not return the generated pair.
   - The caller (`server.go:545`) already handles an error: web push is disabled for this run with one WARN. That is the right outcome for a broken database, and it can no longer happen on a healthy fresh one.
4. **Make first creation safe when two processes race.** Add to `db.Service` (`server/internal/db/service.go:1647-1654`) a method such as `CreateAppSettingsIfAbsent(ctx, settings map[string]string) error` that inserts every pair in **one transaction** with `ON CONFLICT (key) DO NOTHING`, in both `sqlite/app_settings.go` and `postgres/app_settings.go`. `GetOrCreateVAPIDKeys` generates a pair, calls it, then **re-reads both keys** and returns what is stored. Whoever lost the race uses the winner's pair. If the re-read pair is incomplete, return an error.
   - The repair case stays: a public key without a private key (left by the old two-statement write) is replaced. Use `SetAppSetting` for both keys inside the same transaction for that case only, and log it at INFO.
5. **Recovery for browsers already bitten (frontend, small).** In `useWebPushSubscription.subscribe` (`web/dash0/src/hooks/useWebPushSubscription.ts:27-37`), call `reg.pushManager.getSubscription()` first. If a subscription exists and its `options.applicationServerKey` differs from the current key, `unsubscribe()` it, then subscribe. This also covers an operator who changes the env keys on purpose. Keep the `data-testid`s untouched.
6. **No data migration.** An install that has restarted once already has a stable, persisted pair. An install still on its first run gets a fresh, persisted pair on the upgrade restart, which is the same outcome as today's first restart. Env-provisioned keys keep priority (`vapid.go:38-40`).
7. **Out of scope, do not change here:** treating 401/403 from the push service as "subscription gone" (it would also prune every subscription when an operator sets a wrong env key), the ignored `WebPush.Enabled` flag, and the OVH callback ordering.

## Tests
Table-driven per `server/AGENTS.md`.

- `server/internal/webpush/vapid_test.go`, new cases:
  - **unmigrated database, no env keys:** `sqlite.New` in memory without `Initialize`. `GetOrCreateVAPIDKeys` returns an error and empty keys.
  - **unmigrated database, env keys set:** returns the env keys, no error, no DB access.
  - **persist failure:** a stub `db.Service` whose `CreateAppSettingsIfAbsent` fails. Returns an error, no keys.
  - **partial state:** only `webpush.vapid_public_key` stored. Returns a complete pair, both keys stored, the public key is the stored one.
  - **concurrent first creation:** file-backed SQLite in `t.TempDir()`, 8 goroutines call `GetOrCreateVAPIDKeys` with fresh `Config{}` values. All return the same pair, and it equals the stored pair.
  - Existing cases (`TestGetOrCreateVAPIDKeys_GeneratesAndPersists`, `_Idempotent`, `_UsesConfigValues`) keep passing.
- `server/internal/webpush/vapid_postgres_test.go` (new), following the embedded pattern of `server/internal/db/postgres/dek_cold_reload_postgres_test.go`: self-skip under `-short`, `testsupport.PostgresUnavailable` / `PostgresInitFailed` on startup errors, a port not used by any sibling (15615 is the highest today, check at implementation time). Cases: unmigrated returns an error; after `Initialize`, two calls with fresh configs return the same pair; the concurrent-creation case above.
- `server/internal/app/webpush_boot_test.go` (new), `//nolint:paralleltest` because it uses `captureLogs` (`server/internal/app/saas_test.go:106`):
  - **first boot, SQLite file:** `cfg.Database.Type = "sqlite"`, `cfg.Database.Dir = t.TempDir()`. `NewServer` then `Initialize`. The captured log contains no `app_settings`, no `SQL query failed`, no `webpush:` warning, and one `Generated VAPID keys`. `server.Services().WebPushOptions.VAPIDPublicKey` is non-empty and equals `cfg.WebPush.VAPIDPublicKey`.
  - **second boot, same dir:** `Close` the first server, build a **new** `config.Config` (the first one was mutated with the keys, which would short-circuit through the env path and hide the bug), boot again. Same public key, and no `Generated VAPID keys` line.
  - **before Initialize:** right after `NewServer`, `WebPushOptions.VAPIDPublicKey` is empty.
  - `sqlite-memory` single boot: same log assertions.
  - Do not assert "no WARN at all". A fresh boot also logs legitimate warnings (plaintext credentials, JWT secret auto-generated, see To verify).
- Frontend: a unit test next to the hook (`web/dash0/src/hooks/useWebPushSubscription.test.ts`), with `pushManager` mocked: an existing subscription with a different key is unsubscribed before `subscribe`; one with the same key is not.
- E2E: `web/dash0/e2e/webpush.spec.ts:7` already checks the public-key endpoint returns a non-empty key. No new E2E.

## To verify
- That no other code reads `WebPushOptions` or `cfg.WebPush.VAPID*` between `NewServer` and `Initialize`, including `SetHeadless` and the MCP stdio path (`cmd_mcp.go:131`).
- That no scenario test relies on web push before step 2 is in place (`grep -ri webpush server/test` found nothing today).
- That bun's `RunInTx` with `ON CONFLICT DO NOTHING` blocks the second inserter until the first commits on Postgres (default READ COMMITTED), so the loser's re-read sees the winner's pair. On SQLite writes are serialized.
- The exact push-service status for a VAPID mismatch (expected 403 on FCM, 401 or 403 on Mozilla autopush) and the browser error when subscribing with a different key while a subscription exists (the Push API spec says `InvalidStateError`). This only confirms step 5 is needed, it does not change the backend.
- Separate spec, not this one: the OVH delivery callback (`server.go:520-526`) is registered with a token derived from the pre-overlay JWT secret. With no JWT secret in env/YAML and no `SP_SMS_OVH_DLR_TOKEN`, that is the default `change-me-in-production` (`config.go:1952`, treated as unset by `systemconfig.go:1558`), while the handler (`server.go:2305`) expects the token derived from the database secret. If confirmed, OVH delivery receipts are rejected on every boot of such an install.
- Separate, cosmetic: a fresh install logs `Using deprecated koanf aggregation retention` (`server/internal/jobs/jobtypes/job_aggregation.go:391`) for `audit.retention_days` although the operator set nothing. Worth checking whether it fires on defaults.

## Acceptance Criteria
- [ ] A fresh SQLite or Postgres database boots without any `app_settings`, `SQL query failed` or `webpush:` warning.
- [ ] The VAPID public key served after the first boot is the same after a restart.
- [ ] `GetOrCreateVAPIDKeys` never returns a pair that is not stored in `app_settings`.
- [ ] Two processes creating the keys at the same time end up with one stored pair, and both use it.
- [ ] Env-provisioned keys still win and still need no database.
- [ ] A browser holding a subscription for an old key can re-enable push from the UI.
