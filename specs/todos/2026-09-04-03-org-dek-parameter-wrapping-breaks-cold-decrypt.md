---
model: opus
effort: high
---

# Org DEK is persisted as `{"value": "<envelope>"}` but read back raw, so every process except the one that generated it fails to decrypt check credentials

## Problem

Incident #506 on the dev instance (org `stonal`, check `api.stonal.io/datalake (http)`,
uid `2d188b92-b5da-4742-ae9e-c308f53c686d`, region `gravelines`) opened at
2026-09-03 22:48 UTC with the user-facing cause:

> check credentials could not be decrypted — re-save the check's credentials

Re-saving the check did not help. The worker log carries the real reason:

```
level=ERROR msg="Cannot decrypt claimed job credentials"
error="check credentials could not be decrypted — re-save the check's credentials: unwrap org DEK: unknown envelope version"
check_uid=2d188b92-… organization_uid=3c9d374e-e655-431d-880c-5c161777b75c
```

`unknown envelope version` comes from unwrapping the **org DEK** (not the check's
envelope), in
[`service.go:296-304`](../../server/internal/crypto/credentials/service.go)
(`decryptWith`, called from `EnsureOrgKey` at
[`service.go:161-166`](../../server/internal/crypto/credentials/service.go)).

### Root cause: write/read shape mismatch on the `encryption.dek` parameter

The DEK store was written on 2026-05-08 (`cf910d469`) against a `SetOrgParameter`
that stored the envelope string as-is. Its reader,
[`param_store.go:36-61`](../../server/internal/crypto/credentials/param_store.go)
(`ParamStore.LoadDEK`), still assumes that: it unquotes the value if it starts with
`"`, otherwise passes the raw bytes through "so a raw envelope still works".

