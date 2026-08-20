package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// HTTP surface of the OAuth 2.0 Device Authorization Grant (RFC 8628,
// spec 2026-08-08-02).
//
// The request/response field names on these four endpoints are the RFC's
// snake_case spellings (device_code, user_code, verification_uri, expires_in,
// interval, grant_type, and the RFC 6749 §5.2 {error, error_description}
// envelope) rather than solidping's house camelCase. That is deliberate: this
// is the interoperable OAuth contract that off-the-shelf device-flow clients
// speak, and it takes precedence over the local style here. The two consent
// endpoints are solidping's own (not part of RFC 8628) and DO follow the house
// camelCase style.

// DeviceAuthorizationRequestBody is the body of POST /auth/device.
// clientName labels the request on the consent page; clientId is accepted for
// OAuth compatibility and ignored (solidping is not a multi-client
// authorization server on this endpoint).
type DeviceAuthorizationRequestBody struct {
	ClientName string `json:"clientName"`
	ClientID   string `json:"clientId"`
}

// StartDeviceAuthorization opens a device-authorization request. Public: it
// grants nothing until a logged-in user approves it.
// POST /api/v1/auth/device.
func (h *Handler) StartDeviceAuthorization(writer http.ResponseWriter, req *http.Request) error {
	var body DeviceAuthorizationRequestBody

	// A body is optional; an empty or malformed one just means "no label".
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		body = DeviceAuthorizationRequestBody{}
	}

	resp, err := h.svc.StartDeviceAuthorization(req.Context(), body.ClientName)
	if err != nil {
		if errors.Is(err, ErrDeviceClientNameTooLong) {
			return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
				{Name: "clientName", Message: "Client name is too long"},
			})
		}

		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// DeviceTokenRequestBody is the body of POST /auth/device/token. RFC 8628
// names.
//
//nolint:tagliatelle // RFC 8628 wire format requires snake_case field names.
type DeviceTokenRequestBody struct {
	GrantType  string `json:"grant_type"`
	DeviceCode string `json:"device_code"`
	ClientID   string `json:"client_id"`
}

// DeviceTokenResponse is the successful RFC 8628 §3.5 token response. The
// access token is a solidping PAT, so the client can use it as a normal bearer
// credential with no further exchange.
//
//nolint:tagliatelle // RFC 6749 wire format requires snake_case field names.
type DeviceTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// PollDeviceToken is the RFC 8628 token endpoint. Public and deliberately not
// given a stricter rate limit than the global one: a legitimate client polls it
// every few seconds for the request's whole TTL, and the device_code (32 random
// bytes) is the real capability — brute-forcing it is infeasible. Over-eager
// polling is answered with slow_down rather than 429.
// POST /api/v1/auth/device/token.
func (h *Handler) PollDeviceToken(writer http.ResponseWriter, req *http.Request) error {
	var body DeviceTokenRequestBody

	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.writeOAuthError(writer, "invalid_request", "grant_type and device_code are required")
	}

	if body.GrantType != DeviceGrantType {
		return h.writeOAuthError(writer, "unsupported_grant_type", "unsupported grant_type")
	}

	token, err := h.svc.PollDeviceToken(req.Context(), body.DeviceCode)
	if err != nil {
		return h.writeDeviceTokenError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, DeviceTokenResponse{
		AccessToken: token,
		TokenType:   tokenTypeBearer,
	})
}

// writeDeviceTokenError maps the service sentinels onto RFC 8628 §3.5 error
// codes. Anything unrecognized is a genuine server fault.
func (h *Handler) writeDeviceTokenError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrDeviceAuthorizationPending):
		return h.writeOAuthError(writer, "authorization_pending", "the authorization request is still pending")
	case errors.Is(err, ErrDeviceSlowDown):
		return h.writeOAuthError(writer, "slow_down", "polling too fast; respect the advertised interval")
	case errors.Is(err, ErrDeviceAccessDenied):
		return h.writeOAuthError(writer, "access_denied", "the authorization request was denied")
	case errors.Is(err, ErrDeviceExpiredToken):
		return h.writeOAuthError(writer, "expired_token", "the device_code has expired, is unknown, or was already used")
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// oauthErrorBody is the RFC 6749 §5.2 error envelope — distinct from
// solidping's usual {title, code, detail} shape, because OAuth clients parse
// this one.
//
//nolint:tagliatelle // RFC 6749 wire format requires snake_case field names.
type oauthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// writeOAuthError emits the RFC 6749 §5.2 error body. All token-endpoint
// errors are HTTP 400 per the RFC.
func (h *Handler) writeOAuthError(writer http.ResponseWriter, code, description string) error {
	return h.WriteJSON(writer, http.StatusBadRequest, oauthErrorBody{
		Error:            code,
		ErrorDescription: description,
	})
}

// GetDeviceConsent returns the public details of a device-authorization
// request so the consent page can render them. Authenticated (the user code is
// the brute-forceable surface) and rate limited at the route level.
// GET /api/v1/auth/device/consent?userCode=WDJP-4KXR.
func (h *Handler) GetDeviceConsent(writer http.ResponseWriter, req *http.Request) error {
	if _, ok := getClaimsFromContext(req); !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	info, err := h.svc.GetDeviceConsent(req.Context(), req.URL.Query().Get("userCode"))
	if err != nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound,
			"Device authorization request not found or expired")
	}

	return h.WriteJSON(writer, http.StatusOK, info)
}

// DeviceConsentRequest is the body of POST /auth/device/consent. Org is the
// slug the approving user picked on the consent page — the minted PAT is bound
// to it, NOT implicitly to the session's org. Empty means "the session's org".
type DeviceConsentRequest struct {
	UserCode string `json:"userCode"`
	Org      string `json:"org"`
	Approve  bool   `json:"approve"`
}

// RespondToDeviceConsent approves or denies a pending device-authorization
// request. Approval mints a real PAT owned by the approving user and scoped to
// the selected org, so this requires a full authenticated session.
// POST /api/v1/auth/device/consent.
func (h *Handler) RespondToDeviceConsent(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var body DeviceConsentRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if body.UserCode == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "userCode", Message: "User code is required"},
		})
	}

	err := h.svc.RespondToDeviceConsent(req.Context(), claims, body.UserCode, body.Org, body.Approve)
	if err != nil {
		return h.writeDeviceConsentError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"approved": body.Approve})
}

// writeDeviceConsentError maps consent failures onto the house error shape.
func (h *Handler) writeDeviceConsentError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrDeviceAuthNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound,
			"Device authorization request not found or expired")
	case errors.Is(err, ErrDeviceAuthAlreadyResolved):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict,
			"This request was already approved or denied")
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found")
	case errors.Is(err, ErrUserNotFound):
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden,
			"You are not a member of the selected organization")
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
