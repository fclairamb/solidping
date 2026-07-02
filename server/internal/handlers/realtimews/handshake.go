package realtimews

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// cookieAuthToken mirrors middleware.CookieAuthToken — duplicated here rather
// than imported to avoid a middleware<->realtimews import cycle (middleware
// does not depend on this package, but keeping the WS handshake
// self-contained avoids ever creating one).
const cookieAuthToken = "access_token"

const bearerTokenParts = 2

// handshake authenticates the connection, either immediately from the
// upgrade request's Authorization header (CLI, tests, curl/websocat —
// anything that can set headers) or access_token cookie, or by waiting up to
// authGrace for a client `{"type":"auth","token":"..."}` message (browsers,
// which cannot set headers on a WebSocket upgrade).
//
// On success it returns the validated claims and organization. On failure it
// returns (nil, nil, closeCode, reason) — the caller closes the socket with
// that code without ever having sent `hello`.
func (h *Handler) handshake(
	ctx context.Context, req *http.Request, conn *websocket.Conn, orgSlug string,
) (*auth.Claims, *models.Organization, websocket.StatusCode, string) {
	if token := extractHeaderToken(req); token != "" {
		claims, org, code, reason := h.authenticate(ctx, token, orgSlug)
		if claims != nil {
			return claims, org, 0, ""
		}
		// An Authorization header is a credential the caller deliberately
		// set (a browser cannot) — a present-but-invalid one fails fast
		// rather than falling through to the grace window.
		return nil, nil, code, reason
	}

	// The access_token cookie is set by the login/refresh endpoints for an
	// unrelated server-rendered flow (OAuth consent) — browsers attach it
	// to EVERY same-origin request automatically, including this upgrade,
	// so its presence is incidental rather than a deliberate credential
	// choice. Unlike the header, a stale/mismatched cookie (e.g. left over
	// from an earlier login than the token the page currently holds) must
	// not permanently fail the socket: fall through and give the client's
	// fresh in-band `auth` message a chance.
	if token := extractCookieToken(req); token != "" {
		if claims, org, _, _ := h.authenticate(ctx, token, orgSlug); claims != nil {
			return claims, org, 0, ""
		}
	}

	return h.awaitAuthMessage(ctx, conn, orgSlug)
}

// extractHeaderToken mirrors middleware.extractToken's Authorization header
// handling.
func extractHeaderToken(req *http.Request) string {
	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", bearerTokenParts)
	if len(parts) != bearerTokenParts || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}

	return parts[1]
}

// extractCookieToken mirrors middleware.extractToken's access_token cookie
// fallback.
func extractCookieToken(req *http.Request) string {
	if cookie, err := req.Cookie(cookieAuthToken); err == nil {
		return cookie.Value
	}

	return ""
}

// awaitAuthMessage waits up to h.authGrace for the first client message. Only
// `auth` is accepted before authentication; anything else — including a
// subscribe sent too early — closes with 4401.
//
// conn.Read closes the connection itself on ANY error, including a context
// deadline (see the Conn doc comment in coder/websocket) — with the
// library's own default status, not ours. So the grace window cannot be a
// context passed straight into Read: that would race our intended 4401
// against the library's own close. Instead the read runs on the request
// context (no deadline of its own) in a goroutine, and a separate timer
// decides whether we ever waited long enough to give up and close 4401
// ourselves first.
func (h *Handler) awaitAuthMessage(
	ctx context.Context, conn *websocket.Conn, orgSlug string,
) (*auth.Claims, *models.Organization, websocket.StatusCode, string) {
	type readResult struct {
		data []byte
		err  error
	}

	resultCh := make(chan readResult, 1)
	go func() {
		_, data, err := conn.Read(ctx)
		resultCh <- readResult{data: data, err: err}
	}()

	select {
	case <-time.After(h.authGrace):
		return nil, nil, CloseAuthFailed, "no auth message within the grace window"
	case <-ctx.Done():
		return nil, nil, CloseAuthFailed, "connection ended before authentication"
	case res := <-resultCh:
		if res.err != nil {
			return nil, nil, CloseAuthFailed, "connection ended before authentication"
		}

		var msg clientMessage
		if jsonErr := json.Unmarshal(res.data, &msg); jsonErr != nil || msg.Type != msgTypeAuth || msg.Token == "" {
			return nil, nil, CloseAuthFailed, "first message must be auth"
		}

		claims, org, code, reason := h.authenticate(ctx, msg.Token, orgSlug)
		if claims == nil {
			return nil, nil, code, reason
		}

		return claims, org, 0, ""
	}
}

// authenticate validates the token and checks org membership, mirroring
// middleware.RequireAuth + RequireOrgAccess but executed in-handler (the
// route is registered outside that middleware chain — browsers cannot
// present credentials at WebSocket-upgrade time).
func (h *Handler) authenticate(
	ctx context.Context, token, orgSlug string,
) (*auth.Claims, *models.Organization, websocket.StatusCode, string) {
	claims, err := h.authService.ValidateToken(ctx, token)
	if err != nil {
		return nil, nil, CloseAuthFailed, "invalid or expired token"
	}

	user, err := h.dbService.GetUser(ctx, claims.UserUID)
	if err != nil {
		return nil, nil, CloseAuthFailed, "user not found"
	}

	org, err := h.dbService.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, nil, CloseForbidden, "organization not found"
	}

	if !claims.IsSuperAdmin() && !user.SuperAdmin {
		if claims.OrgSlug != orgSlug {
			return nil, nil, CloseForbidden, "access to this organization is denied"
		}
		if _, err := h.dbService.GetMemberByUserAndOrg(ctx, user.UID, org.UID); err != nil {
			return nil, nil, CloseForbidden, "access to this organization is denied"
		}
	}

	return claims, org, 0, ""
}
