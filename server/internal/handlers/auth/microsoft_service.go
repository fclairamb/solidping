package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Microsoft OAuth specific errors.
var (
	ErrMicrosoftTokenExchange = errors.New("microsoft token exchange failed")
	ErrMicrosoftAPI           = errors.New("microsoft API error")
)

const (
	microsoftOAuthStatePrefix = "oauth_state:microsoft:"
	microsoftOAuthStateTTL    = 10 * time.Minute
	microsoftUserURL          = "https://graph.microsoft.com/v1.0/me"

	// microsoftClaimEmailVerified is Entra's "email domain owner verified"
	// optional ID-token claim. It is emitted only once the app registration
	// adds it (Token configuration → optional claims → ID token).
	microsoftClaimEmailVerified = "xms_edov"

	// microsoftTenantCommon is the default tenant: any work, school or
	// personal Microsoft account.
	microsoftTenantCommon = "common"
)

// microsoftIDTokenClaims is the subset of the Entra ID token this connector
// reads to decide whether the sign-in email may be trusted.
type microsoftIDTokenClaims struct {
	OID   string
	TID   string
	Email string
	// EmailVerified is true only when xms_edov is the JSON boolean true.
	EmailVerified bool
}

// MicrosoftUserInfo represents user info from Microsoft Graph /me endpoint.
type MicrosoftUserInfo struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

// MicrosoftTokenResponse represents the response from Microsoft's token exchange.
//
//nolint:tagliatelle // Microsoft OAuth uses snake_case JSON field names
type MicrosoftTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	ExpiresIn   int    `json:"expires_in"`
	IDToken     string `json:"id_token,omitempty"`
	Error       string `json:"error,omitempty"`
	ErrorDesc   string `json:"error_description,omitempty"`
}

// MicrosoftOAuthResult contains the result of a successful Microsoft OAuth flow.
// It is the ProviderOutcome every federated callback hands to
// finishProviderCallback.
type MicrosoftOAuthResult = ProviderOutcome

// MicrosoftOAuthService handles Microsoft OAuth authentication logic.
type MicrosoftOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	httpClient  *http.Client

	// tokenURL / userURL override the Microsoft endpoints. Empty in
	// production (the real login.microsoftonline.com / graph.microsoft.com
	// URLs are used); tests point them at an httptest server so the REAL
	// HandleCallback can be driven end to end instead of a re-implementation
	// of it, which would not notice a regression reintroduced in the real
	// code path.
	tokenURL string
	userURL  string
}

// NewMicrosoftOAuthService creates a new Microsoft OAuth service.
func NewMicrosoftOAuthService(dbService db.Service, cfg *config.Config, authService *Service) *MicrosoftOAuthService {
	return &MicrosoftOAuthService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}
}

// GenerateOAuthState creates a new OAuth state and stores it in the database.
func (s *MicrosoftOAuthService) GenerateOAuthState(ctx context.Context, redirectURI, orgSlug string) (string, error) {
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
	ttl := microsoftOAuthStateTTL

	if err := s.db.SetStateEntry(ctx, nil, microsoftOAuthStatePrefix+nonce, stateValue, &ttl); err != nil {
		return "", fmt.Errorf("failed to store state: %w", err)
	}

	return nonce, nil
}

// ValidateOAuthState validates and consumes an OAuth state.
func (s *MicrosoftOAuthService) ValidateOAuthState(ctx context.Context, stateParam string) (*OAuthState, error) {
	entry, err := s.db.GetStateEntry(ctx, nil, microsoftOAuthStatePrefix+stateParam)
	if err != nil || entry == nil {
		return nil, ErrInvalidOAuthState
	}

	// Delete state (one-time use)
	_, _ = s.db.DeleteStateEntry(ctx, nil, microsoftOAuthStatePrefix+stateParam)

	stateJSON, ok := (*entry.Value)[keyState].(string)
	if !ok {
		return nil, ErrInvalidOAuthState
	}

	var state OAuthState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrInvalidOAuthState
	}

	if time.Now().Unix()-state.CreatedAt > int64(microsoftOAuthStateTTL.Seconds()) {
		return nil, ErrInvalidOAuthState
	}

	return &state, nil
}

