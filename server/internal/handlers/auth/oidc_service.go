package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
// It is the ProviderOutcome every federated callback hands to
// finishProviderCallback.
type OIDCOAuthResult = ProviderOutcome

// oidcUserInfo is the set of claims extracted from a validated ID token, per
// the configured (or default) claim mappings.
type oidcUserInfo struct {
	Subject string
	Email   string
	// EmailVerified is true only when the "email_verified" claim (of the ID
	// token, or of the userinfo response for the same address) is present
	// AND is an explicit `true` (boolean, or the string "true"). Unlike the other
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
	// Groups is the IdP groups claim (config.OIDCOAuthConfig.GroupsClaim),
	// read from the validated ID token only — see parseGroupsClaim.
	Groups []string
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
	// One snapshot for the whole lookup (spec 2026-11-25-02).
	oidcCfg := s.cfg.SSO().OIDC

	issuer := strings.TrimSpace(oidcCfg.IssuerURL)
	if !oidcCfg.Enabled || issuer == "" || oidcCfg.ClientID == "" || oidcCfg.ClientSecret == "" {
		return nil, nil, ErrOIDCNotConfigured
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.provider == nil || s.cachedIssuer != issuer {
		discoveryCtx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
		defer cancel()

		discoveryCtx = oidc.ClientContext(discoveryCtx, s.httpClient)

		provider, err := oidc.NewProvider(discoveryCtx, issuer)

		// go-oidc fetches the same discovery document with or without a
		// trailing slash, then demands the advertised issuer match byte for
		// byte. Providers disagree on the slash (Authentik adds it, Keycloak
		// does not), so accept the advertised form when that is the only
		// difference. ID tokens are still checked exactly against it.
		var mismatch *oidc.IssuerMismatchError
		if errors.As(err, &mismatch) &&
			strings.TrimSuffix(mismatch.Discovered, "/") == strings.TrimSuffix(issuer, "/") {
			provider, err = oidc.NewProvider(discoveryCtx, mismatch.Discovered)
		}

		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrOIDCDiscovery, err)
		}

		s.provider = provider
		s.cachedIssuer = issuer
	}

	// ClientID check is mandatory (SkipClientIDCheck stays false): this is
	// what ties the validated token to *our* client registration with the
	// IdP, not just any client of the same issuer.
	verifier := s.provider.Verifier(&oidc.Config{ClientID: oidcCfg.ClientID})

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
	oidcCfg := s.cfg.SSO().OIDC

	return &oauth2.Config{
		ClientID:     oidcCfg.ClientID,
		ClientSecret: oidcCfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  s.getCallbackURL(),
		Scopes:       scopes,
	}, nil
}

// scopes returns the configured scope list, always including "openid".
func (s *OIDCOAuthService) scopes() []string {
	fields := strings.Fields(s.cfg.SSO().OIDC.Scopes)
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

	if emailErr := s.requireVerifiedEmail(ctx, exchangeCtx, token, userInfo); emailErr != nil {
		return nil, emailErr
	}

	// Look up organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, fmt.Errorf("organization not found: %w", err)
	}

	// Find or create user
	user, _, err := s.findOrCreateUser(ctx, userInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create user: %w", err)
	}

	// IdP group → role mapping (spec 2026-11-21-02). The groups come from
	// the ID token verified above, merged with the userinfo response of the
	// same token exchange; never from the request or the state.
	roleChanges, syncErr := s.applyGroupMapping(ctx, exchangeCtx, token, userInfo, user, org)
	if syncErr != nil {
		return nil, syncErr
	}

	// Admission policy + session minting, shared by every connector
	// (see Service.JoinOrgViaLogin). A user the org does not admit gets
	// login.Pending instead of a membership, with a session on an org they
	// already belong to (login.FallbackOrgSlug) or an org-less one.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user, s.loginOptions()...)
	if err != nil {
		return nil, err
	}

	outcome := login.Outcome(org.Slug, user.UID)
	outcome.RoleChanges = roleChanges

	return outcome, nil
}

// requireVerifiedEmail completes the email claims from the userinfo endpoint
// when the ID token lacks them, then rejects the login unless the IdP
// asserts a verified email.
func (s *OIDCOAuthService) requireVerifiedEmail(
	ctx, exchangeCtx context.Context, token *oauth2.Token, userInfo *oidcUserInfo,
) error {
	// Many IdPs (Authelia, Authentik, Keycloak with some client scopes…)
	// keep the ID token minimal and only expose email/email_verified on the
	// userinfo endpoint. Complete the missing claims from there.
	if userInfo.Email == "" || !userInfo.EmailVerified {
		s.completeFromUserInfo(userInfo, s.userInfoClaims(exchangeCtx, token, userInfo.Subject))
	}

	if userInfo.Email == "" {
		slog.WarnContext(ctx, "OIDC login rejected: no email claim in the ID token nor the userinfo response",
			"email_claim", s.emailClaimName())

		return ErrEmailNotVerified
	}

	// Reject outright (never silently create/link a user) unless the IdP
	// explicitly asserts a verified email, consistent with Google/Discord's
	// handling of their own !EmailVerified case. See oidcUserInfo.EmailVerified
	// for why this doesn't default to "verified" when the claim is missing or
	// of the wrong type.
	if !userInfo.EmailVerified {
		slog.WarnContext(ctx, "OIDC login rejected: the IdP does not assert email_verified=true",
			"email", userInfo.Email)

		return ErrEmailNotVerified
	}

	return nil
}

