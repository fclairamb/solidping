# Enterprise auth protocols: LDAP, generic OAuth2/OIDC, and SAML login support

## Problem

SolidPing's login story today is email/password, passkeys, and a fixed set of
six hardcoded social OAuth providers (Google, GitHub, GitLab, Microsoft, Slack,
Discord — `server/internal/handlers/auth/{google,github,gitlab,microsoft,slack,discord}_service.go`,
wired in `server/internal/app/server.go:484-530`). Organizations that run their
own identity infrastructure cannot plug it in:

- **Generic OAuth2/OIDC**: there is no way to connect an arbitrary
  OAuth2/OIDC identity provider (Keycloak, Authentik, Okta, Auth0, a corporate
  Azure AD tenant beyond the hardcoded Microsoft app…). `ProviderTypeOIDC`
  exists only as an enum constant (`server/internal/db/models/auth.go:112`) —
  no handler, no config, no route. Each of the six existing providers is a
  bespoke `*_service.go` pair; adding an IdP means writing code.
- **SAML**: same situation — `ProviderTypeSAML` is a bare constant
  (`server/internal/db/models/auth.go:111`) with zero implementation. SAML is
  still the lingua franca of enterprise SSO (Okta, ADFS, Ping, Shibboleth).
- **LDAP**: entirely absent — no code, config key, enum, or dependency.
  Self-hosted deployments with an existing LDAP/Active Directory often want
  bind-based password verification against the directory instead of local
  password hashes.

The groundwork for OAuth-shaped providers is well established (per-provider
config structs in `server/internal/config/*_oauth.go`, system-parameter overlay
keys in `server/internal/systemconfig/systemconfig.go:54-73`, the
`GET /api/v1/auth/providers` discovery endpoint in
`server/internal/handlers/auth/providers_available.go`, login-page buttons in
`web/dash0/src/routes/orgs/$org/login.tsx`, super-admin config UI in
`web/dash0/src/routes/orgs/$org/server.auth.tsx`, `UserProvider` identity
linking, and the `maxSsoUsers` entitlement enforced via
`CheckSSOMembership` in `server/internal/entitlements/service.go:185`).
SAML and LDAP do not fit that redirect/callback shape and are greenfield.

## Proposal

Add three new authentication mechanisms, reusing the existing provider
plumbing wherever it fits:

### 1. Generic OAuth2/OIDC provider (likely first — cheapest, broadest reach)

- New config struct `server/internal/config/oidc_oauth.go` (or a generic
  `custom_oauth.go`): issuer URL (with OIDC discovery), client id/secret,
  scopes, optional claim mappings (email, name, avatar), `enabled`.
- Declare the keys in `systemconfig.go` so they are editable as system
  parameters, and add the provider to the `providers[]` array in
  `server.auth.tsx`.
- New `oidc.go`/`oidc_service.go` handler pair following the existing
  `/auth/oidc/login` + `/auth/oidc/callback` shape, resolving/creating users
  through `UserProvider` with `ProviderTypeOIDC`, and going through the same
  `ensureMembership` + `CheckSSOMembership` entitlement path as the social
  providers.
- Surface it on the login page via the existing `useProviders()` discovery,
  with a configurable display name/label (the button should say
  "Continue with <Company IdP>", not "OIDC").

### 2. SAML 2.0 Service Provider

- SP-initiated login: `GET /api/v1/auth/saml/login` → redirect to the IdP with
  an AuthnRequest; ACS endpoint `POST /api/v1/auth/saml/acs` consumes the
  assertion, maps NameID/attributes to email/name, resolves via `UserProvider`
  with `ProviderTypeSAML`, and issues the normal JWT/refresh session.
- Serve SP metadata at a well-known path so IdP admins can configure their
  side by URL.
- Config: IdP metadata URL (or pasted metadata XML), SP entity ID, attribute
  mappings, signing/encryption certificates. Use a maintained Go library
  (e.g. `crewjam/saml`) rather than hand-rolling XML-DSig.
- Reuse `oauthstate`-style relay-state handling for `returnTo`.

### 3. LDAP / Active Directory bind authentication

- LDAP is not redirect/callback — it plugs into the **password login path**
  (`POST /api/v1/auth/login`, `handler.go`/`service.go` in
  `server/internal/handlers/auth/`): when LDAP is enabled, verify the
  submitted password with an LDAP bind (search-then-bind: service account DN +
  user filter, e.g. `(mail=%s)` / `(sAMAccountName=%s)`) instead of — or as a
  fallback ordering question with — the local `PasswordHash`.
