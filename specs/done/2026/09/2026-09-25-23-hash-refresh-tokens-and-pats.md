---
model: opus
effort: medium
---

# Session refresh tokens and PATs are stored in plaintext in `user_tokens.token`

## Problem

`NewUserToken` stores the raw token (`db/models/auth.go:355`, written at
`handlers/auth/service.go:2245`); lookups match the plaintext verbatim
(`GetUserTokenByToken` → `service.go:1321`, `ValidatePATToken` →
`service.go:1428`). Refresh tokens are 32 crypto/rand bytes, PATs are
`pat_` + 24 bytes — but both sit in clear in the DB. A stolen database dump
yields live credentials for up to 7 days, even with
`SP_ENCRYPTION_MASTER_KEY` correctly configured, and contradicts the
package's own DB-theft threat model.

The codebase already knows the right pattern everywhere else:
- password-reset tokens are SHA-256 hashed at rest (`service.go:2716-2724`)
- kiosk tokens are sha256-hex (`statuspagekiosk/statuspagekiosk.go:29-35`)
- agent tokens are SHA-256 hashed (`db/models/agent.go:137`)
- OAuth client secrets are argon2id-hashed (`oauth/service.go:610-615`)

## Proposal

1. SHA-256 is the right hash here (256-bit random values, no need for
   argon2id — see the kiosk token justification). Store `sha256hex(token)`
   in `user_tokens.token`; lookups hash the presented token and compare.
2. Migration (both dialects): one pass converting existing plaintext rows —
   `UPDATE user_tokens SET token = sha256(token)`-equivalent done in the Go
   migration (row loop, not raw SQL, so SQLite and Postgres share the logic
   and the hashing stays in Go). Rows are all high-entropy random values, so
   the hash is not reversible in practice. Column stays `token` (or rename
   to `token_hash` with a view-compat rename — prefer the rename for
   honesty; both dialects support it).
3. Update every lookup/creation site: `GetUserTokenByToken`,
   `ValidatePATToken`, refresh-token validation on `Refresh` (the JWT claims
   carry `RefreshUID`; find and hash-compare), session pruning paths, and
   `pkg/cli` helpers that read tokens directly.
4. PAT cache (`service.go:1512-1520`): key by the presented token's hash
   instead of the plaintext (also removes plaintext tokens from memory map
   keys).
5. Keep the response shapes unchanged — this is purely at-rest.
6. Docs: no user-visible change; changelog entry (security hardening).

Pair with spec `2026-09-25-24` (logout invalidation): once rows are hashed,
`Logout` deleting the row is the revocation story.

## Tests

- Migration test: seed plaintext rows, run migration, assert stored values
  are 64-char hex equal to `sha256` of the original, and the original
  tokens still authenticate.
- Both DBs: refresh flow (login → refresh → new access token) works end to
  end; PAT create/validate/revoke works; wrong token → same generic 401.
- Assert the DB row never contains the raw token after login (read the row
  in the test).
- Revoked PAT + cached hash → still rejected immediately (cache keyed by
  hash, row gone).