On 2026-08-10 (`f1dd54833`, #209, shipped in v0.22.0) both `SetOrgParameter`
implementations started wrapping every value in the standard scalar envelope
`models.ParameterValue(value)` = `{"value": <v>}`
([`postgres.go:4462`](../../server/internal/db/postgres/postgres.go),
[`sqlite.go:4395`](../../server/internal/db/sqlite/sqlite.go),
[`models/parameter.go:66-70`](../../server/internal/db/models/parameter.go)).
The `Load` adapter wired in
[`server.go:3873-3891`](../../server/internal/app/server.go)
(`BuildCredentialsService`) just `json.Marshal`s `param.Value`, so `LoadDEK` now
receives `{"value":"{\"v\":1,\"alg\":\"AES-256-GCM\",…}"}`. That starts with `{`,
not `"`, so it is passed straight to `decryptWith`, which parses it as an
`envelopeJSON` with `v == 0` → `ErrUnknownVersion`. Every other `GetOrgParameter`
reader in the codebase unwraps `param.Value["value"]` (e.g.
[`regions.go:205`](../../server/internal/regions/regions.go)); the DEK adapter is
the one that was never updated.

Confirmed on the dev database:

```
select organization_uid, created_at, jsonb_typeof(value), left(value::text, 28)
  from parameters where key = 'encryption.dek';
-- 3c9d374e-… | 2026-09-03 22:45:11+00 | object | {"value": "{\"v\":1,\"alg\":
```

### Why "re-save" does not fix it, and why it only surfaced now

- The DEK was **generated for the first time** at 22:45:11 UTC, three minutes
  before the incident — this org's checks had only ever used sealed-only agent
  regions (`config_private` stays NULL, no DEK needed) until this check was put on
  a server region. Dev has exactly one `encryption.dek` row; **prod has none**, so
  the bug is latent there and will fire on the first org that stores a server-side
  secret.
- `EnsureOrgKey` caches the freshly generated DEK bytes in memory
  ([`service.go:186`](../../server/internal/crypto/credentials/service.go)) and
  never re-reads the row. The API process that generated it therefore keeps
  encrypting happily — every re-save succeeds and writes a valid envelope under
  the cached DEK — while every **other** process (the five `checks` workers, and
  the API itself after its next restart) does a cold `LoadDEK` and fails. The
  user-facing advice "re-save the check's credentials" is actively wrong here.
- Existing tests miss it because they only exercise generate → encrypt → decrypt
  inside **one** service instance (warm cache), e.g.
  [`system_agent_test.go:109-125`](../../server/internal/handlers/agentws/system_agent_test.go)
  builds the same marshal-`param.Value` adapter but never reloads the DEK from a
  second instance. There is no cold-reload test against a real `SetOrgParameter`
  / `GetOrgParameter` pair.

### Blast radius

- Any org that generates a DEK from v0.22.0 onward: all its server-region checks
  with secrets fail on every worker, and everything that decrypts server-side
  (notification channels, integrations, SSH tunnels, agent re-seal) breaks after
  the next API restart. After that restart the API can no longer decrypt to
  PATCH-merge either, so even re-saving stops working.
- The data is **not lost**: the wrapped DEK is intact inside `value.value` and the
  KEK is unchanged. Nothing must be rotated or re-entered.

## Proposal

1. **Fix the reader, accepting both shapes.** Make DEK loading unwrap the standard
   `{"value": …}` scalar envelope (use `models.ParameterValueKey`) and keep the
   legacy behaviours (`"…"` JSON string, raw envelope object with `v`/`alg`) so
   rows written before `f1dd54833` still open. Put the unwrap in one place —
   either `ParamStore.LoadDEK` or the `Load` adapter in `BuildCredentialsService` —
   and delete the now-false "we wrote it via SetOrgParameter(string)" comment.
   Reject ambiguity explicitly: if the value is an object that has neither
   `value` nor `v`, return a descriptive error instead of `ErrUnknownVersion`.
   No DB migration needed; existing wrapped rows become readable as-is.

2. **Verify the write round-trips at generation time.** In `EnsureOrgKey`, after
   `SaveDEK`, reload through the store and unwrap with the KEK before caching. A
   storage-shape regression must fail the *first* encrypt loudly (the API returns
   an error and no check is saved under a DEK nobody else can read) instead of
   silently poisoning every other process. Without this, the in-memory cache
   turns a store bug into a delayed, cross-process outage that the writing
   process can never observe.

3. **Cold-reload test against real storage.** Add a testcontainers (Postgres) and
   SQLite test that builds a credentials service via `BuildCredentialsService`'s
   real adapter (extract it so tests and the server share it — the copy in
   `system_agent_test.go` should be deleted in favour of it), encrypts for an org,
   then constructs a **second** service on the same DB and decrypts. Include a
   fixture asserting a row stored in the pre-`f1dd54833` raw-string shape still
   opens (positive control for the legacy path), and a fixture for the current
   `{"value": …}` shape that fails on the unfixed reader (negative control, so
   the test genuinely pins this bug).

4. **Make the failure diagnosable from the incident.** The check result currently
   says "re-save the check's credentials", which sends the operator down the wrong
   path. Distinguish "the org key itself cannot be opened" (a server/operator
   problem: wrong `SP_ENCRYPTION_MASTER_KEY` on this worker, or a corrupt DEK row)
   from "this check's envelope cannot be opened" (re-save may help). The
   worker's ERROR log line already carries the wrapped cause; the result output
   should at least name which layer failed and point at the worker logs.

5. **Dev remediation (ops, after the fix ships).** Deploy the fix to the API
   **and** all five `checks` workers on `solidping-dev` (they are still on 0.22.0
   — see `project_k8xp_solidping_three_deployments_synced_tag`; there are now
   five). No SQL change is required once the reader accepts the wrapped shape.
   If a hot mitigation is wanted before the release, rewriting the row to the
   bare string (`update parameters set value = value->'value' where key =
   'encryption.dek'`) makes the current readers work, because the value then
   starts with `"`; note this in the spec's closing notes if used, so the
   legacy-shape test fixture matches what is actually in the dev DB.

6. **Docs.** Update `wiki/features/` (credentials/encryption page) with the
   storage shape of `encryption.dek`, the fact that the API caches DEKs for the
   process lifetime, and the "cold process cannot open the DEK" symptom → check
   worker logs for `unwrap org DEK`.

### Open questions

- Should `EnsureOrgKey`'s in-memory DEK cache have any invalidation at all (e.g.
  drop the entry on decrypt failure and retry one cold reload)? It would have made
  the API notice its own broken write on the next restart rather than never; not
  required for this fix but worth a decision while in the file.
