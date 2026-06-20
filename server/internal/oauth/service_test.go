package oauth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

const oauthTestIssuer = "https://solidping.test"

// oauthFixture bundles everything the service-level tests need: the OAuth and
// auth services plus a seeded org, user, and registered public client.
type oauthFixture struct {
	svc     *Service
	authSvc *auth.Service
	db      db.Service
	org     *models.Organization
	user    *models.User
	client  *models.OAuthClient
	//nolint:containedctx // test fixture convenience; carries the per-test context.
	ctx context.Context
}

// setupOAuthService builds an OAuth Service backed by in-memory SQLite and a
// real auth.Service (for JWT minting/validation), plus a seeded org + user and a
// registered public client with a loopback redirect. This is the SQLite path of
// the new-tables coverage; the db package's testOAuthRepos covers Postgres too.
func setupOAuthService(t *testing.T) oauthFixture {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	fullCfg := &config.Config{
		Server: config.ServerConfig{BaseURL: oauthTestIssuer},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
	}

	authSvc := auth.NewService(dbService, fullCfg.Auth, fullCfg, nil, nil)
	oauthSvc := NewService(dbService, authSvc, fullCfg)

	org := models.NewOrganization("oauth-svc-org", "")
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	user := models.NewUser("oauth-svc@example.com")
	require.NoError(t, dbService.CreateUser(ctx, user))

	client, _, err := oauthSvc.RegisterClient(
		ctx, "Test Client",
		[]string{"http://127.0.0.1:1234/cb"},
		[]string{GrantAuthorizationCode, GrantRefreshToken},
		[]string{ScopeMCP}, true,
	)
	require.NoError(t, err)

	return oauthFixture{
		svc: oauthSvc, authSvc: authSvc, db: dbService,
		org: org, user: user, client: client, ctx: ctx,
	}
}

// grant builds a standard auth-code grant for the seeded fixtures.
func (f oauthFixture) grant(challenge, scope string) *AuthCodeGrant {
	return &AuthCodeGrant{
		ClientID:            f.client.ClientID,
		UserUID:             f.user.UID,
		OrgUID:              f.org.UID,
		OrgSlug:             f.org.Slug,
		RedirectURI:         "http://127.0.0.1:1234/cb",
		Scope:               scope,
		Resource:            oauthTestIssuer + "/api/v1/mcp",
		CodeChallenge:       challenge,
		CodeChallengeMethod: CodeChallengeMethodS256,
	}
}

const (
	testVerifier    = "abcdefghijklmnopqrstuvwxyz1234567890ABCDEFG"
	testRedirectURI = "http://127.0.0.1:1234/cb"
)

func TestExchangeAuthCodeHappyPath(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	require.NoError(t, err)

	res, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	require.NoError(t, err)
	require.NotEmpty(t, res.AccessToken)
	require.NotEmpty(t, res.RefreshToken)

	// The minted access token must validate and carry the MCP audience + scope,
	// so it is accepted at the MCP resource (audience binding works end to end).
	claims, err := f.authSvc.ValidateToken(f.ctx, res.AccessToken)
	require.NoError(t, err)
	require.Contains(t, claims.Scopes, ScopeMCP)
	require.True(t, TokenHasResourceAudience(claims, oauthTestIssuer+"/api/v1/mcp"))
	require.Equal(t, f.org.Slug, claims.OrgSlug)
}

func TestExchangeAuthCodeReadOnlyScope(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	// A consent for the read-only scope must produce a token carrying mcp:read
	// (and not the broader mcp), which the MCP handler uses to refuse mutations.
	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCPRead))
	require.NoError(t, err)

	res, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	require.NoError(t, err)

	claims, err := f.authSvc.ValidateToken(f.ctx, res.AccessToken)
	require.NoError(t, err)
	require.Contains(t, claims.Scopes, ScopeMCPRead)
	require.NotContains(t, claims.Scopes, ScopeMCP)
}

func TestExchangeAuthCodeRejectsReplay(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	require.NoError(t, err)

	_, err = f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	require.NoError(t, err)

	// Second redemption of the same code must fail (single-use).
	_, err = f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	require.ErrorIs(t, err, errInvalidGrant)
}

func TestExchangeAuthCodeRejectsBadPKCE(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	require.NoError(t, err)

	_, err = f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI,
		"wrong-verifier-padding-to-min-length-1234567")
	require.ErrorIs(t, err, errPKCEFailed)
}

func TestExchangeAuthCodeRejectsWrongRedirect(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	require.NoError(t, err)

	_, err = f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, "http://127.0.0.1:9999/evil", testVerifier)
	require.ErrorIs(t, err, errInvalidGrant)
}

func TestExchangeAuthCodeRejectsExpired(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	fake := clock.NewFake(time.Now())
	f.svc.SetClock(fake)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	require.NoError(t, err)

	// Advance past the short auth-code TTL.
	fake.Advance(2 * time.Minute)

	_, err = f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	require.ErrorIs(t, err, errInvalidGrant)
}

func TestRefreshTokenRotationInvalidatesPrior(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	require.NoError(t, err)

	first, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	require.NoError(t, err)

	// Rotate once: a fresh pair is issued.
	rotated, err := f.svc.ExchangeRefreshToken(f.ctx, first.RefreshToken, f.client.ClientID)
	require.NoError(t, err)
	require.NotEmpty(t, rotated.RefreshToken)
	require.NotEqual(t, first.RefreshToken, rotated.RefreshToken)

	// The prior refresh token is now invalid.
	_, err = f.svc.ExchangeRefreshToken(f.ctx, first.RefreshToken, f.client.ClientID)
	require.ErrorIs(t, err, errInvalidGrant)

	// The new one still works.
	_, err = f.svc.ExchangeRefreshToken(f.ctx, rotated.RefreshToken, f.client.ClientID)
	require.NoError(t, err)
}

func TestRegisterClientPublicVsConfidential(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	pub, pubSecret, err := f.svc.RegisterClient(
		f.ctx, "Public", []string{"http://127.0.0.1:1/cb"},
		[]string{GrantAuthorizationCode}, []string{ScopeMCP}, true,
	)
	require.NoError(t, err)
	require.Empty(t, pubSecret, "public client gets no secret")
	require.Nil(t, pub.SecretHash)
	require.True(t, pub.IsPublic)

	conf, confSecret, err := f.svc.RegisterClient(
		f.ctx, "Confidential", []string{"https://app.example.com/cb"},
		[]string{GrantAuthorizationCode}, []string{ScopeMCP}, false,
	)
	require.NoError(t, err)
	require.NotEmpty(t, confSecret, "confidential client gets a secret")
	require.NotNil(t, conf.SecretHash)
	require.False(t, conf.IsPublic)
}
