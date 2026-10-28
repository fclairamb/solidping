package auth

import (
	"context"
	"errors"
	"net/http"
)

// ErrPATForbidden is returned when a personal access token asks for something
// it must never do (spec 2026-10-28-01): mint a session, a token for another
// org, or change how the account signs in. Mapped to 403 PAT_FORBIDDEN.
var ErrPATForbidden = errors.New("not allowed with a personal access token")

// PATForbiddenMessage is the human half of every PAT_FORBIDDEN response.
const PATForbiddenMessage = "A personal access token is bound to one organization. " +
	"It cannot open a session, switch organization, or change how the account signs in: " +
	"sign in to the dashboard, or with `sp auth login`, to do that."

// IsPAT reports whether these claims were produced by ValidatePATToken, i.e.
// the request authenticated with a personal access token. The flag is an
// unexported field, so no signed JWT can claim it, nor shed it.
func (c *Claims) IsPAT() bool {
	return c != nil && c.pat
}

// patForbiddenRoutes is what a personal access token may never reach (spec
// 2026-10-28-01). A PAT is bound to the org it was created in, and the device
// consent page has the user choose that org on purpose; everything on this
// list would let its holder step out of that binding: a session (with a
// refresh token that outlives the PAT) in another org of the owner, a PAT for
// another org, or a sign-in factor that works in every org and survives the
// PAT's revocation.
//
// It starts from impersonationForbiddenRoutes, minus what a PAT may keep:
// minting a PAT or an enrollment key in its own org (same org, same role),
// revoking tokens, and discovery scans. The rows match on chi's resolved
// route pattern, like the demo and impersonation guards.
//
// The denylist is not the only wall: Service.startSession refuses to mint any
// session from a PAT request, whatever the route, and the device consent and
// self-service email change refuse a PAT in the service too.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const slices.
var patForbiddenRoutes = []demoAllowedRoute{
	// Session minting: a full session, with a refresh token, in any org of
	// the owner.
	{http.MethodPost, ImpersonationPathSwitchOrg},
	{http.MethodPost, ImpersonationPathOrgs},
	{http.MethodPost, ImpersonationPathAcceptInvite},
	// Sign-in factors. Each one signs in to every org of the account.
	{http.MethodPost, ImpersonationPathChangePassword},
	{http.MethodPost, ImpersonationPath2FASetup},
	{http.MethodPost, ImpersonationPath2FAConfirm},
	// Also spends the shared login 2FA budget: a PAT holder could keep the
	// owner's TOTP sign-in at 429, and so keep them from revoking the token.
	{http.MethodDelete, ImpersonationPath2FA},
	{http.MethodPost, ImpersonationPathPasskeyBegin},
	{http.MethodPost, ImpersonationPathPasskeyFinish},
	{http.MethodDelete, ImpersonationPatternPasskey},
	// A super-admin route that changes another user's sign-in email.
	{http.MethodPatch, ImpersonationPatternSystemUser},
	// A device grant for any org of the owner.
	{http.MethodPost, ImpersonationPathDeviceConsent},
	// External identity binding: a bound Discord identity signs in.
	{http.MethodPost, ImpersonationPatternDiscordLink},
	// Paging redirection to a chat the holder controls.
	{http.MethodPost, ImpersonationPatternDiscordConnect},
	{http.MethodPost, ImpersonationPatternTelegramLink},
	// Impersonation mints a token acting as someone else.
	{http.MethodPost, ImpersonationPatternImpersonate},
}

// IsPATAllowed reports whether a personal access token may perform this
// request. Safe methods always pass.
func IsPATAllowed(method, routePattern string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	for i := range patForbiddenRoutes {
		if patForbiddenRoutes[i].method == method && patForbiddenRoutes[i].pattern == routePattern {
			return false
		}
	}

	return true
}

// PATForbiddenRoutes returns the denylist as (method, pattern) pairs, for the
// route-table test in internal/app that proves every entry names a real route.
func PATForbiddenRoutes() [][2]string {
	out := make([][2]string, 0, len(patForbiddenRoutes))
	for i := range patForbiddenRoutes {
		out = append(out, [2]string{patForbiddenRoutes[i].method, patForbiddenRoutes[i].pattern})
	}

	return out
}

type patCredentialContextKey struct{}

// WithPATCredential marks the request context as authenticated by a personal
// access token. Called by RequireAuth. Its own key, like the impersonator, so
// no later rewrite of the audit actor can drop it.
func WithPATCredential(ctx context.Context) context.Context {
	return context.WithValue(ctx, patCredentialContextKey{}, true)
}

// IsPATCredential reports whether the request authenticated with a personal
// access token.
func IsPATCredential(ctx context.Context) bool {
	is, _ := ctx.Value(patCredentialContextKey{}).(bool)

	return is
}
