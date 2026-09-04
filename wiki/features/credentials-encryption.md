# Credentials encryption at rest

Two key layers protect the secret half of a check's configuration:

- a **KEK** (key-encryption key) that lives outside the database —
  `SP_ENCRYPTION_MASTER_KEY` or `SP_ENCRYPTION_MASTER_KEY_FILE`, 32 raw bytes
  base64-encoded;
- a per-organization **DEK** (data-encryption key), randomly generated on first
  use, wrapped with the KEK and stored in the `parameters` table.

Secret fields are split off the public config at write time and stored as an
AES-256-GCM envelope in a `*_private` column (`checks.config_private`,
`check_jobs.config_private`, `integration_connections.settings_private`). With
no KEK configured at all, secrets fall back to a *plaintext* envelope — the
documented self-hosted V1 fallback, logged at startup.

## Where it lives

- Service, envelope format, DEK cache: [`server/internal/crypto/credentials/service.go`](../../server/internal/crypto/credentials/service.go)
- DEK storage adapter: [`credentials/param_store.go`](../../server/internal/crypto/credentials/param_store.go)
- Production wiring: `app.BuildCredentialsService` in [`internal/app/server.go`](../../server/internal/app/server.go)
- Worker-side open/merge and its failure taxonomy: [`checkworker/checkjobsvc/secrets.go`](../../server/internal/checkworker/checkjobsvc/secrets.go)

## How the org DEK is stored

The wrapped DEK is one org-scoped parameter row:

| column | value |
|---|---|
| `key` | `encryption.dek` |
| `secret` | `true` (so it never appears alongside normal org settings) |
| `value` | `{"value": "{\"v\":1,\"alg\":\"AES-256-GCM\",\"nonce\":…,\"ct\":…}"}` |

**The wrapper is not optional.** `SetOrgParameter` wraps *every* scalar it is
given as `{"value": …}` (`models.ParameterValue`), on both engines, and has done
since the first commit. `parameters.value` also decodes into a
`models.JSONMap`, so the column must always hold a JSON **object** — a bare
string in there makes `GetOrgParameter` itself fail to scan.

`ParamStore.LoadDEK` therefore unwraps `value.value`, and additionally accepts
two shapes no production row currently has: a bare envelope object (one carrying
`v`), and a JSON string. Anything else — an object with neither `value` nor
`v` — is rejected with `ErrDEKParamShape`, *not* with an envelope-version
error, so the log names the real problem.

## The DEK cache, and the failure mode it can hide

`EnsureOrgKey` caches the unwrapped DEK in memory for the **lifetime of the
process**, keyed by org UID (`DEKCacheLen()` is exported as a gauge). Two
consequences worth internalising:

1. **A key that was written wrong stays usable in the process that wrote it.**
   That process encrypts and decrypts happily from cache while every *other*
   process — each `checks` worker, and the same API after its next restart —
   fails to open the row. Symptoms are therefore asymmetric and delayed, and
   "it works in the API" proves nothing.
2. **Re-saving the check does not help**, because saving goes through the same
   org key. Advice that names the check is wrong whenever the *org key* is what
   failed.

Two guards exist because of exactly this (incident #506, 2026-09-03):

- **Write verification** — after generating and saving a DEK, `EnsureOrgKey`
  reloads it through the store and unwraps it *before* caching. A storage-shape
  regression now fails the first encrypt loudly instead of arming a
  cross-process outage.
- **Bounded cache invalidation** — when an envelope fails to open with a cached
  DEK, that one entry is dropped and cold-reloaded exactly once before the error
  is surfaced. One retry, no loop, no cache-wide flush. The retry **reloads,
  never regenerates**: a missing DEK row is an error there, because minting a
  replacement would orphan every secret already encrypted for that org.

## Diagnosing "credentials could not be decrypted"

A check goes red with a credentials message. Which layer failed decides what to
do, and the result output now says which:

| Result output | Layer | What to do |
|---|---|---|
| `…SP_ENCRYPTION_MASTER_KEY is not configured on this worker` | no key at all | configure the KEK on that worker |
| `this organization's encryption key could not be opened on this worker — re-saving the check will not help; check the worker logs for "unwrap org DEK"` | **org DEK** | operator problem: wrong KEK on this process, or an unreadable `encryption.dek` row |
| `check credentials could not be decrypted — re-save the check's credentials` | this check's envelope | re-save the check's secrets |

The worker's ERROR log carries the wrapped cause. Grep for:

```
"Cannot decrypt claimed job credentials" … unwrap org DEK
```

`unwrap org DEK` is the marker for the org-key layer specifically. If it is
present, look at the KEK this process was given and at the `encryption.dek` row
— not at the check.

### Do not "repair" the row with SQL

A tempting mitigation is to unwrap the row in place:

```sql
-- DON'T
update parameters set value = value->'value' where key = 'encryption.dek';
```

That leaves a bare JSON string in a column that decodes into a `JSONMap`, so
`GetOrgParameter` fails to scan it and the parameter becomes unreadable — a
worse failure than the one being fixed. The reader accepts the wrapped shape as
it is; the remediation for a version that could not read it is to **deploy the
fixed version to every process** (API *and* all checks workers), with no SQL and
no re-entry of secrets. The data was never lost: the wrapped DEK is intact and
the KEK is unchanged.

## Testing notes

A credentials test that generates, encrypts and decrypts inside **one** service
instance proves nothing about storage: the DEK never leaves the cache. Any test
touching DEK persistence must build a **second** service over the same database
and decrypt from cold — see
[`credentials/dek_cold_reload_test.go`](../../server/internal/crypto/credentials/dek_cold_reload_test.go)
and [`postgres/dek_cold_reload_postgres_test.go`](../../server/internal/db/postgres/dek_cold_reload_postgres_test.go).

Tests must also wire the DEK store through `credentials.NewParamStore()`, the
same adapter the server uses. Hand-rolled copies of production wiring are what
allowed the write/read shape mismatch to pass CI for months.
