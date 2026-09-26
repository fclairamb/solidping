---
model: sonnet
effort: medium
---

# OAuth confidential clients never authenticate: client_secret is accepted and ignored

## Problem

`oauth/register.go:80-102` mints and returns a client secret for
confidential clients, and `oauth/metadata.go:155` advertises
`client_secret_post` in the server metadata — but the token endpoint
(`oauth/token.go:42-64`, `oauth/service.go:201-247`) reads only
`code/client_id/redirect_uri/code_verifier`. The secret is hashed and
persisted (`service.go:615`) and then never verified. Any client relying on
`client_secret_post` as authentication has none, and the advertised grant
type is a lie.

Impact is currently bounded — PKCE (S256-only, `oauth/pkce.go:16-25`) is
mandatory for every grant and codes are single-use with a 60 s TTL — but the
contract is broken and a future endpoint that trusts "confidential" would
inherit the hole.

## Proposal

1. Token endpoint: for non-public clients, require the client secret.
   - Accept both `client_secret_post` (body field) and HTTP Basic
     (`client_secret_basic`) — Basic is what most libraries send; the
     metadata should advertise both.
   - Compare with the stored argon2id hash via the existing
     `passwords.Verify` path (constant-time by construction).
   - Public clients (`token_endpoint_auth_method: none`) skip the check —
     PKCE stays mandatory for them as today.
2. Registration: a client registered with `token_endpoint_auth_method`
   post/basic gets a secret (as today); `none` gets none. Reject
   confidential registrations without a secret request.
3. Migration for existing confidential clients that (validly) never sent a
   secret: behind system parameter `oauth.enforce_client_secret`, default
   **true** on fresh installs; when first deployed as true, existing
   clients that fail verification get a WARN logged with the client ID for
   30 days (flag in a parameter or in-memory) before errors become hard.
   Simplest honest version: enforce immediately, changelog-mark as
   breaking-with-notice, and log a WARN (not 4xx difference) for the first
   occurrence per client. Pick the in-process-WARN variant; no grace-mode
   state machine.
4. MCP flows: `mcp` clients register as public + PKCE (existing tests
   confirm) — they must be unaffected; add a regression test.
5. Docs: update the OAuth docs page + changelog (breaking for clients that
   stored their secret but never sent it).

## Tests

- Both DBs: confidential client + correct secret (body and Basic forms) →
  token issued; wrong secret → 401 `UNAUTHORIZED`; missing secret → 401;
  secret on a public client is ignored (accepted without).
- Registration: confidential → secret returned once and stored hashed
  (assert row is not the raw secret); `none` → no secret minted.
- MCP dynamic-client flow (existing `mcp_endpoint_test.go` cases) keeps
  passing — public clients unaffected.
- Error for bad secret is byte-identical to the error for unknown client
  (no client-enumeration oracle).