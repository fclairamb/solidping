---
model: opus
effort: high
---

# In-process check worker never decrypts/merges `config_private` — encrypted checks run without their secrets

## Problem

When credential encryption is enabled (`SP_ENCRYPTION_MASTER_KEY` set), a
check's secret fields are split out of the public config into an encrypted
`config_private` envelope. `check_jobs` rows correctly carry both halves —
`server/internal/db/postgres/postgres.go:1172-1175` copies `check.Config`
(public) plus `ConfigPrivate`/`ConfigPrivateKeys` onto each job.

But the standard **in-process** execution path only ever reads the public
half:

- `server/internal/checkworker/worker.go:729` — `checkerConfigMap :=
  checkJob.Config`, then straight into `config.FromMap(...)`.
- `rg "MergeConfig|ConfigPrivate|credentials" server/internal/checkworker/`
  returns **zero matches** — the whole package never touches the private
  envelope.
- `checkjobsvc.NewService(dbService.DB())`
  (`server/internal/app/server.go:269`) receives no credentials service, so
  the claim path has nothing to decrypt with either.

The only decrypt-and-merge in the codebase lives in the remote-worker HTTP
claim API — `server/internal/handlers/workers/service.go:199-228`
(`creds.DecryptForOrg` + `credentials.MergeConfig`, then stripping the
envelope). Per `2026-07-16-02-per-org-deported-agent-websocket.md`, that HTTP
path has no production client.

**Consequence**: with encryption enabled, every secret-bearing check executed
by the in-process worker (http `password`/`secretHeaders`, ssh/sftp
`private_key`, DB passwords, …) runs **without** its secrets — auth silently
missing, checks failing or, worse, "succeeding" against endpoints that don't
actually require the credential. The bug is masked on deployments without the
master key (e.g. the k8xp dev deploy), where secrets fall back to plaintext in
the public config — which is why it has gone unnoticed.

## Proposal

1. **Confirm by test first**: enable encryption in a test (see
   `server/internal/crypto/credentials/`), create e.g. an http check with a
   password, run the in-process worker path, and assert the checker receives
   the merged config (it currently won't — the test should fail before the
   fix).
2. **Fix on the in-process claim path**: decrypt `ConfigPrivate` for the job's
   org and `credentials.MergeConfig` it into `job.Config` before the checker
   config is parsed, mirroring the semantics of
   `handlers/workers/service.go:199-228` (including the "strip the envelope
   after merge" invariant). Seam options:
   - inject the credentials service into `checkjobsvc` and merge at claim
     time, or
   - inject it into `checkworker` and merge just before `config.FromMap`.

   Pick the seam that also suits the planned `WorkerBackend` refactor in
   `2026-07-16-02-per-org-deported-agent-websocket.md` — decrypting once at
   the claim/dispatch boundary (so every backend, in-process or deported,
   receives an already-merged config) is likely the right shape; factor the
   merge logic so the remote-worker HTTP handler and the in-process path share
   it rather than duplicating it.
3. **Failure semantics**: decide what an in-process worker does when the
   envelope exists but decryption is impossible (no master key, decrypt
   error). The HTTP path skips the job with a warning; the in-process path
   should not silently run the check without secrets — skipping with a log, or
   failing the job with an explicit error result, both beat the current silent
   secret-drop. Document the choice.
4. **Regression tests for both PG and SQLite** (testify/require,
   `t.Parallel()`, per `server/CLAUDE.md`): encrypted job → checker sees
   merged secrets and no envelope; encryption disabled → plaintext behavior
   unchanged; decrypt failure → chosen failure semantics.

## Implementation Plan

### Seam: `DirectBackend` (the in-process claim/dispatch boundary)

The `WorkerBackend` refactor from `2026-07-16-02` has already landed, so the
claim/dispatch boundary the spec anticipated exists today:

- `backend.DirectBackend` — in-process claim (`ClaimJobs`,
  `ClaimJobsForCheck`). **This is where the fix goes.**
- `backend.WSBackend` — agent claim; already unseals `ConfigSealed` and merges
  (`ws.go:184`), so a deported agent already receives a merged config.

Merging inside `DirectBackend.ClaimJobs`/`ClaimJobsForCheck` means every job
handed to `CheckWorker` is already merged, for both backends — `worker.go`
itself needs no change and stays transport-agnostic.

Rejected alternatives:
- `checkjobsvc.serviceImpl` — would force a decrypt on the deported-agent claim
  path (`ClaimJobsForAgent`), which must ship `ConfigSealed` verbatim and must
  never server-side decrypt (`agents/protocol.go:100`).
- `checkworker.worker.go` just before `config.FromMap` — leaves the envelope
  travelling further than it must and duplicates the merge per call site.

### Shared merge helper

`credentials.MergeConfig` + `DecryptForOrg` are today open-coded in
`handlers/workers/service.go:106-140`. Factor that into
`checkjobsvc.MergeJobSecrets(ctx, creds, job) (SecretMerge, error)` (new file
`checkjobsvc/secrets.go`) — `checkjobsvc` is the common import of both
`backend` and `handlers/workers`. It:

1. returns `SecretMergeNoop` when `ConfigPrivate` is nil/empty (the plaintext /
   encryption-disabled fallback — **untouched**);
2. returns `SecretMergeUnavailable` when an envelope exists but
   `creds == nil || !creds.Enabled()`;
3. returns `SecretMergeFailed` + err on decrypt error;
4. on success merges private into `job.Config`, strips the envelope
   (`ConfigPrivate`/`ConfigPrivateKeys` = nil, `Encrypted` = false) and returns
   `SecretMergeMerged`.

Callers decide the failure policy; the helper never logs the plaintext.

### Failure semantics (spec item 3) — explicit error result

The in-process path follows the precedent `WSBackend.submitSealError`
(`ws.go:195`) already set: **an explicit error result, not a silent skip**.
`DirectBackend` drops the job from the claim batch and submits a
`StatusError` result with an actionable message, which also releases the lease
and drives incident processing — so the check goes visibly red rather than
silently never running (a plain skip would leave the job wedging until lease
expiry, retrying forever with nothing in the check's history). The only text in
the result `output.error` is the static reason; never any config value.

### Steps

1. `checkjobsvc/secrets.go` — `MergeJobSecrets` + `SecretMerge` outcomes.
2. `handlers/workers/service.go` — `ClaimJobs` reuses the helper (behavior
   unchanged: skip + log).
3. `backend/direct.go` — `creds credentials.Service` field +
   `NewDirectBackend` param; `mergeClaimedSecrets` applied to `ClaimJobs` and
   `ClaimJobsForCheck`; error result on unavailable/failed.
4. `checkworker/worker.go:176` — pass `svc.Credentials` (set at
   `app/server.go:292`, well before the worker is built at `server.go:2137`).
5. Tests:
   - `checkjobsvc/secrets_test.go` — unit: noop / merged+stripped / disabled /
     decrypt-failure.
   - `backend/direct_test.go` (SQLite) — **the proving test**: an encrypted job
     claimed through `DirectBackend.ClaimJobs` yields a merged `Config`
     carrying the secret and no envelope. Fails before the fix.
   - `checkworker/worker_test.go` (SQLite) — end-to-end through the in-process
     worker: an encrypted check's checker receives its secret; and the
     decrypt-failure job produces a `StatusError` result.
   - PG parity via the existing `checkjobsvc` PG test harness.
   - A leak assertion: the merged secret appears in neither the result
     `output`/`metrics` nor the log output.
