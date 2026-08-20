// Package auth provides authentication services and handlers.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/fclairamb/solidping/server/internal/activation"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/orgslug"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// State entry value keys shared between producers and consumers of
// password-reset and counter entries; centralized so a typo can't drift
// the wire format silently.
const (
	stateValueKeyUserUID = "userUid"
	stateValueKeyCount   = "count"
)

// emailJobConfig mirrors the JSON shape of jobtypes.EmailJobConfig. We
// duplicate it here to avoid an import cycle (auth → jobtypes →
// notifications → slack → auth). Keep the JSON tags in sync with the
// receiver struct in jobs/jobtypes/job_email.go.
type emailJobConfig struct {
	To           []string `json:"to"`
	Subject      string   `json:"subject"`
	Template     string   `json:"template,omitempty"`
	TemplateData any      `json:"templateData,omitempty"`
}

// Internal property/key constants used in JSONMap fields and OAuth flows.
const (
	keyState       = "state"
	keyToken       = "token"
	keyEmail       = "email"
	keyName        = "name"
	keyMethod      = "method"
	keyCreatedWith = "created_with"
	keyScopes      = "scopes"

	tokenTypeBearer = "Bearer"
	jwtIssuer       = "solidping"
	durationLabel24 = "24h"
	appNameDash0    = "dash0"

	// maxActiveSessions caps the number of active `refresh`-type user_tokens
	// rows (dashboard sessions) per user. Enforced at every refresh-token
	// mint site (password/OAuth/passkey/2FA login, registration, invite
	// acceptance, switch-org) — required hygiene now that sliding expiry
	// means an active session never dies on its own.
	maxActiveSessions = 10
)

// Service errors.
var (
	ErrInvalidCredentials      = errors.New("invalid credentials")
	ErrUserNotFound            = errors.New("user not found")
	ErrInvalidToken            = errors.New("invalid token")
	ErrTokenExpired            = errors.New("token expired")
	ErrOrganizationNotFound    = errors.New("organization not found")
	ErrTokenNotFound           = errors.New("token not found")
	ErrUnexpectedSigningMethod = errors.New("unexpected signing method")
	ErrRegistrationDisabled    = errors.New("registration is not enabled")
	ErrEmailNotAllowed         = errors.New("email does not match allowed pattern")
	ErrRegistrationExpired     = errors.New("registration link has expired")
	ErrEmailAlreadyTaken       = errors.New("email is already registered")
	ErrInvitationNotFound      = errors.New("invitation not found")
	ErrInvitationExpired       = errors.New("invitation has expired")
	ErrEmailMismatch           = errors.New("email does not match invitation")
	ErrOrgSlugTaken            = errors.New("organization slug is already taken")
	ErrInvalidOrgSlug          = errors.New("invalid organization slug")
	ErrPasswordResetExpired    = errors.New("password reset link has expired or is invalid")
	// ErrInvalidCurrentPassword is returned by ChangePassword when the caller
	// supplies a currentPassword that does not match the stored hash. Kept
	// distinct from ErrInvalidCredentials so the handler can emit the
	// field-specific INVALID_CURRENT_PASSWORD code.
	ErrInvalidCurrentPassword = errors.New("current password is incorrect")
	ErrInvalid2FACode         = errors.New("invalid 2FA code")
	ErrInvalidRecoveryCode    = errors.New("invalid recovery code")
	ErrTwoFAAlreadyEnabled    = errors.New("2FA is already enabled")
	ErrTwoFANotEnabled        = errors.New("2FA is not enabled")
	// ErrRateLimited is returned when a client exceeds the per-endpoint
	// rate limit. The handler maps this to HTTP 429.
	ErrRateLimited = errors.New("rate limit exceeded")
	// ErrInvalidAutoJoinRegex is returned when an admin tries to set a
	// dangerously broad registration email pattern.
	ErrInvalidAutoJoinRegex = errors.New("invalid auto-join regex pattern")
	// ErrInvalidEscalationPolicy is returned when the default-escalation-policy
	// UID does not reference a policy that exists in this org.
	ErrInvalidEscalationPolicy = errors.New("escalation policy not found in this organization")
	// ErrAlreadyAMember is returned when a user tries to request membership
	// in an org they already belong to.
	ErrAlreadyAMember = errors.New("already a member of this organization")
	// ErrRequestPending is returned when a user re-requests membership
	// while a previous request is still pending.
	ErrRequestPending = errors.New("a membership request is already pending")
	// ErrRequestNotFound is returned when a membership request lookup fails.
	ErrRequestNotFound = errors.New("membership request not found")
	// ErrRequestCooldownActive is returned when a user re-requests
	// membership during the rejection cooldown window.
	ErrRequestCooldownActive = errors.New("membership request cooldown active")
)

// Service provides authentication business logic.
type Service struct {
	db db.Service
	// cfg is a boot-time-FROZEN value copy of config.AuthConfig, taken in
	// NewService BEFORE InitializeSystemConfig applies the system-parameter
	// overlay (see server/main.go boot order). Read it ONLY for fields the
	// overlay never mutates — AccessTokenExpiry and RefreshTokenExpiry. Every
	// field the overlay CAN mutate (JWTSecret, RegistrationEmailPattern,
	// SessionMaxDuration) must be read live via fullCfg.Auth instead, or it
	// stays frozen at its pre-overlay (usually empty/default) value forever.
	cfg config.AuthConfig
	// fullCfg is the LIVE shared *config.Config pointer. The systemconfig
	// overlay and ensureJWTSecret mutate fullCfg.Auth after NewService, so
	// reads through it see the post-overlay values.
	fullCfg      *config.Config
	jobsSvc      jobsvc.Service
	entitlements EntitlementsChecker
	patCache     map[string]*cachedPATClaims
	cacheMux     sync.RWMutex
}

// EntitlementsChecker is the slice of the entitlements service the auth
// package needs. Defined as an interface so we can stub it in tests
// without dragging the full service in.
type EntitlementsChecker interface {
	// CheckMembership returns a non-nil error (wrapping
	// entitlements.ErrEntitlementExceeded) when adding another member to
	// orgUID would breach the MaxUsers cap.
	CheckMembership(ctx context.Context, orgUID string) error
}

type cachedPATClaims struct {
	claims    *Claims
	expiresAt time.Time
}

// Claims represents the JWT token claims.
type Claims struct {
	UserUID string `json:"userUid"`
	OrgSlug string `json:"orgSlug"`
	Role    string `json:"role,omitempty"`
	// Scopes lists fine-grained capabilities granted to this credential.
	// Empty means "no scope restrictions" — the credential is treated as a
	// full user session (back-compat for dashboard JWTs that pre-date scopes).
	// Populated values gate access to specific subsystems; see e.g. the
	// "mcp" / "mcp:read" scopes consumed by the MCP handler.
	Scopes []string `json:"scopes,omitempty"`
	// RefreshUID is the user_tokens.uid of the refresh-token row that issued
	// this access token. Set on login and refresh so the sessions listing can
	// flag the caller's own row (isCurrent) and "sign out other sessions" can
	// spare it. Empty for PAT-validated claims and 2FA temp tokens — neither
	// is minted from a refresh-token row.
	RefreshUID string `json:"refreshUid,omitempty"`
	jwt.RegisteredClaims
}

// RoleSuperAdmin is the role value for super administrators.
const RoleSuperAdmin = "superadmin"

// IsSuperAdmin returns true if the claims indicate a super admin.
func (c *Claims) IsSuperAdmin() bool {
	return c.Role == RoleSuperAdmin
}

// HasOrgRole reports whether the claims carry at least the given org role for
// the org the token is scoped to. Super admins always pass.
//
// Use this instead of `claims.Role == "admin"`: with the owner role above admin
// (spec 2026-08-08-11) an equality check silently locks owners out of every
// admin surface, and enumerating `|| "owner"` at each call site is exactly the
// per-site drift the role hierarchy exists to prevent.
func (c *Claims) HasOrgRole(minRole models.MemberRole) bool {
	if c == nil {
		return false
	}

	if c.IsSuperAdmin() {
		return true
	}

	return models.MemberRole(c.Role).AtLeast(minRole)
}

// Context contains metadata about the authentication request.
type Context struct {
	UserAgent  string `json:"userAgent,omitempty"`
	RemoteAddr string `json:"remoteAddr,omitempty"`
}

// ToMap converts Context to a map for storage.
func (c *Context) ToMap() map[string]any {
	return map[string]any{
		"userAgent":  c.UserAgent,
		"remoteAddr": c.RemoteAddr,
	}
}

// UserInfo represents user information returned in responses.
type UserInfo struct {
	UID       string `json:"uid"`
	Email     string `json:"email"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
	Role      string `json:"role"`
}

// OrganizationInfo represents organization information.
type OrganizationInfo struct {
	UID  string `json:"uid"`
	Slug string `json:"slug"`
	Name string `json:"name,omitempty"`
	// LogoURL is the org's logo (absolute http(s) URL, or /pub/org-logos/<uid>
	// for an uploaded one). Null means "no logo" and the client falls back to
	// the product default.
	LogoURL *string `json:"logoUrl"`
}

// newOrganizationInfo builds the org payload every login-shaped response
// carries. Funneling the construction through one function is what keeps a new
// org field (like logoUrl) from reaching some responses and not others.
func newOrganizationInfo(org *models.Organization) *OrganizationInfo {
	if org == nil {
		return nil
	}

	return &OrganizationInfo{
		UID:     org.UID,
		Slug:    org.Slug,
		Name:    org.Name,
		LogoURL: org.LogoURL,
	}
}

// OrganizationSummary represents a brief organization entry for listing.
type OrganizationSummary struct {
	Slug    string  `json:"slug"`
	Name    string  `json:"name,omitempty"`
	LogoURL *string `json:"logoUrl"`
	Role    string  `json:"role"`
}

// LoginAction describes how the frontend should handle the login result.
type LoginAction string

const (
	// LoginActionDefault means normal login to the requested or resolved org.
	LoginActionDefault LoginAction = ""
	// LoginActionOrgRedirect means the user was redirected to their only available org.
	LoginActionOrgRedirect LoginAction = "orgRedirect"
	// LoginActionOrgChoice means the user has multiple orgs and must choose.
	LoginActionOrgChoice LoginAction = "orgChoice"
	// LoginActionNoOrg means the user has no organizations.
	LoginActionNoOrg LoginAction = "noOrg"
)

// LoginResponse contains the response data for a successful login.
type LoginResponse struct {
	AccessToken   string                `json:"accessToken,omitempty"`
	RefreshToken  string                `json:"refreshToken,omitempty"`
	ExpiresIn     int                   `json:"expiresIn,omitempty"`
	TokenType     string                `json:"tokenType,omitempty"`
	User          *UserInfo             `json:"user,omitempty"`
	Organization  *OrganizationInfo     `json:"organization,omitempty"`
	Organizations []OrganizationSummary `json:"organizations,omitempty"`
	LoginAction   LoginAction           `json:"loginAction,omitempty"`
	Requires2FA   bool                  `json:"requires2Fa,omitempty"`
	TempToken     string                `json:"tempToken,omitempty"`
}

// TwoFAClaims represents JWT claims for the temporary 2FA token.
type TwoFAClaims struct {
	UserUID string `json:"userUid"`
	OrgSlug string `json:"orgSlug"`
	Role    string `json:"role"`
	Purpose string `json:"purpose"`
	jwt.RegisteredClaims
}

// Setup2FAResponse contains the TOTP setup data.
type Setup2FAResponse struct {
	URI    string `json:"uri"`
	Secret string `json:"secret"`
}

// Confirm2FAResponse contains the recovery codes after enabling 2FA.
type Confirm2FAResponse struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

// Verify2FARequest contains the request data for verifying a 2FA code.
type Verify2FARequest struct {
	Code string `json:"code"`
}

// Recovery2FARequest contains the request data for using a recovery code.
type Recovery2FARequest struct {
	RecoveryCode string `json:"recoveryCode"`
}

// Disable2FARequest contains the request data for disabling 2FA.
type Disable2FARequest struct {
	Code string `json:"code"`
}

// MeResponse contains the current user's information.
type MeResponse struct {
	User                      *UserInfo                  `json:"user"`
	Organization              *OrganizationInfo          `json:"organization,omitempty"`
	Organizations             []OrganizationSummary      `json:"organizations"`
	TOTPEnabled               bool                       `json:"totpEnabled"`
	PasskeyCount              int                        `json:"passkeyCount"`
	HasPassword               bool                       `json:"hasPassword"`
	PendingMembershipRequests []MembershipRequestSummary `json:"pendingMembershipRequests,omitempty"`
}

// MembershipRequestSummary is the compact form returned on /auth/me and
// /auth/membership-requests for the requester to render their queue.
type MembershipRequestSummary struct {
	UID            string                         `json:"uid"`
	Organization   OrganizationRef                `json:"organization"`
	Status         models.MembershipRequestStatus `json:"status"`
	Message        string                         `json:"message,omitempty"`
	DecisionReason string                         `json:"decisionReason,omitempty"`
	CreatedAt      time.Time                      `json:"createdAt"`
	DecidedAt      *time.Time                     `json:"decidedAt,omitempty"`
}

// OrganizationRef is a slug+name handle to an org.
type OrganizationRef struct {
	UID  string `json:"uid"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// UpdateProfileRequest contains the fields that can be updated on the user profile.
type UpdateProfileRequest struct {
	Name *string `json:"name"`
}

// LogoutResponse contains the response data for a logout operation.
type LogoutResponse struct {
	Success       bool `json:"success"`
	TokensDeleted int  `json:"tokensDeleted"`
}

// TokenInfo represents a user token for listing.
type TokenInfo struct {
	UID       string    `json:"uid"`
	Name      string    `json:"name,omitempty"`
	Type      string    `json:"type"`
	OrgSlug   string    `json:"orgSlug,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// LastUsedAt is kept for back-compat with existing PAT consumers.
	// LastActiveAt duplicates the same value under the name the sessions UI
	// expects; both are populated for every token type.
	LastUsedAt   *time.Time `json:"lastUsedAt,omitempty"`
	LastActiveAt *time.Time `json:"lastActiveAt,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	// IsCurrent is set on the row that issued the caller's own access token —
	// its uid matches the caller's Claims.RefreshUID. That covers both a
	// session `refresh` row (the dashboard's "this device") and an
	// `oauth_refresh` grant (an MCP client / CLI identifying the grant it
	// rides on). Always false for `pat` rows, which never back a RefreshUID.
	IsCurrent bool `json:"isCurrent,omitempty"`
	// CreatedWith surfaces the login-method forensics recorded at token
	// creation (properties.created_with) as camelCase fields. Nil when the
	// row predates this metadata or carries none (e.g. PATs).
	CreatedWith *TokenCreatedWith      `json:"createdWith,omitempty"`
	Properties  map[string]interface{} `json:"properties,omitempty"`
}

// TokenCreatedWith is the camelCase projection of a user_tokens row's
// properties.created_with metadata — the login method and request context
// recorded when the token (session or PAT) was minted.
type TokenCreatedWith struct {
	Method     string `json:"method,omitempty"`
	UserAgent  string `json:"userAgent,omitempty"`
	RemoteAddr string `json:"remoteAddr,omitempty"`
}

// TokenListResponse contains a list of tokens.
type TokenListResponse struct {
	Data []TokenInfo `json:"data"`
}

// CreateTokenRequest contains the request data for creating a token.
type CreateTokenRequest struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	// Scopes restricts the capabilities of the token. Empty means the token
	// inherits the user's full role (back-compat). See Claims.Scopes for the
	// well-known values, e.g. "mcp" or "mcp:read".
	Scopes []string `json:"scopes,omitempty"`
}

