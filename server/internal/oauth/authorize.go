package oauth

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// authRequest is a validated /authorize request, populated only after every
// parameter has passed its check. The handler builds it once on GET and again on
// the approve POST so the consent UI cannot smuggle altered parameters past
// validation.
type authRequest struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string
	Scope               string
	State               string
	Resource            string
	CodeChallenge       string
	CodeChallengeMethod string
}

// parseAuthRequest reads the OAuth authorization parameters from the query
// string (GET) or form body (POST).
func parseAuthRequest(req *http.Request) *authRequest {
	get := req.URL.Query().Get
	if req.Method == http.MethodPost {
		_ = req.ParseForm()
		get = req.Form.Get
	}

	return &authRequest{
		ClientID:            get(paramClientID),
		RedirectURI:         get("redirect_uri"),
		ResponseType:        get("response_type"),
		Scope:               get("scope"),
		State:               get("state"),
		Resource:            get("resource"),
		CodeChallenge:       get("code_challenge"),
		CodeChallengeMethod: get("code_challenge_method"),
	}
}

// validateAuthRequest applies every authorization-request check in spec order.
// Errors that occur before a trusted redirect_uri is established are returned as
// render errors (shown to the user) so we never redirect to an unvalidated URI;
// once client + redirect_uri are trusted, later errors redirect back to the
// client per RFC 6749 §4.1.2.1.
func (h *Handler) validateAuthRequest(req *http.Request, authReq *authRequest) (*authRequest, *authError) {
	meta := h.metadata()

	if authReq.ClientID == "" {
		return nil, newRenderError(ErrInvalidRequest, "client_id is required")
	}

	client, err := h.svc.GetClient(req.Context(), authReq.ClientID)
	if err != nil {
		return nil, newRenderError(ErrInvalidClient, "unknown client_id")
	}

	// redirect_uri must match a registered URI exactly (loopback ignores port).
	if authReq.RedirectURI == "" || !RedirectURIAllowed(authReq.RedirectURI, client.RedirectURIs) {
		return nil, newRenderError(ErrInvalidRedirectURI, "redirect_uri is not registered for this client")
	}

	// From here on a trusted redirect target exists → report errors by redirect.
	if authReq.ResponseType != ResponseTypeCode {
		return nil, newRedirectError(ErrUnsupportedResponseType,
			"only response_type=code is supported", authReq.RedirectURI, authReq.State)
	}

	// PKCE is mandatory and S256-only (OAuth 2.1 forbids plain).
	if authReq.CodeChallenge == "" {
		return nil, newRedirectError(ErrInvalidRequest, "code_challenge is required (PKCE)",
			authReq.RedirectURI, authReq.State)
	}

	if authReq.CodeChallengeMethod == "" {
		authReq.CodeChallengeMethod = CodeChallengeMethodS256
	}

	if authReq.CodeChallengeMethod != CodeChallengeMethodS256 {
		return nil, newRedirectError(ErrInvalidRequest,
			"code_challenge_method must be S256", authReq.RedirectURI, authReq.State)
	}

	if !validCodeChallenge(authReq.CodeChallenge) {
		return nil, newRedirectError(ErrInvalidRequest, "malformed code_challenge",
			authReq.RedirectURI, authReq.State)
	}

	// scope defaults to full mcp; explicit scopes must be recognized.
	if authReq.Scope == "" {
		authReq.Scope = ScopeMCP
	}

	if !ScopesValid(ParseScopes(authReq.Scope)) {
		return nil, newRedirectError(ErrInvalidScope, "unsupported scope requested",
			authReq.RedirectURI, authReq.State)
	}

	// resource (RFC 8707) must match the MCP resource when supplied; default it.
	if authReq.Resource == "" {
		authReq.Resource = meta.ResourceURL()
	}

	if authReq.Resource != meta.ResourceURL() {
		return nil, newRedirectError(ErrInvalidRequest,
			"resource does not match the MCP resource", authReq.RedirectURI, authReq.State)
	}

	return authReq, nil
}

// session extracts and validates the logged-in user from the request's
// access_token cookie (set by the dashboard login) or bearer header. Returns the
// claims when a valid session exists.
func (h *Handler) session(req *http.Request) (*auth.Claims, bool) {
	token := extractSessionToken(req)
	if token == "" {
		return nil, false
	}

	claims, err := h.svc.authSvc.ValidateToken(req.Context(), token)
	if err != nil {
		return nil, false
	}

	return claims, true
}

// Authorize is the GET authorization endpoint. It validates the request, then
// either redirects to the dashboard login (no session) or to the dashboard
// consent screen (valid session). The actual code is minted by ApproveAuthorize
// once the user consents.
func (h *Handler) Authorize(writer http.ResponseWriter, req *http.Request) error {
	authReq, authErr := h.validateAuthRequest(req, parseAuthRequest(req))
	if authErr != nil {
		return h.handleAuthError(writer, req, authErr)
	}

	claims, ok := h.session(req)
	if !ok {
		// No session → bounce through the dashboard login, returning here after.
		h.redirectToLogin(writer, req)

		return nil
	}

	// Valid session → hand off to the consent SPA route, which will POST back to
	// approve. Pass the already-validated parameters through.
	h.redirectToConsent(writer, claims.OrgSlug, authReq)

	return nil
}

