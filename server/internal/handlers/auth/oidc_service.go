package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Generic OIDC specific errors.
var (
	ErrOIDCNotConfigured = errors.New("generic OIDC provider is not configured")
	ErrOIDCDiscovery     = errors.New("oidc discovery failed")
	ErrOIDCTokenExchange = errors.New("oidc token exchange failed")
	ErrOIDCNoIDToken     = errors.New("oidc token response did not include an id_token")
	ErrOIDCTokenInvalid  = errors.New("oidc id token validation failed")
)

const (
	oidcOAuthStatePrefix = "oauth_state:oidc:"
	oidcOAuthStateTTL    = 10 * time.Minute
	oidcDiscoveryTimeout = 15 * time.Second

	// oidcDefaultAvatarClaim is the standard OIDC claim name used when
	// AvatarClaim is left blank. The email/name equivalents reuse the
	// package's existing keyEmail/keyName constants (both already "email"
	// and "name" respectively) rather than redeclaring them.
	oidcDefaultAvatarClaim = "picture"

	// oidcClaimEmailVerified is the standard OIDC claim indicating whether
	// the IdP has verified the user's email address.
	oidcClaimEmailVerified = "email_verified"
)

// OIDCOAuthResult contains the result of a successful generic OIDC flow.
type OIDCOAuthResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	OrgSlug      string
	UserUID      string
	// Pending is true when the login succeeded but the org did not admit
	// the user: no membership was created, a membership request is awaiting
	// admin approval, and the tokens above are an org-less session.
	Pending bool
}

// oidcUserInfo is the set of claims extracted from a validated ID token, per
// the configured (or default) claim mappings.
type oidcUserInfo struct {
	Subject string
	Email   string
	// EmailVerified is true only when the token's "email_verified" claim is
	// present AND is literally the JSON boolean `true`. Unlike the other
	// (fixed-catalog) providers, generic OIDC connects to an arbitrary,
	// admin-chosen IdP, so a malicious or misconfigured issuer could assert
	// any existing user's email. We therefore never default to "verified":
	// an absent claim, a `false` claim, or a claim of the wrong type
	// (e.g. the string "false", which some real IdPs emit) all count as
	// unverified. This mirrors google_service.go/discord_service.go, which
	// also reject the login outright when the provider doesn't assert a
	// verified email — see the `!EmailVerified` check in HandleCallback.
	EmailVerified bool
	Name          string
	AvatarURL     string
}

// OIDCOAuthService handles generic OpenID Connect authentication logic.
//
// Exactly one issuer is supported (spec 2026-07-08-08, part 1 — global-only,
// single instance). Discovery and the JWKS key set are cached in-process and
// only re-fetched when the configured issuer URL changes, so a super-admin
// edit to the config takes effect on the next login without a restart.
type OIDCOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	httpClient  *http.Client

	mu           sync.Mutex
	cachedIssuer string
	provider     *oidc.Provider
}

// NewOIDCOAuthService creates a new generic OIDC service.
func NewOIDCOAuthService(dbService db.Service, cfg *config.Config, authService *Service) *OIDCOAuthService {
	return &OIDCOAuthService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}
}

// GenerateOAuthState creates a new OAuth state and stores it in the database.
func (s *OIDCOAuthService) GenerateOAuthState(ctx context.Context, redirectURI, orgSlug string) (string, error) {
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	nonce := base64.URLEncoding.EncodeToString(nonceBytes)

	state := OAuthState{
		Nonce:       nonce,
		RedirectURI: redirectURI,
		OrgSlug:     orgSlug,
		CreatedAt:   time.Now().Unix(),
	}

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("failed to marshal state: %w", err)
	}

	stateValue := &models.JSONMap{keyState: string(stateJSON)}
	ttl := oidcOAuthStateTTL

	if err := s.db.SetStateEntry(ctx, nil, oidcOAuthStatePrefix+nonce, stateValue, &ttl); err != nil {
		return "", fmt.Errorf("failed to store state: %w", err)
	}

	return nonce, nil
}