// CreateTokenResponse contains the response data for a created token.
type CreateTokenResponse struct {
	UID       string     `json:"uid"`
	Token     string     `json:"token"`
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// NewService creates a new authentication service.
//
// entitlements is optional; pass nil for callers that do not need SSO
// membership-cap enforcement (e.g. unit tests of unrelated paths).
//
//nolint:gocritic // cfg embeds the WebAuthn block (~112 bytes) — value semantics keep call sites simple.
func NewService(
	dbService db.Service, cfg config.AuthConfig, fullCfg *config.Config,
	jobsSvc jobsvc.Service, entitlements EntitlementsChecker,
) *Service {
	return &Service{
		db:           dbService,
		cfg:          cfg,
		fullCfg:      fullCfg,
		jobsSvc:      jobsSvc,
		entitlements: entitlements,
		patCache:     make(map[string]*cachedPATClaims),
	}
}

// CheckMembershipSlot is exposed for OAuth services that already share the
// auth Service. It delegates to the configured entitlements checker;
// nil checker = no-op (passes through).
func (s *Service) CheckMembershipSlot(ctx context.Context, orgUID string) error {
	if s.entitlements == nil {
		return nil
	}

	return s.entitlements.CheckMembership(ctx, orgUID)
}

// enqueueEmail builds an email job and pushes it onto the job queue.
// Errors are logged but never bubbled to the caller — transactional emails
// must not block registration, password reset, or invitation flows.
//
// The subject is left blank: every template defines its own
// {{define "subject"}} block, so duplicating the subject at the call
// site only invites drift.
func (s *Service) enqueueEmail(
	ctx context.Context, orgUID, recipient, template string, data any,
) {
	if s.jobsSvc == nil || recipient == "" {
		return
	}

	cfg := emailJobConfig{
		To:           []string{recipient},
		Template:     template,
		TemplateData: data,
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to marshal email job config",
			"template", template, "error", err)
		return
	}

	if _, err := s.jobsSvc.CreateJob(ctx, orgUID, string(jobdef.JobTypeEmail), raw, nil); err != nil {
		slog.ErrorContext(ctx, "Failed to enqueue email job",
			"template", template, "error", err)
	}
}

// Login authenticates a user and returns access and refresh tokens.
// orgSlug is treated as a preference — the system will try to honor it but will
// gracefully fall back to available organizations if the user is not a member.
//
// Credential verification order (spec 2026-07-08-08, part 3 — LDAP bind
// auth): a local password hash, when the user has one, is always tried
// first and is the ONLY mechanism attempted for that user — LDAP is never
// consulted, even on a wrong password. This is deliberate on two counts:
// it's what keeps the bootstrap super-admin (and anyone else with a local
// password) able to log in regardless of LDAP being disabled,
// misconfigured, or unreachable, and it avoids bouncing a merely mistyped
// local password off the directory, which can trip an Active Directory
// account-lockout policy on repeated failures. Only a user with NO local
// password hash — a brand-new identity, or one previously provisioned by
// LDAP/OIDC/SAML — falls through to the LDAP bind, when configured. See
// authenticateViaLDAP (ldap_service.go) for why that fallback can never be
// used to hijack an existing local account.
func (s *Service) Login(
	ctx context.Context, orgSlug, email, password string, authContext Context,
) (*LoginResponse, error) {
	// Get user by email (global user lookup). sql.ErrNoRows is not fatal
	// here — it just means the LDAP branch below may be auto-provisioning a
	// brand-new identity.
	user, err := s.db.GetUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	authMethod := "password"

	switch {
	case user != nil && user.PasswordHash != nil && *user.PasswordHash != "":
		if verifyErr := s.verifyLocalPassword(ctx, user, password); verifyErr != nil {
			return nil, verifyErr
		}
	default:
		ldapUser, ldapErr := s.authenticateViaLDAP(ctx, orgSlug, email, password)
		if ldapErr != nil {
			return nil, ldapErr
		}

		user = ldapUser
		authMethod = "ldap"
	}

	// Resolve organization treating orgSlug as a preference
	resolvedOrg, role, loginAction, orgSummaries, err := s.resolveOrgPreference(ctx, orgSlug, user)
	if err != nil {
		return nil, err
	}

	// Check if 2FA is enabled — if so, return a temporary token instead of full login
	if user.TOTPEnabled {
		orgSlugForToken := ""
		if resolvedOrg != nil {
			orgSlugForToken = resolvedOrg.Slug
		}

		tempToken, tokenErr := s.generate2FATempToken(user.UID, orgSlugForToken, role)
		if tokenErr != nil {
			return nil, tokenErr
		}

		return &LoginResponse{
			Requires2FA: true,
			TempToken:   tempToken,
		}, nil
	}

	return s.completeLogin(ctx, user, resolvedOrg, role, loginAction, orgSummaries, authMethod, authContext)
}

// maybeRehashPassword transparently upgrades a user's stored password hash when
// it no longer matches the configured hashing policy (algorithm or cost
// parameters changed). It is called with the just-verified plaintext on a
// successful password login.
//
// It is strictly best-effort: the caller is already authenticated, so any error
// (hashing or persistence) is logged and swallowed — never propagated — and the
// upgrade is simply retried on the next login. Concurrent logins for the same
// user are safe (both write a valid current-policy hash; last writer wins).
func (s *Service) maybeRehashPassword(ctx context.Context, user *models.User, password string) {
	if user.PasswordHash == nil || !passwords.ShouldRehash(*user.PasswordHash) {
		return
	}

	newHash, err := passwords.Hash(password)
	if err != nil {
		slog.WarnContext(ctx, "password rehash failed", "userUid", user.UID, "error", err)
		return
	}

	if err := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{PasswordHash: &newHash}); err != nil {
		slog.WarnContext(ctx, "password rehash persist failed", "userUid", user.UID, "error", err)
	}
}

// completeLogin is the shared post-authentication path used by Login,
// the OAuth callback, and the passkey FinishLogin. It updates last-active,
// issues access + refresh tokens (or just access for the no-org case),
// and writes the refresh-token row tagged with the authentication method
// for forensics. Callers must have already validated the user's identity
// and resolved the organization preference (resolvedOrg may be nil for
// users with no membership).
//
// Token issuance is identical across paths; the only knob is `method`
// (one of "password", "passkey", "oauth") which lands in the token's
// Properties.created_with.method field.
func (s *Service) completeLogin(
	ctx context.Context,
	user *models.User,
	resolvedOrg *models.Organization,
	role string,
	loginAction LoginAction,
	orgSummaries []OrganizationSummary,
	method string,
	authContext Context,
) (*LoginResponse, error) {
	now := time.Now()

	if updateErr := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{LastActiveAt: &now}); updateErr != nil {
		slog.ErrorContext(ctx, "Failed to update user last_active_at", "error", updateErr, "userUID", user.UID)
	}

	userInfo := &UserInfo{
		UID:       user.UID,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Role:      role,
	}

	if resolvedOrg == nil {
		accessToken, tokenErr := s.generateAccessToken(user.UID, "", role, "")
		if tokenErr != nil {
			return nil, tokenErr
		}

		return &LoginResponse{
			AccessToken:   accessToken,
			ExpiresIn:     int(s.cfg.AccessTokenExpiry.Seconds()),
			TokenType:     tokenTypeBearer,
			User:          userInfo,
			Organizations: orgSummaries,
			LoginAction:   loginAction,
		}, nil
	}

	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	refreshToken := models.NewUserToken(user.UID, &resolvedOrg.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := s.refreshTokenExpiry(ctx, resolvedOrg.UID, now, now)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now

	createdWith := authContext.ToMap()
	if createdWith == nil {
		createdWith = map[string]any{}
	}
	createdWith[keyMethod] = method
	refreshToken.Properties = models.JSONMap{
		keyCreatedWith: createdWith,
	}

	if err = s.db.CreateUserToken(ctx, refreshToken); err != nil {
		return nil, err
	}

	s.enforceSessionCap(ctx, user.UID)

	accessToken, err := s.generateAccessToken(user.UID, resolvedOrg.Slug, role, refreshToken.UID)
	if err != nil {
		return nil, err
	}

	return &LoginResponse{
		AccessToken:   accessToken,
		RefreshToken:  refreshTokenValue,
		ExpiresIn:     int(s.cfg.AccessTokenExpiry.Seconds()),
		TokenType:     tokenTypeBearer,
		User:          userInfo,
		Organization:  newOrganizationInfo(resolvedOrg),
		Organizations: orgSummaries,
		LoginAction:   loginAction,
	}, nil
}

// enforceSessionCap prunes the user's active `refresh`-type sessions down to
// maxActiveSessions, soft-deleting the least-recently-active rows beyond the
// cap. Called after a new refresh-token row is created at every login-style
// path (password, OAuth, passkey, 2FA, registration, invite, switch-org).
//
// Best-effort: a listing or delete error is logged and swallowed. The user
// is already authenticated via the row just created, so failing the whole
// login over a cap-enforcement hiccup would be worse than temporarily
// exceeding the cap by one.
func (s *Service) enforceSessionCap(ctx context.Context, userUID string) {
	sessions, err := s.db.ListUserTokensByType(ctx, userUID, models.TokenTypeRefresh)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list sessions for cap enforcement", "error", err, "userUID", userUID)

		return
	}

	if len(sessions) <= maxActiveSessions {
		return
	}

	// Sort ascending by "activity" (LastActiveAt, falling back to CreatedAt
	// for a session that was minted but never refreshed) so the least
	// recently active rows are pruned first.
	activityOf := func(tok *models.UserToken) time.Time {
		if tok.LastActiveAt != nil {
			return *tok.LastActiveAt
		}

		return tok.CreatedAt
	}

	sort.Slice(sessions, func(i, j int) bool {
		return activityOf(sessions[i]).Before(activityOf(sessions[j]))
	})

	excess := len(sessions) - maxActiveSessions
	for _, tok := range sessions[:excess] {
		if _, delErr := s.db.DeleteUserToken(ctx, tok.UID); delErr != nil {
			slog.ErrorContext(ctx, "Failed to prune session over cap", "error", delErr, "tokenUID", tok.UID)
		}
	}
}

// resolveOrgPreference resolves the organization for login, treating orgSlug as a preference.
// Returns the resolved org (may be nil for no-org case), the user's role, the login action,
// and the list of available organizations.
func (s *Service) resolveOrgPreference(
	ctx context.Context, orgSlug string, user *models.User,
) (*models.Organization, string, LoginAction, []OrganizationSummary, error) {
	// Super admins: resolve org normally (they can access any org)
	if user.SuperAdmin {
		return s.resolveOrgForSuperAdmin(ctx, orgSlug, user.UID)
	}

	// List all memberships for this user
	members, err := s.db.ListMembersByUser(ctx, user.UID)
	if err != nil {
		return nil, "", "", nil, err
	}

	orgSummaries := buildOrgSummaries(members)

	// If orgSlug provided, try to use it
	if orgSlug != "" {
		org, role, found := s.findMembershipBySlug(members, orgSlug)
		if found {
			return org, role, LoginActionDefault, orgSummaries, nil
		}

		// Org preference not matched, fall back to membership-based resolution
		return s.resolveFromMemberships(ctx, members, orgSummaries, user.UID)
	}

	// No orgSlug provided, use the default org resolution
	return s.resolveDefaultOrgForLogin(ctx, members, orgSummaries, user.UID)
}

// buildOrgSummaries creates OrganizationSummary entries from memberships.
func buildOrgSummaries(members []*models.OrganizationMember) []OrganizationSummary {
	summaries := make([]OrganizationSummary, 0, len(members))
	for _, member := range members {
		if member.Organization == nil {
			continue
		}

		summaries = append(summaries, OrganizationSummary{
			Slug:    member.Organization.Slug,
			Name:    member.Organization.Name,
			LogoURL: member.Organization.LogoURL,
			Role:    string(member.Role),
		})
	}

	return summaries
}

// resolveDefaultOrgForLogin resolves the default org when no orgSlug was provided.
func (s *Service) resolveDefaultOrgForLogin(
	ctx context.Context, members []*models.OrganizationMember,
	orgSummaries []OrganizationSummary, userUID string,
) (*models.Organization, string, LoginAction, []OrganizationSummary, error) {
	org, resolveErr := s.resolveDefaultOrg(ctx, userUID)
	if resolveErr != nil {
		if len(members) == 0 {
			return nil, "", LoginActionNoOrg, orgSummaries, nil
		}

		return nil, "", "", nil, resolveErr
	}

	role := findRoleInMembers(members, org.UID)

	return org, role, LoginActionDefault, orgSummaries, nil
}

// resolveFromMemberships resolves an org when the preferred org didn't match.
func (s *Service) resolveFromMemberships(
	ctx context.Context, members []*models.OrganizationMember,
	orgSummaries []OrganizationSummary, userUID string,
) (*models.Organization, string, LoginAction, []OrganizationSummary, error) {
	switch len(members) {
	case 0:
		return nil, "", LoginActionNoOrg, orgSummaries, nil
	case 1:
		return members[0].Organization, string(members[0].Role), LoginActionOrgRedirect, orgSummaries, nil
	default:
		org, _ := s.resolveDefaultOrg(ctx, userUID)
		if org == nil {
			org = members[0].Organization
		}

		role := findRoleInMembers(members, org.UID)

		return org, role, LoginActionOrgChoice, orgSummaries, nil
	}
}

// findRoleInMembers looks up the user's role in the given org from their memberships.
func findRoleInMembers(members []*models.OrganizationMember, orgUID string) string {
	for _, member := range members {
		if member.OrganizationUID == orgUID {
			return string(member.Role)
		}
	}

	return ""
}

