package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	josejwk "github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

const (
	fakeIdPClientID     = "test-client-id"
	fakeIdPClientSecret = "test-client-secret"
	fakeIdPKeyID        = "test-key-1"
)

// fakeOIDCIdP is a minimal in-repo OIDC identity provider used to drive
// end-to-end tests of the generic OIDC connector without any external
// dependency: it serves just enough of discovery + JWKS + token exchange for
// oidcService.HandleCallback to run its full real code path (discovery,
// token exchange, ID token signature/issuer/audience/expiry validation).
type fakeOIDCIdP struct {
	server  *httptest.Server
	privKey *rsa.PrivateKey

	// nextIDToken is served as the "id_token" field of the next /token
	// response. Set per (sub)test before invoking the flow under test.
	nextIDToken string
}

func newFakeOIDCIdP(t *testing.T) *fakeOIDCIdP {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	idp := &fakeOIDCIdP{privKey: priv}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.server.URL,
			"authorization_endpoint":                idp.server.URL + "/authorize",
			"token_endpoint":                        idp.server.URL + "/token",
			"jwks_uri":                              idp.server.URL + "/jwks",
			"userinfo_endpoint":                     idp.server.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		jwk := josejwk.JSONWebKey{
			Key:       &idp.privKey.PublicKey,
			KeyID:     fakeIdPKeyID,
			Algorithm: "RS256",
			Use:       "sig",
		}
		set := josejwk.JSONWebKeySet{Keys: []josejwk.JSONWebKey{jwk}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idp.nextIDToken,
		})
	})

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)

	return idp
}

// issueIDToken signs an ID token with the fake IdP's key. mutate lets tests
// override individual claims (issuer, audience, expiry) to exercise the
// negative validation paths.
func (idp *fakeOIDCIdP) issueIDToken(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()

	now := time.Now()
	claims := jwt.MapClaims{
		"iss":            idp.server.URL,
		"aud":            fakeIdPClientID,
		"sub":            "oidc-user-123",
		"email":          "test@example.com",
		"email_verified": true,
		"name":           "Test User",
		"picture":        "https://example.com/avatar.png",
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	}

	if mutate != nil {
		mutate(claims)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = fakeIdPKeyID

	signed, err := token.SignedString(idp.privKey)
	require.NoError(t, err)

	return signed
}

// issueIDTokenSignedByOtherKey signs a token with a different (unpublished)
// key, simulating a forged/incorrectly-signed token.
func (idp *fakeOIDCIdP) issueIDTokenSignedByOtherKey(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	now := time.Now()
	claims := jwt.MapClaims{
		"iss":            idp.server.URL,
		"aud":            fakeIdPClientID,
		"sub":            "oidc-user-123",
		"email":          "test@example.com",
		"email_verified": true,
		"name":           "Test User",
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	}

	if mutate != nil {
		mutate(claims)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = fakeIdPKeyID

	signed, err := token.SignedString(otherKey)
	require.NoError(t, err)

	return signed
}

// stubEntitlementsChecker lets tests force CheckSSOMembership to succeed or
// fail without pulling in the full entitlements service.
type stubEntitlementsChecker struct {
	err error
}

func (s *stubEntitlementsChecker) CheckSSOMembership(_ context.Context, _ string) error {
	return s.err
}

// setupOIDCTestService wires an OIDCOAuthService against the given fake IdP.
// entitlements may be nil (no-op, matching production default when no
// entitlements service is configured).
func setupOIDCTestService(
	t *testing.T, idp *fakeOIDCIdP, entitlements EntitlementsChecker,
) (*OIDCOAuthService, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		OIDC: config.OIDCOAuthConfig{
			Enabled:      true,
			IssuerURL:    idp.server.URL,
			ClientID:     fakeIdPClientID,
			ClientSecret: fakeIdPClientSecret,
		},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{
			BaseURL: "http://localhost:4000",
		},
	}

	authService := NewService(dbService, cfg.Auth, cfg, nil, entitlements)
	svc := NewOIDCOAuthService(dbService, cfg, authService)

	return svc, ctx
}

func setupOIDCTestOrg(ctx context.Context, t *testing.T, svc *OIDCOAuthService) *models.Organization {
	t.Helper()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	return org
}

func TestOIDCHandleCallback_HappyPath(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	org := setupOIDCTestOrg(ctx, t, svc)

	idp.nextIDToken = idp.issueIDToken(t, nil)

	result, err := svc.HandleCallback(ctx, "fake-code", org.Slug)
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
	assert.NotEmpty(t, result.RefreshToken)
	assert.Equal(t, org.Slug, result.OrgSlug)

	// A User + UserProvider(ProviderTypeOIDC) must have been created.
	user, err := svc.db.GetUserByEmail(ctx, "test@example.com")
	require.NoError(t, err)
	require.NotNil(t, user)
	assert.Equal(t, "Test User", user.Name)

	provider, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeOIDC, "oidc-user-123")
	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.Equal(t, user.UID, provider.UserUID)

	// A session (org membership) must exist too.
	member, err := svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	assert.Equal(t, user.UID, member.UserUID)
}