- Config: server URL (ldap:// / ldaps:// + StartTLS option), bind DN +
  password (secret), base DN, user search filter, attribute mappings
  (email, display name), group filter (see open questions).
- On first successful bind, auto-provision the `User` (with nil
  `PasswordHash`) and a `UserProvider` row with a new `ProviderTypeLDAP`
  constant, so LDAP users count toward `maxSsoUsers` like other SSO users.

### Cross-cutting

- **Scope of configuration**: the existing six providers are configured
  globally via system parameters (super-admin). Enterprise SSO is usually
  **per-org** — `OrganizationProvider` (`server/internal/db/models/auth.go:11`)
  already exists with an AES-GCM-encrypted `MetadataPrivate` envelope and is
  the natural home for org-scoped IdP config. Decide global-only first pass
  vs. per-org from the start (see open questions).
- All three paths must respect the `maxSsoUsers` entitlement
  (`CheckSSOMembership`) and record identities in `UserProvider`.
- Login page (`login.tsx`) gains buttons/fields per newly enabled mechanism
  via the providers discovery endpoint; LDAP mode may simply change what the
  email/password form validates against (no visible button needed).
- E2E coverage: OIDC can be tested against a lightweight in-repo fake IdP;
  LDAP against a testcontainers OpenLDAP/glauth; SAML at minimum with
  library-level unit tests plus a mocked IdP flow.

## Open questions

- **Per-org vs. global config**: first pass global-only (system parameters,
  matching the existing pattern) or per-org from day one via
  `OrganizationProvider`? Per-org is the real enterprise ask but touches org
  settings UI, admin permissions, and multi-IdP discovery on the login page.
- **Multiple instances**: one generic OIDC + one SAML + one LDAP connector,
  or N configurable instances of each? Suggest exactly one of each per scope
  for the first pass.
- **LDAP fallback ordering**: when LDAP is enabled, do local-password users
  (e.g. the bootstrap admin) still authenticate locally? Suggested: try local
  hash first if the user has one, else LDAP — and never lock out the
  super-admin.
- **Group/role mapping**: map IdP groups (LDAP groups, SAML attributes, OIDC
  claims) to `OrganizationMember.Role`? Suggest out of scope for the first
  pass; default role on auto-provision like the social providers do.
- **IdP-initiated SAML**: support it, or SP-initiated only (safer, simpler)?
- **SLO (single logout)**: out of scope for the first pass?
- **SCIM / directory sync**: explicitly out of scope here; this spec is
  login-time provisioning only.

## Implementation Plan — Part 1: Generic OAuth2/OIDC

This spec is implemented in three staged passes (OIDC, then SAML, then LDAP).
This section covers **part 1 only** — generic OAuth2/OIDC. Parts 2 (SAML) and
3 (LDAP) will each add their own plan section below when implemented.

Resolved for this pass (cross-cutting decisions adopted, not re-litigated):
global-only config via system parameters (no `OrganizationProvider`), exactly
one OIDC connector instance, no group/role mapping (default role like social
providers), standard OIDC discovery for issuer configuration.

1. **Config** — `server/internal/config/oidc_oauth.go`: `OIDCOAuthConfig`
   (`Enabled`, `DisplayName`, `IssuerURL`, `ClientID`, `ClientSecret`,
   `Scopes`, `EmailClaim`/`NameClaim`/`AvatarClaim` mappings with standard-claim
   fallbacks). Wired into `config.Config` as `OIDC`.
2. **System parameters** — register `auth.oidc.*` keys in
   `systemconfig.go`'s `getKnownParameters()`, following the existing
   provider key pattern (secret flag on `client_secret` only).
3. **Handler pair** — `server/internal/handlers/auth/oidc.go` +
   `oidc_service.go`, modeled on `google.go`/`google_service.go`:
   - `GET /api/v1/auth/oidc/login` and `GET /api/v1/auth/oidc/callback`,
     reusing the shared `OAuthState`/`ErrInvalidOAuthState` machinery.
   - Real OIDC discovery + ID token validation via
     `github.com/coreos/go-oidc/v3` (issuer, audience, signature via JWKS,
     expiry) — a new dependency added deliberately instead of hand-rolling
     JWT/JWKS verification, since that is the security-critical part of this
     feature.
   - `findOrCreateUser`/`ensureMembership` mirror the social providers
     exactly, including the `CheckSSOSlot` (`maxSsoUsers`) enforcement call.
4. **Routing** — wire `/auth/oidc/{login,callback}` in `server.go` alongside
   the six existing providers, gated on `Enabled && IssuerURL != "" &&
   ClientID != ""`.
5. **Discovery + admin UI** — add the provider to
   `providers_available.go` (using `DisplayName`, default "SSO") and to
   `server.auth.tsx`'s `providers[]` array (new fields: display name, issuer
   URL, scopes, claim mappings).
6. **Login page** — no code change needed beyond the icon map: `login.tsx`
   already renders providers generically from `useProviders()`; added a
   generic shield icon for `type: "oidc"`.
7. **Tests** — Go service-level tests with an in-repo fake OIDC IdP
   (`httptest.Server` serving discovery + JWKS + token + userinfo), covering:
   happy-path login (creates `User` + `UserProvider(ProviderTypeOIDC)` +
   session), and negative paths (wrong issuer, wrong audience, expired token,
   bad signature) — plus a `maxSsoUsers`/`CheckSSOMembership` enforcement
   test using a stub `EntitlementsChecker`.
