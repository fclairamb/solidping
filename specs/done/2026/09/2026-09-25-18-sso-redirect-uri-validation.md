---
model: opus
effort: medium
---

# SSO logins redirect to an attacker-chosen `redirect_uri` that used to carry the tokens

## Problem

Every provider-login handler takes `redirect_uri` straight from the request
query string with no scheme/host validation, stashes it in the OAuth state
payload, and the success callback used to append `access_token` /
`refresh_token` to it:

- `server/internal/handlers/auth/{github,gitlab,google,microsoft,oidc,saml}.go:42-48`
  (each has the same shape), `discord.go:34`, `slack.go:33`
- `buildSuccessRedirect` in each handler (e.g. `github.go:109-123`)

Attack: victim clicks `https://<host>/api/v1/auth/github/login?org=default&redirect_uri=https://evil.example`,
completes their normal GitHub/Google login, and the callback 302s to
`evil.example` — with the minted tokens in the query string before spec
`2026-09-25-12` ships, or with the single-use handoff code after it ships.
A handoff code redirected to an attacker URL is still exfiltrated: the code
is exchanged by whoever presents it first. The `oauthstate` nonce protects
against CSRF only — the state payload faithfully carries the attacker's URL
because the attacker minted the login link.

The in-repo MCP authorize flow does this correctly and is the template:
`internal/oauth/authorize.go:229-239` encodes `returnTo` as a **relative
path only**, with `internal/app/mcp_endpoint_test.go:503-509` asserting
scheme/host rejection.

## Proposal

1. Add a shared helper in `handlers/auth` (one function, used by all eight
   handlers + `join_policy.go`'s pendingMembershipRedirect):
   `sanitizePostLoginRedirect(raw string, orgSlug string) string`:
   - Accept only relative paths: must start with `/`, must not start with
     `//` or `/\`, no scheme, no authority, no backslash. Reject anything
     else, including absolute URLs, even same-origin ones (relative is
     same-origin by construction and matches the MCP guard).
   - Cap length (e.g. 512 chars).
   - On any rejection, fall back to the current default:
     `config.DashboardBasePath + "/orgs/" + orgSlug`.
2. Apply at mint time (before `GenerateOAuthState`) so the state payload can
   never carry an unsanitized value, and assert again in
   `buildSuccessRedirect` / the error-redirect path (defense in depth — a
   state minted by an older deploy during a rolling upgrade must not become
   a redirect vector).
3. Add a parameter to disable the fallback-to-default behavior? No: silently
   falling back to the default dashboard destination is the correct,
   non-breaking behavior. Log a WARN with the rejected value (truncated) so
   misconfigured deep-links are diagnosable.

Spec `2026-09-25-12` is complementary and stays: it removes tokens from the
URL entirely. This spec closes the open-redirect itself, which also protects
the handoff code and the error-redirect path (`redirectWithError` echoes the
same unvalidated URI plus a description).

## Tests

- Table-driven unit test on `sanitizePostLoginRedirect`: accepts
  `/d/orgs/acme/checks`, `/d/login?returnTo=…`; rejects `https://evil.com`,
  `//evil.com`, `/\evil.com`, `javascript:alert(1)`, `/\t/evil`,
  a 600-char path; rejection falls back to the default destination.
- One handler test per provider: login with `redirect_uri=https://evil.com`,
  complete the fake-provider callback, assert `Location` is the default
  dashboard URL and never carries the attacker host (before spec
  `2026-09-25-12` lands this also asserts no tokens in the location; after it
  lands, keep the no-attacker-host assertion).
- Same test on the error path: a callback failure redirects to the default
  destination with the error params, not to the attacker URL.
- `join_policy.go` pendingMembershipRedirect covered by a dedicated case.