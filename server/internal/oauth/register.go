package oauth

import (
	"encoding/json"
	"net/http"

	"github.com/uptrace/bunrouter"
)

// registrationRequest is the RFC 7591 §2 client-metadata request body. Only the
// fields the MCP flow needs are modeled; unknown fields are ignored.
type registrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// registrationResponse is the RFC 7591 §3.2.1 success response. client_secret is
// present only for confidential clients.
type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// Register is the RFC 7591 dynamic client registration endpoint. Native MCP
// clients register as public clients (token_endpoint_auth_method=none) with PKCE
// and loopback redirect URIs; a client that asks for a secret-based auth method
// is treated as confidential and gets a generated secret. All redirect URIs are
// validated (loopback http or https only) before persistence so the issuer can
// never be turned into an open redirector.
func (h *Handler) Register(w http.ResponseWriter, req bunrouter.Request) error {
	var body registrationRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.writeRegisterError(w, ErrInvalidClientMetadata, "malformed registration body")
	}

	if len(body.RedirectURIs) == 0 {
		return h.writeRegisterError(w, ErrInvalidRedirectURI, "at least one redirect_uri is required")
	}

	for _, uri := range body.RedirectURIs {
		if !IsValidRedirectURI(uri) {
			return h.writeRegisterError(w, ErrInvalidRedirectURI,
				"redirect_uri must be https or an http loopback address: "+uri)
		}
	}

	grantTypes := body.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = []string{GrantAuthorizationCode, GrantRefreshToken}
	}

	responseTypes := body.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = []string{"code"}
	}

	scopes := ParseScopes(body.Scope)
	if len(scopes) == 0 {
		scopes = []string{ScopeMCP}
	}

	if !ScopesValid(scopes) {
		return h.writeRegisterError(w, ErrInvalidClientMetadata, "unsupported scope requested")
	}

	// Confidential only when the client explicitly opts into a secret-based auth
	// method. Default (and "none") is a public client — the MCP native case.
	isPublic := body.TokenEndpointAuthMethod == "" || body.TokenEndpointAuthMethod == "none"

	client, secret, err := h.svc.RegisterClient(
		req.Context(), body.ClientName, body.RedirectURIs, grantTypes, scopes, isPublic,
	)
	if err != nil {
		return h.WriteInternalError(w, err)
	}

	authMethod := "none"
	if !isPublic {
		authMethod = "client_secret_post"
	}

	resp := registrationResponse{
		ClientID:                client.ClientID,
		ClientSecret:            secret,
		ClientName:              client.ClientName,
		RedirectURIs:            client.RedirectURIs,
		GrantTypes:              grantTypes,
		ResponseTypes:           responseTypes,
		Scope:                   body.Scope,
		TokenEndpointAuthMethod: authMethod,
	}

	return h.writeNoStoreJSON(w, http.StatusCreated, resp)
}

// writeRegisterError writes an RFC 7591 §3.2.2 registration error response.
func (h *Handler) writeRegisterError(w http.ResponseWriter, code, description string) error {
	w.Header().Set("Cache-Control", "no-store")

	return h.WriteJSON(w, http.StatusBadRequest, errorBody{Error: code, ErrorDescription: description})
}
