---
model: sonnet
effort: medium
---

# Document LDAP sign-in, and keep its settings from drifting out of the docs again

## Problem
LDAP / Active Directory sign-in shipped in batch 2026-08-26-2 (spec 2026-07-08-08, part 3) and works, but the documentation never says so beyond one line. A self-hoster who wants to plug SolidPing into lldap, OpenLDAP or AD has no way to find out that it exists, where to set it, or what to put in the fields.

**What exists in code.**
- Settings: `config.LDAPConfig` (`server/internal/config/ldap_auth.go:16-61`), one global connector: `enabled`, `server_url`, `start_tls`, `insecure_skip_verify`, `bind_dn`, `bind_password`, `base_dn`, `user_filter` (one `%s`, RFC 4515-escaped, default `(mail=%s)`), `email_attribute` (default `mail`), `name_attribute` (default `cn`).
- Three ways to set them, with precedence env > database > config file:
  - system parameters `auth.ldap.*` (`server/internal/systemconfig/systemconfig.go:160-169`, definitions around `:1100-1195`), each with an env var: `SP_LDAP_ENABLED`, `SP_LDAP_SERVER_URL`, `SP_LDAP_START_TLS`, `SP_LDAP_INSECURE_SKIP_VERIFY`, `SP_LDAP_BIND_DN`, `SP_LDAP_BIND_PASSWORD` (secret), `SP_LDAP_BASE_DN`, `SP_LDAP_USER_FILTER`, `SP_LDAP_EMAIL_ATTRIBUTE`, `SP_LDAP_NAME_ATTRIBUTE`. The overlay at `systemconfig.go:1531-1543` reads them directly, so they work even though koanf's own env loader (`config.go:2231-2247`, `_` → `.`) cannot reach snake_case keys.
  - the dashboard: Server settings → Authentication → LDAP (`web/dash0/src/routes/orgs/$org/server.auth.tsx:227-258`, route `/orgs/$org/server/auth`, super admin).
  - the `ldap:` section of `config.yml` (file loader keeps snake_case keys).
- Login flow: there is no separate LDAP button. `POST /api/v1/auth/login` tries the local password first and falls back to LDAP only for a user with no local password hash (`server/internal/handlers/auth/ldap_service.go:345-420`, `authenticateViaLDAP`). Search-then-bind with the service account (anonymous search when `bind_dn` is blank), empty passwords never sent, every failure is a generic 401 (anti-enumeration). A successful bind auto-provisions the user, or links an existing passwordless (OIDC/SAML/LDAP) account with the same email. An account that already has a local password can never be taken over through LDAP.

**What the docs say.** `web/docs/docs/configuration/authentication.md` has sections for email/password, OAuth providers (Google, GitHub, GitLab, Microsoft, Slack, Discord), roles, 2FA and the config file. LDAP appears once, in passing (`:125`, "signs in through OAuth, OIDC, SAML or LDAP"), and in `intro.md:25`. None of the ten `SP_LDAP_*` variables appears anywhere in `web/docs/`.

Found while preparing an lldap example config for promotion (lldap keeps per-app guides in `example_configs/`): without docs, that guide would have had to be reverse-engineered from the code.

## Proposal
1. **Add an "LDAP / Active Directory" section** to `web/docs/docs/configuration/authentication.md`, after the OAuth providers. It covers:
   - what it does: same sign-in form, local password first, LDAP fallback, auto-provisioning, the account-safety rule above;
   - the three ways to configure it and their precedence (env > dashboard/database > config file);
   - a table of the ten env vars with default and description (mark `SP_LDAP_BIND_PASSWORD` as secret);
   - the `config.yml` form (`ldap:` block);
   - TLS: use `ldaps://` or `SP_LDAP_START_TLS=true`; `insecure_skip_verify` is for local testing only;
   - `user_filter` rules: exactly one `%s`, escaped, composite filters allowed;
   - two worked examples: **lldap** (`ldap://lldap:3890`, bind `uid=admin,ou=people,dc=example,dc=com`, base `ou=people,dc=example,dc=com`, filter `(mail=%s)` or `(uid=%s)`) and **Active Directory** (`ldaps://dc.example.com:636`, filter `(sAMAccountName=%s)`, name attribute `displayName`);
   - troubleshooting: every failure is a 401 on purpose, so check the server log; how to test the bind with `ldapsearch`.
2. **Mention LDAP where the other providers are listed**: `intro.md` already does; also the dashboard's auth help text if it links to the docs.
3. **A drift test.** In `server/internal/systemconfig`, add a test that every `EnvVar` of the `auth.ldap.*` parameters appears in `web/docs/docs/configuration/authentication.md` (read the file relative to the repo root, as `checkers/registry/docs_anchor_test.go` does). Same spirit as the check-type count test (spec 2026-10-12-02). If cheap, extend it to every `auth.*` parameter, but only if that does not flood the doc with variables nobody should set (open question 2).
4. **lldap example config** (out of repo, for later promotion): once the doc exists, an `example_configs/solidping.md` PR to lldap/lldap becomes a copy of the lldap example above, tested against a real lldap container. Not part of this spec's implementation.

## Tests
- New unit test from step 3 fails when one of the ten `SP_LDAP_*` names is removed from the doc, and passes on the updated doc.
- Existing docs build (`web/docs`, `make` target used in CI) still passes; the new section renders with its table.
- No code path changes, so no auth test changes.

## To verify
- Manual check of the documented lldap example, so the doc is not just transcribed from code: run `lldap/lldap` and SolidPing in Docker, create a user in lldap, set the `SP_LDAP_*` env vars from the doc, sign in through the normal form with that user, confirm the user is auto-provisioned. Then confirm a wrong password and an unknown user both give the same 401.
- Confirm env > dashboard precedence as documented: set a value in the dashboard, override it with the env var, restart, check which one applies.

## Open questions
1. **OIDC and SAML have the same gap.** Generic OIDC and SAML are also only mentioned in passing in `authentication.md`. Fold them into this spec, or one spec each? Recommended: a separate spec each, so this one stays small and lands first.
2. **Scope of the drift test.** Only `auth.ldap.*`, or every system parameter with an `EnvVar`? Recommended: start with `auth.*` (LDAP, OIDC, SAML, password), which are the ones a self-hoster sets by env.