// HandleCallback processes the OAuth callback from Microsoft.
func (s *MicrosoftOAuthService) HandleCallback(
	ctx context.Context, code, orgSlug string,
) (*MicrosoftOAuthResult, error) {
	// Exchange code for tokens
	tokenResp, err := s.exchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMicrosoftTokenExchange, err)
	}

	// Fetch user info from Microsoft Graph
	userInfo, err := s.fetchUserProfile(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrMicrosoftAPI, err)
	}

	idClaims, err := s.parseIDToken(tokenResp.IDToken, userInfo.ID, time.Now())
	if err != nil {
		return nil, err
	}

	// Trust rule for the sign-in email (spec 2026-10-16-01, "nOAuth"):
	//
	//   - Graph `mail` is a directory attribute any tenant admin can set to any
	//     address without proving they own it, and Graph returns no
	//     verification flag. Linking or creating an account from it would let
	//     the admin of ANY Entra tenant take over (or pre-register) a local
	//     account by email.
	//   - Pinned tenant (TenantID set and not common/organizations/consumers):
	//     only that directory's users can get a token, and its admins are the
	//     operator's own. That is the same admin-established trust boundary
	//     SAML documents, so `mail` (falling back to the UPN) is trusted.
	//   - Multi-tenant: the email is trusted only when the ID token carries a
	//     non-empty `email` and `xms_edov` is the JSON boolean true, and that
	//     ID-token email is what is used, never Graph `mail`.
	//   - Anything else is untrusted: an already-linked Microsoft ID still
	//     signs in, but no account is linked or created by email.
	//
	// UPN caveat (pinned tenant): when Graph returns an empty `mail`, the UPN
	// we fall back to is often the tenant's *.onmicrosoft.com form
	// (alice@acme.onmicrosoft.com) rather than the address the person actually
	// uses (alice@acme.com). That mints a SECOND local user instead of linking
	// the existing one, and it is also the address registration.email_pattern
	// is matched against. Resolving the real address (Graph otherMails /
	// proxyAddresses) is scope for another spec.
	email, trusted := s.resolveSignInEmail(userInfo, idClaims)

	// Validate email
	if email == "" {
		return nil, ErrEmailNotVerified
	}

	// Look up organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, fmt.Errorf("organization not found: %w", err)
	}

	// Find or create user
	user, _, err := s.findOrCreateUser(ctx, userInfo, email, trusted)
	if err != nil {
		if errors.Is(err, ErrEmailNotVerified) {
			slog.WarnContext(ctx,
				"microsoft sign-in refused: email not verified; a multi-tenant app registration "+
					"must add the 'email' and 'xms_edov' optional claims to the ID token",
				"tid", idClaims.TID, "oid", userInfo.ID, "missingClaim", microsoftClaimEmailVerified)
		}

		return nil, fmt.Errorf("failed to find/create user: %w", err)
	}

	// Admission policy + session minting, shared by every connector
	// (see Service.JoinOrgViaLogin). A user the org does not admit gets
	// login.Pending instead of a membership, with a session on an org they
	// already belong to (login.FallbackOrgSlug) or an org-less one.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user,
		WithLoginMethod(signupMethodMicrosoft))
	if err != nil {
		return nil, err
	}

	return login.Outcome(org.Slug, user.UID), nil
}

// isPinnedTenant reports whether the configured tenant restricts sign-in to a
// single Entra directory.
func (s *MicrosoftOAuthService) isPinnedTenant() bool {
	// common / organizations / consumers admit users of any Entra tenant
	// (and/or personal accounts) rather than a single directory.
	switch strings.ToLower(strings.TrimSpace(s.cfg.Microsoft.TenantID)) {
	case "", microsoftTenantCommon, "organizations", "consumers":
		return false
	default:
		return true
	}
}

// resolveSignInEmail returns the email to use for this sign-in and whether it
// may be trusted to link or create an account (see the trust rule in
// HandleCallback).
func (s *MicrosoftOAuthService) resolveSignInEmail(
	userInfo *MicrosoftUserInfo, idClaims microsoftIDTokenClaims,
) (string, bool) {
	graphEmail := userInfo.Mail
	if graphEmail == "" {
		graphEmail = userInfo.UserPrincipalName
	}

	if s.isPinnedTenant() {
		return graphEmail, graphEmail != ""
	}

	if idClaims.Email != "" && idClaims.EmailVerified {
		return idClaims.Email, true
	}

	// Untrusted: the email is still returned so an already-linked Microsoft
	// ID keeps signing in, but findOrCreateUser will not match or create by it.
	return graphEmail, false
}

