package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// Token lifetimes and grant constants for the embedded authorization server.
const (
	// authCodeTTL is how long an issued authorization code stays redeemable.
	// Codes are single-use and very short-lived (OAuth 2.1 recommends ≤ 60s).
	authCodeTTL = 60 * time.Second
	// accessTokenTTL is the lifetime of an OAuth-issued MCP access token.
	// Short by design — clients refresh with the rotating refresh token.
	accessTokenTTL = 30 * time.Minute
	// refreshTokenTTL is the lifetime of a refresh grant.
	refreshTokenTTL = 30 * 24 * time.Hour

	// tokenEntropyBytes is the size of the random secret backing codes, refresh
	// tokens, and generated client IDs/secrets before base64url encoding.
	tokenEntropyBytes = 32
)

// Sentinel errors returned by the service for the token/authorize flows. The
// handler maps these to the appropriate OAuth error response. They are
// package-internal — callers outside the package interact via the HTTP surface.
var (
	// errClientNotFound means no registered client matches the client_id.
	errClientNotFound = errors.New("oauth: client not found")
	// errInvalidGrant means the auth code or refresh token is missing, expired,
	// already used, revoked, or fails a binding check.
	errInvalidGrant = errors.New("oauth: invalid grant")
	// errPKCEFailed means the supplied code_verifier does not match the stored
	// code_challenge.
	errPKCEFailed = errors.New("oauth: pkce verification failed")
)

// Service holds the business logic for the embedded OAuth 2.1 authorization
// server: client validation, authorization-code issuance/redemption, and token
// minting (delegated to the auth service for JWT signing).
type Service struct {
	db      db.Service
	authSvc *auth.Service
	cfg     *config.Config
	clock   clock.Clock
}

// NewService builds the OAuth service. authSvc is reused for JWT signing and
// refresh-token semantics so OAuth-issued credentials are indistinguishable
// from session credentials downstream.
func NewService(dbService db.Service, authSvc *auth.Service, cfg *config.Config) *Service {
	return &Service{
		db:      dbService,
		authSvc: authSvc,
		cfg:     cfg,
		clock:   clock.Real{},
	}
}

// SetClock overrides the service clock. It exists so tests can drive
// authorization-code and refresh-token expiry deterministically.
func (s *Service) SetClock(c clock.Clock) {
	s.clock = c
}

// randomToken returns a base64url-encoded, unpadded random secret.
func randomToken() (string, error) {
	buf := make([]byte, tokenEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GetClient looks up a registered client by its client_id.
func (s *Service) GetClient(ctx context.Context, clientID string) (*models.OAuthClient, error) {
	client, err := s.db.GetOAuthClientByClientID(ctx, clientID)
	if err != nil {
		return nil, errClientNotFound
	}

	return client, nil
}

// AuthCodeGrant captures everything an authorization code binds together. The
// token endpoint replays these bindings (client, redirect, PKCE, resource) to
// stop a stolen code from being redeemed under different parameters.
type AuthCodeGrant struct {
	ClientID            string
	UserUID             string
	OrgUID              string
	OrgSlug             string
	RedirectURI         string
	Scope               string
	Resource            string
	CodeChallenge       string
	CodeChallengeMethod string
}

// IssueAuthCode mints a single-use, short-lived authorization code bound to the
// grant. The returned code is the opaque value handed back to the client via the
// redirect.
func (s *Service) IssueAuthCode(ctx context.Context, grant *AuthCodeGrant) (string, error) {
	code, err := randomToken()
	if err != nil {
		return "", err
	}

	now := s.clock.Now()
	row := &models.OAuthAuthCode{
		UID:                 uuid.New().String(),
		Code:                code,
		ClientID:            grant.ClientID,
		UserUID:             grant.UserUID,
		OrganizationUID:     grant.OrgUID,
		RedirectURI:         grant.RedirectURI,
		Scope:               grant.Scope,
		Resource:            grant.Resource,
		CodeChallenge:       grant.CodeChallenge,
		CodeChallengeMethod: grant.CodeChallengeMethod,
		ExpiresAt:           now.Add(authCodeTTL),
		CreatedAt:           now,
	}

	if err := s.db.CreateOAuthAuthCode(ctx, row); err != nil {
		return "", fmt.Errorf("persist auth code: %w", err)
	}

	return code, nil
}

// TokenResult is the successful output of a token-endpoint exchange.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	Scope        string
	ExpiresIn    int
}

// ExchangeAuthCode redeems an authorization code for an access + refresh token.
// It enforces, in order: the code exists, is unexpired, and is consumed exactly
// once (atomic compare-and-set); the redirect_uri and client_id match the
// binding; and the PKCE code_verifier matches the stored S256 challenge. Any
// failure returns errInvalidGrant / errPKCEFailed without minting a token.
func (s *Service) ExchangeAuthCode(
	ctx context.Context, code, clientID, redirectURI, codeVerifier string,
) (*TokenResult, error) {
	row, err := s.db.GetOAuthAuthCode(ctx, code)
	if err != nil {
		return nil, errInvalidGrant
	}

	now := s.clock.Now()
	if now.After(row.ExpiresAt) {
		return nil, errInvalidGrant
	}

	if row.ClientID != clientID || row.RedirectURI != redirectURI {
		return nil, errInvalidGrant
	}

	if !VerifyPKCE(codeVerifier, row.CodeChallenge) {
		return nil, errPKCEFailed
	}

	// Atomic single-use guard: a replay updates zero rows and is rejected.
	won, err := s.db.ConsumeOAuthAuthCode(ctx, code, now)
	if err != nil {
		return nil, fmt.Errorf("consume auth code: %w", err)
	}

	if !won {
		return nil, errInvalidGrant
	}

	orgSlug, err := s.orgSlug(ctx, row.OrganizationUID)
	if err != nil {
		return nil, err
	}

	return s.mintTokens(ctx, &mintInput{
		clientID: row.ClientID,
		userUID:  row.UserUID,
		orgUID:   row.OrganizationUID,
		orgSlug:  orgSlug,
		scope:    row.Scope,
		resource: row.Resource,
	})
}

// ExchangeRefreshToken rotates a refresh grant: it validates the presented token
// is active and unexpired, atomically revokes it, and issues a fresh access +
// refresh pair. Because revocation is an atomic compare-and-set, a concurrent or
// replayed use of the same refresh token loses the race and is rejected — the
// rotation invalidates the prior token.
func (s *Service) ExchangeRefreshToken(
	ctx context.Context, refreshToken, clientID string,
) (*TokenResult, error) {
	row, err := s.db.GetOAuthRefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, errInvalidGrant
	}

	now := s.clock.Now()
	if row.RevokedAt != nil || now.After(row.ExpiresAt) {
		return nil, errInvalidGrant
	}

	if clientID != "" && row.ClientID != clientID {
		return nil, errInvalidGrant
	}

	won, err := s.db.RevokeOAuthRefreshToken(ctx, refreshToken, now)
	if err != nil {
		return nil, fmt.Errorf("rotate refresh token: %w", err)
	}

	if !won {
		// Lost the race → already rotated/revoked → reject.
		return nil, errInvalidGrant
	}

	orgSlug, err := s.orgSlug(ctx, row.OrganizationUID)
	if err != nil {
		return nil, err
	}

	return s.mintTokens(ctx, &mintInput{
		clientID: row.ClientID,
		userUID:  row.UserUID,
		orgUID:   row.OrganizationUID,
		orgSlug:  orgSlug,
		scope:    row.Scope,
		resource: row.Resource,
	})
}

