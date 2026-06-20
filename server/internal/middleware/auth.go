// Package middleware provides HTTP middleware for authentication and authorization.
package middleware

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/oauth"
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
	cfg         *config.Config
}

// NewAuthMiddleware creates a new authentication middleware.
func NewAuthMiddleware(authService *auth.Service, dbService db.Service, cfg *config.Config) *AuthMiddleware {
	return &AuthMiddleware{
		HandlerBase: base.NewHandlerBase(cfg),
		authService: authService,
		dbService:   dbService,
		cfg:         cfg,
	}
}

// RequireAuth is a middleware that requires a valid authentication token.
func (m *AuthMiddleware) RequireAuth(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		slog.Debug("RequireAuth middleware called", "path", req.URL.Path)

		// A request already authorized by ServiceTokenBypass carries no user
		// JWT — let it through for the handler to attribute.
		if isServiceAuthorized(req.Context()) {
			return next(writer, req)
		}

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

// RequireMCPAuth is RequireAuth specialized for the MCP resource server. It
// behaves like RequireAuth for valid credentials but, on any authentication
// failure, emits the OAuth 2.1 discovery hook required by the MCP authorization
// spec: HTTP 401 with a `WWW-Authenticate: Bearer resource_metadata="…"` header
// pointing at the protected-resource metadata, so a standard MCP client can
// discover the authorization server and start the OAuth flow.
//
// It also enforces RFC 8707 audience binding: an access token carrying an `aud`
// claim is accepted only if that audience includes the MCP resource. Tokens with
// no audience (PATs, legacy session JWTs) pass the audience gate for
// back-compat — the MCP handler's scope check still governs them.
func (m *AuthMiddleware) RequireMCPAuth(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		meta := oauth.NewMetadata(m.cfg.Server.BaseURL)

		token := extractToken(req.Request)
		if token == "" {
			return m.writeMCPChallenge(writer, meta, base.ErrorCodeNoToken, "Authorization token is required")
		}

		claims, err := m.authService.ValidateToken(req.Context(), token)
		if err != nil {
			return m.writeMCPChallenge(writer, meta, base.ErrorCodeInvalidToken, "Invalid or expired token")
		}

		// Audience binding (RFC 8707): reject a token minted for another resource.
		if !oauth.TokenHasResourceAudience(claims, meta.ResourceURL()) {
			return m.writeMCPChallenge(writer, meta, base.ErrorCodeInvalidToken,
				"Token audience does not include the MCP resource")
		}

		user, err := m.dbService.GetUser(req.Context(), claims.UserUID)
		if err != nil {
			return m.writeMCPChallenge(writer, meta, base.ErrorCodeUserNotFound, "User not found")
		}

		ctx := req.Context()
		ctx = context.WithValue(ctx, base.ContextKeyClaims, claims)
		ctx = context.WithValue(ctx, base.ContextKeyUser, user)

		return next(writer, req.WithContext(ctx))
	}
}

// writeMCPChallenge writes a 401 carrying the RFC 9728 resource_metadata pointer
// so MCP clients can discover the authorization server.
func (m *AuthMiddleware) writeMCPChallenge(
	writer http.ResponseWriter, meta oauth.Metadata, code base.ErrorCode, message string,
) error {
	writer.Header().Set("WWW-Authenticate",
		`Bearer resource_metadata="`+meta.ProtectedResourceMetadataURL()+`"`)

	return m.WriteError(writer, http.StatusUnauthorized, code, message)
}

// RequireOrgAccess is a middleware that verifies the user has access to the organization.
// Must be used after RequireAuth.
func (m *AuthMiddleware) RequireOrgAccess(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		slog.Debug("RequireOrgAccess middleware called", "path", req.URL.Path)

		// Trusted service requests are cross-org by design; skip membership.
		if isServiceAuthorized(req.Context()) {
			return next(writer, req)
		}

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

// serviceAuthContextKey marks a request authorized via a shared service token
// (see ServiceTokenBypass). RequireAuth and RequireOrgAccess treat such
// requests as already trusted and skip their user-centric checks.
type serviceAuthContextKey struct{}

// ServiceTokenBypass authorizes a request as a trusted internal service when
// its bearer token matches the secret stored in the named system parameter
// (e.g. the billing service's entitlements.service_token). It records that on
// the request context so a following RequireAuth + RequireOrgAccess become
// no-ops, then the downstream handler attributes the call and applies any
// finer-grained gating. Every other request passes through untouched and is
// authenticated normally. Place this before RequireAuth on the group.
//
// The parameter key is passed in rather than imported to avoid an import
// cycle with the handler package that owns it.
func (m *AuthMiddleware) ServiceTokenBypass(
	paramKey string,
) func(bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
		return func(writer http.ResponseWriter, req bunrouter.Request) error {
			if token := extractToken(req.Request); token != "" {
				expected := m.systemParamString(req.Context(), paramKey)
				if expected != "" && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1 {
					ctx := context.WithValue(req.Context(), serviceAuthContextKey{}, true)

					return next(writer, req.WithContext(ctx))
				}
			}

			return next(writer, req)
		}
	}
}

// isServiceAuthorized reports whether ServiceTokenBypass already authorized
// this request as a trusted internal service.
func isServiceAuthorized(ctx context.Context) bool {
	v, _ := ctx.Value(serviceAuthContextKey{}).(bool)

	return v
}

// systemParamString reads a string-valued system parameter, returning "" when
// absent or on error (callers then fall back to user authentication).
func (m *AuthMiddleware) systemParamString(ctx context.Context, key string) string {
	param, err := m.dbService.GetSystemParameter(ctx, key)
	if err != nil || param == nil {
		return ""
	}

	if v, ok := param.Value["value"].(string); ok {
		return v
	}

	return ""
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

// RequireOrgAdmin is a middleware that requires the authenticated user to be an
// admin of the organization (member with role "admin") or a super admin.
// Must be used after RequireAuth and RequireOrgAccess (it relies on the
// organization being resolved into the context).
func (m *AuthMiddleware) RequireOrgAdmin(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		user, userOK := GetUserFromContext(req.Context())
		if !userOK {
			return m.WriteError(
				writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
		}

		// Super admins are always allowed.
		if user.SuperAdmin {
			return next(writer, req)
		}

		org, orgOK := GetOrganizationFromContext(req.Context())
		if !orgOK {
			return m.WriteError(
				writer, http.StatusForbidden, base.ErrorCodeForbidden, "Organization context missing")
		}

		member, err := m.dbService.GetMemberByUserAndOrg(req.Context(), user.UID, org.UID)
		if err != nil {
			return m.WriteError(
				writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
		}

		if member.Role != models.MemberRoleAdmin {
			return m.WriteError(
				writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin access required")
		}

		return next(writer, req)
	}
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