// resolveOrgForSuperAdmin resolves the org for a super admin user.
func (s *Service) resolveOrgForSuperAdmin(
	ctx context.Context, orgSlug, userUID string,
) (*models.Organization, string, LoginAction, []OrganizationSummary, error) {
	role := RoleSuperAdmin

	// Build org summaries for super admin
	orgSummaries, err := s.getOrganizationsForUser(ctx, userUID)
	if err != nil {
		orgSummaries = nil
	}

	if orgSlug != "" {
		org, orgErr := s.db.GetOrganizationBySlug(ctx, orgSlug)
		if orgErr == nil {
			return org, role, LoginActionDefault, orgSummaries, nil
		}

		// Requested org doesn't exist — fall back with appropriate action
		return s.resolveOrgFallback(ctx, orgSummaries, role, userUID)
	}

	// No orgSlug provided, use default org
	org, _ := s.resolveDefaultOrg(ctx, userUID)
	if org == nil {
		return nil, role, LoginActionNoOrg, orgSummaries, nil
	}

	return org, role, LoginActionDefault, orgSummaries, nil
}

// resolveOrgFallback picks the best org and login action when the preferred org didn't match.
func (s *Service) resolveOrgFallback(
	ctx context.Context, orgSummaries []OrganizationSummary, role, userUID string,
) (*models.Organization, string, LoginAction, []OrganizationSummary, error) {
	switch len(orgSummaries) {
	case 0:
		return nil, role, LoginActionNoOrg, orgSummaries, nil
	case 1:
		org, _ := s.db.GetOrganizationBySlug(ctx, orgSummaries[0].Slug)
		if org == nil {
			return nil, role, LoginActionNoOrg, orgSummaries, nil
		}

		return org, role, LoginActionOrgRedirect, orgSummaries, nil
	default:
		org, _ := s.resolveDefaultOrg(ctx, userUID)
		if org == nil {
			org, _ = s.db.GetOrganizationBySlug(ctx, orgSummaries[0].Slug)
		}

		return org, role, LoginActionOrgChoice, orgSummaries, nil
	}
}

// findMembershipBySlug checks if the user has a membership for the given org slug.
func (s *Service) findMembershipBySlug(
	members []*models.OrganizationMember, orgSlug string,
) (*models.Organization, string, bool) {
	for _, member := range members {
		if member.Organization != nil && member.Organization.Slug == orgSlug {
			return member.Organization, string(member.Role), true
		}
	}

	return nil, "", false
}

// resolveDefaultOrg finds the default organization for a user.
// It checks the most recent refresh token first, then falls back to first membership.
func (s *Service) resolveDefaultOrg(ctx context.Context, userUID string) (*models.Organization, error) {
	// Try most recent refresh token
	tokens, err := s.db.ListUserTokensByType(ctx, userUID, models.TokenTypeRefresh)
	if err == nil && len(tokens) > 0 {
		// Find most recent by created_at
		var mostRecent *models.UserToken
		for _, t := range tokens {
			if t.OrganizationUID != nil && (mostRecent == nil || t.CreatedAt.After(mostRecent.CreatedAt)) {
				mostRecent = t
			}
		}

		if mostRecent != nil && mostRecent.OrganizationUID != nil {
			org, orgErr := s.db.GetOrganization(ctx, *mostRecent.OrganizationUID)
			if orgErr == nil {
				return org, nil
			}
		}
	}

	// Fallback: first membership
	members, err := s.db.ListMembersByUser(ctx, userUID)
	if err != nil {
		return nil, ErrInvalidCredentials
	}

	if len(members) == 0 {
		return nil, ErrInvalidCredentials
	}

	org, err := s.db.GetOrganization(ctx, members[0].OrganizationUID)
	if err != nil {
		return nil, ErrInvalidCredentials
	}

	return org, nil
}

// Logout invalidates a refresh token.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	token, err := s.db.GetUserTokenByToken(ctx, refreshToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // Token already deleted or doesn't exist
		}

		return err
	}

	_, err = s.db.DeleteUserToken(ctx, token.UID)

	return err
}

// LogoutUser invalidates all refresh tokens for a user across all orgs.
func (s *Service) LogoutUser(ctx context.Context, userUID string) (*LogoutResponse, error) {
	// Verify user exists
	_, err := s.db.GetUser(ctx, userUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	// Get all refresh tokens for user
	tokens, err := s.db.ListUserTokensByType(ctx, userUID, models.TokenTypeRefresh)
	if err != nil {
		return nil, err
	}

	// Delete all refresh tokens
	deleted := 0

	for _, token := range tokens {
		if _, deleteErr := s.db.DeleteUserToken(ctx, token.UID); deleteErr != nil {
			slog.ErrorContext(ctx, "Failed to delete refresh token", "error", deleteErr, "tokenUID", token.UID)

			continue
		}

		deleted++
	}

	// Tear down any OAuth-issued MCP refresh grants for this user too, so a
	// full logout also invalidates connectors authorized via the OAuth flow.
	s.revokeUserTokensOfType(ctx, userUID, models.TokenTypeOAuthRefresh)

	return &LogoutResponse{
		Success:       true,
		TokensDeleted: deleted,
	}, nil
}

// LogoutOtherSessions deletes every `refresh`-type session row for the user
// EXCEPT the one identified by currentRefreshUID (the caller's own session).
// Unlike LogoutUser, the caller's own session survives — this is a "sign out
// other devices" action, not a logout, and the caller's access token remains
// valid until it naturally expires (its own refresh-token row was never
// touched).
func (s *Service) LogoutOtherSessions(ctx context.Context, userUID, currentRefreshUID string) (*LogoutResponse, error) {
	// Verify user exists
	_, err := s.db.GetUser(ctx, userUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	tokens, err := s.db.ListUserTokensByType(ctx, userUID, models.TokenTypeRefresh)
	if err != nil {
		return nil, err
	}

	deleted := 0

	for _, token := range tokens {
		if token.UID == currentRefreshUID {
			continue // Spare the caller's own session.
		}

		if _, deleteErr := s.db.DeleteUserToken(ctx, token.UID); deleteErr != nil {
			slog.ErrorContext(ctx, "Failed to delete other session", "error", deleteErr, "tokenUID", token.UID)

			continue
		}

		deleted++
	}

	return &LogoutResponse{
		Success:       true,
		TokensDeleted: deleted,
	}, nil
}

// roleForOrg resolves a user's role within orgUID: super admins bypass
// membership entirely; everyone else must have a membership row, or
// ErrUserNotFound is returned (matches the historical behavior of the
// inline checks this factors out of Refresh/SwitchOrg-style flows).
func (s *Service) roleForOrg(ctx context.Context, user *models.User, orgUID string) (string, error) {
	if user.SuperAdmin {
		return RoleSuperAdmin, nil
	}

	membership, err := s.db.GetMemberByUserAndOrg(ctx, user.UID, orgUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrUserNotFound
		}

		return "", err
	}

	return string(membership.Role), nil
}

// resolveSessionMaxDuration returns the effective hard cap on session
// lifetime for orgUID: an org-scoped auth.session_max_duration parameter
// override (spec B.2), falling back to the system-wide value in
// s.fullCfg.Auth.SessionMaxDuration (systemconfig.KeySessionMaxDuration —
// applied onto the LIVE *config.Config by the startup overlay and editable
// at runtime via PUT /api/v1/system/parameters, effective on restart). It
// MUST be read from fullCfg, not the frozen s.cfg copy: the overlay runs
// after NewService, so s.cfg.SessionMaxDuration is stale (this was the
// original bug — a system-wide cap set via env/DB was silently ignored).
// Falls back to 0 (no cap — today's behavior, only the sliding
// RefreshTokenExpiry idle window applies). orgUID may be empty (no org
// resolved yet) — that's simply "no org override to check."
//
// A per-call DB read for the org override is fine: this is only invoked at
// refresh-token mint/slide time, which is rare compared to request volume.
// Cache like patCacheDuration if that ever shows up in a profile.
func (s *Service) resolveSessionMaxDuration(ctx context.Context, orgUID string) time.Duration {
	if orgUID != "" {
		param, err := s.db.GetOrgParameter(ctx, orgUID, string(systemconfig.KeySessionMaxDuration))
		if err != nil {
			slog.WarnContext(ctx, "failed to read org session_max_duration override; falling back to system default",
				"error", err, "orgUID", orgUID)
		} else if seconds, ok := parseParamSeconds(param); ok && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}

	return s.fullCfg.Auth.SessionMaxDuration
}

// parseParamSeconds extracts an integer seconds value from a parameter row's
// JSON value (stored as {"value": <number>} by Set{Org,System}Parameter).
// Returns false for a nil param (no override set) or an unparseable value.
func parseParamSeconds(param *models.Parameter) (int, bool) {
	if param == nil {
		return 0, false
	}

	switch v := param.Value["value"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

// refreshTokenExpiry computes a refresh-token row's expires_at for a mint
// (loginTime == now) or slide (loginTime == the row's original CreatedAt)
// operation: now + cfg.RefreshTokenExpiry (the sliding idle window this
// already was), capped to loginTime + session_max_duration when an org or
// system override sets an absolute maximum session lifetime (spec B.3 —
// "measured from login", so sliding can never extend a session past the
// cap). This is the single replacement for every direct
// s.cfg.RefreshTokenExpiry read at mint/slide sites.
func (s *Service) refreshTokenExpiry(ctx context.Context, orgUID string, loginTime, now time.Time) time.Time {
	slidingExpiry := now.Add(s.cfg.RefreshTokenExpiry)

	maxDuration := s.resolveSessionMaxDuration(ctx, orgUID)
	if maxDuration <= 0 {
		return slidingExpiry
	}

	if hardCap := loginTime.Add(maxDuration); hardCap.Before(slidingExpiry) {
		return hardCap
	}

	return slidingExpiry
}

// slideSessionExpiry extends a refresh-token row's expires_at to
// now + refresh_token_expiry on activity, so an active session never hits
// the idle TTL. Gated by the same hourly write granularity already used for
// last_active_at elsewhere (ValidatePATToken) — no write amplification on
// rapid-fire refreshes (e.g. several tabs refreshing close together).
// Best-effort: a write failure is logged and swallowed, matching the
// existing last_active_at update it replaces.
func (s *Service) slideSessionExpiry(ctx context.Context, token *models.UserToken) {
	now := time.Now()

	if token.LastActiveAt != nil && now.Sub(*token.LastActiveAt) <= time.Hour {
		return
	}

	orgUID := ""
	if token.OrganizationUID != nil {
		orgUID = *token.OrganizationUID
	}

	newExpiresAt := s.refreshTokenExpiry(ctx, orgUID, token.CreatedAt, now)
	update := models.UserTokenUpdate{LastActiveAt: &now, ExpiresAt: &newExpiresAt}

	if updateErr := s.db.UpdateUserToken(ctx, token.UID, update); updateErr != nil {
		slog.ErrorContext(ctx, "Failed to update token last_active_at/expires_at", "error", updateErr, "tokenUID", token.UID)
	}
}

// Refresh generates a new access token using a valid refresh token.
// The org is derived from the refresh token itself.
func (s *Service) Refresh(ctx context.Context, refreshTokenValue string) (*LoginResponse, error) {
	// Get refresh token
	token, err := s.db.GetUserTokenByToken(ctx, refreshTokenValue)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidToken
		}

		return nil, err
	}

	// Verify token is a refresh token and not expired
	if token.Type != models.TokenTypeRefresh {
		return nil, ErrInvalidToken
	}

	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, ErrTokenExpired
	}

	if token.OrganizationUID == nil {
		return nil, ErrInvalidToken
	}

	// Get organization from token
	org, err := s.db.GetOrganization(ctx, *token.OrganizationUID)
	if err != nil {
		return nil, ErrInvalidToken
	}

	// Get user
	user, err := s.db.GetUser(ctx, token.UserUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	// Get role from membership (super admins bypass)
	role, err := s.roleForOrg(ctx, user, org.UID)
	if err != nil {
		return nil, err
	}

	// Generate new access token, bound to the refresh-token row that issued it.
	accessToken, err := s.generateAccessToken(user.UID, org.Slug, role, token.UID)
	if err != nil {
		return nil, err
	}

	s.slideSessionExpiry(ctx, token)

	return &LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenValue,
		ExpiresIn:    int(s.cfg.AccessTokenExpiry.Seconds()),
		TokenType:    tokenTypeBearer,
		User: &UserInfo{
			UID:       user.UID,
			Email:     user.Email,
			Name:      user.Name,
			AvatarURL: user.AvatarURL,
			Role:      role,
		},
		Organization: newOrganizationInfo(org),
	}, nil
}

// ValidateToken validates a JWT or PAT token and returns its claims.
func (s *Service) ValidateToken(ctx context.Context, tokenString string) (*Claims, error) {
	// Check if it's a PAT token
	if strings.HasPrefix(tokenString, "pat_") {
		return s.ValidatePATToken(ctx, tokenString)
	}

	// Validate JWT token
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: %v", ErrUnexpectedSigningMethod, token.Header["alg"])
		}

		return []byte(s.fullCfg.Auth.JWTSecret), nil
	})
	if err != nil {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// ValidatePATToken validates a Personal Access Token.
//
//nolint:cyclop,funlen
func (s *Service) ValidatePATToken(ctx context.Context, patToken string) (*Claims, error) {
	// Check cache first
	s.cacheMux.RLock()

	if cached, exists := s.patCache[patToken]; exists && time.Now().Before(cached.expiresAt) {
		s.cacheMux.RUnlock()

		return cached.claims, nil
	}

	s.cacheMux.RUnlock()

	// Query database for PAT
	token, err := s.db.GetUserTokenByToken(ctx, patToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidToken
		}

		return nil, err
	}

	// Verify token is a PAT and not expired
	if token.Type != models.TokenTypePAT {
		return nil, ErrInvalidToken
	}

	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, ErrTokenExpired
	}

	// PATs must be org-scoped
	if token.OrganizationUID == nil {
		return nil, ErrInvalidToken
	}

	// Get user and organization
	user, err := s.db.GetUser(ctx, token.UserUID)
	if err != nil {
		return nil, ErrUserNotFound
	}

	org, err := s.db.GetOrganization(ctx, *token.OrganizationUID)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Get role from membership (super admins bypass)
	var role string
	if user.SuperAdmin {
		role = RoleSuperAdmin
	} else {
		membership, memberErr := s.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
		if memberErr != nil {
			if errors.Is(memberErr, sql.ErrNoRows) {
				return nil, ErrUserNotFound
			}

			return nil, memberErr
		}

		role = string(membership.Role)
	}

	// Update last used timestamp (with hourly precision to reduce DB writes)
	now := time.Now()
	if token.LastActiveAt == nil || now.Sub(*token.LastActiveAt) > time.Hour {
		if updateErr := s.db.UpdateUserToken(ctx, token.UID, models.UserTokenUpdate{LastActiveAt: &now}); updateErr != nil {
			slog.ErrorContext(ctx, "Failed to update PAT last_active_at", "error", updateErr, "tokenUID", token.UID)
		}
	}

	var expiresAt *jwt.NumericDate
	if token.ExpiresAt != nil {
		expiresAt = jwt.NewNumericDate(*token.ExpiresAt)
	}

	claims := &Claims{
		UserUID: user.UID,
		OrgSlug: org.Slug,
		Role:    role,
		Scopes:  scopesFromProperties(token.Properties),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: expiresAt,
			IssuedAt:  jwt.NewNumericDate(token.CreatedAt),
			Issuer:    jwtIssuer,
		},
	}

	const patCacheDuration = 15 * time.Minute

	// Cache the result for 15 minutes
	s.cacheMux.Lock()
	s.patCache[patToken] = &cachedPATClaims{
		claims:    claims,
		expiresAt: time.Now().Add(patCacheDuration),
	}
	s.cacheMux.Unlock()

	// Periodically cleanup expired cache entries (1% chance on each call)
	if time.Now().UnixNano()%100 == 0 {
		go s.cleanupExpiredPATCache()
	}

	return claims, nil
}

