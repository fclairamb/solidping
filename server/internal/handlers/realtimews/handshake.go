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
// upgrade request's Authorization header / access_token cookie (CLI, tests,
// curl/websocat — anything that can set headers), or by waiting up to
// authGrace for a client `{"type":"auth","token":"..."}` message (browsers,
// which cannot set headers on a WebSocket upgrade).
//
// On success it returns the validated claims and organization. On failure it
// returns (nil, nil, closeCode, reason) — the caller closes the socket with
// that code without ever having sent `hello`.
func (h *Handler) handshake(
	ctx context.Context, req *http.Request, conn *websocket.Conn, orgSlug string,
) (*auth.Claims, *models.Organization, websocket.StatusCode, string) {
	if token := extractPreAuthToken(req); token != "" {
		claims, org, code, reason := h.authenticate(ctx, token, orgSlug)
		if claims != nil {
			return claims, org, 0, ""
		}
		// A present-but-invalid header/cookie token fails fast rather than
		// falling through to the grace window — an explicit credential that
		// doesn't validate is not "try again via message".
		return nil, nil, code, reason
	}

	return h.awaitAuthMessage(ctx, conn, orgSlug)
}

// extractPreAuthToken mirrors middleware.extractToken: Authorization header
// first, then the access_token cookie.
func extractPreAuthToken(req *http.Request) string {
	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		if cookie, err := req.Cookie(cookieAuthToken); err == nil {
			return cookie.Value
		}

		return ""
	}

	parts := strings.SplitN(authHeader, " ", bearerTokenParts)
	if len(parts) != bearerTokenParts || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}

	return parts[1]
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
