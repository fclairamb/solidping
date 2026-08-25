package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// SAML-specific error codes surfaced to the frontend via the `error` query
// parameter on the redirect-back URL.
const (
	SAMLCodeAssertionInvalid = "ASSERTION_INVALID"
	SAMLCodeNoEmail          = "NO_EMAIL"
	SAMLCodeNotConfigured    = "NOT_CONFIGURED"
)

// SAMLHandler handles SAML 2.0 Service Provider authentication endpoints.
type SAMLHandler struct {
	base.HandlerBase
	svc *SAMLService
	cfg *config.Config
}

// NewSAMLHandler creates a new SAML SP handler.
func NewSAMLHandler(service *SAMLService, cfg *config.Config) *SAMLHandler {
	return &SAMLHandler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// Login initiates the SP-initiated SAML flow: builds an AuthnRequest and
// redirects the browser to the configured IdP's SSO endpoint.
// GET /api/v1/auth/saml/login?org=...&redirect_uri=...
func (h *SAMLHandler) Login(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := req.URL.Query().Get("org")
	if orgSlug == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "org parameter is required")
	}

	// Validate that the org exists
	if _, err := h.svc.db.GetOrganizationBySlug(req.Context(), orgSlug); err != nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	}

	redirectURI := req.URL.Query().Get("redirect_uri")
	if redirectURI == "" {
		redirectURI = "/dash0/orgs/" + orgSlug
	}

	redirectURL, err := h.svc.GenerateAuthnRequest(req.Context(), redirectURI, orgSlug)
	if err != nil {
		if errors.Is(err, ErrSAMLNotConfigured) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "SAML provider is not configured")
		}

		return h.WriteInternalError(writer, req, err)
	}

	http.Redirect(writer, req, redirectURL, http.StatusFound)

	return nil
}

// ACS (Assertion Consumer Service) consumes the SAML assertion POSTed back
// by the IdP after the user authenticates there.
// POST /api/v1/auth/saml/acs.
func (h *SAMLHandler) ACS(writer http.ResponseWriter, req *http.Request) error {
	// ParseForm reads SAMLResponse/RelayState from the POST body
	// (application/x-www-form-urlencoded), which sp.ParseResponse expects on
	// req.PostForm.
	if err := req.ParseForm(); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Malformed SAML response")
	}

	relayState := req.PostForm.Get("RelayState")
	if relayState == "" {
		return h.redirectWithError(writer, req, "/", OAuthCodeInvalidState, OAuthDescInvalidState)
	}

	// Validate and consume (one-time use) the RelayState we minted at
	// Login time — this is what lets HandleACS pass the matching request ID
	// to sp.ParseResponse for InResponseTo (replay/CSRF) checking.
	state, err := h.svc.ValidateRelayState(req.Context(), relayState)
	if err != nil {
		return h.redirectWithError(writer, req, "/", OAuthCodeInvalidState, OAuthDescInvalidState)
	}

	result, err := h.svc.HandleACS(req.Context(), req, state)
	if err != nil {
		return h.handleSAMLError(writer, req, state.RedirectURI, err)
	}

	// Redirect with tokens. Also set the SPA session cookie so
	// cookie-authenticated surfaces (the embedded MCP OAuth
	// authorize/consent flow) work without a login-page refresh bounce.
	return finishProviderCallback(writer, req,
		h.buildSuccessRedirect(state.RedirectURI, result),
		result.OrgSlug, result.AccessToken, result.ExpiresIn, result.Pending)
}

// Metadata serves this SP's own metadata document (entity ID, ACS URL,
// certificate) so IdP admins can configure their side by URL.
// GET /api/v1/auth/saml/metadata.
func (h *SAMLHandler) Metadata(writer http.ResponseWriter, req *http.Request) error {
	data, err := h.svc.Metadata(req.Context())
	if err != nil {
		if errors.Is(err, ErrSAMLNotConfigured) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "SAML provider is not configured")
		}

		return h.WriteInternalError(writer, req, err)
	}

	writer.Header().Set("Content-Type", "application/samlmetadata+xml")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(data)

	return nil
}

// buildSuccessRedirect constructs the redirect URL with tokens.
func (h *SAMLHandler) buildSuccessRedirect(baseURI string, result *SAMLResult) string {
	parsedURL, err := url.Parse(baseURI)
	if err != nil {
		parsedURL, _ = url.Parse("/")
	}

	query := parsedURL.Query()
	query.Set("access_token", result.AccessToken)
	query.Set("refresh_token", result.RefreshToken)
	query.Set("expires_in", strconv.Itoa(result.ExpiresIn))
	query.Set("org", result.OrgSlug)
	parsedURL.RawQuery = query.Encode()

	return parsedURL.String()
}

// redirectWithError redirects with error parameters.
func (h *SAMLHandler) redirectWithError(
	writer http.ResponseWriter, req *http.Request,
	baseURI, code, description string,
) error {
	parsedURL, err := url.Parse(baseURI)
	if err != nil {
		parsedURL, _ = url.Parse("/")
	}

	query := parsedURL.Query()
	query.Set("error", code)
	query.Set("error_description", description)
	parsedURL.RawQuery = query.Encode()

	http.Redirect(writer, req, parsedURL.String(), http.StatusFound)

	return nil
}

// handleSAMLError handles SAML errors by redirecting with error information.
func (h *SAMLHandler) handleSAMLError(
	writer http.ResponseWriter, req *http.Request,
	redirectURI string, err error,
) error {
	var code, description string

	switch {
	case errors.Is(err, ErrInvalidOAuthState):
		code = OAuthCodeInvalidState
		description = OAuthDescInvalidState
	case errors.Is(err, ErrSAMLAssertionInvalid):
		code = SAMLCodeAssertionInvalid
		description = "SAML assertion failed validation"
	case errors.Is(err, ErrSAMLNoEmail):
		code = SAMLCodeNoEmail
		description = "SAML assertion did not include a usable email address"
	case errors.Is(err, ErrSAMLNotConfigured):
		code = SAMLCodeNotConfigured
		description = "SAML provider is not configured"
	default:
		code = OAuthCodeFailed
		description = logOAuthFailure(req, "saml", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
