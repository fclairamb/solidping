# Authentication

Login, account lifecycle, personal access tokens, second factors, passkeys, and
the external identity providers (OAuth, OIDC, SAML).

## Public

### POST /api/v1/auth/login
Email/password login. Returns access token, refresh token, and user info. Body accepts optional `org` field.

### POST /api/v1/auth/refresh
Refresh an expired access token using a refresh token.

### POST /api/v1/auth/register
Register a new user account. Sends a confirmation email.

### POST /api/v1/auth/confirm-registration
Confirm a registration via email token. Returns access token.

### POST /api/v1/auth/request-password-reset
Request a password reset email.

### POST /api/v1/auth/reset-password
Reset password using a reset token.

### GET /api/v1/auth/invite/:token
Get invitation details by token (used to pre-fill the accept-invite form).

### POST /api/v1/auth/accept-invite
Accept an organization invitation. Creates the user if needed and returns access token.

### POST /api/v1/auth/2fa/verify
Verify a 2FA code during login (when login returns a 2FA challenge).

### POST /api/v1/auth/2fa/recovery
Use a recovery code to bypass 2FA during login.

### GET /api/v1/auth/providers
List enabled authentication providers (password, OAuth providers). Auth: public

## Authenticated

### POST /api/v1/auth/logout
Logout and invalidate the current session. Auth: required

### POST /api/v1/auth/switch-org
Switch the user's active organization context. Auth: required

### GET /api/v1/auth/me
Get the current authenticated user's profile. Auth: required

`organization` is `null` for an org-less session. That covers two cases, both
`200`: a token minted with no org slug (a user who belongs to nothing), and a
token whose org slug **no longer resolves** — the org was deleted or renamed
while the token was in flight. The second case used to `401`, which logged a
user out on a plain reload right after they deleted their own org; the user's
authentication is intact, only their org context is gone, so the response
degrades instead.

### PATCH /api/v1/auth/me
Update the current user's profile (name, password, etc.). Auth: required

### GET /api/v1/auth/tokens
List all personal access tokens for the current user across all organizations. Auth: required

### DELETE /api/v1/auth/tokens/current
Revoke the token the request is authenticated with (self-revoke / "sign out
everywhere else" style flows). Auth: required. Registered **before** the
`:tokenUid` param route so chi matches the literal `current` segment first —
keep that ordering if the routes are ever reshuffled.

### DELETE /api/v1/auth/tokens/:tokenUid
Revoke a personal access token. Auth: required

### POST /api/v1/auth/2fa/setup
Begin 2FA setup. Returns a TOTP secret and QR code URI. Auth: required

### POST /api/v1/auth/2fa/confirm
Confirm 2FA setup by verifying a TOTP code. Auth: required

### DELETE /api/v1/auth/2fa
Disable 2FA for the current user. Auth: required

## Passkeys (WebAuthn)

Login ceremonies are public (the user is not yet authenticated); registration
and management require a session.

### POST /api/v1/auth/passkeys/login/begin
Start a passkey login ceremony. Returns the WebAuthn assertion options. Auth: public

### POST /api/v1/auth/passkeys/login/finish
Complete the login ceremony with the authenticator's assertion. Returns access
and refresh tokens on success. Auth: public

### POST /api/v1/auth/passkeys/register/begin
Start registering a new passkey for the current user. Auth: required

### POST /api/v1/auth/passkeys/register/finish
Complete passkey registration with the authenticator's attestation. Auth: required

### GET /api/v1/auth/passkeys
List the current user's registered passkeys. Auth: required

### PATCH /api/v1/auth/passkeys/:uid
Rename a passkey. Auth: required

### DELETE /api/v1/auth/passkeys/:uid
Remove a passkey. Auth: required

## OAuth Providers (Conditional)

Each provider is only registered if its `ClientID` is configured. All are public.

### GET /api/v1/auth/slack/login
### GET /api/v1/auth/slack/callback

### POST /api/v1/auth/slack/exchange
Exchange a Slack-issued code/identity for a SolidPing session — used by the
Slack app sign-in path rather than the browser redirect flow. Auth: public

### GET /api/v1/auth/google/login
### GET /api/v1/auth/google/callback

### GET /api/v1/auth/github/login
### GET /api/v1/auth/github/callback

### GET /api/v1/auth/microsoft/login
### GET /api/v1/auth/microsoft/callback

### GET /api/v1/auth/gitlab/login
### GET /api/v1/auth/gitlab/callback

### GET /api/v1/auth/discord/login
### GET /api/v1/auth/discord/callback

## OIDC (generic)

Registered when a generic OIDC provider is configured. Both public.

### GET /api/v1/auth/oidc/login
Begin the OIDC authorization-code flow (redirects to the provider).

### GET /api/v1/auth/oidc/callback
OIDC redirect target. Exchanges the code, provisions/links the user, and issues
SolidPing tokens.

## SAML

Registered when a SAML identity provider is configured. All public — the SAML
assertion itself is the credential.

### GET /api/v1/auth/saml/login
Begin the SAML flow (redirect or POST binding to the IdP).

### POST /api/v1/auth/saml/acs
Assertion Consumer Service. Consumes the IdP's SAMLResponse, provisions/links
the user, and issues SolidPing tokens.

### GET /api/v1/auth/saml/metadata
Service-provider metadata XML, for upload into the IdP.
