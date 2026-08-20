package oauth

import (
	"encoding/json"
	"net/http"
)

// registrationRequest is the RFC 7591 §2 client-metadata request body. Only the
// fields the MCP flow needs are modeled; unknown fields are ignored.
//
//nolint:tagliatelle // RFC 7591 wire format requires snake_case field names.
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
//
//nolint:tagliatelle // RFC 7591 wire format requires snake_case field names.
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
func (h *Handler) Register(writer http.ResponseWriter, req *http.Request) error {
	var body registrationRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.writeRegisterError(writer, ErrInvalidClientMetadata, "malformed registration body")
	}

	if len(body.RedirectURIs) == 0 {
		return h.writeRegisterError(writer, ErrInvalidRedirectURI, "at least one redirect_uri is required")
	}

	for _, uri := range body.RedirectURIs {
		if !IsValidRedirectURI(uri) {
			return h.writeRegisterError(writer, ErrInvalidRedirectURI,
				"redirect_uri must be https or an http loopback address: "+uri)
		}
	}

	grantTypes := body.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = []string{GrantAuthorizationCode, GrantRefreshToken}
	}

	responseTypes := body.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = []string{ResponseTypeCode}
	}

	scopes := ParseScopes(body.Scope)
	if len(scopes) == 0 {
		scopes = []string{ScopeMCP}
	}

	if !ScopesValid(scopes) {
		return h.writeRegisterError(writer, ErrInvalidClientMetadata, "unsupported scope requested")
	}

	// Confidential only when the client explicitly opts into a secret-based auth
	// method. Default (and "none") is a public client — the MCP native case.
	isPublic := body.TokenEndpointAuthMethod == "" || body.TokenEndpointAuthMethod == AuthMethodNone

	client, secret, err := h.svc.RegisterClient(
		req.Context(), body.ClientName, body.RedirectURIs, grantTypes, scopes, isPublic,
	)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	authMethod := AuthMethodNone
	if !isPublic {
		authMethod = AuthMethodSecretPost
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

	return h.writeNoStoreJSON(writer, http.StatusCreated, resp)
}

// writeRegisterError writes an RFC 7591 §3.2.2 registration error response.
func (h *Handler) writeRegisterError(writer http.ResponseWriter, code, description string) error {
	writer.Header().Set("Cache-Control", "no-store")

	return h.WriteJSON(writer, http.StatusBadRequest, errorBody{Error: code, ErrorDescription: description})
}