func TestOIDCHandleCallback_ExistingUserLogsInAgain(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	org := setupOIDCTestOrg(ctx, t, svc)

	idp.nextIDToken = idp.issueIDToken(t, nil)
	first, err := svc.HandleCallback(ctx, "fake-code-1", org.Slug)
	require.NoError(t, err)

	idp.nextIDToken = idp.issueIDToken(t, nil)
	second, err := svc.HandleCallback(ctx, "fake-code-2", org.Slug)
	require.NoError(t, err)

	assert.Equal(t, first.UserUID, second.UserUID)
}

func TestOIDCHandleCallback_RejectsInvalidTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		token func(idp *fakeOIDCIdP, t *testing.T) string
	}{
		{
			name: "wrong issuer",
			token: func(idp *fakeOIDCIdP, t *testing.T) string {
				return idp.issueIDToken(t, func(c jwt.MapClaims) {
					c["iss"] = "https://not-the-real-idp.example.com"
				})
			},
		},
		{
			name: "wrong audience",
			token: func(idp *fakeOIDCIdP, t *testing.T) string {
				return idp.issueIDToken(t, func(c jwt.MapClaims) {
					c["aud"] = "some-other-client-id"
				})
			},
		},
		{
			name: "expired token",
			token: func(idp *fakeOIDCIdP, t *testing.T) string {
				return idp.issueIDToken(t, func(c jwt.MapClaims) {
					c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
					c["exp"] = time.Now().Add(-time.Hour).Unix()
				})
			},
		},
		{
			name: "bad signature (unpublished key)",
			token: func(idp *fakeOIDCIdP, t *testing.T) string {
				return idp.issueIDTokenSignedByOtherKey(t, nil)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			idp := newFakeOIDCIdP(t)
			svc, ctx := setupOIDCTestService(t, idp, nil)
			org := setupOIDCTestOrg(ctx, t, svc)

			idp.nextIDToken = tc.token(idp, t)

			_, err := svc.HandleCallback(ctx, "fake-code", org.Slug)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrOIDCTokenInvalid)
		})
	}
}

func TestOIDCHandleCallback_EnforcesMaxSSOUsers(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	quotaErr := errors.New("sso membership quota exceeded")
	svc, ctx := setupOIDCTestService(t, idp, &stubEntitlementsChecker{err: quotaErr})
	org := setupOIDCTestOrg(ctx, t, svc)

	idp.nextIDToken = idp.issueIDToken(t, nil)

	_, err := svc.HandleCallback(ctx, "fake-code", org.Slug)
	require.Error(t, err)
	assert.ErrorIs(t, err, quotaErr)

	// The membership must not have been created since the quota check
	// (CheckSSOSlot -> entitlements.CheckSSOMembership) failed before
	// CreateOrganizationMember ran.
	members, err := svc.db.ListMembersByOrg(ctx, org.UID)
	require.NoError(t, err)
	assert.Empty(t, members)
}
