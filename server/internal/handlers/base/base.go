// Package base provides common handler functionality for HTTP request handling.
package base

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/errorreport"
)

// ErrorCode represents a machine-readable error code.
type ErrorCode string

// Standard error codes.
const (
	ErrorCodeInternalError        ErrorCode = "INTERNAL_ERROR"
	ErrorCodeValidationError      ErrorCode = "VALIDATION_ERROR"
	ErrorCodeNotFound             ErrorCode = "NOT_FOUND"
	ErrorCodeMethodNotAllowed     ErrorCode = "METHOD_NOT_ALLOWED"
	ErrorCodeUnauthorized         ErrorCode = "UNAUTHORIZED"
	ErrorCodeForbidden            ErrorCode = "FORBIDDEN"
	ErrorCodeConflict             ErrorCode = "CONFLICT"
	ErrorCodeOrganizationNotFound ErrorCode = "ORGANIZATION_NOT_FOUND"
	ErrorCodeUserNotFound         ErrorCode = "USER_NOT_FOUND"
	ErrorCodeCheckNotFound        ErrorCode = "CHECK_NOT_FOUND"
	ErrorCodeIntegrationNotFound  ErrorCode = "INTEGRATION_NOT_FOUND"
	// ErrorCodeChannelNotFound is the prior name of ErrorCodeIntegrationNotFound,
	// kept as a deprecated alias for one release cycle. PR-E removes it.
	ErrorCodeChannelNotFound     ErrorCode = ErrorCodeIntegrationNotFound
	ErrorCodeInvalidCredentials  ErrorCode = "INVALID_CREDENTIALS"
	ErrorCodeInvalidToken        ErrorCode = "INVALID_TOKEN"
	ErrorCodeNoToken             ErrorCode = "NO_TOKEN"
	ErrorCodeInvalidOrg          ErrorCode = "INVALID_ORG"
	ErrorCodeTokenNotFound       ErrorCode = "TOKEN_NOT_FOUND"
	ErrorCodeOAuthFailed         ErrorCode = "OAUTH_FAILED"
	ErrorCodeInvalidState        ErrorCode = "INVALID_STATE"
	ErrorCodeEmailNotVerified    ErrorCode = "EMAIL_NOT_VERIFIED"
	ErrorCodeTokenExchangeFailed ErrorCode = "TOKEN_EXCHANGE_FAILED"
	ErrorCodeStatusPageNotFound  ErrorCode = "STATUS_PAGE_NOT_FOUND"
	// ErrorCodeStatusPageLocked accompanies the 401 a password-protected
	// status page answers with until the visitor unlocks it (spec
	// 2026-08-21-07). It is deliberately NOT ErrorCodeUnauthorized: status0
	// keys the unlock form off this exact code, and conflating it with a
	// session problem would send the visitor to a login page they have no
	// account for.
	ErrorCodeStatusPageLocked          ErrorCode = "STATUS_PAGE_LOCKED"
	ErrorCodeStatusPageSectionNotFound ErrorCode = "STATUS_PAGE_SECTION_NOT_FOUND"
	ErrorCodeCheckGroupNotFound        ErrorCode = "CHECK_GROUP_NOT_FOUND"
	ErrorCodeSeverityNotFound          ErrorCode = "SEVERITY_NOT_FOUND"
	ErrorCodeSeverityInUse             ErrorCode = "SEVERITY_IN_USE"
	ErrorCodeRegistrationDisabled      ErrorCode = "REGISTRATION_DISABLED"
	ErrorCodeEmailNotAllowed           ErrorCode = "EMAIL_NOT_ALLOWED"
	ErrorCodeRegistrationExpired       ErrorCode = "REGISTRATION_EXPIRED"
	ErrorCodeInvitationExpired         ErrorCode = "INVITATION_EXPIRED"
	ErrorCodeInvitationNotFound        ErrorCode = "INVITATION_NOT_FOUND"
	ErrorCodePasswordResetExpired      ErrorCode = "PASSWORD_RESET_EXPIRED"
	ErrorCodeMaintenanceWindowNotFound ErrorCode = "MAINTENANCE_WINDOW_NOT_FOUND"
	ErrorCodeSLONotFound               ErrorCode = "SLO_NOT_FOUND"
	ErrorCodeReportScheduleNotFound    ErrorCode = "REPORT_SCHEDULE_NOT_FOUND"
	ErrorCodeInvalid2FACode            ErrorCode = "INVALID_2FA_CODE"
	ErrorCodeInvalidRecoveryCode       ErrorCode = "INVALID_RECOVERY_CODE"
	ErrorCode2FARequired               ErrorCode = "2FA_REQUIRED"
	ErrorCodeEmailInboxNotConfigured   ErrorCode = "EMAIL_INBOX_NOT_CONFIGURED"
	ErrorCodeEmailInboxDisabled        ErrorCode = "EMAIL_INBOX_DISABLED"
	ErrorCodeEmailInboxTestFailed      ErrorCode = "EMAIL_INBOX_TEST_FAILED"
	ErrorCodeEmailInboxNotAvailable    ErrorCode = "EMAIL_INBOX_NOT_AVAILABLE"
	ErrorCodeResultNotFound            ErrorCode = "RESULT_NOT_FOUND"
	ErrorCodeRateLimited               ErrorCode = "RATE_LIMITED"
	ErrorCodeConcurrencyLimited        ErrorCode = "CONCURRENCY_LIMITED"
	ErrorCodeRequestTimeout            ErrorCode = "REQUEST_TIMEOUT"
	ErrorCodeInvalidAutoJoinRegex      ErrorCode = "INVALID_AUTO_JOIN_REGEX"
	ErrorCodeAlreadyAMember            ErrorCode = "ALREADY_A_MEMBER"
	ErrorCodeRequestPending            ErrorCode = "REQUEST_PENDING"
	ErrorCodeRequestNotFound           ErrorCode = "REQUEST_NOT_FOUND"
	ErrorCodeRequestCooldownActive     ErrorCode = "REQUEST_COOLDOWN_ACTIVE"
	ErrorCodeDependencyCycle           ErrorCode = "DEPENDENCY_CYCLE"
	ErrorCodeDependencySelf            ErrorCode = "DEPENDENCY_SELF"
	ErrorCodeDependencyCrossOrg        ErrorCode = "DEPENDENCY_CROSS_ORG"
	ErrorCodeDependencyNotFound        ErrorCode = "DEPENDENCY_NOT_FOUND"
	ErrorCodeDependencyDuplicate       ErrorCode = "DEPENDENCY_DUPLICATE"
	ErrorCodeDependencyInvalidKind     ErrorCode = "DEPENDENCY_INVALID_KIND"
	ErrorCodeEntitlementExceeded       ErrorCode = "ENTITLEMENT_EXCEEDED"
	ErrorCodeQuotaExceeded             ErrorCode = "QUOTA_EXCEEDED"
	ErrorCodeFeatureNotEntitled        ErrorCode = "FEATURE_NOT_ENTITLED"
	ErrorCodeEntitlementsStale         ErrorCode = "ENTITLEMENTS_STALE"
	ErrorCodePasskeyNotFound           ErrorCode = "PASSKEY_NOT_FOUND"
	ErrorCodePasskeyVerificationFailed ErrorCode = "PASSKEY_VERIFICATION_FAILED"
	ErrorCodePasskeySessionExpired     ErrorCode = "PASSKEY_SESSION_EXPIRED"
	ErrorCodePasskeyLastAuthMethod     ErrorCode = "PASSKEY_LAST_AUTH_METHOD"
	ErrorCodeWebAuthnNotConfigured     ErrorCode = "WEBAUTHN_NOT_CONFIGURED"
	ErrorCodeChannelNotConnected       ErrorCode = "CHANNEL_NOT_CONNECTED"
	// ErrorCodeInvalidCurrentPassword is returned by POST /api/v1/auth/change-password
	// when the supplied currentPassword does not match the stored hash. It is
	// deliberately distinct from INVALID_CREDENTIALS so the dashboard can point
	// the error at the current-password field instead of the whole form.
	ErrorCodeInvalidCurrentPassword ErrorCode = "INVALID_CURRENT_PASSWORD"
)

