---
model: opus
effort: high
---

# A browser-sealed check's secrets are not bound to its target: the server can send them anywhere

## Problem

Found by the security scan of 2026-11-11 (finding AJ1).

- **Severity:** Medium.
- **Who can trigger it:** whoever controls the SolidPing server or its database: the SaaS operator, an attacker who
  compromised the server, or someone holding write access to the database. This is exactly the party browser
  sealing claims to protect against. An org writer can do the same, but that is the accepted trust model (ruled
  2026-10-16), so it is not the issue here.

Browser sealing (#834, spec `2026-11-08-01`) encrypts a check's secrets in the browser to keys only the org's
agents hold. The docs promise that this protects against "a later compromise of the server, and a legal demand on
the operator for your check passwords" (`web/docs/docs/features/browser-sealed-secrets.md:136`). And
`SP_AGENT_REWRAP_ENABLED=false` is presented as the way to "rule out" a compromised server (`:91`).

But the sealed blob is only tied to its recipient keys, not to the check it was sealed for:

- A PATCH that changes only the non-secret config (the URL, host, port, DSN host, check type parameters) keeps
  the blob as it is. Only a region change asks for a re-seal (`server/internal/handlers/checks/service.go:2290`,
  `applySealedOnlyReplacement` at `:5685`, `browser_sealing.go:511`).
- The agent unseals whatever blob the job carries and merges every key of the plaintext into the job's config
  (`server/internal/checkworker/backend/ws.go:340-355`, `credentials.MergeConfig`,
  `server/internal/crypto/credentials/secret_fields.go:61`). It never checks that the config is the one the
  secrets were sealed for.

So whoever can write `checks.config` (or `check_jobs.config`) can point a sealed check at a host they control: an
HTTP check's `url`, a database check's host. They can also copy the blob of one check onto another check of
the same region. On its next run, the agent opens the secrets and sends them there (basic auth header, database
password, SMTP login). This works silently on every stored blob, at once, without the user typing anything. It
also works with `SP_AGENT_REWRAP_ENABLED=false`, which the docs present as the way to rule out a compromised
server.

The threat model of the spec (`specs/done/2026/11/2026-11-08-01-browser-sealed-check-secrets.md:174-190`) and of
the docs lists "modified dashboard JavaScript" and "swapped recipient keys". Both only catch secrets typed after
the compromise. Retargeting stored secrets is not listed at all, and "What the server still sees: targets"
(`browser-sealed-secrets.md:130`) does not say that the server can also change them.

## Proposal

1. **Seal a binding next to the secrets.** dash0 (`web/dash0/src/lib/sealing.ts`), `solidping seal`
   (`server/pkg/cli/seal.go`) and the rewrap executor put a reserved `_binding` object in the sealed plaintext:
   the check type and a digest of the check's *target fields*, the ones that decide where the secrets go.
   - Each checker declares those fields next to its secret fields (`credentials/secret_fields.go`). For example:
     `url` for http, host/port/database for the database checkers, host/port for tcp/smtp/ssh/ftp, the tunnel
     reference.
   - The digest is SHA-256 over a canonical JSON of those fields, normalised the same way on both sides:
     lowercase scheme and host, default port made explicit.
   - Put a golden test vector in a shared fixture (`web/dash0/src/lib/__fixtures__/`), read by both the Go and
     the TS tests.
2. **The agent checks the binding before using the secrets.** In `ws.go` claim handling (and the tunnel unseal
   path), after `UnsealWithIdentities`, recompute the digest from the job's config.
   - On mismatch, or a missing `_binding` on a blob whose origin is `browser`, do not run the check. Submit an
     explicit error result (`sealed secrets were sealed for another target: type them again`), like
     `submitSealError`.
   - Strip `_binding` before `MergeConfig`. Also refuse any plaintext key that is not one of the type's secret
     fields, so a blob can never override a public config field.
3. **The server enforces the same rule, so users see it at write time.** A PATCH that changes a target field of a
   browser-sealed check without new `sealedSecrets` (or plaintext secrets in optional mode) gets
   `409 SEALING_RECIPIENTS_CHANGED`-style `SEALED_TARGET_CHANGED`, like the region change at `service.go:2290`. The
   same goes for bulk update and config-as-code apply/plan (`apply.go`, `plan.go`). dash0 opens the secret inputs
   (**Replace**) when the user edits a target field of a sealed check.
4. **Rewrap keeps the binding.** The executor copies `_binding` as it is. It never rebinds to a target the server
   sends, so a rewrap can never move secrets to a new target.
5. **Existing blobs** (sealed before this change) have no binding. The agent accepts them for now, logs a
   one-time warning per check, and the Re-seal page lists them as "seal again to bind to the target". Open
   question below.
6. **Docs:** in `browser-sealed-secrets.md`, add retargeting to the threat model. Say what the binding covers and
   what it does not: a target the attacker already controls, or DNS of the target host.

## Tests

- Go, `server/internal/checkworker/backend` (ws claim test): a browser blob bound to `https://db.acme.com`, served
  on a job whose `url` is `https://evil.example` → error result, no checker run, no request to the new host.
  Positive control: the same blob on the original URL runs and the checker receives the secret.
- Go: a blob carrying a key outside the type's secret fields (for example `url`) → refused, the public `url` is
  not overridden.
- Go, `handlers/checks`: PATCH changing `url` on a browser-sealed check without `sealedSecrets` → 409
  `SEALED_TARGET_CHANGED`. PATCH changing only `timeout` → 200, blob kept. Bulk update and `/checks/apply`: same
  pair.
- Go/TS: the shared fixture gives the same digest in `sealing.ts` and in the Go canonicaliser.
- Go, `rewraptask`: the rewrapped plaintext keeps the original `_binding`.
- Playwright `browser-sealed-secrets.spec.ts`: editing the URL of a sealed check opens the secret inputs.

## Open questions

- Unbound legacy blobs (sealed by v0.47.0): accept them with a warning until the user re-seals, or refuse them
  after one release? **Recommended:** accept with a warning and list them on the Re-seal page in this change.
  Refuse them in the next minor, with a changelog entry.

## Resolved open questions

- Unbound legacy blobs (sealed by v0.47.0): accept them with a warning until the user re-seals, and list them on the Re-seal page in this change. Refuse them in the next minor, with a changelog entry. (autopilot: recommended answer)
