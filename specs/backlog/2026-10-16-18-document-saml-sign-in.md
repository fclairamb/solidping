---
model: sonnet
effort: medium
---

# Document SAML sign-in

**Status:** backlog. Split out of `specs/done/2026/10/2026-10-14-01-document-ldap-sign-in.md` (resolved open question 1).

## Problem
SAML sign-in works but `web/docs/docs/configuration/authentication.md` only mentions it in passing. None of the `auth.saml.*` env vars is documented. The docs drift test `server/internal/systemconfig/docs_auth_test.go` exempts `auth.saml.*` until this lands.

## Proposal
Add a "SAML" section to `authentication.md` modelled on the LDAP one: what it does, configuration sources and precedence, an env var table, the `config.yml` form, the SP metadata URL and ACS endpoint to give the IdP, attribute mapping (`email_attribute`, `name_attribute`), a worked example, and troubleshooting. Then remove the `auth.saml.` exemption from `docs_auth_test.go`.

## Tests
- `TestAuthEnvVarsDocumented` passes with the exemption removed.
