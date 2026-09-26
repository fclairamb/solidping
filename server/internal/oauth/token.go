package oauth

import (
	"errors"
	"net/http"
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
func (h *Handler) Token(writer http.ResponseWriter, req *http.Request) error {
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
func (h *Handler) tokenAuthorizationCode(writer http.ResponseWriter, req *http.Request) error {
	code := req.Form.Get("code")
	redirectURI := req.Form.Get("redirect_uri")
	codeVerifier := req.Form.Get("code_verifier")
	clientID, clientSecret := clientCredentials(req)

	if code == "" || clientID == "" || redirectURI == "" {
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidRequest,
			"code, client_id, and redirect_uri are required")
	}

	if codeVerifier == "" {
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidRequest,
			"code_verifier is required (PKCE)")
	}

	// Client authentication (RFC 6749 §3.2.1): a no-op for public clients, a
	// hard requirement for confidential ones. Checked before the grant itself
	// so a bad secret never burns the (single-use) authorization code.
	if err := h.svc.AuthenticateClient(req.Context(), clientID, clientSecret); err != nil {
		return h.tokenGrantError(writer, err)
	}

	result, err := h.svc.ExchangeAuthCode(req.Context(), code, clientID, redirectURI, codeVerifier)
	if err != nil {
		return h.tokenGrantError(writer, err)
	}

	return h.writeTokenSuccess(writer, result)
}

// tokenRefresh handles grant_type=refresh_token (rotating refresh).
func (h *Handler) tokenRefresh(writer http.ResponseWriter, req *http.Request) error {
	refreshToken := req.Form.Get("refresh_token")
	clientID, clientSecret := clientCredentials(req)

	if refreshToken == "" {
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidRequest,
			"refresh_token is required")
	}

	// client_id stays optional here, as it always has been: a refresh token is
	// itself a strong bearer credential, and ExchangeRefreshToken separately
	// checks a *presented* client_id against the grant's binding. But when a
	// client_id is presented, and it identifies a confidential client, that
	// client must authenticate the same as it would on the authorization_code
	// grant — a confidential client cannot dodge the secret check by omitting
	// its own client_id.
	if clientID != "" {
		if err := h.svc.AuthenticateClient(req.Context(), clientID, clientSecret); err != nil {
			return h.tokenGrantError(writer, err)
		}
	}

	result, err := h.svc.ExchangeRefreshToken(req.Context(), refreshToken, clientID)
	if err != nil {
		return h.tokenGrantError(writer, err)
	}

	return h.writeTokenSuccess(writer, result)
}

// clientCredentials resolves the client_id/client_secret presented at the
// token endpoint. HTTP Basic (client_secret_basic) wins over body fields
// (client_secret_post) when both are somehow present — RFC 6749 §2.3.1 says a
// client uses only one method, and Basic is the one carrying its own implicit
// client_id, so there is nothing to reconcile against the body's.
func clientCredentials(req *http.Request) (string, string) {
	if basicID, basicSecret, ok := req.BasicAuth(); ok {
		return basicID, basicSecret
	}

	return req.Form.Get(paramClientID), req.Form.Get("client_secret")
}

// tokenGrantError maps a service-layer grant error to the correct OAuth error.
func (h *Handler) tokenGrantError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, errInvalidGrant), errors.Is(err, errPKCEFailed):
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidGrant,
			"invalid, expired, or already-used grant")
	case errors.Is(err, errClientNotFound), errors.Is(err, errClientAuthFailed):
		// Byte-identical to the unknown-client response: an unknown client_id
		// and a confidential client's bad secret must be indistinguishable, or
		// this endpoint becomes a client-ID enumeration oracle.
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