// userInfoGroups reads the groups claim from the IdP's userinfo endpoint,
// called with the access token of the verified exchange. Some IdPs only put
// groups there. Best-effort: no endpoint, an error, or a response whose
// subject differs from the ID token's yields no group.
func (s *OIDCOAuthService) userInfoGroups(ctx context.Context, token *oauth2.Token, subject string) []string {
	claims := s.userInfoClaims(ctx, token, subject)
	if claims == nil {
		return nil
	}

	return parseGroupsClaim(claims[groupsClaimName(&s.cfg.SSO().OIDC)])
}

// userInfoClaims returns the claims of the IdP's userinfo endpoint, called
// with the access token of the verified exchange. Best-effort: no endpoint,
// an error, or a response whose subject differs from the ID token's (OIDC
// Core 5.3.2: such a response must not be used) yields nil.
func (s *OIDCOAuthService) userInfoClaims(ctx context.Context, token *oauth2.Token, subject string) map[string]any {
	provider, _, err := s.providerAndVerifier(ctx)
	if err != nil || provider.UserInfoEndpoint() == "" {
		return nil
	}

	info, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		slog.WarnContext(ctx, "OIDC userinfo request failed, using the ID token claims only", "error", err)

		return nil
	}

	if info.Subject != subject {
		slog.WarnContext(ctx, "OIDC userinfo subject differs from the ID token's, ignoring its claims")

		return nil
	}

	var claims map[string]any
	if err := info.Claims(&claims); err != nil {
		return nil
	}

	return claims
}

// completeFromUserInfo fills the email and its verification from the
// userinfo claims when the ID token lacks them. The ID token's email wins;
// the userinfo email_verified only counts for that same address, so a
// userinfo answer can never verify an email it did not assert.
func (s *OIDCOAuthService) completeFromUserInfo(info *oidcUserInfo, claims map[string]any) {
	if claims == nil {
		return
	}

	email := claimString(claims, s.emailClaimName())

	if info.Email == "" && email != "" {
		info.Email = email
		info.EmailVerified = false

		if info.Name == "" {
			info.Name = claimString(claims, s.nameClaimName())
		}
	}

	if !info.EmailVerified && email != "" && strings.EqualFold(email, info.Email) {
		info.EmailVerified = claimVerified(claims[oidcClaimEmailVerified])
	}

	if info.Name == "" {
		info.Name = info.Email
	}
}

func (s *OIDCOAuthService) emailClaimName() string {
	if c := s.cfg.SSO().OIDC.EmailClaim; c != "" {
		return c
	}

	return keyEmail
}

func (s *OIDCOAuthService) nameClaimName() string {
	if c := s.cfg.SSO().OIDC.NameClaim; c != "" {
		return c
	}

	return keyName
}

// claimVerified reports whether an email_verified claim value asserts a
// verified address: the JSON boolean true, or the string "true" that some
// IdPs (AWS Cognito, some ADFS setups) emit. Anything else is unverified.
func claimVerified(v any) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return strings.EqualFold(val, "true")
	default:
		return false
	}
}

// mapClaims extracts email/name/avatar from the validated ID token's claims,
// using the configured claim names with standard-claim fallbacks.
func (s *OIDCOAuthService) mapClaims(subject string, claims map[string]any) *oidcUserInfo {
	emailClaim := s.emailClaimName()
	nameClaim := s.nameClaimName()

	avatarClaim := s.cfg.SSO().OIDC.AvatarClaim
	if avatarClaim == "" {
		avatarClaim = oidcDefaultAvatarClaim
	}

	info := &oidcUserInfo{
		Subject:   subject,
		Email:     claimString(claims, emailClaim),
		Name:      claimString(claims, nameClaim),
		AvatarURL: claimString(claims, avatarClaim),
		Groups:    parseGroupsClaim(claims[groupsClaimName(&s.cfg.SSO().OIDC)]),
	}

	// Only an explicit `true` (boolean, or the string "true") counts as
	// verified. A missing claim, a `false` claim, or any other value leave
	// EmailVerified at its zero value (false) — see the field doc on
	// oidcUserInfo for the rationale.
	info.EmailVerified = claimVerified(claims[oidcClaimEmailVerified])

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
func (s *OIDCOAuthService) findOrCreateUser(ctx context.Context, userInfo *oidcUserInfo) (*models.User, bool, error) {
	// created reports whether THIS call minted the account.
	var created bool

	// Check by OIDC subject first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeOIDC, userInfo.Subject)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to get user provider: %w", err)
	}

	if err == nil && provider != nil {
		// A link pointing at a soft-deleted user is stale: it is cleared
		// and we fall through to the email lookup / create path rather
		// than failing this login (and every later one) forever.
		user, resolveErr := ResolveLinkedUser(ctx, s.db, models.ProviderTypeOIDC, userInfo.Subject, provider)
		if resolveErr != nil {
			return nil, false, resolveErr
		}

		if user != nil {
			return user, false, nil
		}

		provider = nil
	}

	// Check by email
	user, err := s.db.GetUserByEmail(ctx, userInfo.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to get user by email: %w", err)
	}

	// Create new user if not found
	if user == nil {
		created = true
		user = models.NewUser(userInfo.Email)
		user.Name = userInfo.Name
		user.AvatarURL = userInfo.AvatarURL

		now := time.Now()
		user.EmailVerifiedAt = &now

		// Routed through the package's single account-creation chokepoint so
		// the user_signed_up product event fires for SSO signups too.
		if err := createUserAndCapture(ctx, s.db, user, signupMethodOIDC); err != nil {
			return nil, false, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link OIDC provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeOIDC, userInfo.Subject)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, false, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, created, nil
}

// getCallbackURL returns the OAuth callback URL for the generic OIDC provider.
func (s *OIDCOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/oidc/callback"
}