func (s *Service) cleanupExpiredPATCache() {
	s.cacheMux.Lock()
	defer s.cacheMux.Unlock()

	now := time.Now()
	for token, cached := range s.patCache {
		if now.After(cached.expiresAt) {
			delete(s.patCache, token)
		}
	}
}

// GetUserInfo returns information about the current user.
// orgSlug is extracted from the JWT claims.
func (s *Service) GetUserInfo(ctx context.Context, claims *Claims) (*MeResponse, error) {
	// Zero-org session: the token carries no org slug — the user belongs to no
	// organization yet (a fresh sign-up who hasn't created/joined one, or a
	// user removed from their last org). This is a legitimate state, exactly as
	// completeLogin's resolvedOrg==nil branch treats it, so resolve the user's
	// info WITHOUT an org rather than 401ing on a GetOrganizationBySlug("")
	// miss (which would silently destroy the session on the next page load).
	if claims.OrgSlug == "" {
		return s.getUserInfoNoOrg(ctx, claims)
	}

	org, err := s.db.GetOrganizationBySlug(ctx, claims.OrgSlug)
	if err != nil {
		// The org the token names is gone (deleted while this tab was open, or
		// while a token minted for it was still in a client's hands). That is
		// not an authentication failure: the USER is still perfectly
		// authenticated, they simply no longer have that org context. Falling
		// through to the zero-org response degrades the session instead of
		// destroying it — handleUserInfoError maps ErrOrganizationNotFound to
		// 401, so returning it here would log the user out on a plain reload
		// right after they deleted their own organization (issue #206).
		if errors.Is(err, sql.ErrNoRows) {
			return s.getUserInfoNoOrg(ctx, claims)
		}

		return nil, err
	}

	user, err := s.db.GetUser(ctx, claims.UserUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	// Get role from membership (super admins bypass)
	var role string
	if user.SuperAdmin {
		role = RoleSuperAdmin
	} else {
		membership, memberErr := s.db.GetMemberByUserAndOrg(ctx, claims.UserUID, org.UID)
		if memberErr != nil {
			if errors.Is(memberErr, sql.ErrNoRows) {
				return nil, ErrUserNotFound
			}

			return nil, memberErr
		}

		role = string(membership.Role)
	}

	orgs, err := s.getOrganizationsForUser(ctx, claims.UserUID)
	if err != nil {
		return nil, err
	}

	pending, err := s.listPendingMembershipRequests(ctx, claims.UserUID)
	if err != nil {
		return nil, err
	}

	passkeys, _ := s.db.ListUserPasskeysByUser(ctx, user.UID)
	hasPassword := user.PasswordHash != nil && *user.PasswordHash != ""

	return &MeResponse{
		User: &UserInfo{
			UID:       user.UID,
			Email:     user.Email,
			Name:      user.Name,
			AvatarURL: user.AvatarURL,
			Role:      role,
		},
		Organization:              newOrganizationInfo(org),
		Organizations:             orgs,
		TOTPEnabled:               user.TOTPEnabled,
		PasskeyCount:              len(passkeys),
		HasPassword:               hasPassword,
		PendingMembershipRequests: pending,
	}, nil
}

// getUserInfoNoOrg builds the /auth/me response for a zero-org session (empty
// OrgSlug claim). It skips the org / membership-role lookups entirely and
// returns a nil Organization. The role is derived from the user alone: a plain
// no-org user has an empty role (mirroring the login response's no-org branch,
// which the dashboard's /no-org page already renders correctly), while a
// superadmin keeps their global role.
func (s *Service) getUserInfoNoOrg(ctx context.Context, claims *Claims) (*MeResponse, error) {
	user, err := s.db.GetUser(ctx, claims.UserUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	role := ""
	if user.SuperAdmin {
		role = RoleSuperAdmin
	}

	orgs, err := s.getOrganizationsForUser(ctx, claims.UserUID)
	if err != nil {
		return nil, err
	}

	pending, err := s.listPendingMembershipRequests(ctx, claims.UserUID)
	if err != nil {
		return nil, err
	}

	passkeys, _ := s.db.ListUserPasskeysByUser(ctx, user.UID)
	hasPassword := user.PasswordHash != nil && *user.PasswordHash != ""

	return &MeResponse{
		User: &UserInfo{
			UID:       user.UID,
			Email:     user.Email,
			Name:      user.Name,
			AvatarURL: user.AvatarURL,
			Role:      role,
		},
		Organization:              nil,
		Organizations:             orgs,
		TOTPEnabled:               user.TOTPEnabled,
		PasskeyCount:              len(passkeys),
		HasPassword:               hasPassword,
		PendingMembershipRequests: pending,
	}, nil
}

// UpdateProfile updates the current user's profile fields.
func (s *Service) UpdateProfile(ctx context.Context, claims *Claims, req UpdateProfileRequest) (*MeResponse, error) {
	update := models.UserUpdate{
		Name: req.Name,
	}

	if err := s.db.UpdateUser(ctx, claims.UserUID, &update); err != nil {
		return nil, err
	}

	return s.GetUserInfo(ctx, claims)
}

// getOrganizationsForUser returns the list of organizations a user belongs to.
func (s *Service) getOrganizationsForUser(ctx context.Context, userUID string) ([]OrganizationSummary, error) {
	members, err := s.db.ListMembersByUser(ctx, userUID)
	if err != nil {
		return nil, err
	}

	orgs := make([]OrganizationSummary, 0, len(members))

	for _, member := range members {
		if member.Organization == nil {
			continue
		}

		orgs = append(orgs, OrganizationSummary{
			Slug:    member.Organization.Slug,
			Name:    member.Organization.Name,
			LogoURL: member.Organization.LogoURL,
			Role:    string(member.Role),
		})
	}

	return orgs, nil
}

// tokenToInfo projects a models.UserToken into the API's TokenInfo shape.
// callerRefreshUID is the requesting user's own Claims.RefreshUID (empty for
// PAT-authenticated callers) — used to flag isCurrent on the caller's own
// session (refresh) row or OAuth grant (oauth_refresh) row; orgSlug is filled
// in by the caller when known (org-scoped listing already has it in hand; the
// all-orgs listing resolves it per-token).
func tokenToInfo(tok *models.UserToken, orgSlug, callerRefreshUID string) TokenInfo {
	name := ""
	if tok.Properties != nil {
		if n, ok := tok.Properties[keyName].(string); ok {
			name = n
		}
	}

	info := TokenInfo{
		UID:          tok.UID,
		Name:         name,
		Type:         string(tok.Type),
		OrgSlug:      orgSlug,
		CreatedAt:    tok.CreatedAt,
		LastUsedAt:   tok.LastActiveAt,
		LastActiveAt: tok.LastActiveAt,
		ExpiresAt:    tok.ExpiresAt,
		// A refresh (session) or oauth_refresh (grant) row whose uid matches the
		// caller's RefreshUID is the credential the caller is riding on. PATs
		// never back a RefreshUID, so they can never be flagged current.
		IsCurrent: callerRefreshUID != "" && tok.UID == callerRefreshUID &&
			(tok.Type == models.TokenTypeRefresh || tok.Type == models.TokenTypeOAuthRefresh),
	}

	info.CreatedWith = extractCreatedWith(tok.Properties)

	return info
}

// extractCreatedWith projects a user_tokens row's properties.created_with
// map (JSON-decoded to map[string]any) into the camelCase TokenCreatedWith
// struct the API returns. Returns nil when the row has no such metadata
// (predates this field, or is a token type that never recorded it).
func extractCreatedWith(properties map[string]any) *TokenCreatedWith {
	if properties == nil {
		return nil
	}

	raw, ok := properties[keyCreatedWith].(map[string]any)
	if !ok {
		return nil
	}

	createdWith := &TokenCreatedWith{}
	if method, ok := raw[keyMethod].(string); ok {
		createdWith.Method = method
	}

	if userAgent, ok := raw["userAgent"].(string); ok {
		createdWith.UserAgent = userAgent
	}

	if remoteAddr, ok := raw["remoteAddr"].(string); ok {
		createdWith.RemoteAddr = remoteAddr
	}

	return createdWith
}

// GetUserTokens returns a list of tokens for a user, org-scoped.
// callerRefreshUID is the caller's own Claims.RefreshUID (empty for PATs),
// used to flag isCurrent on the caller's own session row.
func (s *Service) GetUserTokens(
	ctx context.Context, orgSlug, userUID, tokenType, callerRefreshUID string,
) (*TokenListResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	user, err := s.db.GetUser(ctx, userUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	// Check membership (super admins bypass)
	if !user.SuperAdmin {
		_, memberErr := s.db.GetMemberByUserAndOrg(ctx, userUID, org.UID)
		if memberErr != nil {
			if errors.Is(memberErr, sql.ErrNoRows) {
				return nil, ErrUserNotFound
			}

			return nil, memberErr
		}
	}

	var tokens []*models.UserToken
	if tokenType != "" {
		tokens, err = s.db.ListUserTokensByType(ctx, userUID, models.TokenType(tokenType))
	} else {
		tokens, err = s.db.ListUserTokens(ctx, userUID)
	}

	if err != nil {
		return nil, err
	}

	// Filter out expired tokens and convert to response format
	result := make([]TokenInfo, 0, len(tokens))
	now := time.Now()

	for _, tok := range tokens {
		if tok.ExpiresAt != nil && now.After(*tok.ExpiresAt) {
			continue // Skip expired tokens
		}

		result = append(result, tokenToInfo(tok, orgSlug, callerRefreshUID))
	}

	return &TokenListResponse{Data: result}, nil
}

// CreatePAT creates a new Personal Access Token.
func (s *Service) CreatePAT(
	ctx context.Context, orgSlug, userUID string, req CreateTokenRequest,
) (*CreateTokenResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	user, err := s.db.GetUser(ctx, userUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	// Check membership (super admins bypass)
	if !user.SuperAdmin {
		_, memberErr := s.db.GetMemberByUserAndOrg(ctx, userUID, org.UID)
		if memberErr != nil {
			if errors.Is(memberErr, sql.ErrNoRows) {
				return nil, ErrUserNotFound
			}

			return nil, memberErr
		}
	}

	// Generate PAT token value
	tokenValue, err := s.generatePATToken()
	if err != nil {
		return nil, err
	}

	token := models.NewUserToken(userUID, &org.UID, tokenValue, models.TokenTypePAT)
	token.Properties = models.JSONMap{keyName: req.Name}

	if len(req.Scopes) > 0 {
		// Stored as []any so json round-trips through JSONMap cleanly.
		scopes := make([]any, len(req.Scopes))
		for i, s := range req.Scopes {
			scopes[i] = s
		}
		token.Properties[keyScopes] = scopes
	}

	token.ExpiresAt = req.ExpiresAt

	if err := s.db.CreateUserToken(ctx, token); err != nil {
		return nil, err
	}

	return &CreateTokenResponse{
		UID:       token.UID,
		Token:     tokenValue,
		Name:      req.Name,
		ExpiresAt: token.ExpiresAt,
		CreatedAt: token.CreatedAt,
	}, nil
}

// RevokeToken revokes (deletes) a user token. User-scoped, no org check needed.
func (s *Service) RevokeToken(ctx context.Context, userUID, tokenUID string) error {
	token, err := s.db.GetUserToken(ctx, tokenUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTokenNotFound
		}

		return err
	}

	// Verify token belongs to the user
	if token.UserUID != userUID {
		return ErrTokenNotFound
	}

	// Invalidate cache if it's a PAT
	if token.Type == models.TokenTypePAT {
		s.cacheMux.Lock()
		delete(s.patCache, token.Token)
		s.cacheMux.Unlock()

		// A PAT revoke should also tear down OAuth-issued MCP refresh grants for
		// the user, matching the spec's "revocation on logout/PAT-revoke".
		s.revokeUserTokensOfType(ctx, userUID, models.TokenTypeOAuthRefresh)
	}

	_, err = s.db.DeleteUserToken(ctx, tokenUID)

	return err
}

// SwitchOrg switches the user's current organization context.
// It verifies membership and mints new tokens scoped to the target org.
func (s *Service) SwitchOrg(
	ctx context.Context, userUID, targetOrgSlug string, authContext Context,
) (*LoginResponse, error) {
	// Get target organization
	org, err := s.db.GetOrganizationBySlug(ctx, targetOrgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	// Get user
	user, err := s.db.GetUser(ctx, userUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	// Check membership (super admins bypass)
	var role string
	if user.SuperAdmin {
		role = RoleSuperAdmin
	} else {
		membership, memberErr := s.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
		if memberErr != nil {
			if errors.Is(memberErr, sql.ErrNoRows) {
				return nil, ErrInvalidCredentials
			}

			return nil, memberErr
		}

		role = string(membership.Role)
	}

	// Generate refresh token, scoped to the target org. Minting a fresh
	// refresh-token row (rather than mutating the caller's existing one) is
	// the switch-org fix from the spec: a subsequent background refresh
	// reads whichever refresh token the client is holding, and once the
	// client persists this new one it reproduces the switched-to org rather
	// than silently flipping back to the login-time org.
	now := time.Now()

	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	// Store refresh token in database
	refreshToken := models.NewUserToken(user.UID, &org.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := s.refreshTokenExpiry(ctx, org.UID, now, now)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{
		keyCreatedWith: authContext.ToMap(),
	}

	if err = s.db.CreateUserToken(ctx, refreshToken); err != nil {
		return nil, err
	}

	s.enforceSessionCap(ctx, user.UID)

	// Generate access token, bound to the new refresh-token row.
	accessToken, err := s.generateAccessToken(user.UID, org.Slug, role, refreshToken.UID)
	if err != nil {
		return nil, err
	}

	return &LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenValue,
		ExpiresIn:    int(s.cfg.AccessTokenExpiry.Seconds()),
		TokenType:    tokenTypeBearer,
		User: &UserInfo{
			UID:       user.UID,
			Email:     user.Email,
			Name:      user.Name,
			AvatarURL: user.AvatarURL,
			Role:      role,
		},
		Organization: newOrganizationInfo(org),
	}, nil
}

// GetAllUserTokens returns all tokens for a user across all orgs (for
// root-level listing). callerRefreshUID is the caller's own
// Claims.RefreshUID (empty for PATs), used to flag isCurrent.
func (s *Service) GetAllUserTokens(
	ctx context.Context, userUID, tokenType, callerRefreshUID string,
) (*TokenListResponse, error) {
	var tokens []*models.UserToken
	var err error

	if tokenType != "" {
		tokens, err = s.db.ListUserTokensByType(ctx, userUID, models.TokenType(tokenType))
	} else {
		tokens, err = s.db.ListUserTokens(ctx, userUID)
	}

	if err != nil {
		return nil, err
	}

	// Build org UID -> slug map for all referenced orgs
	orgSlugs := make(map[string]string)
	for _, tok := range tokens {
		if tok.OrganizationUID != nil {
			if _, exists := orgSlugs[*tok.OrganizationUID]; !exists {
				org, orgErr := s.db.GetOrganization(ctx, *tok.OrganizationUID)
				if orgErr == nil {
					orgSlugs[*tok.OrganizationUID] = org.Slug
				}
			}
		}
	}

	// Filter out expired tokens and convert to response format
	result := make([]TokenInfo, 0, len(tokens))
	now := time.Now()

	for _, tok := range tokens {
		if tok.ExpiresAt != nil && now.After(*tok.ExpiresAt) {
			continue // Skip expired tokens
		}

		orgSlug := ""
		if tok.OrganizationUID != nil {
			orgSlug = orgSlugs[*tok.OrganizationUID]
		}

		result = append(result, tokenToInfo(tok, orgSlug, callerRefreshUID))
	}

	return &TokenListResponse{Data: result}, nil
}

// generateAccessToken mints an access-token JWT. refreshUID is the
// user_tokens.uid of the refresh-token row this access token is bound to —
// pass "" when there is no such row (no-org login, PAT validation, 2FA temp
// tokens).
func (s *Service) generateAccessToken(userUID, orgSlug, role, refreshUID string) (string, error) {
	now := time.Now()
	claims := &Claims{
		UserUID:    userUID,
		OrgSlug:    orgSlug,
		Role:       role,
		RefreshUID: refreshUID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.AccessTokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    jwtIssuer,
			ID:        uuid.New().String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString([]byte(s.fullCfg.Auth.JWTSecret))
}

// GenerateMCPAccessToken mints a short-lived, audience-bound JWT access token
// for the embedded OAuth 2.1 authorization server (MCP resource). It reuses the
// existing HS256 signing key so the token is validated by the same code path as
// session JWTs and PATs, but adds:
//   - aud = the MCP resource (RFC 8707 audience binding), so the token cannot be
//     replayed at other SolidPing surfaces, and
//   - the consented scopes (mcp / mcp:read), which the MCP handler enforces.
//
// refreshUID is the user_tokens.uid of the oauth_refresh grant backing this
// access token — embedded in Claims.RefreshUID so a client authenticated with
// only the access token can identify (isCurrent) and self-revoke the grant it
// rides on. ttl is supplied by the caller so the OAuth service controls the
// access-token lifetime independently of the dashboard's session expiry.
func (s *Service) GenerateMCPAccessToken(
	userUID, orgSlug string, scopes []string, audience string, ttl time.Duration, refreshUID string,
) (string, error) {
	now := time.Now()
	claims := &Claims{
		UserUID:    userUID,
		OrgSlug:    orgSlug,
		Scopes:     scopes,
		RefreshUID: refreshUID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    jwtIssuer,
			Audience:  jwt.ClaimStrings{audience},
			ID:        uuid.New().String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString([]byte(s.fullCfg.Auth.JWTSecret))
}

const refreshTokenSize = 32

func (s *Service) generateRefreshToken() (string, error) {
	randBytes := make([]byte, refreshTokenSize)
	if _, err := rand.Read(randBytes); err != nil {
		return "", err
	}

	// Include timestamp for uniqueness
	timestamp := time.Now().UnixNano()

	return fmt.Sprintf("%x_%x", randBytes, timestamp), nil
}

const patTokenSize = 24

func (s *Service) generatePATToken() (string, error) {
	randBytes := make([]byte, patTokenSize)
	if _, err := rand.Read(randBytes); err != nil {
		return "", err
	}

	return fmt.Sprintf("pat_%x", randBytes), nil
}

// OAuthLoginResponse contains the response for OAuth-based login.
type OAuthLoginResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
}

// GenerateTokensForOAuth generates access and refresh tokens for OAuth login.
// This is used when a user authenticates via an external OAuth provider (e.g., Slack).
func (s *Service) GenerateTokensForOAuth(
	ctx context.Context,
	user *models.User,
	org *models.Organization,
	role string,
) (*OAuthLoginResponse, error) {
	// Generate refresh token
	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate refresh token: %w", err)
	}

	// Store refresh token in database
	now := time.Now()
	refreshToken := models.NewUserToken(user.UID, &org.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := s.refreshTokenExpiry(ctx, org.UID, now, now)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{
		keyCreatedWith: map[string]any{
			keyMethod: "oauth",
		},
	}

	if err = s.db.CreateUserToken(ctx, refreshToken); err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
	}

	s.enforceSessionCap(ctx, user.UID)

	// Generate access token, bound to the new refresh-token row.
	accessToken, err := s.generateAccessToken(user.UID, org.Slug, role, refreshToken.UID)
	if err != nil {
		return nil, fmt.Errorf("failed to generate access token: %w", err)
	}

	// Update user last active timestamp
	if updateErr := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{LastActiveAt: &now}); updateErr != nil {
		slog.ErrorContext(ctx, "Failed to update user last_active_at", "error", updateErr, "userUID", user.UID)
	}

	return &OAuthLoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenValue,
		ExpiresIn:    int(s.cfg.AccessTokenExpiry.Seconds()),
	}, nil
}

// RegisterRequest contains the registration request data.
type RegisterRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RegisterResponse contains the response after registration.
type RegisterResponse struct {
	Message string `json:"message"`
}

const (
	registrationKeyPrefix       = "email_registration:"
	registrationTTL             = 3 * 24 * time.Hour
	inviteKeyPrefix             = "invite:"
	passwordResetKeyPrefix      = "password_reset:"
	passwordResetCountKeyPrefix = "password_reset_count:"
	passwordResetIPKeyPrefix    = "pwd_reset_rl:"
	passwordResetTTL            = 1 * time.Hour
	passwordResetIPWindow       = time.Minute
	passwordResetMaxPerUser     = 3
	passwordResetMaxPerIP       = 5
	minPasswordLength           = 8
	registrationTokenSize       = 32

	// Authenticated change-password rate limit. The endpoint sits behind a
	// valid session, but a stolen-but-unprivileged token must not become an
	// oracle for the current password, so the per-user attempt count is
	// capped over a rolling window.
	changePasswordCountKeyPrefix = "change_password_count:"
	changePasswordMaxPerUser     = 10
	changePasswordWindow         = 15 * time.Minute
)

// hashResetToken derives the storage key suffix for a plaintext reset
// token. Inputs are 32 random bytes hex-encoded (256 bits), so plain
// SHA-256 is sufficient — no salt or stretching needed.
func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

// Register creates a pending registration entry and sends a confirmation email.
func (s *Service) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	// Check if registration is enabled. Read the LIVE value: the
	// registration_email_pattern is applied by the systemconfig overlay AFTER
	// this Service was constructed, so the frozen s.cfg copy is stale (it would
	// report "disabled" even when an operator enabled registration via env or
	// the system-parameter API). This mirrors how auth.Handler reads it live.
	pattern := s.fullCfg.Auth.RegistrationEmailPattern
	if pattern == "" {
		return nil, ErrRegistrationDisabled
	}

	// Validate email against pattern
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid registration email pattern: %w", err)
	}

	if !re.MatchString(req.Email) {
		return nil, ErrEmailNotAllowed
	}

	// Validate password
	if len(req.Password) < minPasswordLength {
		return nil, fmt.Errorf("%w: password must be at least %d characters", ErrInvalidCredentials, minPasswordLength)
	}

	// Check if email is already taken
	existing, err := s.db.GetUserByEmail(ctx, req.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if existing != nil {
		return nil, ErrEmailAlreadyTaken
	}

	// Hash password
	hash, err := passwords.Hash(req.Password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Generate confirmation token
	tokenBytes := make([]byte, registrationTokenSize)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	token := hex.EncodeToString(tokenBytes)

	// Store in state entries
	stateValue := &models.JSONMap{
		keyToken:       token,
		keyEmail:       req.Email,
		keyName:        req.Name,
		"passwordHash": hash,
	}
	ttl := registrationTTL

	if err := s.db.SetStateEntry(ctx, nil, registrationKeyPrefix+req.Email, stateValue, &ttl); err != nil {
		return nil, fmt.Errorf("failed to store registration: %w", err)
	}

	// Send confirmation email asynchronously via the email job
	confirmURL := fmt.Sprintf("%s/dash0/confirm-registration/%s",
		s.fullCfg.Server.BaseURL, token)
	s.enqueueEmail(ctx, "", req.Email, "registration.html",
		map[string]any{"ConfirmURL": confirmURL},
	)

	return &RegisterResponse{Message: "Check your email to confirm your account"}, nil
}

// ConfirmRegistrationRequest contains the confirmation request data.
type ConfirmRegistrationRequest struct {
	Token string `json:"token"`
}

// ConfirmRegistration confirms a registration and creates the user.
//
//nolint:cyclop,funlen // Registration confirmation requires multiple steps
func (s *Service) ConfirmRegistration(ctx context.Context, token string) (*LoginResponse, error) {
	// Search state entries for matching token
	entries, err := s.db.ListStateEntries(ctx, nil, registrationKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list registration entries: %w", err)
	}

	var matchedEntry *models.StateEntry

	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}

		entryToken, ok := (*entry.Value)["token"].(string)
		if ok && entryToken == token {
			matchedEntry = entry

			break
		}
	}

	if matchedEntry == nil {
		return nil, ErrRegistrationExpired
	}

	// Extract registration data
	val := *matchedEntry.Value
	regEmail, _ := val["email"].(string)
	regName, _ := val[keyName].(string)
	regHash, _ := val["passwordHash"].(string)

	if regEmail == "" || regHash == "" {
		return nil, ErrRegistrationExpired
	}

	// Double-check email is not taken
	existing, err := s.db.GetUserByEmail(ctx, regEmail)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if existing != nil {
		// Delete the state entry
		_, _ = s.db.DeleteStateEntry(ctx, nil, matchedEntry.Key)

		return nil, ErrEmailAlreadyTaken
	}

	// Create user
	user := models.NewUser(regEmail)
	user.Name = regName
	user.PasswordHash = &regHash

	now := time.Now()
	user.EmailVerifiedAt = &now

	// Captured here — the moment the account actually exists — rather than at
	// Register, which only stashes a pending confirmation (spec 2026-08-02-08).
	if createErr := createUserAndCapture(ctx, s.db, user, signupMethodPassword); createErr != nil {
		return nil, fmt.Errorf("failed to create user: %w", createErr)
	}

	// Delete the state entry
	_, _ = s.db.DeleteStateEntry(ctx, nil, matchedEntry.Key)

	// Auto-join matching orgs
	s.autoJoinMatchingOrgs(ctx, user.UID, user.Email)

	// Try to resolve an org for login response
	members, _ := s.db.ListMembersByUser(ctx, user.UID)
	if len(members) == 0 {
		// No org to login to - return minimal response
		return &LoginResponse{
			TokenType: tokenTypeBearer,
			User: &UserInfo{
				UID:   user.UID,
				Email: user.Email,
				Name:  user.Name,
			},
		}, nil
	}

	// Get the first org
	org, err := s.db.GetOrganization(ctx, members[0].OrganizationUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization: %w", err)
	}

	role := string(members[0].Role)

	// Generate tokens
	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	refreshToken := models.NewUserToken(user.UID, &org.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := s.refreshTokenExpiry(ctx, org.UID, now, now)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{keyCreatedWith: map[string]any{"method": "registration"}}

	if err = s.db.CreateUserToken(ctx, refreshToken); err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
	}

	s.enforceSessionCap(ctx, user.UID)

	accessToken, err := s.generateAccessToken(user.UID, org.Slug, role, refreshToken.UID)
	if err != nil {
		return nil, err
	}

	return &LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenValue,
		ExpiresIn:    int(s.cfg.AccessTokenExpiry.Seconds()),
		TokenType:    tokenTypeBearer,
		User: &UserInfo{
			UID:   user.UID,
			Email: user.Email,
			Name:  user.Name,
			Role:  role,
		},
		Organization: newOrganizationInfo(org),
	}, nil
}