- The `Load` adapter is duplicated in `system_agent_test.go`; are there other
  test-side copies of production wiring that should be collapsed the same way?

## Resolved open questions

Answered by the user on 2026-09-04, before implementation started. These are
directives, not suggestions — implement to them.

**Q. Should `EnsureOrgKey`'s in-memory DEK cache have any invalidation at all
(e.g. drop the entry on decrypt failure and retry one cold reload)? It would have
made the API notice its own broken write on the next restart rather than never;
not required for this fix but worth a decision while in the file.**

**Decision: yes — add the invalidation.** On a decrypt failure, drop the cached
entry and retry exactly one cold reload before surfacing the error. The reasoning
is the incident itself: the absence of invalidation is *why* the bad write was
permanent and invisible — the process that generated the key kept serving from
cache while every other process failed, so nothing ever re-read and noticed.
Fixing only the wrap/unwrap mismatch would leave that property intact for the
next bad write. Keep it strictly bounded: one retry, no loop, no cache-wide
flush, and a test that proves a single failure triggers exactly one reload and a
persistent failure still surfaces the error rather than retrying forever.

**Q. The `Load` adapter is duplicated in `system_agent_test.go`; are there other
test-side copies of production wiring that should be collapsed the same way?**

**Decision: collapse the named duplicate only — do NOT sweep the repository.**
Point `system_agent_test.go` at the production `Load` adapter. That one is in
scope precisely because it is load-bearing for this bug: a test exercising
different wiring than production is what allowed the write/read shape mismatch to
pass CI. Do not go hunting for other test-side copies in this spec — that is an
open-ended audit inside a bugfix, and it would bury the actual fix in an
unreviewable diff. If the sweep is worth doing, it is worth its own spec; note
any other duplicates you happen to notice in the final report rather than
changing them.

**Q. (Sequencing, decided by the user alongside the above.)**

