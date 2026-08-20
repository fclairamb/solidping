// Package middleware provides HTTP middleware for authentication and authorization.
package middleware

import (
	"bytes"
	"context"
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/oauth"
	"github.com/fclairamb/solidping/server/internal/servicesig"
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
func (m *AuthMiddleware) RequireAuth(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		slog.DebugContext(req.Context(), "RequireAuth middleware called", "path", req.URL.Path)

		// A request already authorized by ServiceTokenBypass carries no user
		// JWT — let it through for the handler to attribute.
		if isServiceAuthorized(req.Context()) {
			return next(writer, req)
		}

		token := extractToken(req)
		if token == "" {
			return m.WriteError(
				writer, http.StatusUnauthorized, base.ErrorCodeNoToken, "Authorization token is required")
		}

		claims, err := m.authService.ValidateToken(req.Context(), token)
		if err != nil {
			return m.WriteErrorErr(
				writer, req, http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Invalid or expired token", err)
		}

		// Load user
		user, err := m.dbService.GetUser(req.Context(), claims.UserUID)
		if err != nil {
			return m.WriteErrorErr(
				writer, req, http.StatusUnauthorized, base.ErrorCodeUserNotFound, "User not found", err)
		}

		// Add claims and user to context
		ctx := req.Context()
		ctx = context.WithValue(ctx, base.ContextKeyClaims, claims)
		ctx = context.WithValue(ctx, base.ContextKeyUser, user)
		setSentryIdentity(ctx, claims)

		return next(writer, req.WithContext(ctx))
	}
}

// setSentryIdentity attaches the authenticated principal to the request-scoped
// Sentry hub. It lives here, not in SentryMiddleware, because SentryMiddleware
// runs before any authentication: the claims only exist from this point on.
//
// The hub was installed by SentryMiddleware and is the same object the whole
// request reports on, so mutating its scope here is what makes every later
// event — a 500 from a handler, a panic caught by Recoverer — carry the user
// and organization.
func setSentryIdentity(ctx context.Context, claims *auth.Claims) {
	if claims == nil {
		return
	}

	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		return
	}

	hub.Scope().SetUser(sentry.User{ID: claims.UserUID})

	if claims.OrgSlug != "" {
		hub.Scope().SetTag("organization", claims.OrgSlug)
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
func (m *AuthMiddleware) RequireMCPAuth(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		meta := oauth.NewMetadata(m.cfg.Server.BaseURL)

		token := extractToken(req)
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
		setSentryIdentity(ctx, claims)

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
func (m *AuthMiddleware) RequireOrgAccess(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		slog.DebugContext(req.Context(), "RequireOrgAccess middleware called", "path", req.URL.Path)

		// Trusted service requests are cross-org by design; skip membership.
		if isServiceAuthorized(req.Context()) {
			return next(writer, req)
		}

		orgSlug := httpx.Param(req, "org")
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

		// Delegate the actual rule to the shared authorizer so REST and the
		// realtime WS handshake can never diverge (spec 2026-07-15-01).
		org, denial := auth.AuthorizeOrgAccess(req.Context(), m.dbService, claims, user, orgSlug)
		switch denial {
		case auth.OrgAccessGranted:
			// fall through
		case auth.OrgAccessOrgNotFound:
			return m.WriteError(
				writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
		case auth.OrgAccessDenied:
			return m.WriteError(
				writer, http.StatusForbidden, base.ErrorCodeForbidden, "Access to this organization is denied")
		default:
			return m.WriteError(
				writer, http.StatusForbidden, base.ErrorCodeForbidden, "Access to this organization is denied")
		}

		// Add organization to context
		ctx := context.WithValue(req.Context(), base.ContextKeyOrganization, org)

		slog.DebugContext(req.Context(), "RequireOrgAccess: Access granted", "orgSlug", org.Slug, "userUID", user.UID)

		return next(writer, req.WithContext(ctx))
	}
}

// serviceAuthContextKey marks a request authorized via a shared service token
// (see ServiceTokenBypass). RequireAuth and RequireOrgAccess treat such
// requests as already trusted and skip their user-centric checks.
type serviceAuthContextKey struct{}

// ServiceSignature authorizes a request as a trusted internal service when it
// carries a valid X-SP-Signature / X-SP-Timestamp / X-SP-Key-Id triple signed
// with one of the keys held in the named system parameter (see
// internal/servicesig for the scheme).
//
// It is the replacement for ServiceTokenBypass's static bearer: a signature is
// bound to the timestamp, method, path and exact body bytes, so a captured
// request can neither be replayed outside the skew window nor resent with a
// different payload.
//
// Behavior:
//
//   - No signature header at all → pass through untouched, so the legacy
//     bearer path and ordinary user authentication still apply. This is what
//     makes the migration a parameter flip rather than a coordinated restart.
//   - Valid signature → set the same serviceAuthContextKey ServiceTokenBypass
//     sets, so the downstream RequireAuth + RequireOrgAccess become no-ops
//     (billing writes cross-org by design) and the handler attributes the call.
//   - Signature present but invalid → one generic 401. The reason (unknown key
//     id, skew, mismatch) is logged, never returned.
//
// Verifying the body hash means the raw bytes must be read here and handed
// back to the handler, so the body is buffered and restored.
//
// Place this before ServiceTokenBypass and RequireAuth on the group.
func (m *AuthMiddleware) ServiceSignature(
	keysParamKey string,
) func(httpx.HandlerFunc) httpx.HandlerFunc {
	return func(next httpx.HandlerFunc) httpx.HandlerFunc {
		return func(writer http.ResponseWriter, req *http.Request) error {
			if !servicesig.HasSignature(req) {
				return next(writer, req)
			}

			keys, err := servicesig.LoadKeySet(req.Context(), m.dbService, keysParamKey)
			if err != nil {
				slog.ErrorContext(req.Context(), "service signature: cannot load signing keys",
					"param", keysParamKey, "error", err)

				return m.writeServiceAuthDenied(writer)
			}

			body, err := readAndRestoreBody(req)
			if err != nil {
				slog.WarnContext(req.Context(), "service signature: cannot read request body",
					"path", req.URL.Path, "error", err)

				return m.writeServiceAuthDenied(writer)
			}

			key, err := servicesig.VerifyRequest(req, keys, body, time.Now())
			if err != nil {
				slog.WarnContext(req.Context(), "service signature rejected",
					"path", req.URL.Path, "keyId", req.Header.Get(servicesig.HeaderKeyID),
					"reason", err.Error())

				return m.writeServiceAuthDenied(writer)
			}

			slog.DebugContext(req.Context(), "service signature accepted",
				"path", req.URL.Path, "keyId", key.ID)

			ctx := context.WithValue(req.Context(), serviceAuthContextKey{}, true)

			return next(writer, req.WithContext(ctx))
		}
	}
}

// writeServiceAuthDenied is the single generic 401 every signature rejection
// collapses into — the specific reason only ever reaches the log.
func (m *AuthMiddleware) writeServiceAuthDenied(writer http.ResponseWriter) error {
	return m.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
}

// readAndRestoreBody consumes the request body so it can be hashed, then puts
// it back for the handler to decode.
func readAndRestoreBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}

	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()

	if err != nil {
		return nil, err
	}

	req.Body = io.NopCloser(bytes.NewReader(body))

	return body, nil
}

// ServiceTokenBypass authorizes a request as a trusted internal service when
// its bearer token matches the secret stored in the named system parameter
// (e.g. the billing service's entitlements.service_token). It records that on
// the request context so a following RequireAuth + RequireOrgAccess become
// no-ops, then the downstream handler attributes the call and applies any
// finer-grained gating. Every other request passes through untouched and is
// authenticated normally. Place this before RequireAuth on the group.
//
// This is the LEGACY path, superseded by ServiceSignature. It is gated by the
// boolean system parameter named by allowLegacyParamKey (default true when
// unset, so nothing breaks before the billing service starts signing) and logs
// a deprecation warning on every use, so operators can watch the legacy channel
// go quiet before flipping the parameter to false. Once it is off everywhere,
// this middleware and its parameter can be deleted.
//
// The parameter keys are passed in rather than imported to avoid an import
// cycle with the handler package that owns them.
func (m *AuthMiddleware) ServiceTokenBypass(
	paramKey, allowLegacyParamKey string,
) func(httpx.HandlerFunc) httpx.HandlerFunc {
	return func(next httpx.HandlerFunc) httpx.HandlerFunc {
		return func(writer http.ResponseWriter, req *http.Request) error {
			// A signature already authorized this request; nothing to do.
			if isServiceAuthorized(req.Context()) {
				return next(writer, req)
			}

			if token := extractToken(req); token != "" {
				expected := m.systemParamString(req.Context(), paramKey)
				if expected != "" && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1 {
					if !m.systemParamBool(req.Context(), allowLegacyParamKey, true) {
						slog.WarnContext(req.Context(),
							"legacy service token presented but disabled; falling through to normal auth",
							"path", req.URL.Path, "param", allowLegacyParamKey)

						return next(writer, req)
					}

					slog.WarnContext(req.Context(),
						"DEPRECATED: request authorized by the static service token; "+
							"the caller should sign it instead (X-SP-Signature)",
						"path", req.URL.Path, "method", req.Method,
						"remoteAddr", req.RemoteAddr, "userAgent", req.UserAgent())

					ctx := context.WithValue(req.Context(), serviceAuthContextKey{}, true)

					return next(writer, req.WithContext(ctx))
				}
			}

			return next(writer, req)
		}
	}
}

// isServiceAuthorized reports whether ServiceSignature or ServiceTokenBypass
// already authorized this request as a trusted internal service.
func isServiceAuthorized(ctx context.Context) bool {
	v, _ := ctx.Value(serviceAuthContextKey{}).(bool)

	return v
}

// IsServiceAuthorized is the exported form of isServiceAuthorized, for handlers
// that need to attribute a call to the trusted service principal.
func IsServiceAuthorized(ctx context.Context) bool {
	return isServiceAuthorized(ctx)
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

// systemParamBool reads a boolean-valued system parameter, returning def when
// absent, unreadable or unparsable. Both a JSON bool and a stringly-typed
// "true"/"false" are accepted, matching how these parameters get seeded.
func (m *AuthMiddleware) systemParamBool(ctx context.Context, key string, def bool) bool {
	param, err := m.dbService.GetSystemParameter(ctx, key)
	if err != nil || param == nil {
		return def
	}

	switch v := param.Value["value"].(type) {
	case bool:
		return v
	case string:
		parsed, parseErr := strconv.ParseBool(v)
		if parseErr != nil {
			return def
		}

		return parsed
	default:
		return def
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

// RequireOrgAdmin is a middleware that requires the authenticated user to hold
// at least the admin role in the organization (so an owner passes too) or to be
// a super admin. Must be used after RequireAuth and RequireOrgAccess (it relies
// on the organization being resolved into the context).
func (m *AuthMiddleware) RequireOrgAdmin(next httpx.HandlerFunc) httpx.HandlerFunc {
	return m.requireOrgRole(models.MemberRoleAdmin, "Admin access required")(next)
}

// RequireOrgOwner is a middleware that requires the authenticated user to be an
// owner of the organization (or a super admin). It guards the irreversible
// org-lifecycle operations — today, deleting the organization.
// Must be used after RequireAuth and RequireOrgAccess.
func (m *AuthMiddleware) RequireOrgOwner(next httpx.HandlerFunc) httpx.HandlerFunc {
	return m.requireOrgRole(models.MemberRoleOwner, "Owner access required")(next)
}

// requireOrgRole builds a middleware enforcing a minimum org role. The check is
// hierarchical (models.MemberRole.AtLeast), never an equality test: with owner
// sitting above admin, an equality test would lock owners out of every admin
// surface.
func (m *AuthMiddleware) requireOrgRole(
	minRole models.MemberRole, denyMessage string,
) func(httpx.HandlerFunc) httpx.HandlerFunc {
	return func(next httpx.HandlerFunc) httpx.HandlerFunc {
		return func(writer http.ResponseWriter, req *http.Request) error {
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
					writer, http.StatusForbidden, base.ErrorCodeForbidden, denyMessage)
			}

			if !member.Role.AtLeast(minRole) {
				return m.WriteError(
					writer, http.StatusForbidden, base.ErrorCodeForbidden, denyMessage)
			}

			return next(writer, req)
		}
	}
}

// RequireSuperAdmin is a middleware that requires the user to be a super admin.
// Must be used after RequireAuth.
func (m *AuthMiddleware) RequireSuperAdmin(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
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
