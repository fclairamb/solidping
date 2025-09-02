// Package middleware provides HTTP middleware for authentication and authorization.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Use context keys from base package to avoid import cycles.

// CookieAuthToken is the name of the cookie used for storing the access token.
const CookieAuthToken = "access_token"

// Number of parts expected in "Bearer token" header.
const bearerTokenParts = 2

// AuthMiddleware provides authentication middleware functions.
type AuthMiddleware struct {
	base.HandlerBase
	authService *auth.Service
	dbService   db.Service
}

// NewAuthMiddleware creates a new authentication middleware.
func NewAuthMiddleware(authService *auth.Service, dbService db.Service, cfg *config.Config) *AuthMiddleware {
	return &AuthMiddleware{
		HandlerBase: base.NewHandlerBase(cfg),
		authService: authService,
		dbService:   dbService,
	}
}

// RequireAuth is a middleware that requires a valid authentication token.
func (m *AuthMiddleware) RequireAuth(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		slog.Debug("RequireAuth middleware called", "path", req.URL.Path)

		token := extractToken(req.Request)
		if token == "" {
			return m.WriteError(
				writer, http.StatusUnauthorized, base.ErrorCodeNoToken, "Authorization token is required")
		}

		claims, err := m.authService.ValidateToken(req.Context(), token)
		if err != nil {
			return m.WriteErrorErr(
				writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Invalid or expired token", err)
		}

		// Load user
		user, err := m.dbService.GetUser(req.Context(), claims.UserUID)
		if err != nil {
			return m.WriteErrorErr(
				writer, http.StatusUnauthorized, base.ErrorCodeUserNotFound, "User not found", err)
		}

		// Add claims and user to context
		ctx := req.Context()
		ctx = context.WithValue(ctx, base.ContextKeyClaims, claims)
		ctx = context.WithValue(ctx, base.ContextKeyUser, user)

		return next(writer, req.WithContext(ctx))
	}
}

// RequireOrgAccess is a middleware that verifies the user has access to the organization.
// Must be used after RequireAuth.
func (m *AuthMiddleware) RequireOrgAccess(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		slog.Debug("RequireOrgAccess middleware called", "path", req.URL.Path)

		orgSlug := req.Param("org")
		if orgSlug == "" {
			return m.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Organization is required")
		}

		// Get authenticated user and claims from context
		user, userOK := GetUserFromContext(req.Context())
		if !userOK {
			return m.WriteError(
				writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
		}

		claims, claimsOK := GetClaimsFromContext(req.Context())
		if !claimsOK {
			return m.WriteError(
				writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
		}

		// Super admins can access any organization
		if !claims.IsSuperAdmin() {
			// Verify the org in the token matches the request
			if claims.OrgSlug != orgSlug {
				return m.WriteError(
					writer, http.StatusForbidden, base.ErrorCodeForbidden, "Access to this organization is denied")
			}
		}

		// Load organization
		org, err := m.dbService.GetOrganizationBySlug(req.Context(), orgSlug)
		if err != nil {
			return m.WriteErrorErr(
				writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		}

		// For super admins accessing different org, verify membership exists or allow super admin access
		if !claims.IsSuperAdmin() && !user.SuperAdmin {
			// Check membership for regular users
			_, memberErr := m.dbService.GetMemberByUserAndOrg(req.Context(), user.UID, org.UID)
			if memberErr != nil {
				return m.WriteError(
					writer, http.StatusForbidden, base.ErrorCodeForbidden, "Access to this organization is denied")
			}
		}

		// Add organization to context
		ctx := context.WithValue(req.Context(), base.ContextKeyOrganization, org)

		slog.Debug("RequireOrgAccess: Access granted", "orgSlug", org.Slug, "userUID", user.UID)

		return next(writer, req.WithContext(ctx))
	}
}

// extractToken extracts the authentication token from the request.
func extractToken(request *http.Request) string {
	authHeader := request.Header.Get("Authorization")
	if authHeader == "" {
		// Try cookie as fallback
		if cookie, err := request.Cookie(CookieAuthToken); err == nil {
			return cookie.Value
		}

		return ""
	}

	parts := strings.SplitN(authHeader, " ", bearerTokenParts)
	if len(parts) != bearerTokenParts || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}

	return parts[1]
}

// GetUserFromContext retrieves the authenticated user from the context.
func GetUserFromContext(ctx context.Context) (*models.User, bool) {
	user, userOK := ctx.Value(base.ContextKeyUser).(*models.User)

	return user, userOK
}

// GetOrganizationFromContext retrieves the organization from the context.
func GetOrganizationFromContext(ctx context.Context) (*models.Organization, bool) {
	org, orgOK := ctx.Value(base.ContextKeyOrganization).(*models.Organization)

	return org, orgOK
}

// GetClaimsFromContext retrieves the JWT claims from the context.
func GetClaimsFromContext(ctx context.Context) (*auth.Claims, bool) {
	claims, claimsOK := ctx.Value(base.ContextKeyClaims).(*auth.Claims)

	return claims, claimsOK
}

// RequireSuperAdmin is a middleware that requires the user to be a super admin.
// Must be used after RequireAuth.
func (m *AuthMiddleware) RequireSuperAdmin(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		user, ok := GetUserFromContext(req.Context())
		if !ok {
			return m.WriteError(
				writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
		}

		if !user.SuperAdmin {
			return m.WriteError(
				writer, http.StatusForbidden, base.ErrorCodeForbidden, "Super admin access required")
		}

		return next(writer, req)
	}
}