**Decision: this spec runs BEFORE `2026-09-04-02-check-dependencies-view-vs-edit-split`**,
because it fixes a live credential-decryption failure (incident #506) and a
release is pending. This does not change the spec's content — it only means the
implementer should not assume 04-02's changes are present in the tree.

## Implementation Plan

### Correction to the root-cause timeline (found while implementing)

The spec blames `f1dd54833` (v0.22.0) for introducing the `{"value": …}` wrap.
That is wrong, and it matters for the blast radius. `SetOrgParameter` has wrapped
every value since the **first commit** (`eef4383fd`): both engines already read
`jsonValue := models.JSONMap{"value": value}` at `cf910d469^`, the commit that
added the DEK store. `f1dd54833` only factored that literal into the
`models.ParameterValue()` helper.

Consequences:

- The DEK reader has been unable to do a cold load **since the DEK store shipped**
  (`cf910d469`, 2026-05-08), not since v0.22.0. Any org that generated a DEK at
  any point is affected, not only post-0.22.0 ones.
- `ParamStore.LoadDEK`'s `value[0] == '"'` branch has never fired through the
  production adapter, because `param.Value` is a `models.JSONMap` and
  `json.Marshal` of a map always starts with `{`. It is a legacy/other-adapter
  path, kept (per Proposal item 1) but exercised at the store seam, not through a
  DB row.
- **The hot mitigation suggested in Proposal item 5 must NOT be used.**
  `update parameters set value = value->'value'` leaves a bare JSON string in the
  `value` column, and `models.JSONMap.Scan` unmarshals that column into a
  `map[string]any` — a bare string makes `GetOrgParameter` itself fail
  ("cannot unmarshal string into Go value of type models.JSONMap"), turning a
  decrypt failure into a parameter-read failure. The only remediation is to ship
  the reader fix (item 5's main clause), which needs no SQL at all.

### Steps

1. **One-place unwrap in `ParamStore.LoadDEK`**
   (`server/internal/crypto/credentials/param_store.go`). A new
   `unwrapDEKParameterValue` normalizes the stored value to the raw envelope:
   `{"value": …}` (current, via `models.ParameterValueKey`, inner string or
   object), `"…"` (legacy JSON string), `{…}` carrying `v`/`alg` (raw envelope).
   An object with neither `value` nor `v` returns a descriptive error, never
   `ErrUnknownVersion`. Unwrap depth is bounded (one `value` hop). The false
   "we wrote it via SetOrgParameter(string)" comment goes.

2. **Production adapter extracted**: `credentials.NewParamStore(OrgParameterDB)`
   in the same file, over a two-method interface (`GetOrgParameter` /
   `SetOrgParameter`). `app.BuildCredentialsService` and
   `agentws/system_agent_test.go` both call it, so test wiring and production
   wiring can no longer diverge (Resolved-question 2 — named duplicate only).

3. **Write verification in `EnsureOrgKey`** (`service.go`): after `SaveDEK`,
   reload through the store and unwrap with the KEK before caching. A
   storage-shape regression fails the first encrypt loudly.

4. **Bounded DEK cache invalidation** (Resolved-question 1): `DecryptForOrg`
   opens through a helper that reports whether the failure happened *while using
   a cached DEK*. If so: drop that one cache entry, cold-reload exactly once,
   retry once, then surface the error. No loop, no cache-wide flush.

5. **Failure taxonomy**: new `credentials.ErrOrgKeyUnavailable` wraps every
   `EnsureOrgKey` failure (keeping the `unwrap org DEK` substring for log greps).
   `checkjobsvc` gains `ErrSecretsOrgKeyUndecryptable` and `ResultReason()`, and
   the two result-writing sites (`checkworker/backend/direct.go`,
   `handlers/agentws/handler.go`) use it, so an org-key failure no longer tells
   the operator to re-save the check.

6. **Tests**
   - `param_store_test.go`: shape table (current wrapped / legacy string / raw
     envelope / ambiguous object → descriptive error), plus a **negative
     control** that replays the pre-fix reader byte-for-byte and asserts it
     yields `ErrUnknownVersion` on the current wrapped shape.
   - `dek_cold_reload_test.go` (SQLite, real `NewParamStore`): service A
     generates + encrypts, a **second** service B on the same DB decrypts —
     the cold path the old tests never had. Plus the legacy raw-string
     positive control at the store seam.
   - `dek_cold_reload_postgres_test.go` (embedded Postgres, port 15520): same
     cold-reload property on the real engine.
   - `service_test.go`: exactly-one-reload and persistent-failure-still-errors
     tests over a counting store; write-verification test.

7. **Docs**: new `wiki/features/credentials-encryption.md` (storage shape,
   process-lifetime DEK cache, the cold-process symptom and its log grep, the
   remediation note above), indexed from `wiki/README.md`.

Proposal item 5 is ops and is deliberately **not** performed here: no deploy, no
database access, no SQL.

## Closing notes — ops remediation (NOT performed by this change)

Proposal item 5 is an operations step and was deliberately left undone here: no
deploy, no database connection, no SQL was run while implementing.

What remains to be done by an operator, once this ships:

- Deploy the release to the `solidping-dev` API **and** all five `checks`
  workers (they pin their own image tags — see
  `project_k8xp_solidping_three_deployments_synced_tag`). The affected org's
  checks recover on their own; nothing has to be re-entered and no secret is
  lost (the wrapped DEK is intact in `value.value`, the KEK is unchanged).
- **Do not run the hot-mitigation SQL the Proposal floats**
  (`update parameters set value = value->'value' …`). It leaves a bare JSON
  string in a column that decodes into a `models.JSONMap`, so `GetOrgParameter`
  can no longer scan the row at all — it turns a decrypt failure into a
  parameter-read failure. The reader now accepts the wrapped shape as stored,
  so there is nothing to mitigate. This is written up in
  `wiki/features/credentials-encryption.md`.
- Prod has no `encryption.dek` row yet, so nothing to remediate there — the
  latent failure simply never fires once the fix is deployed.
