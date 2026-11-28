---
model: opus
effort: high
---

# Sealing secret org parameters must not break `${param:}` on older direct-DB workers, and a non-string or malformed value must fail closed

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/118

## Source

Maintainer report on the dev instance, after the CI tag deploy moved the API to
v0.57.0 while the region workers (direct DB backend) stayed on v0.55.0:

- At boot, v0.57.0 sealed every secret `usr.` org parameter in place (#1075,
  `b0d4b301`): `parameters.value` became `{"value": {"$sp_sealed": "<envelope>"}}`.
- The v0.55.0 workers resolve `${param:KEY}` themselves and do not know the
  envelope, so they substituted the Go rendering of a map
  (`map[$sp_sealed:{"v":1,"alg":"AES-256-GCM",...}]`) for the secret.
- Effects over ~4.5 hours: an HTTP check posting the password to an OIDC token
  endpoint got `400 invalid_grant` (incident, on-call paged every 15 minutes);
  11 DNSBL checks whose zone is `${param:<key>}.zen.dq.<provider>` queried a zone
  built from that rendering, got NXDOMAIN and reported **up / clean** while
  checking nothing; the envelope (nonce and ciphertext) was written into the
  stored output of those DNSBL results.
- Restarting the workers on v0.57.0 fixed it.

The report is right on all three mechanisms; the code below confirms them.

## Problem

1. **The seal is a one-way format change applied while older readers are live.**
   `MaybeAutoMigrateEncryption` (`server/internal/app/server.go:4659`, the org
   sweep at `server/internal/app/server.go:4744`,
   `sysParams.SealPlaintextOrgParameters`) rewrites rows at boot, gated only on a
   master key and `Encryption.AutoMigrate`. Nothing checks whether a direct-DB
   worker that cannot open `$sp_sealed` (`server/internal/db/sysparamseal/sysparamseal.go:32`)
   still runs. Workers do report their build version
   (`server/internal/db/models/worker.go:44`, `Version`), so the data to gate on
   exists.
2. **A non-scalar parameter value is printed, not rejected.**
   `secretref.StringValue` (`server/internal/secretref/resolvers.go:30-48`) falls
   through to `fmt.Sprintf("%v", val)` for any type that is neither a string nor a
   `fmt.Stringer`, so a map (a sealed envelope read by a reader that does not
   unwrap it, or any malformed row) resolves "successfully" to its Go rendering.
   `LookupParam` (`resolvers.go:63`) and therefore `ParamOverlay`
   (`server/internal/checkworker/checkjobsvc/paramrefs.go:33`) hand it to the
   check. Fixing this only protects future versions (the v0.55.0 workers had the
   old code), but it turns the next format change of this kind into an explicit
   error result instead of a silent wrong value.
3. **DNSBL treats an impossible zone as "clean".** `checkdnsbl/checker.go:96-108`
   builds `reversed + "." + zone` and maps NXDOMAIN to `res.clean[zone] = true`
   without validating the zone. `ResolveBlocklists`
   (`server/internal/checkers/checkdnsbl/config/config.go:119`) returns the
   configured strings as-is, and the resolved `${param:}` value only exists at
   execution time, so save-time validation cannot catch it. A zone containing
   spaces, brackets, `$`, `"` or a label over 63 bytes cannot be a DNS name: the
   NXDOMAIN it gets says nothing about the target.
4. **No upgrade note.** The v0.57.0 changelog does not say direct-DB workers
   must be upgraded together with the API.

## Why a spec

- Correctness hinges on concurrency between versions (rolling upgrade of the API
  vs. live direct-DB workers) and on the handling of sealed secrets.
- A product decision the reporter left open: gate the seal on worker versions,
  or refuse/alert while an older worker is active.
- Cross-cutting: secretref, the encryption auto-migration at boot, the DNSBL
  checker and the release notes.

## Proposal

1. **Gate the org-parameter seal on the live direct-DB workers.** Before
   `SealPlaintextOrgParameters` runs, list the workers seen within the liveness
   window (same notion of "active" the workers page uses) that run on the direct
   DB backend. If any reports a version below the first one that opens
   `$sp_sealed` for org parameters (v0.57.0), or reports no version, skip the org
   sweep, log a warning naming the workers and their versions, and retry on the
   next boot (and, see open question 1, periodically). Plaintext rows keep
   working for every version, so deferring is always safe. New writes through
   `sysparamseal` are the same problem: apply the same gate to sealing on write,
   or keep writing plaintext until the gate opens.
2. **Fail closed on a non-scalar value.** In `secretref.StringValue`, accept
   string, `fmt.Stringer`, booleans and numbers (formatted with
   `strconv`, as today's `%v` does for them); return `ok == false` for maps,
   slices and anything else, so `LookupParam` returns `Unresolvedf` and the check
   gets the explicit error result `ParamOverlay` already produces for an
   unresolvable reference. Unit test: a parameter whose value is
   `{"$sp_sealed": "..."}` unresolved by the store yields `ErrUnresolved`, never
   a string containing `map[`.
3. **DNSBL: an invalid zone is an error, not clean.** Validate each zone after
   resolution (non-empty labels of 1 to 63 bytes, total under 254 bytes, letters,
   digits, `-` and `_` only). An invalid zone is reported as a configuration
   error for that zone (status `error`, the zone named in the output, never its
   value when it came from a `${param:}` reference), never queried and never
   counted as clean. Test with a zone containing `map[` and spaces, and with a
   64-byte label.
4. **Do not leak the resolved value into stored output.** Check that the DNSBL
   result output masks the zone the way server-side masking masks other
   resolved `${param:}` values (`secretref/mask`); the reported results stored
   the envelope verbatim.
5. **Release notes / runbook.** Add a CHANGELOG line under v0.57.0's upgrade
   notes and to `wiki/runbooks/operations-gotchas.md`: direct-DB workers must run
   the API's version before secret org parameters are sealed; with step 1 that
   becomes "the seal waits for them".
6. **Clean up the leaked envelopes (dev only).** Not code: once deployed, purge
   or rewrite the DNSBL result outputs of the affected window on the dev
   instance, and rotate the parameter whose envelope leaked if the nonce and
   ciphertext are judged sensitive (they are not decryptable without the DEK,
   but rotation is cheap).

## Open questions

1. **Gate, or refuse/alert?** Recommended: gate (proposal step 1), retried at
   boot and on a periodic tick once the workers catch up, with a startup warning
   while deferred. Trade-off: a stale worker row could hold the seal back
   forever; bounding "active" to the liveness window avoids that. The
   alternative (refuse to start, or alert while an older worker is active) is
   simpler but turns a routine rolling upgrade into an outage of the API.
2. **Writes while gated?** Recommended: new secret values stay plaintext while
   the gate is closed and are swept when it opens. Trade-off: a short window of
   plaintext at rest, which is today's behaviour without a master key.

## Closing the issue

This spec closes fclairamb/solidping-business#118. The implementing PR body
**must** carry the line `Closes fclairamb/solidping-business#118` so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the
issue is closed and, if the merge did not close it, close it by hand:

    gh issue close 118 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