// autoJoinMatchingOrgs checks org-scoped registration email patterns and auto-joins matching orgs.
func (s *Service) autoJoinMatchingOrgs(ctx context.Context, userUID, userEmail string) {
	params, err := s.db.ListOrgParametersByKey(ctx, "registration.email_pattern")
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list org email patterns", "error", err)

		return
	}

	for _, param := range params {
		if param.OrganizationUID == nil {
			continue
		}

		patternVal, ok := param.Value["value"].(string)
		if !ok || patternVal == "" {
			continue
		}

		// Defensive: leftover unsafe patterns (set before validation existed)
		// must be skipped so they cannot adopt every signup. Log and move on
		// rather than blowing up the registration confirmation path.
		if err := validateAutoJoinRegex(patternVal); err != nil {
			slog.WarnContext(
				ctx, "skipping unsafe auto-join regex",
				"orgUID", *param.OrganizationUID, "error", err,
			)

			continue
		}

		re, err := regexp.Compile(patternVal)
		if err != nil {
			continue
		}

		if !re.MatchString(userEmail) {
			continue
		}

		// Check if already a member
		_, err = s.db.GetMemberByUserAndOrg(ctx, userUID, *param.OrganizationUID)
		if err == nil {
			continue // Already a member
		}

		// Skip orgs that are at their MaxUsers cap. Logged at INFO so
		// operators can see why an auto-join didn't happen.
		if err := s.CheckMembershipSlot(ctx, *param.OrganizationUID); err != nil {
			slog.InfoContext(ctx, "Skipping SSO auto-join, org at cap",
				"orgUID", *param.OrganizationUID, "userUID", userUID, "error", err)

			continue
		}

		// Create membership
		member := models.NewOrganizationMember(*param.OrganizationUID, userUID, models.MemberRoleUser)
		now := time.Now()
		member.JoinedAt = &now

		if createErr := s.db.CreateOrganizationMember(ctx, member); createErr != nil {
			slog.ErrorContext(ctx, "Failed to auto-join org", "error", createErr, "orgUID", *param.OrganizationUID)
		}
	}
}

