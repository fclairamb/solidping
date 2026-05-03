// Package auth provides authentication services and handlers.
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
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
	ErrInvalid2FACode          = errors.New("invalid 2FA code")
	ErrInvalidRecoveryCode     = errors.New("invalid recovery code")
	ErrTwoFAAlreadyEnabled     = errors.New("2FA is already enabled")
	ErrTwoFANotEnabled         = errors.New("2FA is not enabled")
)

// Service provides authentication business logic.
type Service struct {
	db       db.Service
	cfg      config.AuthConfig
	fullCfg  *config.Config
	jobsSvc  jobsvc.Service
	patCache map[string]*cachedPATClaims
	cacheMux sync.RWMutex
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
	jwt.RegisteredClaims
}

// RoleSuperAdmin is the role value for super administrators.
const RoleSuperAdmin = "superadmin"

// IsSuperAdmin returns true if the claims indicate a super admin.
func (c *Claims) IsSuperAdmin() bool {
	return c.Role == RoleSuperAdmin
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
}

// OrganizationSummary represents a brief organization entry for listing.
type OrganizationSummary struct {
	Slug string `json:"slug"`
	Name string `json:"name,omitempty"`
	Role string `json:"role"`
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
	User          *UserInfo             `json:"user"`
	Organization  *OrganizationInfo     `json:"organization"`
	Organizations []OrganizationSummary `json:"organizations"`
	TOTPEnabled   bool                  `json:"totpEnabled"`
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
	UID        string                 `json:"uid"`
	Name       string                 `json:"name,omitempty"`
	Type       string                 `json:"type"`
	OrgSlug    string                 `json:"orgSlug,omitempty"`
	CreatedAt  time.Time              `json:"createdAt"`
	LastUsedAt *time.Time             `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time             `json:"expiresAt,omitempty"`
	Properties map[string]interface{} `json:"properties,omitempty"`
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
func NewService(
	dbService db.Service, cfg config.AuthConfig, fullCfg *config.Config,
	jobsSvc jobsvc.Service,
) *Service {
	return &Service{
		db:       dbService,
		cfg:      cfg,
		fullCfg:  fullCfg,
		jobsSvc:  jobsSvc,
		patCache: make(map[string]*cachedPATClaims),
	}
}

// enqueueEmail builds an email job and pushes it onto the job queue.
// Errors are logged but never bubbled to the caller — transactional emails
// must not block registration, password reset, or invitation flows.
func (s *Service) enqueueEmail(
	ctx context.Context, orgUID, recipient, subject, template string, data any,
) {
	if s.jobsSvc == nil || recipient == "" {
		return
	}

	cfg := emailJobConfig{
		To:           []string{recipient},
		Subject:      subject,
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
//nolint:funlen,cyclop
func (s *Service) Login(
	ctx context.Context, orgSlug, email, password string, authContext Context,
) (*LoginResponse, error) {
	// Get user by email (global user lookup)
	user, err := s.db.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}

		return nil, err
	}

	// Verify password
	if user.PasswordHash == nil || *user.PasswordHash == "" {
		return nil, ErrInvalidCredentials
	}

	// Support plaintext passwords for development (prefix: $plaintext$)
	if plaintextPassword, found := strings.CutPrefix(*user.PasswordHash, "$plaintext$"); found {
		if plaintextPassword != password {
			return nil, ErrInvalidCredentials
		}
	} else {
		// Verify argon2id hash
		if !passwords.Verify(password, *user.PasswordHash) {
			return nil, ErrInvalidCredentials
		}
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

	// No org case: issue token with empty org, skip refresh token
	if resolvedOrg == nil {
		accessToken, tokenErr := s.generateAccessToken(user.UID, "", role)
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

	// Generate access token
	accessToken, err := s.generateAccessToken(user.UID, resolvedOrg.Slug, role)
	if err != nil {
		return nil, err
	}

	// Generate refresh token
	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	// Store refresh token in database
	refreshToken := models.NewUserToken(user.UID, &resolvedOrg.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := now.Add(s.cfg.RefreshTokenExpiry)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{
		keyCreatedWith: authContext.ToMap(),
	}

	if err := s.db.CreateUserToken(ctx, refreshToken); err != nil {
		return nil, err
	}

	return &LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenValue,
		ExpiresIn:    int(s.cfg.AccessTokenExpiry.Seconds()),
		TokenType:    tokenTypeBearer,
		User:         userInfo,
		Organization: &OrganizationInfo{
			UID:  resolvedOrg.UID,
			Slug: resolvedOrg.Slug,
			Name: resolvedOrg.Name,
		},
		Organizations: orgSummaries,
		LoginAction:   loginAction,
	}, nil
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
			Slug: member.Organization.Slug,
			Name: member.Organization.Name,
			Role: string(member.Role),
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

	return s.db.DeleteUserToken(ctx, token.UID)
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
		if deleteErr := s.db.DeleteUserToken(ctx, token.UID); deleteErr != nil {
			slog.ErrorContext(ctx, "Failed to delete refresh token", "error", deleteErr, "tokenUID", token.UID)

			continue
		}

		deleted++
	}

	return &LogoutResponse{
		Success:       true,
		TokensDeleted: deleted,
	}, nil
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

	// Generate new access token
	accessToken, err := s.generateAccessToken(user.UID, org.Slug, role)
	if err != nil {
		return nil, err
	}

	// Update last used timestamp
	now := time.Now()

	if updateErr := s.db.UpdateUserToken(ctx, token.UID, models.UserTokenUpdate{LastActiveAt: &now}); updateErr != nil {
		slog.ErrorContext(ctx, "Failed to update token last_active_at", "error", updateErr, "tokenUID", token.UID)
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
		Organization: &OrganizationInfo{
			UID:  org.UID,
			Slug: org.Slug,
			Name: org.Name,
		},
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

		return []byte(s.cfg.JWTSecret), nil
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
	org, err := s.db.GetOrganizationBySlug(ctx, claims.OrgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
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

	return &MeResponse{
		User: &UserInfo{
			UID:       user.UID,
			Email:     user.Email,
			Name:      user.Name,
			AvatarURL: user.AvatarURL,
			Role:      role,
		},
		Organization: &OrganizationInfo{
			UID:  org.UID,
			Slug: org.Slug,
			Name: org.Name,
		},
		Organizations: orgs,
		TOTPEnabled:   user.TOTPEnabled,
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
			Slug: member.Organization.Slug,
			Name: member.Organization.Name,
			Role: string(member.Role),
		})
	}

	return orgs, nil
}

// GetUserTokens returns a list of tokens for a user.
func (s *Service) GetUserTokens(ctx context.Context, orgSlug, userUID, tokenType string) (*TokenListResponse, error) {
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

		name := ""

		if tok.Properties != nil {
			if n, ok := tok.Properties[keyName].(string); ok {
				name = n
			}
		}

		result = append(result, TokenInfo{
			UID:        tok.UID,
			Name:       name,
			Type:       string(tok.Type),
			CreatedAt:  tok.CreatedAt,
			LastUsedAt: tok.LastActiveAt,
			ExpiresAt:  tok.ExpiresAt,
		})
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
	}

	return s.db.DeleteUserToken(ctx, tokenUID)
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

	// Generate access token
	accessToken, err := s.generateAccessToken(user.UID, org.Slug, role)
	if err != nil {
		return nil, err
	}

	// Generate refresh token
	now := time.Now()

	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	// Store refresh token in database
	refreshToken := models.NewUserToken(user.UID, &org.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := now.Add(s.cfg.RefreshTokenExpiry)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{
		keyCreatedWith: authContext.ToMap(),
	}

	if err := s.db.CreateUserToken(ctx, refreshToken); err != nil {
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
		Organization: &OrganizationInfo{
			UID:  org.UID,
			Slug: org.Slug,
			Name: org.Name,
		},
	}, nil
}

// GetAllUserTokens returns all tokens for a user across all orgs (for root-level listing).
func (s *Service) GetAllUserTokens(ctx context.Context, userUID, tokenType string) (*TokenListResponse, error) {
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

		name := ""
		if tok.Properties != nil {
			if n, ok := tok.Properties[keyName].(string); ok {
				name = n
			}
		}

		orgSlug := ""
		if tok.OrganizationUID != nil {
			orgSlug = orgSlugs[*tok.OrganizationUID]
		}

		result = append(result, TokenInfo{
			UID:        tok.UID,
			Name:       name,
			Type:       string(tok.Type),
			OrgSlug:    orgSlug,
			CreatedAt:  tok.CreatedAt,
			LastUsedAt: tok.LastActiveAt,
			ExpiresAt:  tok.ExpiresAt,
		})
	}

	return &TokenListResponse{Data: result}, nil
}

func (s *Service) generateAccessToken(userUID, orgSlug, role string) (string, error) {
	now := time.Now()
	claims := &Claims{
		UserUID: userUID,
		OrgSlug: orgSlug,
		Role:    role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.AccessTokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    jwtIssuer,
			ID:        uuid.New().String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString([]byte(s.cfg.JWTSecret))
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
	// Generate access token
	accessToken, err := s.generateAccessToken(user.UID, org.Slug, role)
	if err != nil {
		return nil, fmt.Errorf("failed to generate access token: %w", err)
	}

	// Generate refresh token
	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate refresh token: %w", err)
	}

	// Store refresh token in database
	now := time.Now()
	refreshToken := models.NewUserToken(user.UID, &org.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := now.Add(s.cfg.RefreshTokenExpiry)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{
		keyCreatedWith: map[string]any{
			keyMethod: "oauth",
		},
	}

	if err := s.db.CreateUserToken(ctx, refreshToken); err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
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
	registrationKeyPrefix  = "email_registration:"
	registrationTTL        = 3 * 24 * time.Hour
	inviteKeyPrefix        = "invite:"
	passwordResetKeyPrefix = "password_reset:"
	passwordResetTTL       = 1 * time.Hour
	minPasswordLength      = 8
	registrationTokenSize  = 32
)

// Register creates a pending registration entry and sends a confirmation email.
func (s *Service) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	// Check if registration is enabled
	pattern := s.cfg.RegistrationEmailPattern
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
	s.enqueueEmail(ctx, "", req.Email, "", "registration.html",
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
		_ = s.db.DeleteStateEntry(ctx, nil, matchedEntry.Key)

		return nil, ErrEmailAlreadyTaken
	}

	// Create user
	user := models.NewUser(regEmail)
	user.Name = regName
	user.PasswordHash = &regHash

	now := time.Now()
	user.EmailVerifiedAt = &now

	if createErr := s.db.CreateUser(ctx, user); createErr != nil {
		return nil, fmt.Errorf("failed to create user: %w", createErr)
	}

	// Delete the state entry
	_ = s.db.DeleteStateEntry(ctx, nil, matchedEntry.Key)

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
	accessToken, err := s.generateAccessToken(user.UID, org.Slug, role)
	if err != nil {
		return nil, err
	}

	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	refreshToken := models.NewUserToken(user.UID, &org.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := now.Add(s.cfg.RefreshTokenExpiry)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{keyCreatedWith: map[string]any{"method": "registration"}}

	if err := s.db.CreateUserToken(ctx, refreshToken); err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
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
		Organization: &OrganizationInfo{
			UID:  org.UID,
			Slug: org.Slug,
			Name: org.Name,
		},
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
// Always returns success to prevent email enumeration.
func (s *Service) RequestPasswordReset(
	ctx context.Context, req RequestPasswordResetRequest,
) (*RequestPasswordResetResponse, error) {
	successMsg := &RequestPasswordResetResponse{
		Message: "If an account exists with that email, a reset link has been sent.",
	}

	// Look up user by email — return success even if not found (anti-enumeration)
	user, _ := s.db.GetUserByEmail(ctx, req.Email)
	if user == nil || user.PasswordHash == nil || *user.PasswordHash == "" {
		return successMsg, nil
	}

	// Generate reset token
	tokenBytes := make([]byte, registrationTokenSize)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	token := hex.EncodeToString(tokenBytes)

	// Store in state entries (upsert — replaces any existing reset for this email)
	stateValue := &models.JSONMap{
		keyToken: token,
		keyEmail: req.Email,
	}
	ttl := passwordResetTTL

	if err := s.db.SetStateEntry(ctx, nil, passwordResetKeyPrefix+req.Email, stateValue, &ttl); err != nil {
		return nil, fmt.Errorf("failed to store password reset: %w", err)
	}

	// Send reset email asynchronously via the email job
	resetURL := fmt.Sprintf("%s/dash0/reset-password/%s",
		s.fullCfg.Server.BaseURL, token)
	s.enqueueEmail(ctx, "", req.Email, "", "password-reset.html",
		map[string]any{"ResetURL": resetURL},
	)

	return successMsg, nil
}

// ResetPassword validates a reset token and sets a new password.
func (s *Service) ResetPassword(ctx context.Context, req ResetPasswordRequest) (*ResetPasswordResponse, error) {
	// Search state entries for matching token
	entries, err := s.db.ListStateEntries(ctx, nil, passwordResetKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list password reset entries: %w", err)
	}

	var matchedEntry *models.StateEntry

	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}

		val := *entry.Value
		if tokenVal, ok := val["token"].(string); ok && tokenVal == req.Token {
			matchedEntry = entry

			break
		}
	}

	if matchedEntry == nil {
		return nil, ErrPasswordResetExpired
	}

	// Extract email from key
	resetEmail := strings.TrimPrefix(matchedEntry.Key, passwordResetKeyPrefix)

	// Look up user
	user, err := s.db.GetUserByEmail(ctx, resetEmail)
	if err != nil || user == nil {
		return nil, ErrPasswordResetExpired
	}

	// Validate new password
	if len(req.Password) < minPasswordLength {
		return nil, fmt.Errorf("%w: password must be at least %d characters",
			ErrInvalidCredentials, minPasswordLength)
	}

	// Hash new password
	hash, err := passwords.Hash(req.Password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Update user's password
	if err := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{PasswordHash: &hash}); err != nil {
		return nil, fmt.Errorf("failed to update password: %w", err)
	}

	// Delete the state entry
	if err := s.db.DeleteStateEntry(ctx, nil, matchedEntry.Key); err != nil {
		slog.ErrorContext(ctx, "Failed to delete password reset entry", "error", err)
	}

	return &ResetPasswordResponse{
		Message: "Your password has been reset. You can now log in.",
	}, nil
}

// CreateOrgRequest contains the request data for creating an organization.
type CreateOrgRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// OrgResponse contains the response for org creation.
type OrgResponse struct {
	UID  string `json:"uid"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

var orgSlugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,18}[a-z0-9]$`)

// CreateOrg creates a new organization and makes the user an admin.
func (s *Service) CreateOrg(ctx context.Context, userUID string, req CreateOrgRequest) (*OrgResponse, error) {
	// Validate slug
	if !orgSlugRegex.MatchString(req.Slug) {
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

	if err := s.db.CreateOrganization(ctx, org); err != nil {
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}

	// Make user admin
	member := models.NewOrganizationMember(org.UID, userUID, models.MemberRoleAdmin)
	member.JoinedAt = &now

	if err := s.db.CreateOrganizationMember(ctx, member); err != nil {
		return nil, fmt.Errorf("failed to create membership: %w", err)
	}

	return &OrgResponse{
		UID:  org.UID,
		Slug: org.Slug,
		Name: org.Name,
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
	// Validate role
	role := models.MemberRole(req.Role)
	if role != models.MemberRoleAdmin && role != models.MemberRoleUser && role != models.MemberRoleViewer {
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
			return s.db.DeleteStateEntry(ctx, &org.UID, entry.Key)
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

		if createErr := s.db.CreateUser(ctx, user); createErr != nil {
			return nil, fmt.Errorf("failed to create user: %w", createErr)
		}
	}

	// Check existing membership
	_, err = s.db.GetMemberByUserAndOrg(ctx, user.UID, matchedOrg.UID)
	if err == nil {
		// Already a member, just clean up and login
		_ = s.db.DeleteStateEntry(ctx, &matchedOrg.UID, stateKey)
	} else {
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
		_ = s.db.DeleteStateEntry(ctx, &matchedOrg.UID, stateKey)
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
	accessToken, err := s.generateAccessToken(user.UID, matchedOrg.Slug, role)
	if err != nil {
		return nil, err
	}

	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	refreshToken := models.NewUserToken(user.UID, &matchedOrg.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := now.Add(s.cfg.RefreshTokenExpiry)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{keyCreatedWith: map[string]any{"method": "invitation"}}

	if err := s.db.CreateUserToken(ctx, refreshToken); err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
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
		Organization: &OrganizationInfo{
			UID:  matchedOrg.UID,
			Slug: matchedOrg.Slug,
			Name: matchedOrg.Name,
		},
	}, nil
}

// OrgSettingsResponse contains org settings.
type OrgSettingsResponse struct {
	RegistrationEmailPattern string `json:"registrationEmailPattern"`
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

	return &OrgSettingsResponse{RegistrationEmailPattern: pattern}, nil
}

// UpdateOrgSettingsRequest contains the request data for updating org settings.
type UpdateOrgSettingsRequest struct {
	RegistrationEmailPattern *string `json:"registrationEmailPattern"`
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

	return s.GetOrgSettings(ctx, orgSlug)
}

func (s *Service) updateEmailPattern(ctx context.Context, orgUID, pattern string) error {
	if pattern == "" {
		return s.db.DeleteOrgParameter(ctx, orgUID, "registration.email_pattern")
	}

	if _, compileErr := regexp.Compile(pattern); compileErr != nil {
		return fmt.Errorf("%w: invalid regex pattern", ErrInvalidCredentials)
	}

	return s.db.SetOrgParameter(ctx, orgUID, "registration.email_pattern", pattern, false)
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

	s.enqueueEmail(ctx, orgUID, recipientEmail, "", "invitation.html",
		map[string]any{
			"OrgName":     orgName,
			"Role":        role,
			"InviterName": inviterName,
			"InviteURL":   inviteURL,
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

	return token.SignedString([]byte(s.cfg.JWTSecret))
}

// validate2FATempToken parses and validates a 2FA temporary token.
func (s *Service) validate2FATempToken(tempToken string) (*TwoFAClaims, error) {
	token, err := jwt.ParseWithClaims(tempToken, &TwoFAClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: %v", ErrUnexpectedSigningMethod, token.Header["alg"])
		}

		return []byte(s.cfg.JWTSecret), nil
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
		accessToken, tokenErr := s.generateAccessToken(user.UID, "", role)
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

	accessToken, err := s.generateAccessToken(user.UID, orgSlug, role)
	if err != nil {
		return nil, err
	}

	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	refreshToken := models.NewUserToken(user.UID, &org.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := now.Add(s.cfg.RefreshTokenExpiry)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{
		keyCreatedWith: authContext.ToMap(),
	}

	if createErr := s.db.CreateUserToken(ctx, refreshToken); createErr != nil {
		return nil, createErr
	}

	return &LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenValue,
		ExpiresIn:    int(s.cfg.AccessTokenExpiry.Seconds()),
		TokenType:    tokenTypeBearer,
		User:         userInfo,
		Organization: &OrganizationInfo{
			UID:  org.UID,
			Slug: org.Slug,
			Name: org.Name,
		},
	}, nil
}