// parseIDToken decodes the ID token the token endpoint returned alongside the
// access token. The signature is not checked: the token came over the back
// channel, from the token endpoint, over TLS, in exchange for our client
// secret, which OIDC Core §3.1.3.7 (6) accepts in place of a signature check.
// The audience, expiry and subject binding (oid == Graph id) are still
// enforced; a mismatch is ErrMicrosoftAPI. A missing ID token yields empty
// claims (no verified email), not an error.
func (s *MicrosoftOAuthService) parseIDToken(
	rawToken, graphID string, now time.Time,
) (microsoftIDTokenClaims, error) {
	var out microsoftIDTokenClaims

	if rawToken == "" {
		return out, nil
	}

	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(rawToken, claims); err != nil {
		return out, fmt.Errorf("%w: invalid id_token: %w", ErrMicrosoftAPI, err)
	}

	aud, err := claims.GetAudience()
	if err != nil || !slices.Contains([]string(aud), s.cfg.Microsoft.ClientID) {
		return out, fmt.Errorf("%w: id_token audience mismatch", ErrMicrosoftAPI)
	}

	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil || !now.Before(exp.Time) {
		return out, fmt.Errorf("%w: id_token expired or without expiry", ErrMicrosoftAPI)
	}

	out.OID = claimString(claims, "oid")
	if out.OID == "" || out.OID != graphID {
		return out, fmt.Errorf("%w: id_token oid does not match the Graph user id", ErrMicrosoftAPI)
	}

	out.TID = claimString(claims, "tid")
	out.Email = claimString(claims, keyEmail)

	// Only a JSON boolean true counts, as for OIDC email_verified.
	if b, ok := claims[microsoftClaimEmailVerified].(bool); ok {
		out.EmailVerified = b
	}

	return out, nil
}

// getTokenURL returns the Microsoft token endpoint URL for the configured tenant.
func (s *MicrosoftOAuthService) getTokenURL() string {
	if s.tokenURL != "" {
		return s.tokenURL
	}

	tenant := s.cfg.Microsoft.TenantID
	if tenant == "" {
		tenant = microsoftTenantCommon
	}

	return "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token"
}

// exchangeCode exchanges an authorization code for tokens with Microsoft.
func (s *MicrosoftOAuthService) exchangeCode(ctx context.Context, code string) (*MicrosoftTokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", s.cfg.Microsoft.ClientID)
	data.Set("client_secret", s.cfg.Microsoft.ClientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", s.getCallbackURL())
	data.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.getTokenURL(), strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var tokenResp MicrosoftTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if tokenResp.Error != "" {
		return nil, fmt.Errorf("%w: %s: %s", ErrMicrosoftAPI, tokenResp.Error, tokenResp.ErrorDesc)
	}

	return &tokenResp, nil
}

// fetchUserProfile fetches user profile from Microsoft Graph /me endpoint.
func (s *MicrosoftOAuthService) fetchUserProfile(ctx context.Context, accessToken string) (*MicrosoftUserInfo, error) {
	profileURL := microsoftUserURL
	if s.userURL != "" {
		profileURL = s.userURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call API: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrMicrosoftAPI, resp.StatusCode)
	}

	var userInfo MicrosoftUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &userInfo, nil
}

// findOrCreateUser finds or creates a user by Microsoft identity. An
// already-linked Microsoft ID signs in whatever the email status; matching an
// existing account by email, or creating one, requires emailTrusted, and
// otherwise fails with ErrEmailNotVerified before any lookup or write.
func (s *MicrosoftOAuthService) findOrCreateUser(
	ctx context.Context, userInfo *MicrosoftUserInfo, email string, emailTrusted bool,
) (*models.User, bool, error) {
	// created reports whether THIS call minted the account.
	var created bool

	// Check by Microsoft user ID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeMicrosoft, userInfo.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to get user provider: %w", err)
	}

	if err == nil && provider != nil {
		// A link pointing at a soft-deleted user is stale: it is cleared
		// and we fall through to the email lookup / create path rather
		// than failing this login (and every later one) forever.
		user, resolveErr := ResolveLinkedUser(ctx, s.db, models.ProviderTypeMicrosoft, userInfo.ID, provider)
		if resolveErr != nil {
			return nil, false, resolveErr
		}

		if user != nil {
			return user, false, nil
		}

		provider = nil
	}

	// No link: the email paths below would bind this Microsoft identity to
	// whatever account owns the address (or mint one, consuming invitations
	// and auto-join rules for it). Refuse unless the email is trusted.
	if !emailTrusted {
		return nil, false, ErrEmailNotVerified
	}

	// Check by email
	user, err := s.db.GetUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to get user by email: %w", err)
	}

	// Create new user if not found
	if user == nil {
		created = true
		user = models.NewUser(email)
		user.Name = userInfo.DisplayName

		now := time.Now()
		user.EmailVerifiedAt = &now

		// Routed through the package's single account-creation chokepoint so
		// the user_signed_up product event fires for SSO signups too.
		if err := createUserAndCapture(ctx, s.db, user, signupMethodMicrosoft); err != nil {
			return nil, false, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link Microsoft provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeMicrosoft, userInfo.ID)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, false, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, created, nil
}

// getCallbackURL returns the OAuth callback URL for Microsoft.
func (s *MicrosoftOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/microsoft/callback"
}
