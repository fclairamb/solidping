package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// CookieAuthToken is the name of the cookie used for storing the access token.
const CookieAuthToken = "access_token"

const roleAdmin = "admin"

const (
	roleUser            = "user"
	fieldOrg            = "org"
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

// LogoutRequest represents a logout request body.
type LogoutRequest struct {
	DeleteAllTokens bool `json:"deleteAllTokens"`
}

// NewHandler creates a new authentication handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// Login handles user login with email and password.
// Org is read from the request body (optional).
func (h *Handler) Login(writer http.ResponseWriter, req bunrouter.Request) error {
	var loginReq LoginRequest
	if err := json.NewDecoder(req.Body).Decode(&loginReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if loginReq.Email == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "email", Message: msgEmailRequired},
		})
	}

	if loginReq.Password == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "password", Message: msgPasswordRequired},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: extractRemoteAddress(req),
	}

	resp, err := h.svc.Login(req.Context(), loginReq.Org, loginReq.Email, loginReq.Password, authContext)
	if err != nil {
		return h.handleAuthError(writer, err)
	}

	// Set access token cookie
	http.SetCookie(writer, &http.Cookie{
		Name:   CookieAuthToken,
		Value:  resp.AccessToken,
		Path:   "/",
		MaxAge: resp.ExpiresIn,
	})

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Logout handles user logout. Uses claims from middleware context.
func (h *Handler) Logout(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	// Parse optional logout request
	var logoutReq LogoutRequest
	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&logoutReq) // Ignore errors, optional body
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

	// Clear cookie
	h.clearAuthCookie(writer)

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"message": "Successfully logged out"})
}

// Refresh handles token refresh. No org parameter needed — derived from refresh token.
func (h *Handler) Refresh(writer http.ResponseWriter, req bunrouter.Request) error {
	var refreshReq RefreshRequest
	if err := json.NewDecoder(req.Body).Decode(&refreshReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
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

	// Update access token cookie
	http.SetCookie(writer, &http.Cookie{
		Name:   CookieAuthToken,
		Value:  resp.AccessToken,
		Path:   "/",
		MaxAge: resp.ExpiresIn,
	})

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"accessToken": resp.AccessToken,
		"expiresIn":   resp.ExpiresIn,
	})
}

