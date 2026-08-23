package realtimews

import (
	"context"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// cookieAuthToken mirrors middleware.CookieAuthToken — duplicated here rather
// than imported to avoid a middleware<->realtimews import cycle (middleware
// does not depend on this package, but keeping the WS handshake
// self-contained avoids ever creating one).
const cookieAuthToken = "access_token"

const bearerTokenParts = 2

// Subprotocol carrying the SPA's bearer token on the handshake. Browsers
// cannot set an Authorization header on a WebSocket, and the access_token
// cookie is unreliable: cookies ignore ports, so on localhost (and on any
// shared-domain deployment) another app's access_token cookie can shadow ours
// and permanently 401 the handshake while REST — which uses the header —
// keeps working. The SPA therefore offers ["bearer.<jwt>", "solidping.v2"]
// as subprotocols (the Kubernetes-style bearer-in-subprotocol pattern); the
// server reads the token from the bearer.* entry and negotiates the plain
// wsSubprotocol back so the browser accepts the upgrade.
const (
	// wsSubprotocol is the protocol the server echoes back when the client
	// offers it (required — a browser aborts the connection when it offered
	// subprotocols and the server acknowledged none).
	wsSubprotocol = "solidping.v2"
	// bearerSubprotocolPrefix prefixes the token-carrying subprotocol entry.
	bearerSubprotocolPrefix = "bearer."
)

// extractToken returns the request's bearer token: Authorization header first,
// then a bearer.* Sec-WebSocket-Protocol entry, then the access_token cookie.
// This mirrors middleware.extractToken for the header: a present-but-malformed
// Authorization header yields "" and does NOT fall back. Browsers use the
// subprotocol (set by the SPA) or the same-origin cookie; explicit-auth
// clients (CLI/curl/websocat/tests) set the header deliberately.
func extractToken(req *http.Request) string {
	authHeader := req.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", bearerTokenParts)
		if len(parts) != bearerTokenParts || !strings.EqualFold(parts[0], "bearer") {
			return ""
		}

		return parts[1]
	}

	if token := extractSubprotocolToken(req); token != "" {
		return token
	}

	return extractCookieToken(req)
}

// extractSubprotocolToken returns the token carried in a bearer.* entry of the
// Sec-WebSocket-Protocol header, or "" when none is offered. The header is a
// comma-separated list; entries are matched case-sensitively (subprotocol
// names are case-sensitive per RFC 6455, and the JWT payload must not be
// case-folded).
func extractSubprotocolToken(req *http.Request) string {
	for _, header := range req.Header.Values("Sec-WebSocket-Protocol") {
		for _, entry := range strings.Split(header, ",") {
			entry = strings.TrimSpace(entry)
			if token, found := strings.CutPrefix(entry, bearerSubprotocolPrefix); found && token != "" {
				return token
			}
		}
	}

	return ""
}

// extractCookieToken mirrors middleware.extractToken's access_token cookie
// fallback.
func extractCookieToken(req *http.Request) string {
	if cookie, err := req.Cookie(cookieAuthToken); err == nil {
		return cookie.Value
	}

	return ""
}

// authenticateToken validates the token (signature/expiry) and confirms the
// user still exists. It runs BEFORE websocket.Accept so a bad token is
// answered with a real HTTP 401 that explicit-auth clients (CLI/curl/tests)
// can read — a browser sees only a generic 1006 on the failed handshake and
// self-heals by refreshing its cookie and reconnecting. On success it returns
// the validated claims and user; on failure it returns the HTTP error code and
// message the caller writes as a 401.
func (h *Handler) authenticateToken(
	ctx context.Context, token string,
) (*auth.Claims, *models.User, base.ErrorCode, string) {
	if token == "" {
		return nil, nil, base.ErrorCodeNoToken, "Authorization token is required"
	}

	claims, err := h.authService.ValidateToken(ctx, token)
	if err != nil {
		return nil, nil, base.ErrorCodeInvalidToken, "Invalid or expired token"
	}

	user, err := h.dbService.GetUser(ctx, claims.UserUID)
	if err != nil {
		return nil, nil, base.ErrorCodeUserNotFound, "User not found"
	}

	// Forced password rotation (spec 2026-08-23-04). This handler authenticates
	// in-band rather than behind RequireAuth — browsers cannot present a bearer
	// at WebSocket-upgrade time — so the central gate has to be repeated here,
	// through the same shared predicate, or the live socket would be a hole in
	// it. Denied pre-upgrade, so an explicit-auth client reads a real HTTP
	// status with the machine-readable code.
	if auth.PasswordRotationRequired(user) {
		return nil, nil, base.ErrorCodePasswordChangeRequired, auth.PasswordRotationMessage
	}

	return claims, user, "", ""
}

// authorizeOrg runs the shared org-access rule (auth.AuthorizeOrgAccess) that
// the REST middleware also uses, so the two surfaces cannot diverge (spec
// 2026-07-15-01). It runs AFTER the upgrade because the browser can only read a
// WebSocket close code (never an HTTP status) once the socket exists. On
// success it returns the organization; on failure it returns the close code and
// reason the caller writes:
//
//   - org denied (token scoped elsewhere / not a member) -> 4403, retryable
//     after the client refreshes/switches its session.
//   - org does not exist -> 4410, terminal (a refresh cannot conjure the org).
func (h *Handler) authorizeOrg(
	ctx context.Context, claims *auth.Claims, user *models.User, orgSlug string,
) (*models.Organization, websocket.StatusCode, string) {
	org, denial := auth.AuthorizeOrgAccess(ctx, h.dbService, claims, user, orgSlug)
	switch denial {
	case auth.OrgAccessGranted:
		return org, 0, ""
	case auth.OrgAccessOrgNotFound:
		return nil, CloseNotFound, "organization not found"
	case auth.OrgAccessDenied:
		return nil, CloseForbidden, "access to this organization is denied"
	default:
		return nil, CloseForbidden, "access to this organization is denied"
	}
}
