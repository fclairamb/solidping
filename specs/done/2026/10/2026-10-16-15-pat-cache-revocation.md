---
model: opus
effort: medium
---

# A revoked or expired PAT keeps working from the in-memory cache for up to 15 minutes

## Problem
Found by the security scan of 2026-10-16 (finding F1). **Low:** it needs a leaked PAT, and the window is bounded.
- **Who:** whoever holds the leaked token, whatever the owner's role, in every deployment mode.
- **Where the revocation part bites:** multi-replica installs (Postgres). The Kubernetes guide ships `replicas: 2` (`web/docs/docs/installation/kubernetes.md:50`).
- **Where the expiry part bites:** every install, SQLite single-node included.

**The cache.** `ValidatePATToken` (`server/internal/handlers/auth/service.go:1512-1622`) answers from `s.patCache` before touching the database. The cache is keyed by the token hash (`:1514-1525`). Every database hit is cached for `patCacheDuration = 15 * time.Minute` (`:1608-1616`).
- **Expiry overshoot (every install).** The entry's `expiresAt` is `now + 15 min`, whatever the token's own `ExpiresAt`. A hit only compares against the entry (`:1519`). So a PAT used a minute before it expires keeps working for up to 15 minutes after.
- **Revocation is local (multi-replica).** `RevokeToken` (`:2042-2080`) evicts only from the map of the process that served the DELETE (`:2058-2061`). Every other replica keeps serving the token until its entry ends.
  1. The user revokes a leaked token. The request lands on replica A.
  2. The attacker's next calls that land on replica B still succeed.
- **Revocation race (every install; not in the report, found while checking it).** `RevokeToken` evicts the entry *before* it deletes the row (`:2058-2069`). In between, `revokeUserTokensOfType` lists and deletes the user's OAuth grants, which widens the window.
  1. A request with the same PAT arrives on the same replica in that window.
  2. It misses the cache, reads the row (still live) and caches it again for 15 minutes.
  - An attacker hammering the API makes this likely.
- **What still limits the damage:**
  - `RequireAuth` reloads the user row on every request (`server/internal/middleware/auth.go:77-82`).
  - `AuthorizeOrgAccess` re-checks membership on every request (`server/internal/handlers/auth/orgaccess.go:82`), so removing the user from the org still cuts them off.
  - The cached `Role` does stay stale for the same window. That is the M8 class: handlers that trust `claims.Role`.

**A cross-replica bus already exists.** `notifier.EventNotifier` (`server/internal/notifier/notifier.go:14-36`) is built once at boot, before the auth service (`server/internal/app/server.go:375-379`, versus `:533`).
- **Two backends:** Postgres `LISTEN/NOTIFY` (`notifier/postgres.go`), or in-process channels on SQLite (`notifier/local.go`).
- **Precedent:** the realtime hub already consumes it with a resync on reconnect (`server/internal/realtime/hub.go:99-104`, `:206-220`).
- **Delivery is best-effort:**
  - a full listener buffer drops the payload (`ListenerBuffer = 16`, `notifier.go:46`);
  - anything sent while the Postgres listener is reconnecting is lost (`ReconnectNotifier`, `notifier.go:68-80`).

## Proposal
1. **Cap each entry at the token's own expiry** (`service.go:1608-1616`). Store `expiresAt = min(now + patCacheTTL, *token.ExpiresAt)` when the row has an expiry.
2. **Evict after the delete, and make the fill race-proof.**
   - Add `patCacheGen atomic.Uint64` to `Service` (`service.go:176-180`).
   - `ValidatePATToken` reads the generation before its database lookup. It inserts only if the generation is unchanged, checked under `cacheMux`.
   - Every eviction bumps the generation.
   - In `RevokeToken`, move the local eviction after `DeleteUserToken` succeeds (`:2058-2069`).
   - A single global counter is enough: revocations are rare, and a skipped insert costs only one more database read.
