# Passkey Authentication and TOTP UI

## Overview

Add passkey support to SolidPing as a first-class authentication method **and** ship
the missing frontend for the existing TOTP backend. Both belong on the same new
`account/security` tab and the login flow has to handle them coherently — doing one
without the other would leave the UI inconsistent.

Passkeys are phishing-resistant, work across devices via iCloud Keychain / Google
Password Manager / 1Password / Bitwarden, and let users skip the password+TOTP dance
entirely. They are the direction every serious SaaS is moving (Linear, GitHub,
Cloudflare, AWS, Google Workspace). The cryptographic primitive is
[WebAuthn](https://www.w3.org/TR/webauthn-2/); the user-facing brand is **passkey**.
Backend uses [github.com/go-webauthn/webauthn](https://github.com/go-webauthn/webauthn);
frontend uses [@simplewebauthn/browser](https://simplewebauthn.dev/).

TOTP scope: the 2026-03-22 spec landed the User-model fields (`totp_secret`,
`totp_enabled`, `totp_recovery_codes`), the service methods (`Setup2FA`, `Confirm2FA`,
`Verify2FA`, `UseRecoveryCode`, `Disable2FA`), the temp-token (`TwoFAClaims`), and the
`requires2Fa` / `tempToken` fields on `LoginResponse`. The dashboard side — security
page, setup wizard with QR code, recovery-codes screen, and the login-time 2FA
challenge form — was never built. This spec finishes that.

---

## Goals

1. Let any user register one or more passkeys against their account.
2. Let users sign in with a passkey instead of email+password (passwordless).
3. Replace the TOTP step when a user signs in with a passkey (a passkey is already strong auth).
4. Keep PAT, OAuth providers, and password+TOTP working unchanged.
5. Self-hostable: the relying-party identity (RP ID, origin) must come from config, not be hard-coded.
6. Ship the dashboard UI for the existing TOTP backend: enable/disable, QR setup, recovery
   codes screen, and the login-time 2FA challenge step.

## Non-goals

- Replacing passwords for everyone. Passwords stay supported.
- Per-org passkey policies (e.g. "this org requires passkeys"). Defer to a later spec.
- Cross-device CTAP / hybrid transport tuning. We use the library's defaults.
- Attestation verification (we accept any conformant authenticator). Defer to a hardening spec.
- Changes to the TOTP **backend**. That work is already shipped; this spec only builds its UI.

---

## Honest opinion (author's take)

**Worth doing now.** Passkeys are mature in 2026: the major OS and browser stacks support
sync, the libraries are stable, and users increasingly expect them. The marginal complexity
is small because we already have the JWT/refresh-token plumbing — passkeys plug in as a new
*authentication method*, not a new *session model*.

**Don't reuse `user_tokens` as the storage table.** The user said "re-use the current tokens
logic." That makes sense for the post-auth session (passkey login → same JWT + refresh token
we already issue) but **not** for credential storage. A passkey is a long-lived public key,
not a bearer token: never expires by default, has its own metadata (transports, sign count,
backup eligibility, AAGUID), and a credential ID that the WebAuthn library hands back as
bytes. Forcing it into `UserToken` would either bloat `properties` JSONB with WebAuthn
internals or leak abstractions. New table, same conventions.

**Replace TOTP, don't stack on top of it.** When a user has a passkey *and* TOTP enabled,
passkey login should skip TOTP. The passkey already provides multi-factor (something you
have + biometric or PIN). Stacking TOTP on top of passkey login is the kind of friction
that gets users to disable both. Password login still triggers TOTP as today.

**Account recovery is the hard problem.** Passwords are the de-facto recovery path today.
If we ship "passwordless" without thinking through "I lost all my devices and never set a
password," we will get bug reports. Recommendation: every passkey-only user must keep a
recoverable channel — either a verified email (trigger magic-link login on no-passkey
device) or a one-time recovery code printout. The TOTP recovery-code pattern can be reused.

---

## UX proposals

### Login flow

Three entry points, in priority order:

**1. Conditional UI ("autofill")** — the gold-standard UX.
The login form's email field has `autocomplete="username webauthn"`. The browser
detects available passkeys for `solidping.io` and pops them in the autofill dropdown
*as the user focuses the field*. One tap → biometric → signed in. No password, no
email submitted. This is what Cloudflare, GitHub, and Google use.

```
┌──────────────────────────────────────────────────┐
│              [SolidPing Logo]                     │
│            Sign in to SolidPing                   │
│                                                   │
│  Email: [_______________________]                 │
│         ┌────────────────────────────┐            │
│         │ 🔑 Use passkey for         │ ← browser  │
│         │    alice@example.com       │   chip     │
│         └────────────────────────────┘            │
│                                                   │
│  Password: [_______________________]              │
│                                                   │
│  [ Sign in ]   [ Sign in with passkey ]           │
│                                                   │
│  Forgot password?  ·  Use a recovery code         │
└──────────────────────────────────────────────────┘
```

**2. Explicit "Sign in with passkey" button.** Triggers the WebAuthn ceremony with no
allowed-credentials list — the browser shows whatever passkeys it knows about for the
RP. Useful on devices that don't surface conditional UI well, or when the user wants
to use a roaming authenticator (YubiKey).

**3. Email-first fallback.** User types email and submits. Server responds with
`{ authenticatorOptions, hasPassword }`. If passkeys exist for that account, we
trigger the WebAuthn prompt; otherwise we show the password field. This is the
flow Microsoft/Auth0 use. Less elegant than conditional UI but more reliable.

### Registration / management UX

New `account/security` tab (the 2FA spec already proposed it; this spec extends it).
Two cards on that page:

```
┌───────────────────────────────────────────────────┐
│ Passkeys                                           │
│ Sign in without a password using your device,      │
│ a hardware key, or a password manager.             │
│                                                    │
│ ┌─────────────────────────────────────────────┐   │
│ │ 🔑 MacBook Pro · Touch ID                    │   │
│ │    Added 12 Apr 2026 · Last used 2h ago      │   │
│ │                                  [Rename]    │   │
│ │                                  [Remove]    │   │
│ └─────────────────────────────────────────────┘   │
│ ┌─────────────────────────────────────────────┐   │
│ │ 🔐 YubiKey 5C                                │   │
│ │    Added 8 Apr 2026 · Last used 3d ago       │   │
│ │                                  [Rename]    │   │
│ │                                  [Remove]    │   │
│ └─────────────────────────────────────────────┘   │
│                                                    │
│ [ + Add passkey ]                                  │
└───────────────────────────────────────────────────┘

┌───────────────────────────────────────────────────┐
│ Two-Factor Authentication (TOTP)                   │
│ Status: ✓ Enabled                                  │
│ ⓘ Skipped when you sign in with a passkey.        │
│                                  [ Disable 2FA ]   │
└───────────────────────────────────────────────────┘
```

**Adding a passkey:**
1. Click "Add passkey" → server returns registration challenge.
2. Browser pops the platform sheet (Touch ID / Face ID / Windows Hello / security key / password manager).
3. On success, server stores the credential. We pre-fill a name from the authenticator's
   AAGUID (e.g. "iCloud Keychain", "1Password", "YubiKey 5C") and let the user rename it.

**Removing a passkey:**
- If it's the user's *only* sign-in method (no password set, no other passkeys, no OAuth
  link), removal is blocked with a clear error: "Set a password or add another passkey first."

### Onboarding integration

When a user accepts an invite or registers, we offer **"Continue with a passkey instead of a
password"** as a first-class option. This is the win — new users skip choosing a password
they will forget. The existing `frictionless-invite-onboarding` spec should be extended.

---

## Confirmed decisions

These are the design choices for V1, settled before implementation begins.

| # | Decision |
|---|----------|
| D1 | A passkey alone is sufficient to sign in. No password challenge follows a successful passkey assertion. |
| D2 | Passkey login skips TOTP, even when TOTP is enabled on the account. The passkey is already MFA-equivalent. TOTP still applies to password login. |
| D3 | Passkey-only accounts are allowed (no password set), gated on `email_verified_at != NULL`. The registration / "remove password" paths must enforce this. |
| D4 | Recovery for passkey-only users with no working device is the password-reset magic-link flow, repurposed as a "set a password or add a passkey" ceremony to the verified email address. |
| D5 | No passkey-specific recovery codes. Email-verified is the recovery channel. |
| D6 | No attestation verification in V1. Accept any conformant authenticator. A "require attestation" knob can come later if an enterprise customer asks. |
| D7 | Discoverable credentials only (`residentKey: "required"`, `userVerification: "required"`). Required for conditional UI; non-discoverable keys would break the autofill flow and offer no benefit over password. |
| D8 | Single RP ID from config. Multi-domain is out of scope. |
| D9 | PATs and OAuth provider sign-in are untouched. |
| D10 | Per-org passkey policy (e.g. "admins must use passkey") is deferred. Open a separate spec when an entitlement story for it exists. |

---

## Data model

New table `user_passkeys`. Pure storage — secret-free (public keys aren't secrets, so no
encryption-at-rest envelope is needed; the `credential_id` is a random opaque blob).

```sql
CREATE TABLE user_passkeys (
    uid                TEXT PRIMARY KEY,
    user_uid           TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    credential_id      BYTEA NOT NULL,           -- WebAuthn credential ID, opaque
    public_key         BYTEA NOT NULL,           -- COSE-encoded public key
    aaguid             UUID,                     -- authenticator model identifier
    sign_count         BIGINT NOT NULL DEFAULT 0,
    transports         JSONB,                    -- ["internal", "hybrid", "usb", ...]
    backup_eligible    BOOLEAN NOT NULL DEFAULT FALSE,
    backup_state       BOOLEAN NOT NULL DEFAULT FALSE,
    user_verified      BOOLEAN NOT NULL DEFAULT FALSE,
    attestation_format TEXT,                     -- "none" | "packed" | ...
    last_used_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ,
    UNIQUE (user_uid, credential_id)
);
CREATE INDEX idx_user_passkeys_credential_id ON user_passkeys (credential_id);
CREATE INDEX idx_user_passkeys_user_uid ON user_passkeys (user_uid) WHERE deleted_at IS NULL;
```

Bun model in `server/internal/db/models/auth.go`:

```go
type UserPasskey struct {
    UID               string     `bun:"uid,pk,type:varchar(36)"`
    UserUID           string     `bun:"user_uid,notnull"`
    Name              string     `bun:"name,notnull"`
    CredentialID      []byte     `bun:"credential_id,notnull"`
    PublicKey         []byte     `bun:"public_key,notnull"`
    AAGUID            *string    `bun:"aaguid"`
    SignCount         uint32     `bun:"sign_count,notnull,default:0"`
    Transports        []string   `bun:"transports,type:jsonb,nullzero"`
    BackupEligible    bool       `bun:"backup_eligible,notnull,default:false"`
    BackupState       bool       `bun:"backup_state,notnull,default:false"`
    UserVerified      bool       `bun:"user_verified,notnull,default:false"`
    AttestationFormat *string    `bun:"attestation_format"`
    LastUsedAt        *time.Time `bun:"last_used_at"`
    CreatedAt         time.Time  `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt         time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
    DeletedAt         *time.Time `bun:"deleted_at"`

    User *User `bun:"rel:belongs-to,join:user_uid=uid"`
}
```

WebAuthn ceremonies require **server-side session state** between the challenge and the
response (~30s). Two options:

- **(a)** Reuse the JWT-with-purpose pattern from 2FA temp tokens: server signs
  `{purpose: "passkey-register" | "passkey-login", challenge, expectedUser, exp}` and returns
  it to the client; client echoes it back with the WebAuthn response. Stateless, scales well.
- **(b)** A `webauthn_sessions` table with a TTL.

**Recommend (a).** It matches what we already do for 2FA, avoids a new table, and the JWT
secret is already deployed.

---

## API

All passkey endpoints live under `/api/v1/auth/passkeys/...`. The protocol is two
round-trips per ceremony: server issues options + a sealed challenge, client runs WebAuthn,
client sends the response.

### Registration (authenticated user)

#### `POST /api/v1/auth/passkeys/register/begin`

```jsonc
// Response
{
  "options": { /* PublicKeyCredentialCreationOptions in JSON, base64url-encoded fields */ },
  "session": "eyJhbGci..."   // sealed JWT carrying the challenge and userUid
}
```

#### `POST /api/v1/auth/passkeys/register/finish`

```jsonc
// Request
{
  "session": "eyJhbGci...",
  "credential": { /* PublicKeyCredentialJSON returned by navigator.credentials.create */ },
  "name": "MacBook Touch ID"   // optional, server derives from AAGUID if missing
}

// Response
{
  "passkey": {
    "uid": "...",
    "name": "MacBook Touch ID",
    "createdAt": "2026-05-05T10:00:00Z",
    "lastUsedAt": null,
    "aaguidLabel": "iCloud Keychain"
  }
}
```

### Login (unauthenticated)

#### `POST /api/v1/auth/passkeys/login/begin`

```jsonc
// Request — email is OPTIONAL (omit for conditional UI / discoverable credential flow)
{ "email": "alice@example.com" }    // or {}

// Response
{
  "options": { /* PublicKeyCredentialRequestOptions */ },
  "session": "eyJhbGci..."
}
```

When email is present and known, `allowCredentials` lists that user's credential IDs.
When email is omitted, `allowCredentials` is empty — the browser uses discoverable
credentials and the response carries `userHandle`, which the server maps back to the user.

#### `POST /api/v1/auth/passkeys/login/finish`

```jsonc
// Request
{
  "session": "eyJhbGci...",
  "credential": { /* PublicKeyCredentialJSON returned by navigator.credentials.get */ },
  "org": "default"   // optional org preference, same semantics as POST /auth/login
}

// Response — same shape as POST /auth/login
{
  "accessToken": "...",
  "refreshToken": "...",
  "expiresIn": 3600,
  "tokenType": "Bearer",
  "user": { ... },
  "organization": { ... },
  "organizations": [ ... ],
  "loginAction": ""
}
```

The post-auth session — JWT + refresh token — is byte-for-byte identical to the password
flow. Reuses `generateAccessToken`, `generateRefreshToken`, and writes a `models.UserToken`
of type `refresh` with `Properties.createdWith.method = "passkey"` and `passkeyUid`.

### Management (authenticated)

| Method | Path | Purpose |
|--------|------|---------|
| `GET`    | `/api/v1/auth/passkeys` | List the current user's passkeys |
| `PATCH`  | `/api/v1/auth/passkeys/$uid` | Rename |
| `DELETE` | `/api/v1/auth/passkeys/$uid` | Revoke (soft-delete) |

Listing response (matches `TokenInfo` style):

```jsonc
{
  "data": [
    {
      "uid": "...",
      "name": "MacBook Touch ID",
      "aaguidLabel": "iCloud Keychain",
      "transports": ["internal", "hybrid"],
      "backupEligible": true,
      "createdAt": "2026-04-12T08:00:00Z",
      "lastUsedAt": "2026-05-05T10:00:00Z"
    }
  ]
}
```

### Updates to existing endpoints

#### `GET /api/v1/auth/me`

Add:
```jsonc
{
  "totpEnabled": true,
  "passkeyCount": 2,        // NEW
  "hasPassword": true       // NEW — true if user.password_hash is non-empty
}
```

#### `GET /api/v1/auth/providers` (public)

Add a flag the login page reads to decide whether to render the passkey button:
```jsonc
{ "providers": [ ... ], "passkeysEnabled": true }
```

Driven by config: passkeys are auto-enabled when `auth.webauthn.rp_id` is set (or derived
from base URL) and the request origin is HTTPS or localhost.

### New error codes

```go
ErrorCodePasskeyNotFound          = "PASSKEY_NOT_FOUND"
ErrorCodePasskeyVerificationFail  = "PASSKEY_VERIFICATION_FAILED"
ErrorCodePasskeySessionExpired    = "PASSKEY_SESSION_EXPIRED"
ErrorCodePasskeyLastAuthMethod    = "PASSKEY_LAST_AUTH_METHOD"   // can't remove if it's the only auth method
ErrorCodeWebAuthnNotConfigured    = "WEBAUTHN_NOT_CONFIGURED"
```

---

## Backend implementation

### Files

| File | Action | Description |
|------|--------|-------------|
| `server/internal/config/config.go` | Modify | Add `WebAuthn` config block under `Auth` |
| `server/internal/db/models/auth.go` | Modify | Add `UserPasskey` model |
| `server/internal/db/sqlite/migrations/NNN_user_passkeys.up.sql` | Create | Migration |
| `server/internal/db/sqlite/migrations/NNN_user_passkeys.down.sql` | Create | Rollback |
| `server/internal/db/postgres/migrations/NNN_user_passkeys.up.sql` | Create | Migration |
| `server/internal/db/postgres/migrations/NNN_user_passkeys.down.sql` | Create | Rollback |
| `server/internal/db/service.go` | Modify | Add CRUD for `UserPasskey` |
| `server/internal/db/sqlite/passkeys.go` | Create | sqlite impl |
| `server/internal/db/postgres/passkeys.go` | Create | postgres impl |
| `server/internal/handlers/auth/passkey_service.go` | Create | Business logic |
| `server/internal/handlers/auth/passkey_service_test.go` | Create | Service tests |
| `server/internal/handlers/auth/passkey_handler.go` | Create | HTTP layer |
| `server/internal/handlers/auth/handler.go` | Modify | `GET /auth/me` adds passkey count |
| `server/internal/handlers/auth/service.go` | Modify | `GenerateTokensAfterPasskey` helper sharing JWT+refresh code with password/oauth login |
| `server/internal/app/server.go` | Modify | Register routes |
| `server/internal/handlers/base/errors.go` | Modify | Add error codes |
| `server/go.mod` | Modify | `github.com/go-webauthn/webauthn` |

### Configuration

```go
type AuthConfig struct {
    // ... existing fields ...
    WebAuthn WebAuthnConfig `koanf:"webauthn"`
}

type WebAuthnConfig struct {
    Enabled       bool     `koanf:"enabled"`        // default: true if RPID resolvable
    RPID          string   `koanf:"rp_id"`          // e.g. "solidping.io"
    RPDisplayName string   `koanf:"rp_display_name"`// default: "SolidPing"
    Origins       []string `koanf:"origins"`        // e.g. ["https://solidping.io"]
}
```

If `RPID` is empty, derive it from the configured base URL. If the resolved scheme is not
`https` (and host is not `localhost`), log a warning and disable passkeys (browsers refuse
WebAuthn over HTTP). Surface the disabled state via `GET /api/v1/auth/providers`.

Env vars: `SP_AUTH_WEBAUTHN_RP_ID`, `SP_AUTH_WEBAUTHN_RP_DISPLAY_NAME`,
`SP_AUTH_WEBAUTHN_ORIGINS`.

### Service shape

```go
type PasskeyService struct {
    db       db.Service
    auth     *Service              // for token issuance
    webAuthn *webauthn.WebAuthn    // from go-webauthn
    cfg      config.WebAuthnConfig
    jwtKey   []byte                // for sealing/unsealing session JWTs
}

func (s *PasskeyService) BeginRegistration(ctx context.Context, userUID string) (*RegisterBeginResponse, error)
func (s *PasskeyService) FinishRegistration(ctx context.Context, sessionToken string, credentialJSON []byte, name string) (*PasskeyInfo, error)
func (s *PasskeyService) BeginLogin(ctx context.Context, email string) (*LoginBeginResponse, error)
func (s *PasskeyService) FinishLogin(ctx context.Context, sessionToken string, credentialJSON []byte, orgPreference string, authContext Context) (*LoginResponse, error)
func (s *PasskeyService) ListPasskeys(ctx context.Context, userUID string) ([]PasskeyInfo, error)
func (s *PasskeyService) RenamePasskey(ctx context.Context, userUID, passkeyUID, name string) error
func (s *PasskeyService) DeletePasskey(ctx context.Context, userUID, passkeyUID string) error
```

### Last-auth-method guard

`DeletePasskey` and any future "remove auth method" endpoint must verify the user retains
at least one usable login path:

```
hasPassword || hasOtherPasskey || hasLinkedOAuthProvider
```

If none survive, return `409 PASSKEY_LAST_AUTH_METHOD`. The frontend must surface this
clearly: "Add another passkey or set a password before removing this one."

### Login flow integration

`FinishLogin` performs the WebAuthn assertion verification, increments `sign_count`,
updates `last_used_at`, then **delegates to a new shared helper**
`Service.completeLogin(ctx, user, orgPreference, "passkey", authContext)` that:

1. Resolves the org preference (reuses `resolveOrgPreference`).
2. Generates access + refresh tokens (reuses `generateAccessToken`, `generateRefreshToken`).
3. Writes the refresh-token row with `Properties.createdWith.method = "passkey"` plus the
   passkey UID for forensics.
4. Skips TOTP entirely (per D2 — the passkey is already MFA-equivalent).

Refactor `Login` and `GenerateTokensForOAuth` to call the same helper to keep the three
paths consistent. Worth extracting now, low-risk.

### AAGUID → friendly name

Maintain a small lookup table for common AAGUIDs (Apple, Google, 1Password, Bitwarden,
YubiKey families). Falls back to "Security key" when unknown. Source:
https://github.com/passkeydeveloper/passkey-authenticator-aaguids — vendor it as a
JSON asset; do not fetch at runtime.

---

## Frontend implementation

### Dependencies

Add to `web/dash0/package.json`:
- `@simplewebauthn/browser` (handles base64url ↔ ArrayBuffer plumbing for WebAuthn calls)

### Routes

| File | Action | Notes |
|------|--------|-------|
| `web/dash0/src/routes/orgs/$org/account.security.tsx` | Create | Hosts both the Passkeys card and the 2FA / TOTP card |
| `web/dash0/src/routes/orgs/$org/account.tsx` | Modify | Add the Security tab (does not exist yet) |
| `web/dash0/src/routes/login.tsx` | Modify | `autocomplete="username webauthn"`, "Sign in with passkey" button, conditional UI hook, **and** the 2FA challenge step (`requires2Fa` / `tempToken` branch already returned by `POST /auth/login`) |
| `web/dash0/src/api/passkeys.ts` | Create | API client wrappers |
| `web/dash0/src/api/twofa.ts` | Create | API client wrappers for the existing 2FA endpoints |
| `web/dash0/src/contexts/AuthContext.tsx` | Modify | `loginWithPasskey()`, `kickoffConditionalUI()`, `verify2FA()`, `useRecoveryCode()` |
| `web/dash0/src/components/security/TOTPSetupDialog.tsx` | Create | Two-step modal: QR + key fallback, then verify-code |
| `web/dash0/src/components/security/RecoveryCodesDialog.tsx` | Create | Shown once after `Confirm2FA`, with copy-all + saved checkbox gate |
| `web/dash0/src/components/security/TOTPDisableDialog.tsx` | Create | Confirms current code before disabling |
| `web/dash0/src/components/security/PasskeyAddDialog.tsx` | Create | Wraps the WebAuthn registration ceremony + name field |
| `web/dash0/src/locales/{en,fr,de,es}/account.json` | Modify | Add `security.*` keys for both passkeys and TOTP (the 2FA spec drafted the TOTP keys; bring them in here) |
| `web/dash0/src/locales/{en,fr,de,es}/auth.json` | Modify | Add `twoFactor.*` and passkey login keys |

### Conditional UI hook

```ts
import { browserSupportsWebAuthn, browserSupportsWebAuthnAutofill, startAuthentication } from "@simplewebauthn/browser";

useEffect(() => {
  if (!browserSupportsWebAuthn() || !browserSupportsWebAuthnAutofill()) return;
  let cancelled = false;
  (async () => {
    const { options, session } = await api.beginPasskeyLogin({});
    try {
      const credential = await startAuthentication({ optionsJSON: options, useBrowserAutofill: true });
      if (cancelled) return;
      const result = await api.finishPasskeyLogin({ session, credential });
      onLoginSuccess(result);
    } catch (err) {
      if ((err as Error).name !== "NotAllowedError" && !cancelled) console.warn(err);
    }
  })();
  return () => { cancelled = true; };
}, []);
```

### Test IDs

Passkeys:

| Element | Test ID |
|---------|---------|
| "Add passkey" button | `passkey-add-button` |
| Passkey list item | `passkey-row-$uid` |
| Rename action | `passkey-rename-$uid` |
| Remove action | `passkey-remove-$uid` |
| Login: passkey button | `passkey-login-button` |
| Login: passkey error toast | `passkey-login-error` |
| Setup: rename input | `passkey-setup-name` |
| Setup: confirm button | `passkey-setup-confirm` |

2FA / TOTP (carried over from the 2026-03-22 spec — kept stable so any tests that
were drafted against the original spec still match):

| Element | Test ID |
|---------|---------|
| Enable 2FA button | `2fa-enable-button` |
| Disable 2FA button | `2fa-disable-button` |
| 2FA status badge | `2fa-status` |
| QR code image | `2fa-qr-code` |
| Manual secret text | `2fa-manual-secret` |
| Copy secret button | `2fa-copy-secret` |
| Setup code input | `2fa-setup-code` |
| Setup confirm button | `2fa-setup-confirm` |
| Recovery codes list | `2fa-recovery-codes` |
| Recovery codes copy button | `2fa-recovery-copy` |
| Recovery codes saved checkbox | `2fa-recovery-saved-checkbox` |
| Recovery codes done button | `2fa-recovery-done` |
| Login 2FA code input | `2fa-login-code` |
| Login 2FA verify button | `2fa-login-verify` |
| Login recovery link | `2fa-login-recovery-link` |
| Login recovery code input | `2fa-login-recovery-code` |
| Login back to login link | `2fa-login-back` |
| Disable code input | `2fa-disable-code` |
| Disable confirm button | `2fa-disable-confirm` |

### Translations

Add `passkeys` block to `web/dash0/src/locales/en/account.json` and mirror to fr/de/es:
title, description, status, actions, error messages, AAGUID labels.

---

## Security notes

- **Origin binding.** WebAuthn signatures are bound to the RP ID and origin — phishing
  domains can't replay them. We must **not** loosen RP ID to a parent domain (e.g. setting
  it to `solidping.io` while serving the dashboard from `app.solidping.io`) without
  explicitly understanding the subdomain-takeover trade-off.
- **No secret in the credential.** `public_key` and `credential_id` are not secrets and do
  not need encryption-at-rest. `sign_count` is a replay guard — keep it monotonic.
- **Sign-count regressions** indicate a cloned credential. The library reports them; we
  log + alert + reject the assertion.
- **HTTPS-only.** Browsers refuse WebAuthn over plain HTTP (except `localhost`). Document
  this in the self-host setup; surface it in the UI when disabled.
- **Discoverable credentials only.** Required for conditional UI. Authenticators that don't
  support discoverable creds (some old hardware keys) can't register; the UI should fail
  gracefully and explain why.
- **Replay window for the sealed session JWT** is 5 minutes (matches 2FA temp token).
- **Audit log.** Add `events` rows for `passkey.registered`, `passkey.deleted`,
  `passkey.login_succeeded`, `passkey.login_failed` (subject to the existing event model
  for user-account events).

---

## Implementation order

1. **Frontend scaffolding for the security tab.** Create `account/security` route, add
   the tab to `account.tsx`, build the empty card layout. Unblocks both passkey and
   TOTP UI in parallel.
2. **TOTP frontend.** Wire the existing `/auth/2fa/*` endpoints: enable button,
   `TOTPSetupDialog` (QR + manual key + verify), `RecoveryCodesDialog`, disable dialog.
   `requires2Fa` / `tempToken` branch on the login page. E2E coverage of the full flow
   (the 2FA spec already enumerated the test cases — port them).
3. **Passkey backend.** Add `go-webauthn` dep, config block, migration, `UserPasskey`
   model, CRUD, `passkey_service.go`, route registration, error codes.
4. **Refactor `completeLogin`.** Extract the shared post-auth path used by `Login`,
   `GenerateTokensForOAuth`, and the new passkey login. Low-risk, called out so it
   doesn't get skipped.
5. **Passkey backend tests.** Table-driven, `t.Parallel()`, per the test list below.
6. **Passkey frontend management.** `account/security` passkeys card, `PasskeyAddDialog`,
   list / rename / remove.
7. **Passkey frontend login.** "Sign in with passkey" button, `autocomplete="username
   webauthn"`, conditional UI hook in `login.tsx`.
8. **Passkey E2E.** Playwright with the CDP `WebAuthn.addVirtualAuthenticator` API.
9. **Documentation.** Self-host config (RP ID, origins, HTTPS requirement), threat-model
   note, recovery flow, the "passkey replaces TOTP at sign-in" rule.

---

## Tests

### Backend (`passkey_service_test.go`, table-driven, `t.Parallel()`)

| Test | Description |
|------|-------------|
| `TestBeginRegistration_NewUser` | Returns options with empty excludeCredentials |
| `TestBeginRegistration_ExistingPasskey` | excludeCredentials lists current passkeys |
| `TestFinishRegistration_Valid` | Persists credential, returns passkey info |
| `TestFinishRegistration_ExpiredSession` | Rejects after 5-min TTL |
| `TestFinishRegistration_WrongUser` | Rejects when session.userUid ≠ caller |
| `TestFinishRegistration_DuplicateCredentialID` | Returns conflict |
| `TestBeginLogin_KnownEmail` | allowCredentials populated |
| `TestBeginLogin_UnknownEmail` | Returns options indistinguishable from known (no enumeration) |
| `TestBeginLogin_DiscoverableMode` | Empty allowCredentials when email omitted |
| `TestFinishLogin_Valid` | Issues access + refresh, increments sign_count, sets last_used_at |
| `TestFinishLogin_BadSignature` | Rejects |
| `TestFinishLogin_SignCountRegression` | Rejects + audit event |
| `TestFinishLogin_TOTPEnabledUser_SkipsTOTP` | Returns full tokens, not tempToken |
| `TestFinishLogin_OrgPreferenceHonored` | Same semantics as password login |
| `TestFinishLogin_NoOrgUser` | Returns `LoginActionNoOrg` |
| `TestDeletePasskey_LastAuthMethod` | Returns `PASSKEY_LAST_AUTH_METHOD` |
| `TestDeletePasskey_HasPassword` | Allowed |
| `TestDeletePasskey_HasOtherPasskey` | Allowed |

### Passkey E2E (`web/dash0/e2e/passkey.spec.ts`)

Use Playwright's CDP `WebAuthn.addVirtualAuthenticator` to emulate a platform authenticator.

| Test | Steps |
|------|-------|
| Register a passkey from security page | Login with password → security tab → Add passkey → virtual authenticator approves → row appears |
| Login with registered passkey | Sign out → login page → click "Sign in with passkey" → redirected to dashboard |
| Conditional UI fires on focus | Sign out → focus email field → assert WebAuthn ceremony invoked |
| Remove last passkey blocked | Passkey-only user removes their only passkey → blocked with `PASSKEY_LAST_AUTH_METHOD` |
| Rename passkey | Edit name from list → reload → name persisted |
| Passkey login skips TOTP | User with TOTP+passkey signs in via passkey → no TOTP step shown |

### TOTP E2E (`web/dash0/e2e/2fa.spec.ts`)

Generate codes client-side with the `otpauth` npm package (add as a dev dependency).

| Test | Steps |
|------|-------|
| Security page with 2FA disabled | Auth fixture → `/orgs/test/account/security` → `2fa-status` shows "Not enabled", `2fa-enable-button` visible |
| Full setup flow | Click `2fa-enable-button` → assert `2fa-qr-code` + `2fa-manual-secret` → Next → enter generated code in `2fa-setup-code` → confirm → recovery codes shown → tick `2fa-recovery-saved-checkbox` → Done → `2fa-status` becomes "Enabled" |
| Login with TOTP | Login form submit → `2fa-login-code` visible → enter code → redirected to dashboard |
| Login with recovery code | Login → click `2fa-login-recovery-link` → enter saved code → redirected |
| Invalid TOTP code shows error | Login → enter `000000` → assert error message |
| Disable 2FA | Auth fixture (TOTP enabled) → security page → `2fa-disable-button` → enter code → status returns to "Not enabled" |
| Back to login from 2FA step | Login → 2FA step → click `2fa-login-back` → email/password form returns |

---

## Open follow-ups (not in scope of this spec)

- Per-org policy: "require passkey for admins". Needs entitlement modeling.
- Attestation verification for enterprise customers who want hardware-only keys.
- Passkey export to/from password managers — out of our hands; UX guidance only.
- WebAuthn signal API (when supported) to push credential renames/removals back to the
  authenticator.
- Magic-link account recovery as a self-contained spec (the recovery hook required by D4).

---

## Competitor reference

- **GitHub, Cloudflare, Google, Microsoft, AWS, Linear, 1Password** — passkey + password coexist; passkey skips TOTP.
- **Uptime Kuma** — does not support passkeys (Dec 2025).
- **BetterStack, Checkly** — TOTP only as of last check; passkeys would be a differentiator.

## Implementation Plan

The spec is large but the dependency graph is shallow. The plan splits
into two parallel tracks (backend and frontend), each with its own QA
gate, plus a final E2E pass. Each numbered step maps to a small set of
commits.

### Track A — Backend

**A1. Config and dependency.** Add `auth.webauthn` config block
(`RPID`, `RPDisplayName`, `Origins`, `Enabled` — `Enabled` defaults
true when RP ID is resolvable). Wire env vars `SP_AUTH_WEBAUTHN_*`.
Add `github.com/go-webauthn/webauthn` to `go.mod`.

**A2. Migrations + model.** Create
`019_user_passkeys.{up,down}.sql` for both postgres and sqlite, add
the `UserPasskey` bun model in `models/auth.go`. Wire bun's `RegisterModel`.

**A3. DB CRUD.** Add `CreateUserPasskey`, `GetUserPasskeyByCredentialID`,
`ListUserPasskeysByUser`, `UpdateUserPasskey`, `SoftDeleteUserPasskey` to
`db.Service` and implement in both drivers.

**A4. Error codes.** Add `PASSKEY_NOT_FOUND`,
`PASSKEY_VERIFICATION_FAILED`, `PASSKEY_SESSION_EXPIRED`,
`PASSKEY_LAST_AUTH_METHOD`, `WEBAUTHN_NOT_CONFIGURED` to
`base/base.go`.

**A5. Refactor `completeLogin`.** Extract the post-auth path used by
`Login` and `GenerateTokensForOAuth` into a single helper. Both call
sites switch over. Tests stay green.

**A6. Passkey service.** Create
`handlers/auth/passkey_service.go` with `BeginRegistration`,
`FinishRegistration`, `BeginLogin`, `FinishLogin`, `ListPasskeys`,
`RenamePasskey`, `DeletePasskey`. Sessions are sealed JWTs with a 5-min
TTL (matches 2FA temp tokens). `FinishLogin` calls into `completeLogin`
and skips TOTP per D2.

**A7. Passkey handler.** Create
`handlers/auth/passkey_handler.go`; register routes
(`POST /api/v1/auth/passkeys/{register,login}/{begin,finish}`,
`GET/PATCH/DELETE /api/v1/auth/passkeys/$uid`) in
`internal/app/server.go`.

**A8. `/auth/me` and `/auth/providers` updates.** `GET /auth/me`
returns `passkeyCount` + `hasPassword`. `GET /auth/providers` returns
`passkeysEnabled` (true when WebAuthn is enabled in config and the
request is HTTPS or localhost).

**A9. Passkey service tests.** Table-driven, `t.Parallel()`, the test
list from the spec. Use the in-memory sqlite dbSvc and a stubbed
WebAuthn that round-trips fixed assertion data.

**A10. Backend QA.** `make build-backend lint-back test` green.

### Track B — Frontend (dash0)

**B1. Security tab skeleton.** Add `account.security.tsx` route, add a
"Security" tab to `account.tsx`. Empty cards for Passkeys + 2FA.

**B2. TOTP API client.** `api/twofa.ts` wrapping `/auth/2fa/setup`,
`/auth/2fa/confirm`, `/auth/2fa/verify`, `/auth/2fa/recovery`,
`/auth/2fa` (DELETE).

**B3. TOTP dialogs.** `TOTPSetupDialog` (QR + manual key + verify),
`RecoveryCodesDialog`, `TOTPDisableDialog`. Wire on the security
page. Status badge reads `me.totpEnabled`.

**B4. TOTP login challenge.** Extend `login.tsx` to handle
`requires2Fa`/`tempToken` branch — show a 6-digit input + recovery
code link. AuthContext gains `verify2FA` and `useRecoveryCode`.

**B5. Passkey API client.** `api/passkeys.ts` wrapping the new
endpoints. Add `@simplewebauthn/browser` dep.

**B6. Passkey management UI.** Card on the security page; add /
rename / remove via `PasskeyAddDialog` and inline edits. List comes
from `GET /auth/passkeys`.

**B7. Passkey login.** `login.tsx` — add "Sign in with passkey"
button, `autocomplete="username webauthn"` on email field, conditional
UI hook in a `useEffect`. AuthContext gains `loginWithPasskey` and
`kickoffConditionalUI`.

**B8. Translations.** Add `security.*` and `twoFactor.*` /
`passkeys.*` keys to `account.json` and `auth.json` for `en`, then
mirror to `fr`, `de`, `es` (translation can be approximate; the user
will refine).

**B9. Frontend QA.** `make build-dash0 lint-dash` green; smoke the
security page locally with `make dev`.

### Track C — E2E + smoke

**C1. TOTP E2E** at `web/dash0/e2e/2fa.spec.ts` (codes via the
`otpauth` package).

**C2. Passkey E2E** at `web/dash0/e2e/passkey.spec.ts` using
Playwright's CDP `WebAuthn.addVirtualAuthenticator`.

**C3. Manual smoke.** `make dev`, log in, hit each new flow once.

### Track D — Audit + archive

**D1.** Independent subagent audit per the loop. Fix gaps and
re-audit until clean.

**D2.** `git mv` the spec to `specs/done/2026/05/` and merge.

### Pragmatic sequencing

The spec's "Implementation order" puts the security-tab scaffold
first so both UI tracks can proceed in parallel. The plan above
preserves that intent: `B1` is the unblocker for `B2-B7`.
Backend `A1-A10` is independent of `B1-B9` until the
frontend wires up the API calls in `B5/B6/B7`.

### Risks called out

- **AAGUID lookup table.** Vendoring the
  `passkeydeveloper/passkey-authenticator-aaguids` JSON is small
  but not free — keep the asset minimal (top ~30 AAGUIDs) and
  fall back to "Security key" cleanly.
- **Conditional UI silently failing.** Browsers vary. The hook
  must catch `NotAllowedError` quietly so it doesn't spam the
  console while the user types their password.
- **Self-host HTTPS.** Browsers refuse WebAuthn over HTTP outside
  `localhost`. The config must surface this clearly and disable
  passkeys gracefully.