// RequestPasswordResetRequest contains the password reset request data.
type RequestPasswordResetRequest struct {
	Email string `json:"email"`
}

// RequestPasswordResetResponse contains the password reset request response.
type RequestPasswordResetResponse struct {
	Message string `json:"message"`
}

// ResetPasswordRequest contains the password reset confirmation data.
type ResetPasswordRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// ResetPasswordResponse contains the password reset confirmation response.
type ResetPasswordResponse struct {
	Message string `json:"message"`
}

// RequestPasswordReset creates a password reset token and sends a reset email.
// Always returns success to the caller (anti-enumeration); the only error
// path the handler must surface is ErrRateLimited so it can map to 429.
//
// remoteAddr is the request's client IP (best-effort — passed through from
// the handler). Used solely for the per-IP rate limit; an empty string
// disables that check.
func (s *Service) RequestPasswordReset(
	ctx context.Context, req RequestPasswordResetRequest, remoteAddr string,
) (*RequestPasswordResetResponse, error) {
	successMsg := &RequestPasswordResetResponse{
		Message: "If an account exists with that email, a reset link has been sent.",
	}

	// Per-IP rate limit. We always check this first so abusers paying no
	// attention to the success-shaped response also can't drive load on
	// the user lookup or state-entry write.
	if remoteAddr != "" {
		exceeded, err := s.bumpResetIPCounter(ctx, remoteAddr)
		if err != nil {
			slog.WarnContext(ctx, "Failed to track password-reset IP counter", "error", err)
		} else if exceeded {
			return nil, ErrRateLimited
		}
	}

	// Look up user by email — return success even if not found (anti-enumeration)
	user, _ := s.db.GetUserByEmail(ctx, req.Email)
	if user == nil || user.PasswordHash == nil || *user.PasswordHash == "" {
		return successMsg, nil
	}

	// Per-user cap: drop silently above the limit. Returning success keeps
	// the response shape uniform with the unknown-email path so abusers
	// can't tell the difference.
	overCap, err := s.bumpResetUserCounter(ctx, user.UID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to track password-reset user counter", "error", err, "userUID", user.UID)
	} else if overCap {
		return successMsg, nil
	}

	// Generate reset token
	tokenBytes := make([]byte, registrationTokenSize)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	token := hex.EncodeToString(tokenBytes)
	tokenHash := hashResetToken(token)

	// Store at password_reset:<sha256(token)> with userUid only. The
	// plaintext token never lands on disk; a leaked DB snapshot has no
	// way to mint a valid reset URL.
	stateValue := &models.JSONMap{stateValueKeyUserUID: user.UID}
	ttl := passwordResetTTL

	if err := s.db.SetStateEntry(ctx, nil, passwordResetKeyPrefix+tokenHash, stateValue, &ttl); err != nil {
		return nil, fmt.Errorf("failed to store password reset: %w", err)
	}

	// Send reset email asynchronously via the email job
	resetURL := fmt.Sprintf("%s/dash0/reset-password/%s",
		s.fullCfg.Server.BaseURL, token)
	s.enqueueEmail(ctx, "", req.Email, "password-reset.html",
		map[string]any{"ResetURL": resetURL},
	)

	return successMsg, nil
}

// counterValue extracts the integer value from a state entry that holds
// a {"count": N} payload. JSON marshaling promotes ints to float64 so we
// accept both shapes; anything else is treated as zero.
func counterValue(entry *models.StateEntry) int {
	if entry == nil || entry.Value == nil {
		return 0
	}

	switch raw := (*entry.Value)[stateValueKeyCount].(type) {
	case float64:
		return int(raw)
	case int:
		return raw
	case int64:
		return int(raw)
	default:
		return 0
	}
}

// bumpCounter loads / increments / persists a counter at the given state
// key, scoped to the supplied TTL. Reports whether the count was already
// at or above the limit (in which case no bump happens).
func (s *Service) bumpCounter(
	ctx context.Context, key string, limit int, ttl time.Duration,
) (bool, error) {
	current, err := s.db.GetStateEntry(ctx, nil, key)
	if err != nil {
		return false, err
	}

	count := counterValue(current)

	if count >= limit {
		return true, nil
	}

	count++
	value := &models.JSONMap{stateValueKeyCount: count}
	scopedTTL := ttl
	if err := s.db.SetStateEntry(ctx, nil, key, value, &scopedTTL); err != nil {
		return false, err
	}

	return false, nil
}

// bumpResetUserCounter increments (or seeds) the per-user reset counter
// and reports whether the new value exceeds the configured cap. The
// counter shares the reset TTL so it ages out with the entries it bounds.
func (s *Service) bumpResetUserCounter(ctx context.Context, userUID string) (bool, error) {
	return s.bumpCounter(ctx,
		passwordResetCountKeyPrefix+userUID, passwordResetMaxPerUser, passwordResetTTL)
}

// bumpResetIPCounter increments (or seeds) the per-IP reset counter on a
// 1-minute window and reports whether the new value exceeds the cap.
func (s *Service) bumpResetIPCounter(ctx context.Context, remoteAddr string) (bool, error) {
	return s.bumpCounter(ctx,
		passwordResetIPKeyPrefix+remoteAddr, passwordResetMaxPerIP, passwordResetIPWindow)
}

// ResetPassword validates a reset token and sets a new password.
//
// Validation order matters for the regression guarantees in this spec:
// password length is checked *before* the state entry is touched so a
// rejected weak password doesn't burn the user's reset opportunity.
func (s *Service) ResetPassword(ctx context.Context, req ResetPasswordRequest) (*ResetPasswordResponse, error) {
	if len(req.Password) < minPasswordLength {
		return nil, fmt.Errorf("%w: password must be at least %d characters",
			ErrInvalidCredentials, minPasswordLength)
	}

	tokenHash := hashResetToken(req.Token)

	entry, err := s.db.GetStateEntry(ctx, nil, passwordResetKeyPrefix+tokenHash)
	if err != nil {
		return nil, fmt.Errorf("failed to load password reset entry: %w", err)
	}

	if entry == nil || entry.Value == nil {
		return nil, ErrPasswordResetExpired
	}

	userUID, ok := (*entry.Value)[stateValueKeyUserUID].(string)
	if !ok || userUID == "" {
		return nil, ErrPasswordResetExpired
	}

	user, err := s.db.GetUser(ctx, userUID)
	if err != nil || user == nil {
		return nil, ErrPasswordResetExpired
	}

	hash, err := passwords.Hash(req.Password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	if err := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{PasswordHash: &hash}); err != nil {
		return nil, fmt.Errorf("failed to update password: %w", err)
	}

	// Best-effort cleanup. We log but never fail the reset on these:
	// the password is already rotated and the entry is single-use, so
	// stale state is the worst outcome and it ages out at the TTL.
	if _, err := s.db.DeleteStateEntry(ctx, nil, entry.Key); err != nil {
		slog.ErrorContext(ctx, "Failed to delete password reset entry", "error", err)
	}

	if _, err := s.db.DeleteStateEntry(ctx, nil, passwordResetCountKeyPrefix+user.UID); err != nil {
		slog.DebugContext(ctx, "Failed to delete password reset counter", "error", err)
	}

	// Revoke active refresh tokens so an attacker who triggered the reset
	// from a compromised session can't keep using the old session. PATs
	// (TokenTypePAT) are intentionally preserved — they're separately
	// managed credentials the user controls from the tokens UI.
	s.revokeRefreshTokensForUser(ctx, user.UID)

	// Confirmation email so the legitimate user sees a record of the
	// change even if the attacker controls the password-reset link.
	s.enqueueEmail(ctx, "", user.Email, "password-changed.html",
		map[string]any{"ChangedAt": time.Now().UTC().Format(time.RFC1123)},
	)

	return &ResetPasswordResponse{
		Message: "Your password has been reset. You can now log in.",
	}, nil
}

// ChangePasswordRequest is the body of POST /api/v1/auth/change-password.
//
// CurrentPassword is required when the account already has a password, and
// ignored when it does not (the SSO-only "set a password" case).
type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// ChangePasswordResponse confirms the rotation.
type ChangePasswordResponse struct {
	Message string `json:"message"`
}

// bumpChangePasswordCounter increments the per-user change-password counter
// and reports whether the cap for the current window is already reached.
func (s *Service) bumpChangePasswordCounter(ctx context.Context, userUID string) (bool, error) {
	return s.bumpCounter(ctx,
		changePasswordCountKeyPrefix+userUID, changePasswordMaxPerUser, changePasswordWindow)
}

// ChangePassword rotates the password of an already-authenticated user.
//
// It is the authenticated twin of ResetPassword: identical post-change side
// effects (other sessions revoked, PATs preserved, password-changed.html
// confirmation email) so it does not matter which path rotated the password.
//
// Two behaviors are deliberate and load-bearing:
//
//   - When the account has no password yet (signed up through an identity
//     provider), currentPassword is ignored and this *sets* the initial
//     password. Without it an SSO user is permanently locked to their IdP.
//   - currentRefreshUID (the caller's own Claims.RefreshUID) is spared by the
//     revocation sweep. Changing your password from the settings page must not
//     bounce you to the login screen; every *other* session still dies.
func (s *Service) ChangePassword(
	ctx context.Context, userUID, currentRefreshUID string, req ChangePasswordRequest,
) (*ChangePasswordResponse, error) {
	// Rate-limit first: the current-password check below is the brute-force
	// surface, so it must never be reachable an unbounded number of times
	// from a single stolen session token.
	limited, err := s.bumpChangePasswordCounter(ctx, userUID)
	if err != nil {
		return nil, fmt.Errorf("failed to bump change-password counter: %w", err)
	}

	if limited {
		return nil, ErrRateLimited
	}

	user, err := s.db.GetUser(ctx, userUID)
	if err != nil || user == nil {
		return nil, ErrUserNotFound
	}

	hasPassword := user.PasswordHash != nil && *user.PasswordHash != ""

	if hasPassword {
		// Always run the verify — even for an empty currentPassword — so the
		// "field missing" and "field wrong" cases cost the same time.
		if !passwords.Verify(req.CurrentPassword, *user.PasswordHash) {
			return nil, ErrInvalidCurrentPassword
		}
	}

	if len(req.NewPassword) < minPasswordLength {
		return nil, fmt.Errorf("%w: password must be at least %d characters",
			ErrInvalidCredentials, minPasswordLength)
	}

	// A no-op rotation that reports success is misleading: the user believes
	// their password changed and the confirmation email says so too.
	if hasPassword && passwords.Verify(req.NewPassword, *user.PasswordHash) {
		return nil, fmt.Errorf("%w: new password must be different from the current one",
			ErrInvalidCredentials)
	}

	hash, err := passwords.Hash(req.NewPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	if err := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{PasswordHash: &hash}); err != nil {
		return nil, fmt.Errorf("failed to update password: %w", err)
	}

	// Best-effort: the rotation already succeeded, so a stale counter is the
	// worst outcome and it ages out at the window TTL anyway.
	if _, err := s.db.DeleteStateEntry(ctx, nil, changePasswordCountKeyPrefix+user.UID); err != nil {
		slog.DebugContext(ctx, "Failed to delete change-password counter", "error", err)
	}

	// Same policy as the reset flow: session refresh tokens die, PATs
	// (TokenTypePAT) survive as separately managed credentials — except the
	// caller's own grant, which must keep working.
	s.revokeRefreshTokensForUserExcept(ctx, user.UID, currentRefreshUID)

	s.enqueueEmail(ctx, "", user.Email, "password-changed.html",
		map[string]any{"ChangedAt": time.Now().UTC().Format(time.RFC1123)},
	)

	return &ChangePasswordResponse{
		Message: "Your password has been updated.",
	}, nil
}

// revokeRefreshTokensForUser deletes every session refresh token attached to
// the user. PATs are deliberately untouched. Errors are logged, never fatal —
// stateless access tokens (JWTs) can't be revoked synchronously anyway,
// so the goal here is best-effort hygiene, not a security boundary.
func (s *Service) revokeRefreshTokensForUser(ctx context.Context, userUID string) {
	s.revokeRefreshTokensForUserExcept(ctx, userUID, "")
}

// revokeRefreshTokensForUserExcept is revokeRefreshTokensForUser with one
// refresh-token row spared (pass "" to spare none). Used by ChangePassword so
// the caller who just rotated their own password keeps their session.
func (s *Service) revokeRefreshTokensForUserExcept(ctx context.Context, userUID, exceptTokenUID string) {
	tokens, err := s.db.ListUserTokensByType(ctx, userUID, models.TokenTypeRefresh)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list tokens for revocation",
			"error", err, "userUID", userUID, "type", models.TokenTypeRefresh)

		return
	}

	for _, token := range tokens {
		if exceptTokenUID != "" && token.UID == exceptTokenUID {
			continue
		}

		if _, delErr := s.db.DeleteUserToken(ctx, token.UID); delErr != nil {
			slog.ErrorContext(ctx, "Failed to delete token",
				"error", delErr, "tokenUID", token.UID, "type", models.TokenTypeRefresh)
		}
	}
}

// revokeUserTokensOfType best-effort soft-deletes every live token of one
// type for a user. Used for session refresh tokens (password reset) and
// OAuth refresh grants (logout, PAT revoke). Errors are logged, never fatal.
func (s *Service) revokeUserTokensOfType(ctx context.Context, userUID string, tokenType models.TokenType) {
	tokens, err := s.db.ListUserTokensByType(ctx, userUID, tokenType)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list tokens for revocation",
			"error", err, "userUID", userUID, "type", tokenType)

		return
	}

	for _, token := range tokens {
		if _, delErr := s.db.DeleteUserToken(ctx, token.UID); delErr != nil {
			slog.ErrorContext(ctx, "Failed to delete token",
				"error", delErr, "tokenUID", token.UID, "type", tokenType)
		}
	}
}

// CreateOrgRequest contains the request data for creating an organization.
type CreateOrgRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// OrgResponse contains the response for org creation. It carries a fresh
// org-scoped session (accessToken/refreshToken/expiresIn/tokenType)
// alongside the org identity — the caller who creates their first org has no
// other way to obtain a token whose orgSlug claim matches the new org (see
// the 2026-07-08 "create-org missing org-scoped token" spec).
type OrgResponse struct {
	UID          string  `json:"uid"`
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	LogoURL      *string `json:"logoUrl"`
	AccessToken  string  `json:"accessToken"`
	RefreshToken string  `json:"refreshToken"`
	ExpiresIn    int     `json:"expiresIn"`
	TokenType    string  `json:"tokenType"`
}

// The org-slug rule itself lives in internal/orgslug — see orgslug.IsValid.
// Do not restate the pattern here: it is also consulted by the Telegram
// org-qualified-reference parser, and a second copy would drift silently.

