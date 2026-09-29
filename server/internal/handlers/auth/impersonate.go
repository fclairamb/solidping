package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// ImpersonationTTL is the fixed lifetime of an impersonation token (spec
// 2026-09-29-03). There is no refresh: when it runs out the admin is back on
// their own session, so a forgotten tab cannot keep acting as someone else.
const ImpersonationTTL = 30 * time.Minute

// Impersonation refusals. Each is its own error so the handler maps it to the
// right status and so the tests can tell one refusal from another.
var (
	// ErrImpersonationDisabled: auth.impersonation_enabled is false. Mapped to
	// 404, as if the endpoint did not exist.
	ErrImpersonationDisabled = errors.New("impersonation is disabled")
	// ErrImpersonationNotSuperAdmin: the actor is not a super admin. The route
	// is already behind RequireSuperAdmin; this is the service's own check.
	ErrImpersonationNotSuperAdmin = errors.New("only a super admin can impersonate")
	// ErrImpersonationTargetSuperAdmin: no lateral moves between super admins.
	ErrImpersonationTargetSuperAdmin = errors.New("a super admin cannot be impersonated")
	// ErrImpersonationTargetSelf: impersonating yourself is meaningless.
	ErrImpersonationTargetSelf = errors.New("you cannot impersonate yourself")
	// ErrImpersonationChained: the caller's own credential is an impersonation
	// token.
	ErrImpersonationChained = errors.New("an impersonation session cannot start another impersonation")
	// ErrImpersonationTargetDemo: the shared public-demo principal has nothing
	// to reproduce and is guarded by its own rules.
	ErrImpersonationTargetDemo = errors.New("the shared demo account cannot be impersonated")
	// ErrImpersonationTargetNotMember: the target does not belong to the
	// requested org, or belongs to no org at all. Mapped to 404.
	ErrImpersonationTargetNotMember = errors.New("the user is not a member of this organization")
	// ErrImpersonationForbidden: something an impersonation token must never
	// do (mint a session, change a credential). Mapped to 403
	// IMPERSONATION_FORBIDDEN.
	ErrImpersonationForbidden = errors.New("not allowed while impersonating a user")
)

// ImpersonationForbiddenMessage is the human half of every
// IMPERSONATION_FORBIDDEN response.
const ImpersonationForbiddenMessage = "You are viewing the dashboard as another user. " +
	"Their credentials, tokens and sessions cannot be changed from an impersonation session."

// ImpersonateRequest is the body of POST /api/v1/system/users/:uid/impersonate.
type ImpersonateRequest struct {
	// OrgSlug is the organization to view the dashboard in. Optional: the
	// target's default organization is used when empty.
	OrgSlug string `json:"orgSlug,omitempty"`
}

