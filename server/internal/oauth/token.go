package oauth

import (
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"
)

// tokenResponse is the RFC 6749 §5.1 successful token response. The MCP access
// token is a SolidPing JWT (audience-bound to the MCP resource); the refresh
// token is an opaque rotating secret.
//
//nolint:tagliatelle // RFC 6749 wire format requires snake_case field names.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// Token is the OAuth token endpoint. It supports the authorization_code grant
// (with PKCE verification) and the refresh_token grant (with rotation). Errors
// are returned as RFC 6749 §5.2 JSON bodies with the appropriate status.
func (h *Handler) Token(writer http.ResponseWriter, req bunrouter.Request) error {
	if err := req.ParseForm(); err != nil {
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidRequest, "malformed form body")
	}

	grantType := req.Form.Get("grant_type")
	switch grantType {
	case GrantAuthorizationCode:
		return h.tokenAuthorizationCode(writer, req)
	case GrantRefreshToken:
		return h.tokenRefresh(writer, req)
	default:
		return h.writeTokenError(writer, http.StatusBadRequest, ErrUnsupportedGrantType,
			"grant_type must be authorization_code or refresh_token")
	}
}

// tokenAuthorizationCode handles grant_type=authorization_code.
func (h *Handler) tokenAuthorizationCode(writer http.ResponseWriter, req bunrouter.Request) error {
	code := req.Form.Get("code")
	clientID := req.Form.Get("client_id")
	redirectURI := req.Form.Get("redirect_uri")
	codeVerifier := req.Form.Get("code_verifier")

	if code == "" || clientID == "" || redirectURI == "" {
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidRequest,
			"code, client_id, and redirect_uri are required")
	}

	if codeVerifier == "" {
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidRequest,
			"code_verifier is required (PKCE)")
	}

	result, err := h.svc.ExchangeAuthCode(req.Context(), code, clientID, redirectURI, codeVerifier)
	if err != nil {
		return h.tokenGrantError(writer, err)
	}

	return h.writeTokenSuccess(writer, result)
}

// tokenRefresh handles grant_type=refresh_token (rotating refresh).
func (h *Handler) tokenRefresh(writer http.ResponseWriter, req bunrouter.Request) error {
	refreshToken := req.Form.Get("refresh_token")
	clientID := req.Form.Get("client_id")

	if refreshToken == "" {
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidRequest,
			"refresh_token is required")
	}

	result, err := h.svc.ExchangeRefreshToken(req.Context(), refreshToken, clientID)
	if err != nil {
		return h.tokenGrantError(writer, err)
	}

	return h.writeTokenSuccess(writer, result)
}

// tokenGrantError maps a service-layer grant error to the correct OAuth error.
func (h *Handler) tokenGrantError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, errInvalidGrant), errors.Is(err, errPKCEFailed):
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidGrant,
			"invalid, expired, or already-used grant")
	case errors.Is(err, errClientNotFound):
		return h.writeTokenError(writer, http.StatusUnauthorized, ErrInvalidClient, "unknown client")
	default:
		return h.writeTokenError(writer, http.StatusInternalServerError, ErrServerError,
			"could not issue token")
	}
}

// writeTokenSuccess writes the RFC 6749 §5.1 success body with no-store caching.
func (h *Handler) writeTokenSuccess(writer http.ResponseWriter, result *TokenResult) error {
	return h.writeNoStoreJSON(writer, http.StatusOK, tokenResponse{
		AccessToken:  result.AccessToken,
		TokenType:    "Bearer",
		ExpiresIn:    result.ExpiresIn,
		RefreshToken: result.RefreshToken,
		Scope:        result.Scope,
	})
}

// writeTokenError writes an RFC 6749 §5.2 error body with no-store caching.
func (h *Handler) writeTokenError(writer http.ResponseWriter, status int, code, description string) error {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Pragma", "no-cache")

	return h.WriteJSON(writer, status, errorBody{Error: code, ErrorDescription: description})
}