// CreateOrg creates a new organization, makes the caller its OWNER, and mints a
// session scoped to the new org — mirroring SwitchOrg, since this is
// structurally the same "hand the caller a token for an org other than the
// one in their current claims" problem.
//
// The org is always created for the authenticated caller: userUID comes from
// the validated JWT claims, never from the request body, so there is no way to
// create an org on somebody else's behalf.
//
// Slug availability is decided by GetOrganizationBySlug, which filters
// `deleted_at IS NULL` — so a deleted org's slug is free for reuse, matching
// the partial unique index on organizations(slug). A slug held only as a
// renamed org's previous slug is likewise free, and claiming it releases that
// alias (spec 2026-08-08-12).
func (s *Service) CreateOrg(
	ctx context.Context, userUID string, req CreateOrgRequest, authContext Context,
) (*OrgResponse, error) {
	// Validate slug
	if !orgslug.IsValid(req.Slug) {
		return nil, ErrInvalidOrgSlug
	}

	// Check slug availability
	existing, err := s.db.GetOrganizationBySlug(ctx, req.Slug)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if existing != nil {
		return nil, ErrOrgSlugTaken
	}

	// Create organization
	org := &models.Organization{
		UID:  uuid.New().String(),
		Slug: req.Slug,
		Name: req.Name,
	}
	now := time.Now()
	org.CreatedAt = now
	org.UpdatedAt = now

	if createOrgErr := s.db.CreateOrganization(ctx, org); createOrgErr != nil {
		return nil, fmt.Errorf("failed to create organization: %w", createOrgErr)
	}

	// The creator owns the org they just created: owner outranks admin, and
	// only an owner may delete the org or grant/revoke ownership (spec
	// 2026-08-08-11). Without this the creator becomes indistinguishable from
	// any admin they later promote.
	member := models.NewOrganizationMember(org.UID, userUID, models.MemberRoleOwner)
	member.JoinedAt = &now

	if createMemberErr := s.db.CreateOrganizationMember(ctx, member); createMemberErr != nil {
		return nil, fmt.Errorf("failed to create membership: %w", createMemberErr)
	}

	activation.Emit(ctx, s.db, org.UID,
		models.EventTypeOrgActivationSignupCompleted,
		activation.SourceRegularForm, userUID)

	// Mint a session scoped to the new org, exactly like SwitchOrg: a fresh
	// refresh-token row plus an access token whose orgSlug claim is the new
	// org's slug. Without this, the caller's existing token (orgSlug "" for
	// a zero-org user, or their previous org otherwise) 403s on every
	// org-scoped call to the org they just created.
	session, err := s.mintOrgSession(ctx, userUID, org, string(models.MemberRoleOwner), authContext)
	if err != nil {
		return nil, err
	}

	return &OrgResponse{
		UID:          org.UID,
		Slug:         org.Slug,
		Name:         org.Name,
		LogoURL:      org.LogoURL,
		AccessToken:  session.AccessToken,
		RefreshToken: session.RefreshToken,
		ExpiresIn:    session.ExpiresIn,
		TokenType:    session.TokenType,
	}, nil
}

// InviteRequest contains the request data for creating an invitation.
type InviteRequest struct {
	Email     string `json:"email"`
	Role      string `json:"role"`
	ExpiresIn string `json:"expiresIn"` // "1h", "6h", "12h", "24h", "48h", "1w" (default: "24h")
	App       string `json:"app"`       // "dash0" or "dash" (default: "dash0")
}

// getAllowedInviteExpirations returns the accepted expiresIn values mapped to durations.
func getAllowedInviteExpirations() map[string]time.Duration {
	return map[string]time.Duration{
		"1h":            time.Hour,
		"6h":            6 * time.Hour,
		"12h":           12 * time.Hour,
		durationLabel24: 24 * time.Hour,
		"48h":           48 * time.Hour,
		"1w":            7 * 24 * time.Hour,
	}
}

// ErrInvalidExpiresIn is returned when an invalid expiresIn value is provided.
var ErrInvalidExpiresIn = errors.New("invalid expiresIn: must be one of 1h, 6h, 12h, 24h, 48h, 1w")

// ErrInvalidApp is returned when an invalid app value is provided.
var ErrInvalidApp = errors.New("invalid app: must be one of dash0, dash")