// ImpersonationInfo describes the impersonation behind the caller's token on
// /auth/me.
type ImpersonationInfo struct {
	// ImpersonatorUID is the super admin acting as the user.
	ImpersonatorUID string `json:"impersonatorUid"`
	// ImpersonatorEmail is their email, for the banner. Empty if the admin's
	// row can no longer be read.
	ImpersonatorEmail string `json:"impersonatorEmail,omitempty"`
	// ExpiresAt is when the impersonation token stops working.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// ImpersonationInfoFor returns the impersonation behind claims, or nil for an
// ordinary token.
func (s *Service) ImpersonationInfoFor(ctx context.Context, claims *Claims) *ImpersonationInfo {
	if !claims.IsImpersonation() {
		return nil
	}

	info := &ImpersonationInfo{ImpersonatorUID: claims.ImpersonatedBy}

	if claims.ExpiresAt != nil {
		expiresAt := claims.ExpiresAt.UTC()
		info.ExpiresAt = &expiresAt
	}

	if admin, err := s.db.GetUser(ctx, claims.ImpersonatedBy); err == nil && admin != nil {
		info.ImpersonatorEmail = admin.Email
	}

	return info
}

// impersonatorFromContext returns the UID of the admin behind the current
// request's impersonation token, or "" when the request is an ordinary one.
// Both signals RequireAuth puts on the context are read, so a caller that only
// carries one of them (the audit actor, or the claims) is still recognized.
func impersonatorFromContext(ctx context.Context) string {
	if uid := audit.ImpersonatorFromContext(ctx); uid != "" {
		return uid
	}

	if claims, ok := ctx.Value(base.ContextKeyClaims).(*Claims); ok && claims.IsImpersonation() {
		return claims.ImpersonatedBy
	}

	return ""
}

// Impersonate mints a short-lived access token that acts as targetUID in
// orgSlug, on behalf of the super admin actorUID (spec 2026-09-29-03).
//
// Modeled on SwitchOrg, with the differences that make it safe:
//   - the role is the target's REAL membership role, never RoleSuperAdmin, so
//     every permission check downstream runs as the target;
//   - no refresh token and no user_tokens row: the target's session list is
//     untouched and the token dies after ImpersonationTTL;
//   - the claims carry ImpersonatedBy, which the middleware uses to deny super
//     admin routes and the credential-changing surface.
//
// The admin's own authentication is what gates this, so a target with 2FA
// enabled can be impersonated: their 2FA still protects their real sessions.
func (s *Service) Impersonate(
	ctx context.Context, actorUID, targetUID, orgSlug string, authContext Context,
) (*LoginResponse, error) {
	if !s.fullCfg.Auth.ImpersonationEnabled {
		return nil, ErrImpersonationDisabled
	}

	if impersonatorFromContext(ctx) != "" {
		return nil, ErrImpersonationChained
	}

	actor, err := s.db.GetUser(ctx, actorUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrImpersonationNotSuperAdmin
		}

		return nil, err
	}

	if !actor.SuperAdmin {
		return nil, ErrImpersonationNotSuperAdmin
	}

	if targetUID == actorUID {
		return nil, ErrImpersonationTargetSelf
	}

	target, err := s.db.GetUser(ctx, targetUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	if target.DeletedAt != nil {
		return nil, ErrUserNotFound
	}

	if target.SuperAdmin {
		return nil, ErrImpersonationTargetSuperAdmin
	}

	if target.Demo {
		return nil, ErrImpersonationTargetDemo
	}

	org, membership, err := s.impersonationMembership(ctx, target.UID, orgSlug)
	if err != nil {
		return nil, err
	}

	role := string(membership.Role)
	now := time.Now()
	expiresAt := now.Add(ImpersonationTTL)

	accessToken, err := s.generateImpersonationToken(target, org.Slug, role, actor.UID, now, expiresAt)
	if err != nil {
		return nil, err
	}

	s.recordImpersonationStarted(ctx, actor, target, org, role, expiresAt, authContext)

	return &LoginResponse{
		AccessToken:  accessToken,
		ExpiresIn:    int(ImpersonationTTL.Seconds()),
		TokenType:    tokenTypeBearer,
		User:         newUserInfo(target, role),
		Organization: newOrganizationInfo(org),
	}, nil
}

// impersonationMembership resolves the org to impersonate in and the target's
// membership there. An empty orgSlug picks the target's first membership.
func (s *Service) impersonationMembership(
	ctx context.Context, targetUID, orgSlug string,
) (*models.Organization, *models.OrganizationMember, error) {
	var (
		org *models.Organization
		err error
	)

	if orgSlug == "" {
		members, listErr := s.db.ListMembersByUser(ctx, targetUID)
		if listErr != nil {
			return nil, nil, listErr
		}

		if len(members) == 0 {
			return nil, nil, ErrImpersonationTargetNotMember
		}

		org, err = s.db.GetOrganization(ctx, members[0].OrganizationUID)
	} else {
		org, err = s.db.GetOrganizationBySlug(ctx, orgSlug)
	}

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrImpersonationTargetNotMember
		}

		return nil, nil, err
	}

	membership, err := s.db.GetMemberByUserAndOrg(ctx, targetUID, org.UID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrImpersonationTargetNotMember
		}

		return nil, nil, err
	}

	return org, membership, nil
}

