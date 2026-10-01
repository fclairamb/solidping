package auth

import (
	"net/http"
)

// Route patterns an impersonation token must never reach (spec 2026-09-29-03).
// These are chi's RESOLVED route patterns (httpx.RoutePattern), exactly like
// the demo guard's allowlist, so a trailing slash or a previous-slug redirect
// cannot smuggle a request past the list.
const (
	ImpersonationPathSwitchOrg          = "/api/v1/auth/switch-org"
	ImpersonationPathChangePassword     = "/api/v1/auth/change-password"
	ImpersonationPathMe                 = "/api/v1/auth/me"
	ImpersonationPatternSystemUser      = "/api/v1/system/users/{uid}"
	ImpersonationPath2FASetup           = "/api/v1/auth/2fa/setup"
	ImpersonationPath2FAConfirm         = "/api/v1/auth/2fa/confirm"
	ImpersonationPath2FA                = "/api/v1/auth/2fa"
	ImpersonationPathTokenCurrent       = "/api/v1/auth/tokens/current"
	ImpersonationPatternToken           = "/api/v1/auth/tokens/{tokenUid}"
	ImpersonationPathPasskeyBegin       = "/api/v1/auth/passkeys/register/begin"
	ImpersonationPathPasskeyFinish      = "/api/v1/auth/passkeys/register/finish"
	ImpersonationPatternPasskey         = "/api/v1/auth/passkeys/{uid}"
	ImpersonationPathDeviceConsent      = "/api/v1/auth/device/consent"
	ImpersonationPathOrgs               = "/api/v1/orgs"
	ImpersonationPatternOrgTokens       = "/api/v1/orgs/{org}/tokens"
	ImpersonationPatternEnrollmentToken = "/api/v1/orgs/{org}/agent-enrollment-tokens"
	ImpersonationPatternImpersonate     = "/api/v1/system/users/{uid}/impersonate"
	ImpersonationPatternDiscordLink     = "/api/v1/orgs/{org}/users/me/discord/link-start"
	ImpersonationPatternDiscordConnect  = "/api/v1/orgs/{org}/users/me/discord/connect"
	ImpersonationPatternTelegramLink    = "/api/v1/orgs/{org}/users/me/telegram/link"
)

// impersonationForbiddenRoutes is the credential-changing surface. It is a
// DENYLIST, unlike the demo guard's allowlist, on purpose: an impersonation
// session exists to reproduce what the user sees and does, so everything the
// user can do is allowed, except what would change who can sign in as them,
// hand out a credential or a session in their name, or chain another
// impersonation.
//
// The denylist is not the only wall. Service.startSession refuses to mint any
// session from an impersonated request, whatever the route, so a future
// session-minting path cannot turn an impersonation into a takeover even if
// nobody remembers this file.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const slices.
var impersonationForbiddenRoutes = []demoAllowedRoute{
	// Would mint a full session (with a refresh token) for the target.
	{http.MethodPost, ImpersonationPathSwitchOrg},
	// Org creation mints an owner session for the creator.
	{http.MethodPost, ImpersonationPathOrgs},
	// Credentials.
	{http.MethodPost, ImpersonationPathChangePassword},
	// The profile update carries the sign-in email (spec 2026-09-30-08).
	{http.MethodPatch, ImpersonationPathMe},
	// A super-admin route (RequireSuperAdmin refuses it already) that changes
	// another user's sign-in email.
	{http.MethodPatch, ImpersonationPatternSystemUser},
	{http.MethodPost, ImpersonationPath2FASetup},
	{http.MethodPost, ImpersonationPath2FAConfirm},
	{http.MethodDelete, ImpersonationPath2FA},
	{http.MethodPost, ImpersonationPathPasskeyBegin},
	{http.MethodPost, ImpersonationPathPasskeyFinish},
	{http.MethodDelete, ImpersonationPatternPasskey},
	// The target's own sessions and tokens.
	{http.MethodDelete, ImpersonationPathTokenCurrent},
	{http.MethodDelete, ImpersonationPatternToken},
	// Token creation: a PAT, an agent enrollment key, a device grant.
	{http.MethodPost, ImpersonationPatternOrgTokens},
	{http.MethodPost, ImpersonationPatternEnrollmentToken},
	{http.MethodPost, ImpersonationPathDeviceConsent},
	// External identity binding. The Discord link round trip binds whatever
	// Discord account completes the OAuth dance to the TARGET, and a bound
	// Discord identity signs in: under impersonation that is the admin's own
	// Discord account, i.e. a lasting login as the target.
	{http.MethodPost, ImpersonationPatternDiscordLink},
	// Paging redirection. Both bind a DM channel as the target's contact; the
	// Telegram link is redeemed by whichever Telegram account opens it, so the
	// target's pages would land on an admin-controlled chat.
	{http.MethodPost, ImpersonationPatternDiscordConnect},
	{http.MethodPost, ImpersonationPatternTelegramLink},
	// No chaining. RequireSuperAdmin already refuses it; this makes the
	// answer the explicit IMPERSONATION_FORBIDDEN.
	{http.MethodPost, ImpersonationPatternImpersonate},
}

// IsImpersonationAllowed reports whether an impersonation token may perform
// this request. Safe methods always pass: reading is the whole point.
func IsImpersonationAllowed(method, routePattern string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	for i := range impersonationForbiddenRoutes {
		if impersonationForbiddenRoutes[i].method == method &&
			impersonationForbiddenRoutes[i].pattern == routePattern {
			return false
		}
	}

	return true
}

// ImpersonationForbiddenRoutes returns the denylist as (method, pattern)
// pairs, for the route-table test in internal/app that proves every entry
// names a route that really exists.
func ImpersonationForbiddenRoutes() [][2]string {
	out := make([][2]string, 0, len(impersonationForbiddenRoutes))
	for i := range impersonationForbiddenRoutes {
		out = append(out, [2]string{impersonationForbiddenRoutes[i].method, impersonationForbiddenRoutes[i].pattern})
	}

	return out
}