// InviteResponse contains the response after creating an invitation.
type InviteResponse struct {
	UID       string    `json:"uid"`
	Token     string    `json:"token"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	InviteURL string    `json:"inviteUrl"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// InviteListItem represents an invitation in a list response.
type InviteListItem struct {
	UID       string     `json:"uid"`
	Email     string     `json:"email"`
	Role      string     `json:"role"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// InviteListResponse contains a list of invitations.
type InviteListResponse struct {
	Data []InviteListItem `json:"data"`
}

// InviteInfoResponse contains public info about an invitation.
type InviteInfoResponse struct {
	OrgName string `json:"orgName"`
	OrgSlug string `json:"orgSlug"`
	Role    string `json:"role"`
	Email   string `json:"email"`
}

// AcceptInviteRequest contains the request data for accepting an invitation.
type AcceptInviteRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// CreateInvitation creates an invitation for a user to join an organization.
func (s *Service) CreateInvitation(
	ctx context.Context, orgSlug, inviterUID string, req InviteRequest,
) (*InviteResponse, error) {
	// Validate role. `owner` is deliberately NOT invitable: ownership is granted
	// by an owner on the members page, where the caller's owner role is checked
	// live (spec 2026-08-08-11). An invitation is consumed later, by whoever
	// holds the link, long after the inviter's role could have changed.
	if role := models.MemberRole(req.Role); role != models.MemberRoleAdmin &&
		role != models.MemberRoleUser && role != models.MemberRoleViewer {
		return nil, fmt.Errorf("%w: invalid role", ErrInvalidCredentials)
	}

	// Validate app
	if req.App != appNameDash0 && req.App != "dash" {
		return nil, ErrInvalidApp
	}

	// Resolve expiration duration
	ttl, ok := getAllowedInviteExpirations()[req.ExpiresIn]
	if !ok {
		return nil, ErrInvalidExpiresIn
	}

	// Get organization
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Generate token
	tokenBytes := make([]byte, registrationTokenSize)

	if _, randErr := rand.Read(tokenBytes); randErr != nil {
		return nil, fmt.Errorf("failed to generate token: %w", randErr)
	}

	token := hex.EncodeToString(tokenBytes)

	// Store in state entries (org-scoped)
	stateValue := &models.JSONMap{
		keyToken:     token,
		keyEmail:     req.Email,
		"role":       req.Role,
		"inviterUID": inviterUID,
	}

	stateKey := inviteKeyPrefix + token

	if storeErr := s.db.SetStateEntry(ctx, &org.UID, stateKey, stateValue, &ttl); storeErr != nil {
		return nil, fmt.Errorf("failed to store invitation: %w", storeErr)
	}

	// Get the state entry to return its UID
	entry, err := s.db.GetStateEntry(ctx, &org.UID, stateKey)
	if err != nil || entry == nil {
		return nil, fmt.Errorf("failed to retrieve invitation: %w", err)
	}

	baseURL := s.fullCfg.Server.BaseURL
	inviteURL := fmt.Sprintf("%s/%s/invite/%s", baseURL, req.App, token)

	expiresAt := time.Now().Add(ttl)

	// Send invitation email
	s.sendInvitationEmail(ctx, org.UID, req.Email, inviterUID, org.Name, req.Role, inviteURL)

	return &InviteResponse{
		UID:       entry.UID,
		Token:     token,
		Email:     req.Email,
		Role:      req.Role,
		InviteURL: inviteURL,
		ExpiresAt: expiresAt,
	}, nil
}

// ListInvitations lists pending invitations for an organization.
func (s *Service) ListInvitations(ctx context.Context, orgSlug string) (*InviteListResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	entries, err := s.db.ListStateEntries(ctx, &org.UID, inviteKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list invitations: %w", err)
	}

	items := make([]InviteListItem, 0, len(entries))

	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}

		val := *entry.Value

		items = append(items, InviteListItem{
			UID:       entry.UID,
			Email:     stringFromMap(val, "email"),
			Role:      stringFromMap(val, "role"),
			ExpiresAt: entry.ExpiresAt,
			CreatedAt: entry.CreatedAt,
		})
	}

	return &InviteListResponse{Data: items}, nil
}

// RevokeInvitation revokes an invitation by its UID.
func (s *Service) RevokeInvitation(ctx context.Context, orgSlug, invitationUID string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	// Find the entry by UID in the list
	entries, err := s.db.ListStateEntries(ctx, &org.UID, inviteKeyPrefix)
	if err != nil {
		return fmt.Errorf("failed to list invitations: %w", err)
	}

	for _, entry := range entries {
		if entry.UID == invitationUID {
			_, err := s.db.DeleteStateEntry(ctx, &org.UID, entry.Key)

			return err
		}
	}

	return ErrInvitationNotFound
}

// GetInviteInfo returns public information about an invitation.
func (s *Service) GetInviteInfo(ctx context.Context, token string) (*InviteInfoResponse, error) {
	stateKey := inviteKeyPrefix + token

	// Search across all orgs for the invite
	// We need to find which org this invite belongs to
	orgs, err := s.db.ListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list organizations: %w", err)
	}

	for _, org := range orgs {
		entry, getErr := s.db.GetStateEntry(ctx, &org.UID, stateKey)
		if getErr != nil || entry == nil {
			continue
		}

		if entry.Value == nil {
			continue
		}

		val := *entry.Value

		return &InviteInfoResponse{
			OrgName: org.Name,
			OrgSlug: org.Slug,
			Role:    stringFromMap(val, "role"),
			Email:   maskEmail(stringFromMap(val, "email")),
		}, nil
	}

	return nil, ErrInvitationNotFound
}

// AcceptInvite accepts an invitation and creates/authenticates the user.
//
//nolint:cyclop,funlen // Invitation acceptance requires multiple steps
func (s *Service) AcceptInvite(ctx context.Context, req AcceptInviteRequest) (*LoginResponse, error) {
	stateKey := inviteKeyPrefix + req.Token

	// Find the invitation across orgs
	orgs, err := s.db.ListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list organizations: %w", err)
	}

	var matchedEntry *models.StateEntry
	var matchedOrg *models.Organization

	for _, org := range orgs {
		entry, getErr := s.db.GetStateEntry(ctx, &org.UID, stateKey)
		if getErr != nil || entry == nil {
			continue
		}

		matchedEntry = entry
		matchedOrg = org

		break
	}

	if matchedEntry == nil || matchedOrg == nil {
		return nil, ErrInvitationNotFound
	}

	val := *matchedEntry.Value
	inviteEmail := stringFromMap(val, "email")
	inviteRole := stringFromMap(val, "role")
	inviterUID := stringFromMap(val, "inviterUID")

	// Check if user exists
	user, err := s.db.GetUserByEmail(ctx, inviteEmail)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	now := time.Now()

	if user == nil {
		// Create new user
		if len(req.Password) < minPasswordLength {
			return nil, fmt.Errorf("%w: password must be at least %d characters", ErrInvalidCredentials, minPasswordLength)
		}

		hash, hashErr := passwords.Hash(req.Password)
		if hashErr != nil {
			return nil, fmt.Errorf("failed to hash password: %w", hashErr)
		}

		user = models.NewUser(inviteEmail)
		user.Name = req.Name
		user.PasswordHash = &hash
		user.EmailVerifiedAt = &now

		if createErr := createUserAndCapture(ctx, s.db, user, signupMethodInvite); createErr != nil {
			return nil, fmt.Errorf("failed to create user: %w", createErr)
		}
	}

	// Check existing membership
	_, err = s.db.GetMemberByUserAndOrg(ctx, user.UID, matchedOrg.UID)
	if err == nil {
		// Already a member, just clean up and login
		_, _ = s.db.DeleteStateEntry(ctx, &matchedOrg.UID, stateKey)
	} else {
		// Enforce the MaxUsers cap before adding this member. Invitation
		// acceptance is a membership-creation path, so it must honor the
		// same seat limit as the SSO join paths.
		if slotErr := s.CheckMembershipSlot(ctx, matchedOrg.UID); slotErr != nil {
			return nil, slotErr
		}

		// Create membership
		role := models.MemberRole(inviteRole)
		member := models.NewOrganizationMember(matchedOrg.UID, user.UID, role)
		member.JoinedAt = &now
		if inviterUID != "" {
			member.InvitedByUID = &inviterUID
			member.InvitedAt = &now
		}

		if createErr := s.db.CreateOrganizationMember(ctx, member); createErr != nil {
			return nil, fmt.Errorf("failed to create membership: %w", createErr)
		}

		// Delete the invitation
		_, _ = s.db.DeleteStateEntry(ctx, &matchedOrg.UID, stateKey)
	}

	// Auto-join matching orgs for new users
	s.autoJoinMatchingOrgs(ctx, user.UID, user.Email)

	// Get the actual membership role (might differ if already existed)
	membership, err := s.db.GetMemberByUserAndOrg(ctx, user.UID, matchedOrg.UID)
	if err != nil {
		return nil, fmt.Errorf("failed to get membership: %w", err)
	}

	role := string(membership.Role)

	// Generate tokens
	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	refreshToken := models.NewUserToken(user.UID, &matchedOrg.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := s.refreshTokenExpiry(ctx, matchedOrg.UID, now, now)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{keyCreatedWith: map[string]any{"method": "invitation"}}

	if err = s.db.CreateUserToken(ctx, refreshToken); err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
	}

	s.enforceSessionCap(ctx, user.UID)

	accessToken, err := s.generateAccessToken(user.UID, matchedOrg.Slug, role, refreshToken.UID)
	if err != nil {
		return nil, err
	}

	orgSummaries, errOrgs := s.getOrganizationsForUser(ctx, user.UID)
	if errOrgs != nil {
		return nil, fmt.Errorf("failed to list user organizations: %w", errOrgs)
	}

	return &LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenValue,
		ExpiresIn:    int(s.cfg.AccessTokenExpiry.Seconds()),
		TokenType:    tokenTypeBearer,
		User: &UserInfo{
			UID:   user.UID,
			Email: user.Email,
			Name:  user.Name,
			Role:  role,
		},
		Organization:  newOrganizationInfo(matchedOrg),
		Organizations: orgSummaries,
	}, nil
}

// OrgSettingsResponse contains org settings.
type OrgSettingsResponse struct {
	RegistrationEmailPattern string `json:"registrationEmailPattern"`
	// SessionMaxDurationSeconds is this org's auth.session_max_duration
	// override, in seconds, or nil when the org has no override and
	// inherits the system-wide value (spec B.2/B.4). The UI shows the
	// effective/inherited value separately via
	// GET /api/v1/system/parameters (super-admin) — this field is only the
	// org-level override itself, so the settings page can distinguish "not
	// set" from "explicitly set to the same number".
	SessionMaxDurationSeconds *int `json:"sessionMaxDurationSeconds,omitempty"`
	// DefaultEscalationPolicyUID is the org-wide fallback escalation policy
	// applied to checks that resolve to no policy of their own (check → group →
	// org default → none). nil/absent = no org default (legacy behavior).
	DefaultEscalationPolicyUID *string `json:"defaultEscalationPolicyUid,omitempty"`
	// InheritingCheckCount is how many of the org's live checks currently
	// resolve to no policy of their own — the blast radius of setting or
	// changing DefaultEscalationPolicyUID. Always present.
	InheritingCheckCount int `json:"inheritingCheckCount"`
}

// GetOrgSettings returns settings for an organization.
func (s *Service) GetOrgSettings(ctx context.Context, orgSlug string) (*OrgSettingsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	param, err := s.db.GetOrgParameter(ctx, org.UID, "registration.email_pattern")
	if err != nil {
		return nil, err
	}

	pattern := ""
	if param != nil {
		if v, ok := param.Value["value"].(string); ok {
			pattern = v
		}
	}

	sessionParam, err := s.db.GetOrgParameter(ctx, org.UID, string(systemconfig.KeySessionMaxDuration))
	if err != nil {
		return nil, err
	}

	var sessionMaxDurationSeconds *int
	if seconds, ok := parseParamSeconds(sessionParam); ok {
		sessionMaxDurationSeconds = &seconds
	}

	inheritingCount, err := s.db.CountChecksInheritingOrgDefault(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	return &OrgSettingsResponse{
		RegistrationEmailPattern:   pattern,
		SessionMaxDurationSeconds:  sessionMaxDurationSeconds,
		DefaultEscalationPolicyUID: org.DefaultEscalationPolicyUID,
		InheritingCheckCount:       inheritingCount,
	}, nil
}

// UpdateOrgSettingsRequest contains the request data for updating org settings.
type UpdateOrgSettingsRequest struct {
	RegistrationEmailPattern *string `json:"registrationEmailPattern"`
	// SessionMaxDurationSeconds, when present: a value <= 0 clears the org
	// override (the org falls back to the system-wide value); a positive
	// value sets/replaces it. Omit the field entirely to leave the
	// override untouched.
	SessionMaxDurationSeconds *int `json:"sessionMaxDurationSeconds"`
	// DefaultEscalationPolicyUID, when present: an empty string clears the org
	// default (checks fall back to no policy); a non-empty UID sets it (the UID
	// must be a policy in this org). Omit the field to leave it untouched.
	DefaultEscalationPolicyUID *string `json:"defaultEscalationPolicyUid"`
}

// UpdateOrgSettings updates settings for an organization.
func (s *Service) UpdateOrgSettings(
	ctx context.Context, orgSlug string, req UpdateOrgSettingsRequest,
) (*OrgSettingsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	if req.RegistrationEmailPattern != nil {
		if updateErr := s.updateEmailPattern(ctx, org.UID, *req.RegistrationEmailPattern); updateErr != nil {
			return nil, updateErr
		}
	}

	if req.SessionMaxDurationSeconds != nil {
		if updateErr := s.updateSessionMaxDuration(ctx, org.UID, *req.SessionMaxDurationSeconds); updateErr != nil {
			return nil, updateErr
		}
	}

	if req.DefaultEscalationPolicyUID != nil {
		if updateErr := s.updateDefaultEscalationPolicy(ctx, org.UID, *req.DefaultEscalationPolicyUID); updateErr != nil {
			return nil, updateErr
		}
	}

	return s.GetOrgSettings(ctx, orgSlug)
}

// updateDefaultEscalationPolicy sets or clears the org's default escalation
// policy. An empty uid clears it; a non-empty uid must reference a policy that
// lives in this org (else ErrInvalidEscalationPolicy). Resolution of in-flight
// incidents is unaffected — the default only applies at future incident-open.
func (s *Service) updateDefaultEscalationPolicy(ctx context.Context, orgUID, policyUID string) error {
	if policyUID == "" {
		return s.db.UpdateOrganization(ctx, orgUID, models.OrganizationUpdate{
			ClearDefaultEscalationPolicyUID: true,
		})
	}

	// Guard against pointing the org default at a policy from another org (or a
	// non-existent/deleted one); GetEscalationPolicy is org-scoped.
	if _, err := s.db.GetEscalationPolicy(ctx, orgUID, policyUID); err != nil {
		return ErrInvalidEscalationPolicy
	}

	return s.db.UpdateOrganization(ctx, orgUID, models.OrganizationUpdate{
		DefaultEscalationPolicyUID: &policyUID,
	})
}

func (s *Service) updateEmailPattern(ctx context.Context, orgUID, pattern string) error {
	if pattern == "" {
		return s.db.DeleteOrgParameter(ctx, orgUID, "registration.email_pattern")
	}

	if err := validateAutoJoinRegex(pattern); err != nil {
		return err
	}

	return s.db.SetOrgParameter(ctx, orgUID, "registration.email_pattern", pattern, false)
}

// updateSessionMaxDuration sets or clears the org's auth.session_max_duration
// override. seconds <= 0 clears it (the org reverts to inheriting the
// system-wide value); a positive value sets/replaces it.
func (s *Service) updateSessionMaxDuration(ctx context.Context, orgUID string, seconds int) error {
	if seconds <= 0 {
		return s.db.DeleteOrgParameter(ctx, orgUID, string(systemconfig.KeySessionMaxDuration))
	}

	return s.db.SetOrgParameter(ctx, orgUID, string(systemconfig.KeySessionMaxDuration), seconds, false)
}

// scopesFromProperties extracts the scopes list previously stored on a
// PAT's Properties JSONMap. JSONMap round-trips through json.Unmarshal,
// so a stored []string comes back as []any of strings; we coerce it
// back. Unknown shapes return nil (= no scope restrictions).
func scopesFromProperties(props models.JSONMap) []string {
	raw, ok := props[keyScopes]
	if !ok {
		return nil
	}

	switch typed := raw.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, v := range typed {
			if s, isStr := v.(string); isStr {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func stringFromMap(m models.JSONMap, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}

	return ""
}

func maskEmail(email string) string {
	if email == "" {
		return ""
	}

	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return email
	}

	local := parts[0]
	if len(local) <= 2 {
		return local[:1] + "***@" + parts[1]
	}

	return local[:2] + "***@" + parts[1]
}

func (s *Service) sendInvitationEmail(
	ctx context.Context, orgUID, recipientEmail, inviterUID, orgName, role, inviteURL string,
) {
	if recipientEmail == "" {
		return
	}

	inviterName := s.getInviterName(ctx, inviterUID)

	s.enqueueEmail(ctx, orgUID, recipientEmail, "invitation.html",
		map[string]any{
			emailKeyOrgName: orgName,
			"Role":          role,
			"InviterName":   inviterName,
			"InviteURL":     inviteURL,
		},
	)
}

func (s *Service) getInviterName(ctx context.Context, inviterUID string) string {
	inviter, err := s.db.GetUser(ctx, inviterUID)
	if err != nil || inviter == nil {
		return ""
	}

	if inviter.Name != "" {
		return inviter.Name
	}

	return inviter.Email
}

// --- 2FA Methods ---

const (
	twoFAPurpose         = "2fa"
	twoFATempTokenExpiry = 5 * time.Minute
	recoveryCodeCount    = 10
	recoveryCodeBytes    = 5 // 10 hex chars
)

// generate2FATempToken creates a short-lived JWT for the 2FA verification step.
func (s *Service) generate2FATempToken(userUID, orgSlug, role string) (string, error) {
	now := time.Now()
	claims := &TwoFAClaims{
		UserUID: userUID,
		OrgSlug: orgSlug,
		Role:    role,
		Purpose: twoFAPurpose,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(twoFATempTokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    jwtIssuer,
			ID:        uuid.New().String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString([]byte(s.fullCfg.Auth.JWTSecret))
}

// validate2FATempToken parses and validates a 2FA temporary token.
func (s *Service) validate2FATempToken(tempToken string) (*TwoFAClaims, error) {
	token, err := jwt.ParseWithClaims(tempToken, &TwoFAClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: %v", ErrUnexpectedSigningMethod, token.Header["alg"])
		}

		return []byte(s.fullCfg.Auth.JWTSecret), nil
	})
	if err != nil {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*TwoFAClaims)
	if !ok || !token.Valid || claims.Purpose != twoFAPurpose {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// generateRecoveryCodes generates random hex-encoded recovery codes.
func generateRecoveryCodes() ([]string, error) {
	codes := make([]string, recoveryCodeCount)

	for i := range codes {
		randBytes := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(randBytes); err != nil {
			return nil, err
		}

		codes[i] = hex.EncodeToString(randBytes)
	}

	return codes, nil
}

// Setup2FA generates a TOTP secret for the user and stores it (not yet enabled).
func (s *Service) Setup2FA(ctx context.Context, userUID string) (*Setup2FAResponse, error) {
	user, err := s.db.GetUser(ctx, userUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	if user.TOTPEnabled {
		return nil, ErrTwoFAAlreadyEnabled
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "SolidPing",
		AccountName: user.Email,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate TOTP key: %w", err)
	}

	secret := key.Secret()
	if updateErr := s.db.UpdateUser(ctx, userUID, &models.UserUpdate{
		TOTPSecret: &secret,
	}); updateErr != nil {
		return nil, updateErr
	}

	return &Setup2FAResponse{
		URI:    key.URL(),
		Secret: secret,
	}, nil
}

// Confirm2FA validates the TOTP code, enables 2FA, and returns recovery codes.
func (s *Service) Confirm2FA(ctx context.Context, userUID, code string) (*Confirm2FAResponse, error) {
	user, err := s.db.GetUser(ctx, userUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	if user.TOTPEnabled {
		return nil, ErrTwoFAAlreadyEnabled
	}

	if user.TOTPSecret == nil || *user.TOTPSecret == "" {
		return nil, ErrTwoFANotEnabled
	}

	if !totp.Validate(code, *user.TOTPSecret) {
		return nil, ErrInvalid2FACode
	}

	recoveryCodes, err := generateRecoveryCodes()
	if err != nil {
		return nil, fmt.Errorf("failed to generate recovery codes: %w", err)
	}

	enabled := true
	if updateErr := s.db.UpdateUser(ctx, userUID, &models.UserUpdate{
		TOTPEnabled:       &enabled,
		TOTPRecoveryCodes: &recoveryCodes,
	}); updateErr != nil {
		return nil, updateErr
	}

	return &Confirm2FAResponse{
		RecoveryCodes: recoveryCodes,
	}, nil
}

// Verify2FA validates a TOTP code during login and returns full login tokens.
func (s *Service) Verify2FA(
	ctx context.Context, tempToken, code string, authContext Context,
) (*LoginResponse, error) {
	claims, err := s.validate2FATempToken(tempToken)
	if err != nil {
		return nil, err
	}

	user, err := s.db.GetUser(ctx, claims.UserUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	if !user.TOTPEnabled || user.TOTPSecret == nil {
		return nil, ErrTwoFANotEnabled
	}

	if !totp.Validate(code, *user.TOTPSecret) {
		return nil, ErrInvalid2FACode
	}

	return s.completeLoginAfter2FA(ctx, user, claims.OrgSlug, claims.Role, authContext)
}

// Recovery2FA validates a recovery code during login and returns full login tokens.
func (s *Service) Recovery2FA(
	ctx context.Context, tempToken, recoveryCode string, authContext Context,
) (*LoginResponse, error) {
	claims, err := s.validate2FATempToken(tempToken)
	if err != nil {
		return nil, err
	}

	user, err := s.db.GetUser(ctx, claims.UserUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	if !user.TOTPEnabled {
		return nil, ErrTwoFANotEnabled
	}

	// Find and remove the recovery code
	found := false
	remaining := make([]string, 0, len(user.TOTPRecoveryCodes))

	for _, storedCode := range user.TOTPRecoveryCodes {
		if storedCode == recoveryCode && !found {
			found = true

			continue
		}

		remaining = append(remaining, storedCode)
	}

	if !found {
		return nil, ErrInvalidRecoveryCode
	}

	// Update recovery codes (remove used one)
	if updateErr := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{
		TOTPRecoveryCodes: &remaining,
	}); updateErr != nil {
		return nil, updateErr
	}

	return s.completeLoginAfter2FA(ctx, user, claims.OrgSlug, claims.Role, authContext)
}

// Disable2FA disables 2FA for the user after validating the current TOTP code.
func (s *Service) Disable2FA(ctx context.Context, userUID, code string) error {
	user, err := s.db.GetUser(ctx, userUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}

		return err
	}

	if !user.TOTPEnabled || user.TOTPSecret == nil {
		return ErrTwoFANotEnabled
	}

	if !totp.Validate(code, *user.TOTPSecret) {
		return ErrInvalid2FACode
	}

	emptySecret := ""
	disabled := false
	emptyCodes := []string{}

	return s.db.UpdateUser(ctx, userUID, &models.UserUpdate{
		TOTPSecret:        &emptySecret,
		TOTPEnabled:       &disabled,
		TOTPRecoveryCodes: &emptyCodes,
	})
}

// completeLoginAfter2FA generates full login tokens after successful 2FA verification.
func (s *Service) completeLoginAfter2FA(
	ctx context.Context,
	user *models.User,
	orgSlug, role string,
	authContext Context,
) (*LoginResponse, error) {
	// Update last active timestamp
	now := time.Now()

	if updateErr := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{LastActiveAt: &now}); updateErr != nil {
		slog.ErrorContext(ctx, "Failed to update user last_active_at", "error", updateErr, "userUID", user.UID)
	}

	userInfo := &UserInfo{
		UID:       user.UID,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Role:      role,
	}

	// No org case
	if orgSlug == "" {
		accessToken, tokenErr := s.generateAccessToken(user.UID, "", role, "")
		if tokenErr != nil {
			return nil, tokenErr
		}

		return &LoginResponse{
			AccessToken: accessToken,
			ExpiresIn:   int(s.cfg.AccessTokenExpiry.Seconds()),
			TokenType:   tokenTypeBearer,
			User:        userInfo,
		}, nil
	}

	// Resolve org for refresh token storage
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	refreshToken := models.NewUserToken(user.UID, &org.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := s.refreshTokenExpiry(ctx, org.UID, now, now)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{
		keyCreatedWith: authContext.ToMap(),
	}

	if createErr := s.db.CreateUserToken(ctx, refreshToken); createErr != nil {
		return nil, createErr
	}

	s.enforceSessionCap(ctx, user.UID)

	accessToken, err := s.generateAccessToken(user.UID, orgSlug, role, refreshToken.UID)
	if err != nil {
		return nil, err
	}

	return &LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenValue,
		ExpiresIn:    int(s.cfg.AccessTokenExpiry.Seconds()),
		TokenType:    tokenTypeBearer,
		User:         userInfo,
		Organization: newOrganizationInfo(org),
	}, nil
}