// generateImpersonationToken mints the impersonation JWT. It is deliberately
// separate from generateAccessToken: the expiry is ImpersonationTTL rather
// than the configured access-token lifetime, RefreshUID is always empty (no
// session row backs it), and ImpersonatedBy is set.
func (s *Service) generateImpersonationToken(
	target *models.User, orgSlug, role, actorUID string, now, expiresAt time.Time,
) (string, error) {
	claims := &Claims{
		UserUID:        target.UID,
		OrgSlug:        orgSlug,
		Role:           role,
		Demo:           target.Demo,
		ImpersonatedBy: actorUID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    jwtIssuer,
			ID:        uuid.New().String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString([]byte(s.fullCfg.Auth.JWTSecret))
}

// recordImpersonationStarted writes auth.impersonation_started in the target's
// organization. The actor is the ADMIN (with the request provenance); the
// target is the impersonated user.
func (s *Service) recordImpersonationStarted(
	ctx context.Context,
	actor, target *models.User,
	org *models.Organization,
	role string,
	expiresAt time.Time,
	authContext Context,
) {
	audit.Record(auditActorCtx(ctx, actor.UID, authContext), s.db, org.UID,
		models.EventTypeAuthImpersonationStarted,
		audit.Target{Type: auditTargetUser, UID: target.UID, Name: target.Email},
		models.JSONMap{
			auditKeyMethod:            AuthMethodImpersonate,
			auditKeyEmail:             target.Email,
			auditKeyRole:              role,
			auditKeyImpersonatorEmail: actor.Email,
			auditKeyExpiresAt:         expiresAt.UTC().Format(time.RFC3339),
		})
}

// Impersonate handles POST /api/v1/system/users/:uid/impersonate. The route
// sits in the super-admin system group (RequireAuth + RequireSuperAdmin); the
// service re-checks everything anyway.
func (h *Handler) Impersonate(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	var body ImpersonateRequest
	if req.Body != nil {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
				{Name: fieldBody, Message: msgInvalidJSON},
			})
		}
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: base.ExtractRemoteAddr(req),
	}

	resp, err := h.svc.Impersonate(req.Context(), claims.UserUID, httpx.Param(req, "uid"), body.OrgSlug, authContext)
	if err != nil {
		return h.handleImpersonateError(writer, req, err)
	}

	// Deliberately NO setAccessTokenCookie: the cookie is the admin's own
	// session, and overwriting it would strand them as the target on every
	// cookie-authenticated surface. The dashboard carries the token itself.
	return h.WriteJSON(writer, http.StatusOK, resp)
}

func (h *Handler) handleImpersonateError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrImpersonationDisabled):
		return h.WriteErrorErr(writer, req, http.StatusNotFound, base.ErrorCodeNotFound, "Not found", err)
	case errors.Is(err, ErrUserNotFound):
		return h.WriteErrorErr(writer, req, http.StatusNotFound, base.ErrorCodeUserNotFound, "User not found", err)
	case errors.Is(err, ErrImpersonationTargetNotMember):
		return h.WriteErrorErr(writer, req, http.StatusNotFound, base.ErrorCodeNotFound, err.Error(), err)
	case errors.Is(err, ErrImpersonationNotSuperAdmin):
		return h.WriteErrorErr(writer, req, http.StatusForbidden, base.ErrorCodeForbidden, err.Error(), err)
	case errors.Is(err, ErrImpersonationTargetSuperAdmin),
		errors.Is(err, ErrImpersonationTargetSelf),
		errors.Is(err, ErrImpersonationChained),
		errors.Is(err, ErrImpersonationTargetDemo):
		return h.WriteErrorErr(writer, req, http.StatusForbidden, base.ErrorCodeImpersonationForbidden, err.Error(), err)
	default:
		return h.WriteInternalError(writer, req, err)
	}
}
