---
model: opus
effort: medium
---

# Make the per-user 2FA, password-reset and change-password attempt counters atomic

## Problem
Found by the security scan of 2026-10-15 (finding E2). **Low.** A burst of parallel requests spends one unit of an attempt budget instead of N. Who can trigger it depends on the counter:
- **2FA verification:** anyone with the victim's password, because they need a 2FA temp token. Until H1 is fixed, that token already bypasses TOTP.
- **Password reset:** anyone on the internet who knows the victim's email.
- **Change password and email change:** anyone holding a session of the victim.

This holds in every deployment mode and on both databases.

**The race.** `bumpCounter` (`server/internal/handlers/auth/service.go:3008-3033`) runs three separate statements with no transaction and no conditional write:
1. `GetStateEntry`;
2. a compare with the limit and `count+1` in Go;
3. `SetStateEntry`.

Concurrent requests all read the same `count` and all write `count+1`. SQLite has the race too: it runs on one connection (`sqlite.go:243`), but other goroutines interleave between the statements.

**Callers:**

| Counter | Caller | Limit | Cap applied at |
|---|---|---|---|
| 2FA attempts (TOTP and recovery codes share it) | `bumpTwoFAAttemptCounter` (`:4589-4595`) | 5 per 15 min per user | `Verify2FA` `:4620`, `Recovery2FA` `:4663` |
| Change password and email change | `bumpChangePasswordCounter` (`:3154-3159`) | 10 per 15 min | `:3181`, `email_change.go:178` |
| Password reset, per user | `bumpResetUserCounter` (`:3035-3041`) | 3 per hour | `:2858` |
| Password reset, per IP | `bumpResetIPCounter` (`:3043-3048`) | 5 per minute | `:2816` |

**Impact.**
- **2FA.** The per-IP limiter allows `MaxConcurrent` = 20 in-flight requests (`config.go:1759`). So one IP gets about 100 TOTP guesses per 15 min instead of 5, and K IPs about K×100, because the budget is per user.
- **Password reset.** The same race multiplies the per-user reset-email cap, which turns it into a mail bomb.

**A second race the report missed: duplicate rows on the first burst.** These counters are global state entries (`organization_uid` NULL). The only uniqueness is `unique (organization_uid, key)` (`server/internal/db/postgres/migrations/001_v0_1_0.up.sql:604-615`, `sqlite/migrations/001_v0_1_0.up.sql:444-455`). It never fires for NULL, as `SetStateEntry`'s own comment says (`postgres.go:4502-4509`, `sqlite.go:4465-4471`). Its UPDATE-first path only helps once a row exists. So a burst on a fresh key inserts N rows, each with `count = 1`.

**Why the report's first fix does not work.** "Insert-or-ignore, then a conditional UPDATE" relies on the conflict above, which never fires for these keys. A `SELECT … FOR UPDATE` row lock cannot lock a row that does not exist yet. The increment needs serialization **per key**.

**Why the existing atomic helper cannot be reused as-is.** `AdmitFixedWindows` (`db/service.go:997-1006`; `postgres.go:4792-4878`, `sqlite.go:4755-4840`) is the atomic pattern already in use. But it is org-scoped (`orgUID string`), it seeds with `ON CONFLICT (organization_uid, key) DO NOTHING` (same NULL problem), and it stores a fixed-window value shape.

## Proposal
1. **Add a DB method** to `server/internal/db/service.go`, next to `AdmitFixedWindows` (`:997-1006`):

   ```go
   // BumpGlobalCounter ATOMICALLY counts one attempt against the global
   // (organization_uid IS NULL) counter at key: limited=true, and no write,
   // when the live count is already >= limit; otherwise count+1 with
   // expires_at = now+ttl. Concurrent callers on the same key are serialized.
   BumpGlobalCounter(ctx context.Context, key string, limit int, ttl time.Duration) (limited bool, err error)
   ```

2. **Postgres implementation** (`postgres.go`, next to `AdmitFixedWindows` at `:4792`). Everything runs inside one `s.db.RunInTx`:
   1. **Take a per-key lock.** Run `SELECT pg_advisory_xact_lock(?, hashtext(?))` with a class id and the key. Use the two-int4 form, whose key space is separate from the single-bigint keys in `dblock`.
      - Register the class id in the registry comment of `server/internal/db/dblock/dblock.go:14-29`, and in `wiki/conventions/database.md`.
      - A hash collision only serializes two unrelated keys. It never breaks correctness.
   2. **Read the count.** Select the live rows: `organization_uid IS NULL AND key = ? AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`. This is the predicate `GetStateEntry` uses (`postgres.go:4474-4500`). Take the **max** `count` across the rows, which tolerates duplicates left by the old code.
   3. **Refuse at the limit.** If the count is `>= limit`, return `limited = true` and write nothing. This matches `bumpCounter` today.
   4. **Increment.** Otherwise UPDATE every row with `organization_uid IS NULL AND key = ?`. That is the same row set `SetStateEntry` updates, soft-deleted and expired rows included. Write `{"count": n+1}`, `expires_at = now+ttl` and `deleted_at = NULL`. INSERT only when no row was updated. Under the lock, no new duplicates can appear.
