package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// CookieAuthToken is the name of the cookie used for storing the access token.
const CookieAuthToken = "access_token"

const (
	roleUser            = "user"
	fieldOrg            = "org"
	fieldBody           = "body"
	fieldPassword       = "password"
	fieldCode           = "code"
	msgInvalidJSON      = "Invalid JSON format"
	msgEmailRequired    = "Email is required"
	msgPasswordRequired = "Password is required"
	msgTokenRequired    = "Token is required"
	msgCodeRequired     = "Code is required"
)

// OAuth error codes shared across all OAuth handlers.
const (
	OAuthCodeInvalidState     = "INVALID_STATE"
	OAuthCodeEmailNotVerified = "EMAIL_NOT_VERIFIED"
	OAuthCodeTokenExchange    = "TOKEN_EXCHANGE_FAILED"
	OAuthCodeFailed           = "OAUTH_FAILED"
	OAuthDescInvalidState     = "Invalid or expired state parameter"
	OAuthDescTokenExchange    = "Failed to exchange authorization code"
)

// Handler provides HTTP handlers for authentication endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// LoginRequest represents a login request body.
type LoginRequest struct {
	Org      string `json:"org"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SwitchOrgRequest represents a switch-org request body.
type SwitchOrgRequest struct {
	Org string `json:"org"`
}

// RefreshRequest represents a token refresh request body.
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// LogoutRequest represents a logout request body. DeleteAllTokens and
// SignOutOthers are mutually exclusive: the former ends every session
// (including the caller's own — a full logout), the latter spares the
// caller's own session (a "sign out other devices" action, not a logout).
type LogoutRequest struct {
	DeleteAllTokens bool `json:"deleteAllTokens"`
	SignOutOthers   bool `json:"signOutOthers"`
}

// NewHandler creates a new authentication handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// setAccessTokenCookie sets the SPA session cookie so cookie-authenticated
// surfaces (the embedded MCP OAuth authorize/consent flow) work without a
// refresh bounce. Every login-shaped response — password login, SSO
// callbacks, refresh, org switch, 2FA — must set it through this helper so
// the cookie shape stays defined in one place.
func setAccessTokenCookie(writer http.ResponseWriter, accessToken string, expiresIn int) {
	http.SetCookie(writer, &http.Cookie{
		Name:   CookieAuthToken,
		Value:  accessToken,
		Path:   "/",
		MaxAge: expiresIn,
	})
}

// Login handles user login with email and password.
// Org is read from the request body (optional).
func (h *Handler) Login(writer http.ResponseWriter, req *http.Request) error {
	var loginReq LoginRequest
	if err := json.NewDecoder(req.Body).Decode(&loginReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if loginReq.Email == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: keyEmail, Message: msgEmailRequired},
		})
	}

	if loginReq.Password == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldPassword, Message: msgPasswordRequired},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: base.ExtractRemoteAddr(req),
	}

	resp, err := h.svc.Login(req.Context(), loginReq.Org, loginReq.Email, loginReq.Password, authContext)
	if err != nil {
		return h.handleAuthError(writer, err)
	}

	setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Logout handles user logout. Uses claims from middleware context.
func (h *Handler) Logout(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	// Parse optional logout request
	var logoutReq LogoutRequest
	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&logoutReq) // Ignore errors, optional body
	}

	if logoutReq.DeleteAllTokens && logoutReq.SignOutOthers {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "signOutOthers", Message: "deleteAllTokens and signOutOthers are mutually exclusive"},
		})
	}

	if logoutReq.DeleteAllTokens {
		resp, logoutErr := h.svc.LogoutUser(req.Context(), claims.UserUID)
		if logoutErr != nil {
			return h.handleLogoutError(writer, logoutErr)
		}

		// Clear cookie
		h.clearAuthCookie(writer)

		return h.WriteJSON(writer, http.StatusOK, resp)
	}

	if logoutReq.SignOutOthers {
		if claims.RefreshUID == "" {
			// No current session row to spare (e.g. a PAT hitting /logout) —
			// "sign out others" is meaningless without one.
			return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
				{Name: "signOutOthers", Message: "no current session to keep — this credential is not a session"},
			})
		}

		resp, logoutErr := h.svc.LogoutOtherSessions(req.Context(), claims.UserUID, claims.RefreshUID)
		if logoutErr != nil {
			return h.handleLogoutError(writer, logoutErr)
		}

		// The caller's own session (and cookie) survive — this is not a logout.
		return h.WriteJSON(writer, http.StatusOK, resp)
	}

	// Clear cookie
	h.clearAuthCookie(writer)

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"message": "Successfully logged out"})
}

// Refresh handles token refresh. No org parameter needed — derived from refresh token.
func (h *Handler) Refresh(writer http.ResponseWriter, req *http.Request) error {
	var refreshReq RefreshRequest
	if err := json.NewDecoder(req.Body).Decode(&refreshReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if refreshReq.RefreshToken == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "refreshToken", Message: "Refresh token is required"},
		})
	}

	resp, err := h.svc.Refresh(req.Context(), refreshReq.RefreshToken)
	if err != nil {
		return h.handleRefreshError(writer, err)
	}

	// Re-set the access token cookie exactly like Login does — without this,
	// cookie-authenticated surfaces silently lapse after the first hour even
	// though the bearer-token session keeps refreshing.
	setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"accessToken": resp.AccessToken,
		"expiresIn":   resp.ExpiresIn,
	})
}

// Me returns information about the current authenticated user. Uses claims from middleware context.
func (h *Handler) Me(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	resp, err := h.svc.GetUserInfo(req.Context(), claims)
	if err != nil {
		return h.handleUserInfoError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// UpdateMe updates the current user's profile. Uses claims from middleware context.
func (h *Handler) UpdateMe(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var updateReq UpdateProfileRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	resp, err := h.svc.UpdateProfile(req.Context(), claims, updateReq)
	if err != nil {
		return h.handleUserInfoError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// GetAllUserTokens returns all tokens for the authenticated user across all orgs.
func (h *Handler) GetAllUserTokens(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	// Get optional token type filter
	tokenType := req.URL.Query().Get("type")

	resp, err := h.svc.GetAllUserTokens(req.Context(), claims.UserUID, tokenType, claims.RefreshUID)
	if err != nil {
		return h.handleTokenError(writer, err, http.StatusNotFound)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// GetOrgTokens returns the list of tokens for the authenticated user scoped to an org.
func (h *Handler) GetOrgTokens(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	orgSlug := httpx.Param(req, "org")

	// Get optional token type filter
	tokenType := req.URL.Query().Get("type")

	resp, err := h.svc.GetUserTokens(req.Context(), orgSlug, claims.UserUID, tokenType, claims.RefreshUID)
	if err != nil {
		return h.handleTokenError(writer, err, http.StatusNotFound)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// CreateToken creates a new Personal Access Token. Org-scoped via URL param.
func (h *Handler) CreateToken(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	orgSlug := httpx.Param(req, "org")

	var createReq CreateTokenRequest

	if decodeErr := json.NewDecoder(req.Body).Decode(&createReq); decodeErr != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if createReq.Name == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: keyName, Message: "Token name is required"},
		})
	}

	resp, err := h.svc.CreatePAT(req.Context(), orgSlug, claims.UserUID, createReq)
	if err != nil {
		return h.handleTokenError(writer, err, http.StatusNotFound)
	}

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// RevokeToken revokes (deletes) a user token. User-scoped via middleware context.
func (h *Handler) RevokeToken(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	tokenUID := httpx.Param(req, "tokenUid")
	if tokenUID == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "tokenUid", Message: "Token UID is required"},
		})
	}

	err := h.svc.RevokeToken(req.Context(), claims.UserUID, tokenUID)
	if err != nil {
		return h.handleRevokeError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// RevokeCurrentToken revokes the grant backing the caller's own credential —
// the "I'm done, drop my access" call. It targets the row named by
// Claims.RefreshUID, so it works for a session principal and, crucially, for an
// MCP/OAuth client authenticated with only its short-lived access token (which
// no longer holds the refresh token itself). Scoped-down tokens (mcp:read) may
// self-revoke too: dropping your own credential is never a privilege
// escalation. A credential with no backing grant row (PAT, 2FA temp token) has
// nothing to revoke here and gets a 422 validation error.
func (h *Handler) RevokeCurrentToken(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	if claims.RefreshUID == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "token", Message: "this credential is not backed by a revocable grant"},
		})
	}

	// RevokeToken verifies the row belongs to the caller before deleting, so a
	// forged RefreshUID pointing at someone else's row cannot revoke it.
	if err := h.svc.RevokeToken(req.Context(), claims.UserUID, claims.RefreshUID); err != nil {
		return h.handleRevokeError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// SwitchOrg switches the user's current organization context.
func (h *Handler) SwitchOrg(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var switchReq SwitchOrgRequest
	if err := json.NewDecoder(req.Body).Decode(&switchReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if switchReq.Org == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldOrg, Message: "Organization is required"},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: base.ExtractRemoteAddr(req),
	}

	resp, err := h.svc.SwitchOrg(req.Context(), claims.UserUID, switchReq.Org, authContext)
	if err != nil {
		return h.handleAuthError(writer, err)
	}

	setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// handleAuthError handles errors from Login/SwitchOrg.
// Anti-enumeration: both org-not-found and invalid-credentials return the same error code.
func (h *Handler) handleAuthError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusUnauthorized, base.ErrorCodeInvalidCredentials, "Invalid credentials", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleLogoutError handles errors from LogoutUser.
func (h *Handler) handleLogoutError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrUserNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeUserNotFound, "User not found", err)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleRefreshError handles errors from Refresh.
func (h *Handler) handleRefreshError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrInvalidToken), errors.Is(err, ErrTokenExpired):
		return h.WriteErrorErr(
			writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Invalid or expired refresh token", err)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusUnauthorized, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleUserInfoError handles errors from GetUserInfo.
func (h *Handler) handleUserInfoError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrUserNotFound):
		return h.WriteErrorErr(
			writer, http.StatusUnauthorized, base.ErrorCodeUserNotFound, "User not found", err)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusUnauthorized, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleTokenError handles common token-related errors.
func (h *Handler) handleTokenError(writer http.ResponseWriter, err error, status int) error {
	switch {
	case errors.Is(err, ErrUserNotFound):
		return h.WriteErrorErr(writer, status, base.ErrorCodeUserNotFound, "User not found", err)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(writer, status, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleRevokeError handles errors from RevokeToken.
func (h *Handler) handleRevokeError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrTokenNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeTokenNotFound, "Token not found", err)
	case errors.Is(err, ErrUserNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeUserNotFound, "User not found", err)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// clearAuthCookie clears the authentication cookie.
func (h *Handler) clearAuthCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name:   CookieAuthToken,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}

// getClaimsFromContext retrieves the JWT claims set by the auth middleware.
func getClaimsFromContext(req *http.Request) (*Claims, bool) {
	claims, ok := req.Context().Value(base.ContextKeyClaims).(*Claims)

	return claims, ok
}

// Register handles user self-registration.
func (h *Handler) Register(writer http.ResponseWriter, req *http.Request) error {
	var regReq RegisterRequest
	if err := json.NewDecoder(req.Body).Decode(&regReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if regReq.Email == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: keyEmail, Message: msgEmailRequired},
		})
	}

	if regReq.Password == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldPassword, Message: msgPasswordRequired},
		})
	}

	resp, err := h.svc.Register(req.Context(), regReq)
	if err != nil {
		return h.handleRegistrationError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// ConfirmRegistration handles email confirmation for registration.
func (h *Handler) ConfirmRegistration(writer http.ResponseWriter, req *http.Request) error {
	var confirmReq ConfirmRegistrationRequest
	if err := json.NewDecoder(req.Body).Decode(&confirmReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if confirmReq.Token == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: keyToken, Message: msgTokenRequired},
		})
	}

	resp, err := h.svc.ConfirmRegistration(req.Context(), confirmReq.Token)
	if err != nil {
		return h.handleRegistrationError(writer, err)
	}

	if resp.AccessToken != "" {
		setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// RequestPasswordReset handles password reset requests.
func (h *Handler) RequestPasswordReset(writer http.ResponseWriter, req *http.Request) error {
	var resetReq RequestPasswordResetRequest
	if err := json.NewDecoder(req.Body).Decode(&resetReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if resetReq.Email == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: keyEmail, Message: msgEmailRequired},
		})
	}

	resp, err := h.svc.RequestPasswordReset(req.Context(), resetReq, base.ExtractRemoteAddr(req))
	if err != nil {
		if errors.Is(err, ErrRateLimited) {
			return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeRateLimited,
				"Too many password reset requests, please try again later")
		}

		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// ResetPassword handles setting a new password with a reset token.
func (h *Handler) ResetPassword(writer http.ResponseWriter, req *http.Request) error {
	var resetReq ResetPasswordRequest
	if err := json.NewDecoder(req.Body).Decode(&resetReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if resetReq.Token == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: keyToken, Message: msgTokenRequired},
		})
	}

	if resetReq.Password == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldPassword, Message: msgPasswordRequired},
		})
	}

	resp, err := h.svc.ResetPassword(req.Context(), resetReq)
	if err != nil {
		return h.handlePasswordResetError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// ChangePassword rotates the authenticated caller's password (or sets an
// initial one for an SSO-only account). Unlike ResetPassword it needs no
// emailed token — the session is the proof of identity, plus the current
// password when the account has one.
func (h *Handler) ChangePassword(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var changeReq ChangePasswordRequest
	if err := json.NewDecoder(req.Body).Decode(&changeReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if changeReq.NewPassword == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "newPassword", Message: msgPasswordRequired},
		})
	}

	resp, err := h.svc.ChangePassword(req.Context(), claims.UserUID, claims.RefreshUID, changeReq)
	if err != nil {
		return h.handleChangePasswordError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

func (h *Handler) handleChangePasswordError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrInvalidCurrentPassword):
		return h.WriteErrorErr(writer, http.StatusUnauthorized, base.ErrorCodeInvalidCurrentPassword,
			"Current password is incorrect", err)
	case errors.Is(err, ErrRateLimited):
		return h.WriteErrorErr(writer, http.StatusTooManyRequests, base.ErrorCodeRateLimited,
			"Too many password change attempts, please try again later", err)
	case errors.Is(err, ErrUserNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeUserNotFound, "User not found", err)
	case errors.Is(err, ErrInvalidCredentials):
		return h.WriteErrorErr(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			err.Error(), err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handlePasswordResetError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrPasswordResetExpired):
		return h.WriteErrorErr(writer, http.StatusGone, base.ErrorCodePasswordResetExpired,
			"Reset link has expired or is invalid", err)
	case errors.Is(err, ErrInvalidCredentials):
		return h.WriteErrorErr(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			err.Error(), err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// CreateOrg handles organization creation.
func (h *Handler) CreateOrg(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var createReq CreateOrgRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if createReq.Name == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "name", Message: "Name is required"},
		})
	}

	if createReq.Slug == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "slug", Message: "Slug is required"},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: base.ExtractRemoteAddr(req),
	}

	resp, err := h.svc.CreateOrg(req.Context(), claims.UserUID, createReq, authContext)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidOrgSlug):
			return h.WriteErrorErr(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError,
				"Slug must be 3-20 characters, lowercase alphanumeric with hyphens", err)
		case errors.Is(err, ErrOrgSlugTaken):
			return h.WriteErrorErr(writer, http.StatusConflict, base.ErrorCodeConflict,
				"Organization slug is already taken", err)
		default:
			return h.WriteInternalError(writer, err)
		}
	}

	// Set access token cookie, matching every other login-shaped response
	// (Login, SwitchOrg, Verify2FA, …) — CreateOrg now mints a fresh session
	// too.
	setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)

	// Product analytics (spec 2026-08-02-08). No-op unless PostHog is
	// configured. Only the UUIDs travel — never the org name or slug.
	analytics.Capture(req.Context(), analytics.Event{
		Name:    analytics.EventOrgCreated,
		OrgUID:  resp.UID,
		UserUID: claims.UserUID,
	})

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// DeleteOrg handles DELETE /api/v1/orgs/:org. The route is owner-gated by
// middleware.RequireOrgOwner, so reaching this handler already proves the
// caller owns the org (or is a super admin); the body must additionally repeat
// the org slug as an explicit confirmation.
//
// It answers 200 with a login-shaped session payload rather than a bare 204:
// the caller's old token names an org that no longer resolves, so the response
// carries the replacement session (scoped to a remaining org, or org-less when
// there is none) and refreshes the access-token cookie with it. This mirrors
// the org-rename path below, which re-issues tokens for the same reason.
func (h *Handler) DeleteOrg(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	orgSlug := httpx.Param(req, fieldOrg)

	var delReq DeleteOrgRequest
	if err := json.NewDecoder(req.Body).Decode(&delReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: base.ExtractRemoteAddr(req),
	}

	resp, err := h.svc.DeleteOrg(req.Context(), orgSlug, claims.UserUID, delReq, authContext)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrgSlugConfirmationMismatch):
			return h.WriteValidationError(writer, "Organization slug confirmation does not match",
				[]base.ValidationErrorField{
					{Name: "slug", Message: "Type the organization slug exactly to confirm deletion"},
				})
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
				"Organization not found")
		default:
			return h.WriteInternalError(writer, err)
		}
	}

	if resp == nil || resp.AccessToken == "" {
		// No replacement session could be minted. The deletion still happened,
		// so drop the cookie rather than leaving a token that now 404s.
		h.clearAuthCookie(writer)

		return h.WriteJSON(writer, http.StatusOK, &LoginResponse{})
	}

	setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// UpdateOrgProfile handles PATCH /api/v1/orgs/:org. The route is owner-gated by
// middleware.RequireOrgOwner (an admin gets the standard 403, not a hidden
// button), so this handler only validates and translates errors.
//
// On a slug rename the response carries a fresh org-scoped session, and the
// access-token cookie is refreshed with it — exactly like CreateOrg/SwitchOrg.
// Without that the dashboard's very next request, now addressed to the new
// slug, would 403 on the token-scope check.
func (h *Handler) UpdateOrgProfile(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	orgSlug := httpx.Param(req, fieldOrg)

	var profileReq UpdateOrgProfileRequest
	if err := json.NewDecoder(req.Body).Decode(&profileReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: base.ExtractRemoteAddr(req),
	}

	resp, err := h.svc.UpdateOrgProfile(req.Context(), orgSlug, claims.UserUID, profileReq, authContext)
	if err != nil {
		return h.writeOrgProfileError(writer, err)
	}

	if resp.AccessToken != "" {
		setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// writeOrgProfileError maps the profile-update domain errors onto the standard
// error shape, reusing exactly the messages CreateOrg emits for the shared slug
// errors so the dashboard can render one validation string per field.
func (h *Handler) writeOrgProfileError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrInvalidOrgSlug):
		return h.WriteErrorErr(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError,
			"Slug must be 3-20 characters, lowercase alphanumeric with hyphens", err)
	case errors.Is(err, ErrOrgSlugTaken):
		return h.WriteErrorErr(writer, http.StatusConflict, base.ErrorCodeConflict,
			"Organization slug is already taken", err)
	case errors.Is(err, ErrInvalidOrgName):
		return h.WriteErrorErr(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError,
			"Name must be between 1 and 100 characters", err)
	case errors.Is(err, ErrInvalidLogoURL):
		return h.WriteErrorErr(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError,
			"Logo URL must be an absolute http(s) URL", err)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// CreateInvitation handles invitation creation.
func (h *Handler) CreateInvitation(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	// Admin check
	if !claims.HasOrgRole(models.MemberRoleAdmin) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := httpx.Param(req, "org")

	var inviteReq InviteRequest
	if err := json.NewDecoder(req.Body).Decode(&inviteReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if inviteReq.Email == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: keyEmail, Message: msgEmailRequired},
		})
	}

	if inviteReq.Role == "" {
		inviteReq.Role = roleUser
	}

	if inviteReq.ExpiresIn == "" {
		inviteReq.ExpiresIn = durationLabel24
	}

	if inviteReq.App == "" {
		inviteReq.App = appNameDash0
	}

	resp, err := h.svc.CreateInvitation(req.Context(), orgSlug, claims.UserUID, inviteReq)
	if err != nil {
		return h.handleInvitationError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// ListInvitations lists pending invitations for an organization.
func (h *Handler) ListInvitations(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	if !claims.HasOrgRole(models.MemberRoleAdmin) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := httpx.Param(req, "org")

	resp, err := h.svc.ListInvitations(req.Context(), orgSlug)
	if err != nil {
		return h.handleInvitationError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// RevokeInvitation revokes a pending invitation.
func (h *Handler) RevokeInvitation(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	if !claims.HasOrgRole(models.MemberRoleAdmin) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := httpx.Param(req, "org")
	invitationUID := httpx.Param(req, "uid")

	err := h.svc.RevokeInvitation(req.Context(), orgSlug, invitationUID)
	if err != nil {
		return h.handleInvitationError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// GetInviteInfo returns public info about an invitation (no auth required).
func (h *Handler) GetInviteInfo(writer http.ResponseWriter, req *http.Request) error {
	token := httpx.Param(req, "token")
	if token == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: keyToken, Message: msgTokenRequired},
		})
	}

	resp, err := h.svc.GetInviteInfo(req.Context(), token)
	if err != nil {
		return h.handleInvitationError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// AcceptInvite accepts an invitation (no auth required for new users).
func (h *Handler) AcceptInvite(writer http.ResponseWriter, req *http.Request) error {
	var acceptReq AcceptInviteRequest
	if err := json.NewDecoder(req.Body).Decode(&acceptReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if acceptReq.Token == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: keyToken, Message: msgTokenRequired},
		})
	}

	resp, err := h.svc.AcceptInvite(req.Context(), acceptReq)
	if err != nil {
		return h.handleInvitationError(writer, err)
	}

	if resp.AccessToken != "" {
		setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// GetOrgSettings returns organization settings.
func (h *Handler) GetOrgSettings(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	if !claims.HasOrgRole(models.MemberRoleAdmin) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := httpx.Param(req, "org")

	resp, err := h.svc.GetOrgSettings(req.Context(), orgSlug)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// UpdateOrgSettings updates organization settings.
func (h *Handler) UpdateOrgSettings(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	if !claims.HasOrgRole(models.MemberRoleAdmin) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := httpx.Param(req, "org")

	var updateReq UpdateOrgSettingsRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	resp, err := h.svc.UpdateOrgSettings(req.Context(), orgSlug, updateReq)
	if err != nil {
		if errors.Is(err, ErrInvalidAutoJoinRegex) {
			return h.WriteErrorErr(
				writer, http.StatusBadRequest,
				base.ErrorCodeInvalidAutoJoinRegex,
				err.Error(), err,
			)
		}

		if errors.Is(err, ErrOrganizationNotFound) {
			return h.WriteErrorErr(
				writer, http.StatusNotFound,
				base.ErrorCodeOrganizationNotFound,
				"Organization not found", err,
			)
		}

		if errors.Is(err, ErrInvalidEscalationPolicy) {
			return h.WriteErrorErr(
				writer, http.StatusBadRequest,
				base.ErrorCodeValidationError,
				"Escalation policy not found in this organization", err,
			)
		}

		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// handleRegistrationError handles errors from registration endpoints.
func (h *Handler) handleRegistrationError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrRegistrationDisabled):
		return h.WriteErrorErr(writer, http.StatusForbidden, base.ErrorCodeRegistrationDisabled,
			"Registration is not enabled", err)
	case errors.Is(err, ErrEmailNotAllowed):
		return h.WriteErrorErr(writer, http.StatusForbidden, base.ErrorCodeEmailNotAllowed,
			"Email does not match the allowed pattern", err)
	case errors.Is(err, ErrEmailAlreadyTaken):
		return h.WriteErrorErr(writer, http.StatusConflict, base.ErrorCodeConflict,
			"Email is already registered", err)
	case errors.Is(err, ErrRegistrationExpired):
		return h.WriteErrorErr(writer, http.StatusGone, base.ErrorCodeRegistrationExpired,
			"Registration link has expired or is invalid", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleInvitationError handles errors from invitation endpoints.
func (h *Handler) handleInvitationError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrInvitationNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeInvitationNotFound,
			"Invitation not found", err)
	case errors.Is(err, ErrInvitationExpired):
		return h.WriteErrorErr(writer, http.StatusGone, base.ErrorCodeInvitationExpired,
			"Invitation has expired", err)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found", err)
	case errors.Is(err, ErrInvalidExpiresIn):
		return h.WriteErrorErr(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			err.Error(), err)
	case errors.Is(err, ErrInvalidApp):
		return h.WriteErrorErr(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			err.Error(), err)
	case errors.Is(err, entitlements.ErrEntitlementExceeded):
		return h.WriteErrorErr(writer, http.StatusForbidden, base.ErrorCodeEntitlementExceeded,
			"This organization has reached its user limit", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// Setup2FA initiates TOTP 2FA setup for the authenticated user.
func (h *Handler) Setup2FA(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	resp, err := h.svc.Setup2FA(req.Context(), claims.UserUID)
	if err != nil {
		return h.handle2FAError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Confirm2FA validates the TOTP code and enables 2FA, returning recovery codes.
func (h *Handler) Confirm2FA(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var confirmReq Verify2FARequest
	if err := json.NewDecoder(req.Body).Decode(&confirmReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if confirmReq.Code == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldCode, Message: msgCodeRequired},
		})
	}

	resp, err := h.svc.Confirm2FA(req.Context(), claims.UserUID, confirmReq.Code)
	if err != nil {
		return h.handle2FAError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Verify2FA validates a TOTP code during login using a temporary token.
func (h *Handler) Verify2FA(writer http.ResponseWriter, req *http.Request) error {
	tempToken := extractBearerToken(req)
	if tempToken == "" {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Temporary token required")
	}

	var verifyReq Verify2FARequest
	if err := json.NewDecoder(req.Body).Decode(&verifyReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if verifyReq.Code == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldCode, Message: msgCodeRequired},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: base.ExtractRemoteAddr(req),
	}

	resp, err := h.svc.Verify2FA(req.Context(), tempToken, verifyReq.Code, authContext)
	if err != nil {
		return h.handle2FAError(writer, err)
	}

	setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Recovery2FA uses a recovery code during login to complete authentication.
func (h *Handler) Recovery2FA(writer http.ResponseWriter, req *http.Request) error {
	tempToken := extractBearerToken(req)
	if tempToken == "" {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Temporary token required")
	}

	var recoveryReq Recovery2FARequest
	if err := json.NewDecoder(req.Body).Decode(&recoveryReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if recoveryReq.RecoveryCode == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "recoveryCode", Message: "Recovery code is required"},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: base.ExtractRemoteAddr(req),
	}

	resp, err := h.svc.Recovery2FA(req.Context(), tempToken, recoveryReq.RecoveryCode, authContext)
	if err != nil {
		return h.handle2FAError(writer, err)
	}

	setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Disable2FA disables 2FA for the authenticated user.
func (h *Handler) Disable2FA(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var disableReq Disable2FARequest
	if err := json.NewDecoder(req.Body).Decode(&disableReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if disableReq.Code == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldCode, Message: msgCodeRequired},
		})
	}

	if err := h.svc.Disable2FA(req.Context(), claims.UserUID, disableReq.Code); err != nil {
		return h.handle2FAError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"message": "2FA disabled successfully"})
}

// handle2FAError handles errors from 2FA endpoints.
func (h *Handler) handle2FAError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrInvalid2FACode):
		return h.WriteErrorErr(writer, http.StatusUnauthorized, base.ErrorCodeInvalid2FACode, "Invalid 2FA code", err)
	case errors.Is(err, ErrInvalidRecoveryCode):
		return h.WriteErrorErr(
			writer, http.StatusUnauthorized, base.ErrorCodeInvalidRecoveryCode, "Invalid recovery code", err)
	case errors.Is(err, ErrTwoFAAlreadyEnabled):
		return h.WriteErrorErr(writer, http.StatusConflict, base.ErrorCodeConflict, "2FA is already enabled", err)
	case errors.Is(err, ErrTwoFANotEnabled):
		return h.WriteErrorErr(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError, "2FA is not enabled", err)
	case errors.Is(err, ErrInvalidToken), errors.Is(err, ErrTokenExpired):
		return h.WriteErrorErr(
			writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Invalid or expired token", err)
	case errors.Is(err, ErrUserNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeUserNotFound, "User not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// extractBearerToken extracts the Bearer token from the Authorization header.
func extractBearerToken(req *http.Request) string {
	authHeader := req.Header.Get("Authorization")

	const prefix = "Bearer "
	if len(authHeader) > len(prefix) && strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return authHeader[len(prefix):]
	}

	return ""
}