// ApproveAuthorize is the POST authorization endpoint hit when the user approves
// the consent screen. It re-validates the request, requires a session, mints a
// single-use code bound to the grant, and redirects to the client's redirect_uri
// with code + state.
func (h *Handler) ApproveAuthorize(writer http.ResponseWriter, req *http.Request) error {
	authReq, authErr := h.validateAuthRequest(req, parseAuthRequest(req))
	if authErr != nil {
		return h.handleAuthError(writer, req, authErr)
	}

	claims, ok := h.session(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
			"A logged-in session is required to authorize")
	}

	// The user denied consent → return to the client with access_denied rather
	// than minting a code (RFC 6749 §4.1.2.1).
	if req.Form.Get("decision") == "deny" {
		return h.handleAuthError(writer, req, newRedirectError(ErrAccessDenied,
			"the user denied the authorization request", authReq.RedirectURI, authReq.State))
	}

	org, err := h.svc.db.GetOrganizationBySlug(req.Context(), claims.OrgSlug)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	code, err := h.svc.IssueAuthCode(req.Context(), &AuthCodeGrant{
		ClientID:            authReq.ClientID,
		UserUID:             claims.UserUID,
		OrgUID:              org.UID,
		OrgSlug:             claims.OrgSlug,
		RedirectURI:         authReq.RedirectURI,
		Scope:               authReq.Scope,
		Resource:            authReq.Resource,
		CodeChallenge:       authReq.CodeChallenge,
		CodeChallengeMethod: authReq.CodeChallengeMethod,
	})
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	dest := appendQuery(authReq.RedirectURI, map[string]string{"code": code, "state": authReq.State})
	http.Redirect(writer, req, dest, http.StatusFound)

	return nil
}

// handleAuthError reports a validation error either by redirecting back to the
// client (safe target established) or by rendering an error to the user.
func (h *Handler) handleAuthError(writer http.ResponseWriter, req *http.Request, authErr *authError) error {
	if authErr.Redirect && authErr.RedirectURI != "" {
		dest := appendQuery(authErr.RedirectURI, map[string]string{
			"error":             authErr.Code,
			"error_description": authErr.Description,
			"state":             authErr.State,
		})
		http.Redirect(writer, req, dest, http.StatusFound)

		return nil
	}

	return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, authErr.Error())
}

// redirectToLogin bounces an unauthenticated /authorize request through the
// dashboard login, encoding the original request as returnTo so the browser
// lands back here (now carrying the session cookie) after logging in.
//
// returnTo is deliberately a RELATIVE path (+query), not an absolute URL: the
// dashboard's open-redirect guard (web/dash0 lib/login-destination.ts) rejects
// any returnTo carrying a scheme, and a path-anchored value is same-origin by
// construction. The previous Issuer-prefixed absolute value was rejected by
// that guard, silently dead-ending the MCP connect flow on the login page.
func (h *Handler) redirectToLogin(writer http.ResponseWriter, req *http.Request) {
	returnTo := req.URL.RequestURI()
	loginURL := "/dash0/login?returnTo=" + url.QueryEscape(returnTo)
	http.Redirect(writer, req, loginURL, http.StatusFound)
}

// redirectToConsent sends the browser to the dashboard consent SPA route with
// the validated authorization parameters so the user can approve or deny.
func (h *Handler) redirectToConsent(writer http.ResponseWriter, orgSlug string, authReq *authRequest) {
	query := url.Values{}
	query.Set(paramClientID, authReq.ClientID)
	query.Set("redirect_uri", authReq.RedirectURI)
	query.Set("response_type", authReq.ResponseType)
	query.Set("scope", authReq.Scope)
	query.Set("state", authReq.State)
	query.Set("resource", authReq.Resource)
	query.Set("code_challenge", authReq.CodeChallenge)
	query.Set("code_challenge_method", authReq.CodeChallengeMethod)

	consentURL := "/dash0/orgs/" + url.PathEscape(orgSlug) + "/oauth/consent?" + query.Encode()
	writer.Header().Set("Location", consentURL)
	writer.WriteHeader(http.StatusFound)
}

// extractSessionToken pulls the session token from the access_token cookie or a
// bearer Authorization header (mirrors middleware.extractToken without importing
// the middleware package, to avoid an import cycle).
func extractSessionToken(req *http.Request) string {
	if authHeader := req.Header.Get("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			return parts[1]
		}

		return ""
	}

	if cookie, err := req.Cookie("access_token"); err == nil {
		return cookie.Value
	}

	return ""
}

// appendQuery appends the given non-empty params to a URL's query string,
// preserving any existing query.
func appendQuery(rawURL string, params map[string]string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	query := parsed.Query()

	for key, value := range params {
		if value != "" {
			query.Set(key, value)
		}
	}

	parsed.RawQuery = query.Encode()

	return parsed.String()
}