3. **SQLite implementation** (`sqlite.go`, next to `AdmitFixedWindows` at `:4755`).
   - Same body inside `s.db.RunInTx`, without the lock statement. The single connection (`sqlite.go:243`) serializes the transaction, exactly as `AdmitFixedWindows` relies on (`:4758-4760`).
   - Keep the `datetime('now')`-style expiry predicate used by `GetStateEntry` (`sqlite.go:4437-4463`).
4. **Service.**
   - `bumpCounter` (`service.go:3008-3033`) becomes `return s.db.BumpGlobalCounter(ctx, key, limit, ttl)`.
   - Move `counterValue` (`service.go:2987-3005`) into `server/internal/db/models` so that both dialects parse the same `{"count": N}` shape. Keep the key name `count` (`stateValueKeyCount`, `service.go:45`).
   - Nothing else changes: same keys, same limits, same sliding TTL, and the resets (`DeleteStateEntry`, `:3119`, `:3236`, `:4602`, `email_change.go:246`) stay as they are. The existing throttle tests then hold unchanged.
5. **Out of scope.** `Recovery2FA` consumes a recovery code with the same read-modify-write (`service.go:4685-4708`). Two parallel requests with one valid code can both mint a session. Fix it in its own small change, with a conditional update on the stored code list.

## Tests
- **New `server/internal/db/global_counter_test.go`**, with SQLite and Postgres twins on the pattern of `incident_number_test.go`.
  - Postgres uses a fresh embedded port. Grep the repo first; the numbering note is at `db/postgres/postgres_headroom_postgres_test.go:20-27`. Startup failures go through `testsupport.PostgresUnavailable` / `PostgresInitFailed`.
  - Table-driven cases:
    - **Burst on a fresh key:** 40 goroutines call `BumpGlobalCounter(key, 5, time.Minute)`. Expect **exactly** 5 `limited=false` and 35 `limited=true`, exactly one live row for the key, and `count == 5`. Serialization makes the result exact, so assert exact counts and never "at most".
    - **Burst on an existing row** (count 2): exactly 3 admitted.
    - **Expired row:** treated as 0, and the next bump writes 1. **Soft-deleted row:** same.
    - **Legacy duplicates:** two live rows with counts 3 and 4. A bump writes 5 to both, and the next one is limited.
    - **Positive control:** sequential bumps admit exactly `limit`, then refuse.
- **`server/internal/handlers/auth/two_fa_attempt_throttle_test.go`.** Add a case "a concurrent burst is counted per attempt" to `runTwoFAAttemptThrottleSuite` (`:86-113`), which already runs on both engines.
  - Fire 20 concurrent `Verify2FA` calls with wrong codes on one temp token.
  - Expect exactly 5 `ErrInvalid2FACode` and 15 `ErrRateLimited`. This fails before the fix: more than 5 calls reach the code check.
  - **Positive control:** a burst of 4 wrong codes, then a correct code, succeeds. The existing "correct code within budget succeeds and resets the counter" case must stay green.
- **`server/internal/handlers/auth/service_test.go` (reset).** 10 concurrent `RequestPasswordReset` calls for one user create at most `passwordResetMaxPerUser` (3) `password_reset:` entries, and queue at most 3 emails. A single request still creates one.
- **`server/internal/handlers/auth/change_password_test.go`.** 20 concurrent `ChangePassword` calls with a wrong current password: exactly `changePasswordMaxPerUser` (10) `ErrInvalidCredentials`, and the rest `ErrRateLimited`. `TestChangePasswordRateLimit` (`:242`) stays green.

## To verify
- That `hashtext` (or `hashtextextended`) is present on the embedded Postgres version the tests use and on prod's CNPG version.
- That no supported topology opens one SQLite file from two server processes, for example split `SP_NODE_ROLE` roles on one host. If one does, single-connection serialization is per process only, and the SQLite implementation must open the transaction with `BEGIN IMMEDIATE` instead.

## Open questions
1. **Window semantics.** Today the TTL slides: every counted attempt pushes `expires_at` to now+ttl. The alternative is a fixed window from the first attempt, as in `models.FixedWindow`. Recommended: keep the sliding window. Atomicity is the fix here; changing the lockout semantics would be a separate, visible behaviour change.

## Resolved open questions
Decided 2026-10-16 (all recommendations accepted):
1. Keep the sliding-window TTL semantics; only make the counters atomic.

## Related
H1 (2026-10-11, the 2FA temp token works as an access token, which bypasses TOTP outright), M10 (2026-10-11, the per-IP reset counter is keyed by a spoofable `X-Forwarded-For`, so making it atomic does not bound an attacker who picks their own key).
