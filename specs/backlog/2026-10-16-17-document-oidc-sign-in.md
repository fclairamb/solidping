---
model: sonnet
effort: medium
---

# Document generic OIDC sign-in

**Status:** backlog. Split out of `specs/done/2026/10/2026-10-14-01-document-ldap-sign-in.md` (resolved open question 1).

## Problem
Generic OIDC sign-in works but `web/docs/docs/configuration/authentication.md` only mentions it in passing. None of the `auth.oidc.*` env vars is documented. The docs drift test `server/internal/systemconfig/docs_auth_test.go` exempts `auth.oidc.*` until this lands.

## Proposal
Add an "OIDC" section to `authentication.md` modelled on the LDAP one: what it does, the three configuration sources and their precedence, an env var table, the `config.yml` form, a worked example (e.g. Keycloak or Authentik at `auth.acme.com`), and troubleshooting. Then remove the `auth.oidc.` exemption from `docs_auth_test.go`.

## Tests
- `TestAuthEnvVarsDocumented` passes with the exemption removed.