3. **Broadcast evictions on the existing bus.**
   - In `RevokeToken`, after the delete, call `Notify(ctx, "auth.patrevoked", token.TokenHash)`.
   - The event name must not contain `_`. The Postgres channel mapping turns `.` into `_` and back (`notifier/postgres.go:82-90`), so an underscore would not survive the round trip.
   - Add `SetEventNotifier(ctx context.Context, n notifier.EventNotifier)` to the auth service. It is a late-injection setter like `SetSupportInbox` (`server/internal/handlers/auth/org_delete.go:296`). Call it in `NewServer` right after `auth.NewService` (`server.go:533`), with the server-lifetime `ctx`.
   - The setter starts one goroutine. It `Listen`s on the event, and for each payload deletes that hash and bumps the generation.
   - If `n` implements `notifier.ReconnectNotifier`, a reconnect signal clears the whole cache and bumps the generation: revocations may have been missed during the gap. The realtime hub does the same.
   - The goroutine stops on `ctx.Done()` or when the channel is closed (`notifier.Close` closes it), then `Unlisten`s.
   - With a nil notifier (unit tests, `solidping mcp --stdio`) the behaviour stays local-only, as today.
4. **SQLite and single-node.**
   - The local notifier delivers in-process, so the code path is the same on both backends.
   - The revoking process evicts synchronously in step 2. Its API answer never waits for the asynchronous delivery.
   - On Postgres the sender also receives its own NOTIFY. Deleting a second time is harmless.
5. **Lower the backstop TTL.** Lower it from 15 to 5 minutes, as a package-level `patCacheTTL`. It now only bounds the rare dropped notification. A cache miss costs four indexed reads: token, user, org and membership (`:1528-1576`).

**Not needed.**
- **Org deletion** soft-deletes the org's tokens (`org_delete.go:87`) without evicting them. A cached claim then names a slug that no longer resolves (`orgaccess.go:69-73`).
- **A deleted user** fails the user reload in `RequireAuth`.

## Tests
- **`server/internal/handlers/auth/pat_cache_test.go`** (new, package `auth`). Run it on both engines, like `token_hash_at_rest_test.go:171-200`. Table-driven:
  - **Expiry.** A PAT with `ExpiresAt = now + 2s`, validated once so it is cached. After its expiry, `ValidatePATToken` refuses it. Fails before the fix, which answers from the cache.
    - Positive control: a PAT with no expiry is still served from the cache on a second call (the map entry is present).
  - **Cross-replica.** Two `Service` instances on the same database. Each gets `SetEventNotifier` with one shared `notifier.NewLocalEventNotifier()`, which stands in for Postgres NOTIFY.
    - Validate on both, revoke on A: B refuses the token within a short `require.Eventually`. Fails before the fix.
    - Positive control: another PAT cached on B stays cached.
  - **Race.** Add a test-only hook between the database read and the insert (nil in production). Use it to run `RevokeToken` at that point: afterwards the token is neither cached nor accepted. Fails before the fix, because of the evict-before-delete order.
  - **Reconnect.** A fake notifier that implements `ReconnectNotifier`. A reconnect signal empties the cache.
- **Must stay green:** the case "PAT create, validate and revoke with the cache keyed by hash" (`token_hash_at_rest_test.go:128-167`).
- **`server/internal/notifier/postgres_test.go`:** `auth.patrevoked` round-trips through a real `PgEventNotifier`, which pins the no-underscore rule. Skipped in `-short`, like the file's other Postgres cases.

## To verify
- The cached `*Claims` is shared by concurrent requests, and `applyDemoClaim` mutates it (`middleware/auth.go:189-199`). This is harmless today: it only ever sets `Demo = true`. But a shallow copy on each cache hit would remove a possible data race. Run the cross-replica test with `-race` to settle it.
- `RevokeToken` is the only path that deletes a single PAT row. Grep `TokenTypePAT` and `DeleteUserToken` to confirm.

## Related
M8 (2026-10-11): handlers that trust `claims.Role`. A cached PAT keeps the old role until the TTL ends, until M8 moves those handlers to the membership row.
