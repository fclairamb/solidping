package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/authhandoff"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// maxHandoffExchangeBody bounds the exchange request body. A real one is
// `{"code":"<43 chars>"}`.
const maxHandoffExchangeBody = 4 << 10

// handoffInvalidTitle is the ONE answer to a code that cannot be redeemed.
// Unknown, used, expired and forged codes must be indistinguishable.
const handoffInvalidTitle = "Sign-in code invalid, expired or already used"

// HandoffExchangeRequest is the body of POST /api/v1/auth/handoff/exchange.
type HandoffExchangeRequest struct {
	Code string `json:"code"`
}

// HandoffExchangeResponse is the login-shaped answer to a redeemed handoff
// code: exactly what a password login returns (so the dashboard feeds it to
// the same applyLoginResponse), plus where to land.
type HandoffExchangeResponse struct {
	LoginResponse

	// ReturnTo is the path the login was started from, or a landing page the
	// callback chose (the Slack install's channel). The dashboard only follows
	// it through its own same-origin / same-org guards.
	ReturnTo string `json:"returnTo,omitempty"`
	// MembershipPending names the org that did not admit the user yet, for the
	// no-org page. Empty when no join request was opened.
	MembershipPending string `json:"membershipPending,omitempty"`
}

// ExchangeHandoffCode redeems a handoff code minted by a federated login
// callback (spec 2026-09-25-12) and returns the session it carried, in the
// login response shape. The user and organization are read fresh, the same
// way /auth/me reads them.
//
// Every unusable code, and a code whose user or membership vanished in the
// meantime, yields authhandoff.ErrInvalidCode.
func (s *Service) ExchangeHandoffCode(ctx context.Context, code string) (*HandoffExchangeResponse, error) {
	session, err := authhandoff.Redeem(ctx, s.db, code)
	if err != nil {
		return nil, err
	}

	info, err := s.GetUserInfo(ctx, &Claims{UserUID: session.UserUID, OrgSlug: session.OrgSlug})
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, authhandoff.ErrInvalidCode
		}

		return nil, err
	}

	loginAction := LoginActionDefault
	if info.Organization == nil {
		loginAction = LoginActionNoOrg
	}

	return &HandoffExchangeResponse{
		LoginResponse: LoginResponse{
			AccessToken:   session.AccessToken,
			RefreshToken:  session.RefreshToken,
			ExpiresIn:     session.ExpiresIn,
			TokenType:     tokenTypeBearer,
			User:          info.User,
			Organization:  info.Organization,
			Organizations: info.Organizations,
			LoginAction:   loginAction,
		},
		ReturnTo:          session.ReturnTo,
		MembershipPending: session.MembershipPending,
	}, nil
}

// ExchangeHandoff trades a single-use handoff code for the session a
// federated login callback minted. Public: the code is the credential.
//
// POST /api/v1/auth/handoff/exchange  body: {"code": "..."}.
//
// A code that cannot be redeemed, for whatever reason, answers the same
// 401 body. The code is never logged.
func (h *Handler) ExchangeHandoff(writer http.ResponseWriter, req *http.Request) error {
	var body HandoffExchangeRequest

	if err := json.NewDecoder(io.LimitReader(req.Body, maxHandoffExchangeBody)).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	resp, err := h.svc.ExchangeHandoffCode(req.Context(), body.Code)
	if err != nil {
		if errors.Is(err, authhandoff.ErrInvalidCode) {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, handoffInvalidTitle)
		}

		return h.WriteInternalError(writer, req, err)
	}

	// The body carries live tokens: never let an intermediary keep it.
	writer.Header().Set("Cache-Control", "no-store")

	// Same as Login and Refresh: cookie-authenticated surfaces (the MCP OAuth
	// consent flow) follow the session the SPA just adopted.
	setAccessTokenCookie(writer, req, resp.AccessToken, resp.ExpiresIn)

	return h.WriteJSON(writer, http.StatusOK, resp)
}