// ContextKey is the type used for context keys in middleware and handlers.
type ContextKey string

// Context keys for middleware-stored values.
const (
	// ContextKeyUser is the context key for the authenticated user.
	ContextKeyUser ContextKey = "user"
	// ContextKeyOrganization is the context key for the current organization.
	ContextKeyOrganization ContextKey = "organization"
	// ContextKeyClaims is the context key for the JWT claims.
	ContextKeyClaims ContextKey = "claims"
)

// HandlerBase provides common functionality for HTTP handlers.
type HandlerBase struct {
	cfg *config.Config
}

// NewHandlerBase creates a new HandlerBase with the given configuration.
func NewHandlerBase(cfg *config.Config) HandlerBase {
	return HandlerBase{cfg: cfg}
}

// ErrorResponse represents a standard error response.
type ErrorResponse struct {
	Title         string `json:"title"`
	Code          string `json:"code,omitempty"`
	Detail        string `json:"detail,omitempty"`
	InternalError string `json:"internalError,omitempty"`
	Source        string `json:"source,omitempty"`
}

// ValidationError represents a validation error response.
type ValidationError struct {
	Title  string                 `json:"title"`
	Code   string                 `json:"code,omitempty"`
	Fields []ValidationErrorField `json:"fields"`
}

// ValidationErrorField represents a single field validation error.
type ValidationErrorField struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// WriteJSON writes a JSON response with the given status code.
func (h *HandlerBase) WriteJSON(w http.ResponseWriter, status int, data interface{}) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(data)
}