// mintInput carries the parameters needed to mint a token pair.
type mintInput struct {
	clientID string
	userUID  string
	orgUID   string
	orgSlug  string
	scope    string
	resource string
}

// mintTokens issues an audience-bound JWT access token (via the auth service's
// signing key) and a persisted rotating refresh token.
func (s *Service) mintTokens(ctx context.Context, input *mintInput) (*TokenResult, error) {
	scopes := ParseScopes(input.scope)

	accessToken, err := s.authSvc.GenerateMCPAccessToken(
		input.userUID, input.orgSlug, scopes, input.resource, accessTokenTTL,
	)
	if err != nil {
		return nil, fmt.Errorf("mint access token: %w", err)
	}

	refreshValue, err := randomToken()
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	refreshRow := &models.OAuthRefreshToken{
		UID:             uuid.New().String(),
		Token:           refreshValue,
		ClientID:        input.clientID,
		UserUID:         input.userUID,
		OrganizationUID: input.orgUID,
		Scope:           input.scope,
		Resource:        input.resource,
		ExpiresAt:       now.Add(refreshTokenTTL),
		CreatedAt:       now,
	}

	if err := s.db.CreateOAuthRefreshToken(ctx, refreshRow); err != nil {
		return nil, fmt.Errorf("persist refresh token: %w", err)
	}

	return &TokenResult{
		AccessToken:  accessToken,
		RefreshToken: refreshValue,
		Scope:        input.scope,
		ExpiresIn:    int(accessTokenTTL.Seconds()),
	}, nil
}

// orgSlug resolves an organization UID to its slug for the access-token claims.
func (s *Service) orgSlug(ctx context.Context, orgUID string) (string, error) {
	org, err := s.db.GetOrganization(ctx, orgUID)
	if err != nil {
		return "", fmt.Errorf("resolve org slug: %w", err)
	}

	return org.Slug, nil
}

// RegisterClient creates a new OAuth client (RFC 7591). Public clients (native /
// loopback, PKCE) get no secret; confidential clients get a generated secret
// returned once in the registration response. The caller has already validated
// the redirect URIs.
func (s *Service) RegisterClient(
	ctx context.Context, name string, redirectURIs, grantTypes, scopes []string, isPublic bool,
) (*models.OAuthClient, string, error) {
	clientID, err := randomToken()
	if err != nil {
		return nil, "", err
	}

	now := s.clock.Now()
	client := &models.OAuthClient{
		UID:          uuid.New().String(),
		ClientID:     clientID,
		ClientName:   name,
		RedirectURIs: redirectURIs,
		GrantTypes:   grantTypes,
		Scopes:       scopes,
		IsPublic:     isPublic,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	var plainSecret string

	if !isPublic {
		plainSecret, err = randomToken()
		if err != nil {
			return nil, "", err
		}

		hash, hashErr := passwords.Hash(plainSecret)
		if hashErr != nil {
			return nil, "", fmt.Errorf("hash client secret: %w", hashErr)
		}

		client.SecretHash = &hash
	}

	if err := s.db.CreateOAuthClient(ctx, client); err != nil {
		return nil, "", fmt.Errorf("persist client: %w", err)
	}

	return client, plainSecret, nil
}
