package oauth

// OAuth 2.0 error codes (RFC 6749 §4.1.2.1 / §5.2, RFC 7591 §3.2.2). These are
// the wire values returned in the `error` field of a token/registration error
// response or appended to a redirect URI on an authorization error.
const (
	ErrInvalidRequest          = "invalid_request"
	ErrInvalidClient           = "invalid_client"
	ErrInvalidGrant            = "invalid_grant"
	ErrUnauthorizedClient      = "unauthorized_client"
	ErrUnsupportedGrantType    = "unsupported_grant_type"
	ErrUnsupportedResponseType = "unsupported_response_type"
	ErrInvalidScope            = "invalid_scope"
	ErrAccessDenied            = "access_denied"
	ErrServerError             = "server_error"
	ErrInvalidRedirectURI      = "invalid_redirect_uri"
	ErrInvalidClientMetadata   = "invalid_client_metadata"
)

// errorBody is the RFC 6749 §5.2 / RFC 7591 §3.2.2 error JSON shape.
type errorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// authError is a structured authorization-time error. It distinguishes errors
// that must be reported by redirecting back to the client (a valid client +
// redirect_uri were established) from errors that must NOT redirect (the client
// or redirect_uri itself is untrusted) — redirecting an unvalidated URI would be
// an open redirector.
type authError struct {
	Code        string
	Description string
	Redirect    bool   // true → report via redirect; false → render an error page
	RedirectURI string // populated when Redirect is true
	State       string // echoed back on redirect
}

func (e *authError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}

	return e.Code
}

// newRenderError builds an authorization error that must be shown to the user
// (no safe redirect target).
func newRenderError(code, description string) *authError {
	return &authError{Code: code, Description: description, Redirect: false}
}

// newRedirectError builds an authorization error reported back to the client by
// redirecting to its validated redirect_uri.
func newRedirectError(code, description, redirectURI, state string) *authError {
	return &authError{
		Code:        code,
		Description: description,
		Redirect:    true,
		RedirectURI: redirectURI,
		State:       state,
	}
}