func (h *HandlerBase) writeJSONError(
	writer http.ResponseWriter, status int, code ErrorCode, message string, internalErr error, callDepth int,
) error {
	errResp := ErrorResponse{
		Code:  string(code),
		Title: message,
	}

	// In development mode, include internal error details
	if internalErr != nil {
		errResp.Detail = internalErr.Error()
	}

	if callDepth > 0 {
		_, file, line, _ := runtime.Caller(callDepth)
		errResp.Source = fmt.Sprintf("%s:%d", file, line)
	}

	return h.WriteJSON(writer, status, errResp)
}

// WriteValidationError writes a validation error response.
func (h *HandlerBase) WriteValidationError(
	writer http.ResponseWriter, message string, details []ValidationErrorField,
) error {
	resp := ValidationError{
		Title:  message,
		Code:   string(ErrorCodeValidationError),
		Fields: details,
	}

	return h.WriteJSON(writer, http.StatusUnprocessableEntity, resp)
}

// WriteError writes an error response without an internal error. It never
// reports to Sentry: it carries no error to report, and every caller uses it
// for an expected, client-visible outcome.
func (h *HandlerBase) WriteError(w http.ResponseWriter, status int, code ErrorCode, message string) error {
	return h.writeJSONError(w, status, code, message, nil, 0)
}

// WriteErrorErr writes an error response with an internal error. The request is
// required because a 5xx written this way is a server fault and is reported to
// Sentry on the request-scoped hub; 4xx statuses are client faults and never
// produce an event.
func (h *HandlerBase) WriteErrorErr(
	writer http.ResponseWriter, request *http.Request,
	status int, code ErrorCode, message string, internalErr error,
) error {
	if status >= http.StatusInternalServerError {
		errorreport.CaptureOnRequest(request, internalErr)
	}

	return h.writeJSONError(writer, status, code, message, internalErr, 0)
}

// WriteInternalError writes a 500 internal server error response and reports the
// error to Sentry on the request-scoped hub.
//
// Reporting is deliberately not opt-in: this is the single funnel for "the
// server broke", so taking the request is what makes every 500 visible without
// each new handler having to remember. When no hub is on the context (a unit
// test, a non-HTTP caller) the capture is a silent no-op.
func (h *HandlerBase) WriteInternalError(w http.ResponseWriter, r *http.Request, err error) error {
	errorreport.CaptureOnRequest(r, err)

	return h.writeJSONError(
		w,
		http.StatusInternalServerError,
		ErrorCodeInternalError,
		"Internal server error",
		err,
		0,
	)
}

// ExtractRemoteAddr extracts the caller's IP address from a request for
// display/forensics purposes (e.g. auth session history, heartbeat caller
// metadata). It follows X-Forwarded-For -> X-Real-IP -> RemoteAddr, trusting
// proxy headers unconditionally.
//
// This is NOT suitable for security-sensitive decisions (e.g. rate limiting)
// since any client can spoof these headers unless a trusted proxy strips or
// overwrites them first. See middleware/ratelimit.go's extractIP for the
// proxy-trust-gated variant used there.
func ExtractRemoteAddr(req *http.Request) string {
	// Try X-Forwarded-For header first (common in reverse proxy setups)
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		if ips := strings.Split(xff, ","); len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Try X-Real-IP header
	if xri := req.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr from the connection
	if req.RemoteAddr != "" {
		return req.RemoteAddr
	}

	return "unknown"
}