// ValidateOAuthState validates and consumes an OAuth state.
func (s *OIDCOAuthService) ValidateOAuthState(ctx context.Context, stateParam string) (*OAuthState, error) {
	entry, err := s.db.GetStateEntry(ctx, nil, oidcOAuthStatePrefix+stateParam)
	if err != nil || entry == nil {
		return nil, ErrInvalidOAuthState
	}

	// Delete state (one-time use)
	_, _ = s.db.DeleteStateEntry(ctx, nil, oidcOAuthStatePrefix+stateParam)

	stateJSON, ok := (*entry.Value)[keyState].(string)
	if !ok {
		return nil, ErrInvalidOAuthState
	}

	var state OAuthState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrInvalidOAuthState
	}

	if time.Now().Unix()-state.CreatedAt > int64(oidcOAuthStateTTL.Seconds()) {
		return nil, ErrInvalidOAuthState
	}

	return &state, nil
}

// providerAndVerifier lazily performs OIDC discovery against the configured
// issuer URL (and fetches its JWKS lazily, on first token verification) and
// caches the result until the issuer URL changes.
func (s *OIDCOAuthService) providerAndVerifier(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	issuer := strings.TrimSpace(s.cfg.OIDC.IssuerURL)
	if !s.cfg.OIDC.Enabled || issuer == "" || s.cfg.OIDC.ClientID == "" || s.cfg.OIDC.ClientSecret == "" {
		return nil, nil, ErrOIDCNotConfigured
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.provider == nil || s.cachedIssuer != issuer {
		discoveryCtx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
		defer cancel()

		discoveryCtx = oidc.ClientContext(discoveryCtx, s.httpClient)

		provider, err := oidc.NewProvider(discoveryCtx, issuer)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrOIDCDiscovery, err)
		}

		s.provider = provider
		s.cachedIssuer = issuer
	}

	// ClientID check is mandatory (SkipClientIDCheck stays false): this is
	// what ties the validated token to *our* client registration with the
	// IdP, not just any client of the same issuer.
	verifier := s.provider.Verifier(&oidc.Config{ClientID: s.cfg.OIDC.ClientID})

	return s.provider, verifier, nil
}

// oauth2Config builds the OAuth2 client config for the current provider,
// performing discovery if not already cached.
func (s *OIDCOAuthService) oauth2Config(ctx context.Context) (*oauth2.Config, error) {
	provider, _, err := s.providerAndVerifier(ctx)
	if err != nil {
		return nil, err
	}

	scopes := s.scopes()

	return &oauth2.Config{
		ClientID:     s.cfg.OIDC.ClientID,
		ClientSecret: s.cfg.OIDC.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  s.getCallbackURL(),
		Scopes:       scopes,
	}, nil
}

// scopes returns the configured scope list, always including "openid".
func (s *OIDCOAuthService) scopes() []string {
	fields := strings.Fields(s.cfg.OIDC.Scopes)
	if len(fields) == 0 {
		return []string{oidc.ScopeOpenID, keyEmail, "profile"}
	}

	for _, sc := range fields {
		if sc == oidc.ScopeOpenID {
			return fields
		}
	}

	return append([]string{oidc.ScopeOpenID}, fields...)
}