// Me returns information about the current authenticated user. Uses claims from middleware context.
func (h *Handler) Me(writer http.ResponseWriter, req bunrouter.Request) error {
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
func (h *Handler) UpdateMe(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var updateReq UpdateProfileRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	resp, err := h.svc.UpdateProfile(req.Context(), claims, updateReq)
	if err != nil {
		return h.handleUserInfoError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// GetAllUserTokens returns all tokens for the authenticated user across all orgs.
func (h *Handler) GetAllUserTokens(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	// Get optional token type filter
	tokenType := req.URL.Query().Get("type")

	resp, err := h.svc.GetAllUserTokens(req.Context(), claims.UserUID, tokenType)
	if err != nil {
		return h.handleTokenError(writer, err, http.StatusNotFound)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// GetOrgTokens returns the list of tokens for the authenticated user scoped to an org.
func (h *Handler) GetOrgTokens(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	orgSlug := req.Param("org")

	// Get optional token type filter
	tokenType := req.URL.Query().Get("type")

	resp, err := h.svc.GetUserTokens(req.Context(), orgSlug, claims.UserUID, tokenType)
	if err != nil {
		return h.handleTokenError(writer, err, http.StatusNotFound)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// CreateToken creates a new Personal Access Token. Org-scoped via URL param.
func (h *Handler) CreateToken(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	orgSlug := req.Param("org")

	var createReq CreateTokenRequest

	if decodeErr := json.NewDecoder(req.Body).Decode(&createReq); decodeErr != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
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
func (h *Handler) RevokeToken(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	tokenUID := req.Param("tokenUid")
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

// SwitchOrg switches the user's current organization context.
func (h *Handler) SwitchOrg(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var switchReq SwitchOrgRequest
	if err := json.NewDecoder(req.Body).Decode(&switchReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if switchReq.Org == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldOrg, Message: "Organization is required"},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: extractRemoteAddress(req),
	}

	resp, err := h.svc.SwitchOrg(req.Context(), claims.UserUID, switchReq.Org, authContext)
	if err != nil {
		return h.handleAuthError(writer, err)
	}

	// Set access token cookie
	http.SetCookie(writer, &http.Cookie{
		Name:   CookieAuthToken,
		Value:  resp.AccessToken,
		Path:   "/",
		MaxAge: resp.ExpiresIn,
	})

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
func getClaimsFromContext(req bunrouter.Request) (*Claims, bool) {
	claims, ok := req.Context().Value(base.ContextKeyClaims).(*Claims)

	return claims, ok
}

// Register handles user self-registration.
func (h *Handler) Register(writer http.ResponseWriter, req bunrouter.Request) error {
	var regReq RegisterRequest
	if err := json.NewDecoder(req.Body).Decode(&regReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if regReq.Email == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "email", Message: msgEmailRequired},
		})
	}

	if regReq.Password == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "password", Message: msgPasswordRequired},
		})
	}

	resp, err := h.svc.Register(req.Context(), regReq)
	if err != nil {
		return h.handleRegistrationError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// ConfirmRegistration handles email confirmation for registration.
func (h *Handler) ConfirmRegistration(writer http.ResponseWriter, req bunrouter.Request) error {
	var confirmReq ConfirmRegistrationRequest
	if err := json.NewDecoder(req.Body).Decode(&confirmReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if confirmReq.Token == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "token", Message: msgTokenRequired},
		})
	}

	resp, err := h.svc.ConfirmRegistration(req.Context(), confirmReq.Token)
	if err != nil {
		return h.handleRegistrationError(writer, err)
	}

	if resp.AccessToken != "" {
		http.SetCookie(writer, &http.Cookie{
			Name:   CookieAuthToken,
			Value:  resp.AccessToken,
			Path:   "/",
			MaxAge: resp.ExpiresIn,
		})
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// RequestPasswordReset handles password reset requests.
func (h *Handler) RequestPasswordReset(writer http.ResponseWriter, req bunrouter.Request) error {
	var resetReq RequestPasswordResetRequest
	if err := json.NewDecoder(req.Body).Decode(&resetReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if resetReq.Email == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "email", Message: msgEmailRequired},
		})
	}

	resp, err := h.svc.RequestPasswordReset(req.Context(), resetReq)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// ResetPassword handles setting a new password with a reset token.
func (h *Handler) ResetPassword(writer http.ResponseWriter, req bunrouter.Request) error {
	var resetReq ResetPasswordRequest
	if err := json.NewDecoder(req.Body).Decode(&resetReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if resetReq.Token == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "token", Message: msgTokenRequired},
		})
	}

	if resetReq.Password == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "password", Message: msgPasswordRequired},
		})
	}

	resp, err := h.svc.ResetPassword(req.Context(), resetReq)
	if err != nil {
		return h.handlePasswordResetError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
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
func (h *Handler) CreateOrg(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var createReq CreateOrgRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
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

	resp, err := h.svc.CreateOrg(req.Context(), claims.UserUID, createReq)
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

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// CreateInvitation handles invitation creation.
func (h *Handler) CreateInvitation(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	// Admin check
	if claims.Role != roleAdmin && claims.Role != RoleSuperAdmin {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := req.Param("org")

	var inviteReq InviteRequest
	if err := json.NewDecoder(req.Body).Decode(&inviteReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if inviteReq.Email == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "email", Message: msgEmailRequired},
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
func (h *Handler) ListInvitations(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	if claims.Role != roleAdmin && claims.Role != RoleSuperAdmin {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := req.Param("org")

	resp, err := h.svc.ListInvitations(req.Context(), orgSlug)
	if err != nil {
		return h.handleInvitationError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// RevokeInvitation revokes a pending invitation.
func (h *Handler) RevokeInvitation(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	if claims.Role != roleAdmin && claims.Role != RoleSuperAdmin {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := req.Param("org")
	invitationUID := req.Param("uid")

	err := h.svc.RevokeInvitation(req.Context(), orgSlug, invitationUID)
	if err != nil {
		return h.handleInvitationError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// GetInviteInfo returns public info about an invitation (no auth required).
func (h *Handler) GetInviteInfo(writer http.ResponseWriter, req bunrouter.Request) error {
	token := req.Param("token")
	if token == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "token", Message: msgTokenRequired},
		})
	}

	resp, err := h.svc.GetInviteInfo(req.Context(), token)
	if err != nil {
		return h.handleInvitationError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// AcceptInvite accepts an invitation (no auth required for new users).
func (h *Handler) AcceptInvite(writer http.ResponseWriter, req bunrouter.Request) error {
	var acceptReq AcceptInviteRequest
	if err := json.NewDecoder(req.Body).Decode(&acceptReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if acceptReq.Token == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "token", Message: msgTokenRequired},
		})
	}

	resp, err := h.svc.AcceptInvite(req.Context(), acceptReq)
	if err != nil {
		return h.handleInvitationError(writer, err)
	}

	if resp.AccessToken != "" {
		http.SetCookie(writer, &http.Cookie{
			Name:   CookieAuthToken,
			Value:  resp.AccessToken,
			Path:   "/",
			MaxAge: resp.ExpiresIn,
		})
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// GetOrgSettings returns organization settings.
func (h *Handler) GetOrgSettings(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	if claims.Role != roleAdmin && claims.Role != RoleSuperAdmin {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := req.Param("org")

	resp, err := h.svc.GetOrgSettings(req.Context(), orgSlug)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// UpdateOrgSettings updates organization settings.
func (h *Handler) UpdateOrgSettings(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	if claims.Role != roleAdmin && claims.Role != RoleSuperAdmin {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
	}

	orgSlug := req.Param("org")

	var updateReq UpdateOrgSettingsRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	resp, err := h.svc.UpdateOrgSettings(req.Context(), orgSlug, updateReq)
	if err != nil {
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
	default:
		return h.WriteInternalError(writer, err)
	}
}

// Setup2FA initiates TOTP 2FA setup for the authenticated user.
func (h *Handler) Setup2FA(writer http.ResponseWriter, req bunrouter.Request) error {
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
func (h *Handler) Confirm2FA(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var confirmReq Verify2FARequest
	if err := json.NewDecoder(req.Body).Decode(&confirmReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if confirmReq.Code == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "code", Message: msgCodeRequired},
		})
	}

	resp, err := h.svc.Confirm2FA(req.Context(), claims.UserUID, confirmReq.Code)
	if err != nil {
		return h.handle2FAError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Verify2FA validates a TOTP code during login using a temporary token.
func (h *Handler) Verify2FA(writer http.ResponseWriter, req bunrouter.Request) error {
	tempToken := extractBearerToken(req)
	if tempToken == "" {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Temporary token required")
	}

	var verifyReq Verify2FARequest
	if err := json.NewDecoder(req.Body).Decode(&verifyReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if verifyReq.Code == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "code", Message: msgCodeRequired},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: extractRemoteAddress(req),
	}

	resp, err := h.svc.Verify2FA(req.Context(), tempToken, verifyReq.Code, authContext)
	if err != nil {
		return h.handle2FAError(writer, err)
	}

	// Set access token cookie
	http.SetCookie(writer, &http.Cookie{
		Name:   CookieAuthToken,
		Value:  resp.AccessToken,
		Path:   "/",
		MaxAge: resp.ExpiresIn,
	})

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Recovery2FA uses a recovery code during login to complete authentication.
func (h *Handler) Recovery2FA(writer http.ResponseWriter, req bunrouter.Request) error {
	tempToken := extractBearerToken(req)
	if tempToken == "" {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Temporary token required")
	}

	var recoveryReq Recovery2FARequest
	if err := json.NewDecoder(req.Body).Decode(&recoveryReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if recoveryReq.RecoveryCode == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "recoveryCode", Message: "Recovery code is required"},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: extractRemoteAddress(req),
	}

	resp, err := h.svc.Recovery2FA(req.Context(), tempToken, recoveryReq.RecoveryCode, authContext)
	if err != nil {
		return h.handle2FAError(writer, err)
	}

	// Set access token cookie
	http.SetCookie(writer, &http.Cookie{
		Name:   CookieAuthToken,
		Value:  resp.AccessToken,
		Path:   "/",
		MaxAge: resp.ExpiresIn,
	})

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Disable2FA disables 2FA for the authenticated user.
func (h *Handler) Disable2FA(writer http.ResponseWriter, req bunrouter.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var disableReq Disable2FARequest
	if err := json.NewDecoder(req.Body).Decode(&disableReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	if disableReq.Code == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "code", Message: msgCodeRequired},
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
func extractBearerToken(req bunrouter.Request) string {
	authHeader := req.Header.Get("Authorization")

	const prefix = "Bearer "
	if len(authHeader) > len(prefix) && strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return authHeader[len(prefix):]
	}

	return ""
}

func extractRemoteAddress(req bunrouter.Request) string {
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
