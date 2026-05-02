// Package base provides common handler functionality for HTTP request handling.
package base

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"

	"github.com/getsentry/sentry-go"

	"github.com/fclairamb/solidping/server/internal/config"
)

// ErrorCode represents a machine-readable error code.
type ErrorCode string

// Standard error codes.
const (
	ErrorCodeInternalError             ErrorCode = "INTERNAL_ERROR"
	ErrorCodeValidationError           ErrorCode = "VALIDATION_ERROR"
	ErrorCodeNotFound                  ErrorCode = "NOT_FOUND"
	ErrorCodeUnauthorized              ErrorCode = "UNAUTHORIZED"
	ErrorCodeForbidden                 ErrorCode = "FORBIDDEN"
	ErrorCodeConflict                  ErrorCode = "CONFLICT"
	ErrorCodeOrganizationNotFound      ErrorCode = "ORGANIZATION_NOT_FOUND"
	ErrorCodeUserNotFound              ErrorCode = "USER_NOT_FOUND"
	ErrorCodeCheckNotFound             ErrorCode = "CHECK_NOT_FOUND"
	ErrorCodeConnectionNotFound        ErrorCode = "CONNECTION_NOT_FOUND"
	ErrorCodeInvalidCredentials        ErrorCode = "INVALID_CREDENTIALS"
	ErrorCodeInvalidToken              ErrorCode = "INVALID_TOKEN"
	ErrorCodeNoToken                   ErrorCode = "NO_TOKEN"
	ErrorCodeInvalidOrg                ErrorCode = "INVALID_ORG"
	ErrorCodeTokenNotFound             ErrorCode = "TOKEN_NOT_FOUND"
	ErrorCodeOAuthFailed               ErrorCode = "OAUTH_FAILED"
	ErrorCodeInvalidState              ErrorCode = "INVALID_STATE"
	ErrorCodeEmailNotVerified          ErrorCode = "EMAIL_NOT_VERIFIED"
	ErrorCodeTokenExchangeFailed       ErrorCode = "TOKEN_EXCHANGE_FAILED"
	ErrorCodeStatusPageNotFound        ErrorCode = "STATUS_PAGE_NOT_FOUND"
	ErrorCodeStatusPageSectionNotFound ErrorCode = "STATUS_PAGE_SECTION_NOT_FOUND"
	ErrorCodeCheckGroupNotFound        ErrorCode = "CHECK_GROUP_NOT_FOUND"
	ErrorCodeRegistrationDisabled      ErrorCode = "REGISTRATION_DISABLED"
	ErrorCodeEmailNotAllowed           ErrorCode = "EMAIL_NOT_ALLOWED"
	ErrorCodeRegistrationExpired       ErrorCode = "REGISTRATION_EXPIRED"
	ErrorCodeInvitationExpired         ErrorCode = "INVITATION_EXPIRED"
	ErrorCodeInvitationNotFound        ErrorCode = "INVITATION_NOT_FOUND"
	ErrorCodePasswordResetExpired      ErrorCode = "PASSWORD_RESET_EXPIRED"
	ErrorCodeMaintenanceWindowNotFound ErrorCode = "MAINTENANCE_WINDOW_NOT_FOUND"
	ErrorCodeInvalid2FACode            ErrorCode = "INVALID_2FA_CODE"
	ErrorCodeInvalidRecoveryCode       ErrorCode = "INVALID_RECOVERY_CODE"
	ErrorCode2FARequired               ErrorCode = "2FA_REQUIRED"
	ErrorCodeEmailInboxNotConfigured   ErrorCode = "EMAIL_INBOX_NOT_CONFIGURED"
	ErrorCodeEmailInboxDisabled        ErrorCode = "EMAIL_INBOX_DISABLED"
	ErrorCodeEmailInboxTestFailed      ErrorCode = "EMAIL_INBOX_TEST_FAILED"
	ErrorCodeEmailInboxNotAvailable    ErrorCode = "EMAIL_INBOX_NOT_AVAILABLE"
	ErrorCodeResultNotFound            ErrorCode = "RESULT_NOT_FOUND"
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

// WriteError writes an error response without an internal error.
func (h *HandlerBase) WriteError(w http.ResponseWriter, status int, code ErrorCode, message string) error {
	return h.writeJSONError(w, status, code, message, nil, 0)
}

// WriteErrorErr writes an error response with an internal error.
func (h *HandlerBase) WriteErrorErr(
	w http.ResponseWriter, status int, code ErrorCode, message string, internalErr error,
) error {
	return h.writeJSONError(w, status, code, message, internalErr, 0)
}

// WriteInternalError writes a 500 internal server error response.
func (h *HandlerBase) WriteInternalError(w http.ResponseWriter, err error) error {
	return h.writeJSONError(
		w,
		http.StatusInternalServerError,
		ErrorCodeInternalError,
		"Internal server error",
		err,
		0,
	)
}

// WriteInternalErrorR writes a 500 internal server error response and reports
// the error to Sentry if a hub is available on the request context.
// Prefer this over WriteInternalError when you have access to the request.
func (h *HandlerBase) WriteInternalErrorR(w http.ResponseWriter, r *http.Request, err error) error {
	if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
		hub.CaptureException(err)
	}

	return h.WriteInternalError(w, err)
}