// HandleCallback processes the OAuth callback from the generic OIDC provider:
// exchanges the code, validates the ID token (issuer, audience, signature via
// JWKS, expiry), maps claims, and resolves/creates the local user.
func (s *OIDCOAuthService) HandleCallback(ctx context.Context, code, orgSlug string) (*OIDCOAuthResult, error) {
	oauth2Cfg, err := s.oauth2Config(ctx)
	if err != nil {
		return nil, err
	}

	exchangeCtx := oidc.ClientContext(ctx, s.httpClient)

	token, err := oauth2Cfg.Exchange(exchangeCtx, code)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOIDCTokenExchange, err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, ErrOIDCNoIDToken
	}

	_, verifier, err := s.providerAndVerifier(ctx)
	if err != nil {
		return nil, err
	}

	// This is the security-critical step: Verify checks the signature
	// against the IdP's published JWKS, the issuer, the audience (our
	// client ID), and the expiry.
	idToken, err := verifier.Verify(exchangeCtx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOIDCTokenInvalid, err)
	}

	var claims map[string]any
	if claimsErr := idToken.Claims(&claims); claimsErr != nil {
		return nil, fmt.Errorf("%w: failed to parse claims: %w", ErrOIDCTokenInvalid, claimsErr)
	}

	userInfo := s.mapClaims(idToken.Subject, claims)

	if userInfo.Email == "" {
		return nil, ErrEmailNotVerified
	}

	// Reject outright (never silently create/link a user) unless the IdP
	// explicitly asserts a verified email, consistent with Google/Discord's
	// handling of their own !EmailVerified case. See oidcUserInfo.EmailVerified
	// for why this doesn't default to "verified" when the claim is missing or
	// of the wrong type.
	if !userInfo.EmailVerified {
		return nil, ErrEmailNotVerified
	}

	// Look up organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, fmt.Errorf("organization not found: %w", err)
	}

	// Find or create user
	user, err := s.findOrCreateUser(ctx, userInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create user: %w", err)
	}

	// Admission policy + session minting, shared by every connector
	// (see Service.JoinOrgViaLogin). A user the org does not admit gets
	// login.Pending and an org-less session instead of a membership.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user)
	if err != nil {
		return nil, err
	}

	return &OIDCOAuthResult{
		AccessToken:  login.AccessToken,
		RefreshToken: login.RefreshToken,
		ExpiresIn:    login.ExpiresIn,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
		Pending:      login.Pending,
	}, nil
}

// mapClaims extracts email/name/avatar from the validated ID token's claims,
// using the configured claim names with standard-claim fallbacks.
func (s *OIDCOAuthService) mapClaims(subject string, claims map[string]any) *oidcUserInfo {
	emailClaim := s.cfg.OIDC.EmailClaim
	if emailClaim == "" {
		emailClaim = keyEmail
	}

	nameClaim := s.cfg.OIDC.NameClaim
	if nameClaim == "" {
		nameClaim = keyName
	}

	avatarClaim := s.cfg.OIDC.AvatarClaim
	if avatarClaim == "" {
		avatarClaim = oidcDefaultAvatarClaim
	}

	info := &oidcUserInfo{
		Subject:   subject,
		Email:     claimString(claims, emailClaim),
		Name:      claimString(claims, nameClaim),
		AvatarURL: claimString(claims, avatarClaim),
	}

	// Only an actual boolean `true` counts as verified. A missing claim, a
	// `false` claim, or a claim of any other type (e.g. a stringly-typed
	// "false" some real IdPs emit) all leave EmailVerified at its zero value
	// (false) — see the field doc on oidcUserInfo for the rationale.
	if b, ok := claims[oidcClaimEmailVerified].(bool); ok {
		info.EmailVerified = b
	}

	if info.Name == "" {
		info.Name = info.Email
	}

	return info
}

// claimString reads a string-valued claim, returning "" when absent or of
// another type.
func claimString(claims map[string]any, key string) string {
	if v, ok := claims[key].(string); ok {
		return v
	}

	return ""
}

// findOrCreateUser finds or creates a user by generic OIDC identity.
func (s *OIDCOAuthService) findOrCreateUser(ctx context.Context, userInfo *oidcUserInfo) (*models.User, error) {
	// Check by OIDC subject first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeOIDC, userInfo.Subject)
	if err == nil && provider != nil {
		return s.db.GetUser(ctx, provider.UserUID)
	}

	// Check by email
	user, err := s.db.GetUserByEmail(ctx, userInfo.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}

	// Create new user if not found
	if user == nil {
		user = models.NewUser(userInfo.Email)
		user.Name = userInfo.Name
		user.AvatarURL = userInfo.AvatarURL

		now := time.Now()
		user.EmailVerifiedAt = &now

		// Routed through the package's single account-creation chokepoint so
		// the user_signed_up product event fires for SSO signups too.
		if err := createUserAndCapture(ctx, s.db, user, signupMethodOIDC); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link OIDC provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeOIDC, userInfo.Subject)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, nil
}

// getCallbackURL returns the OAuth callback URL for the generic OIDC provider.
func (s *OIDCOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/oidc/callback"
}
