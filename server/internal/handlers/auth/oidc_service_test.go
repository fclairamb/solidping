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
	fakeIDPClientID     = "test-client-id"
	fakeIDPClientSecret = "test-client-secret"
	fakeIDPKeyID        = "test-key-1"
)

// errSSOQuotaTest is a static sentinel used by TestOIDCHandleCallback_EnforcesMaxSSOUsers
// to simulate an org that has reached its maxSsoUsers cap.
var errSSOQuotaTest = errors.New("sso membership quota exceeded")

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
			KeyID:     fakeIDPKeyID,
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

// baseClaims returns a valid claim set for the fake IdP's user. Tests mutate
// a copy via issueIDToken/issueIDTokenSignedByOtherKey to exercise negative
// validation paths (wrong issuer, wrong audience, expired, bad signature).
func (idp *fakeOIDCIdP) baseClaims() jwt.MapClaims {
	now := time.Now()

	return jwt.MapClaims{
		"iss":                  idp.server.URL,
		"aud":                  fakeIDPClientID,
		"sub":                  "oidc-user-123",
		keyEmail:               "test@example.com",
		oidcClaimEmailVerified: true,
		keyName:                "Test User",
		"picture":              "https://example.com/avatar.png",
		"iat":                  now.Unix(),
		"exp":                  now.Add(time.Hour).Unix(),
	}
}

// issueIDToken signs an ID token with the fake IdP's key. mutate lets tests
// override individual claims to exercise the negative validation paths.
func (idp *fakeOIDCIdP) issueIDToken(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()

	claims := idp.baseClaims()
	if mutate != nil {
		mutate(claims)
	}

	return idp.signClaims(t, idp.privKey, claims)
}

// issueIDTokenSignedByOtherKey signs a token with a different (unpublished)
// key, simulating a forged/incorrectly-signed token.
func (idp *fakeOIDCIdP) issueIDTokenSignedByOtherKey(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	claims := idp.baseClaims()
	if mutate != nil {
		mutate(claims)
	}

	return idp.signClaims(t, otherKey, claims)
}

func (idp *fakeOIDCIdP) signClaims(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = fakeIDPKeyID

	signed, err := token.SignedString(key)
	require.NoError(t, err)

	return signed
}

// stubEntitlementsChecker lets tests force CheckMembership to succeed or
// fail without pulling in the full entitlements service.
type stubEntitlementsChecker struct {
	err error
}

func (s *stubEntitlementsChecker) CheckMembership(_ context.Context, _ string) error {
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
			ClientID:     fakeIDPClientID,
			ClientSecret: fakeIDPClientSecret,
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
		token func(t *testing.T, idp *fakeOIDCIdP) string
	}{
		{
			name: "wrong issuer",
			token: func(t *testing.T, idp *fakeOIDCIdP) string {
				t.Helper()

				return idp.issueIDToken(t, func(c jwt.MapClaims) {
					c["iss"] = "https://not-the-real-idp.example.com"
				})
			},
		},
		{
			name: "wrong audience",
			token: func(t *testing.T, idp *fakeOIDCIdP) string {
				t.Helper()

				return idp.issueIDToken(t, func(c jwt.MapClaims) {
					c["aud"] = "some-other-client-id"
				})
			},
		},
		{
			name: "expired token",
			token: func(t *testing.T, idp *fakeOIDCIdP) string {
				t.Helper()

				return idp.issueIDToken(t, func(c jwt.MapClaims) {
					c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
					c["exp"] = time.Now().Add(-time.Hour).Unix()
				})
			},
		},
		{
			name: "bad signature (unpublished key)",
			token: func(t *testing.T, idp *fakeOIDCIdP) string {
				t.Helper()

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

			idp.nextIDToken = tc.token(t, idp)

			_, err := svc.HandleCallback(ctx, "fake-code", org.Slug)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrOIDCTokenInvalid)
		})
	}
}

func TestOIDCHandleCallback_EnforcesMaxSSOUsers(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, &stubEntitlementsChecker{err: errSSOQuotaTest})
	org := setupOIDCTestOrg(ctx, t, svc)

	idp.nextIDToken = idp.issueIDToken(t, nil)

	_, err := svc.HandleCallback(ctx, "fake-code", org.Slug)
	require.Error(t, err)
	require.ErrorIs(t, err, errSSOQuotaTest)

	// The membership must not have been created since the quota check
	// (CheckMembershipSlot -> entitlements.CheckMembership) failed before
	// CreateOrganizationMember ran.
	members, err := svc.db.ListMembersByOrg(ctx, org.UID)
	require.NoError(t, err)
	assert.Empty(t, members)
}

// createExistingLocalUser simulates a pre-existing, password-based account
// (e.g. the documented default super-admin) that a malicious/misconfigured
// OIDC IdP might try to take over by asserting the same email address.
func createExistingLocalUser(ctx context.Context, t *testing.T, svc *OIDCOAuthService, email string) *models.User {
	t.Helper()

	hash := "not-a-real-hash"
	user := models.NewUser(email)
	user.Name = "Existing Local User"
	user.PasswordHash = &hash

	require.NoError(t, svc.db.CreateUser(ctx, user))

	return user
}

// TestOIDCHandleCallback_UnverifiedEmailDoesNotAutoLink is the regression test
// for the account-takeover vulnerability: findOrCreateUser used to fall
// through to GetUserByEmail (and then link + issue a session) whenever
// email_verified was absent, false, or a non-boolean value, because mapClaims
// defaulted EmailVerified to true unless the claim was literally a JSON
// boolean. A malicious/misconfigured IdP could exploit this to assert any
// existing user's email - including the documented default super-admin
// admin@solidping.io - and obtain a fully valid session with no password
// check.
func TestOIDCHandleCallback_UnverifiedEmailDoesNotAutoLink(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		targetEmail    string
		mutateClaims   func(c jwt.MapClaims)
		wantErrIs      error
		superAdminUser bool
	}{
		{
			name:        "email_verified claim absent",
			targetEmail: "victim@example.com",
			mutateClaims: func(c jwt.MapClaims) {
				delete(c, oidcClaimEmailVerified)
			},
			wantErrIs: ErrEmailNotVerified,
		},
		{
			name:        "email_verified claim is a non-boolean string",
			targetEmail: "victim2@example.com",
			mutateClaims: func(c jwt.MapClaims) {
				c[oidcClaimEmailVerified] = "false"
			},
			wantErrIs: ErrEmailNotVerified,
		},
		{
			name:        "email_verified explicitly false",
			targetEmail: "victim3@example.com",
			mutateClaims: func(c jwt.MapClaims) {
				c[oidcClaimEmailVerified] = false
			},
			wantErrIs: ErrEmailNotVerified,
		},
		{
			name:        "documented default super-admin email, unverified",
			targetEmail: "admin@solidping.io",
			mutateClaims: func(c jwt.MapClaims) {
				delete(c, oidcClaimEmailVerified)
			},
			wantErrIs:      ErrEmailNotVerified,
			superAdminUser: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			idp := newFakeOIDCIdP(t)
			svc, ctx := setupOIDCTestService(t, idp, nil)
			org := setupOIDCTestOrg(ctx, t, svc)

			existing := createExistingLocalUser(ctx, t, svc, tc.targetEmail)
			if tc.superAdminUser {
				existing.SuperAdmin = true
				require.NoError(t, svc.db.UpdateUser(ctx, existing.UID, &models.UserUpdate{
					SuperAdmin: &existing.SuperAdmin,
				}))
			}

			idp.nextIDToken = idp.issueIDToken(t, func(c jwt.MapClaims) {
				c[keyEmail] = tc.targetEmail
				tc.mutateClaims(c)
			})

			result, err := svc.HandleCallback(ctx, "fake-code", org.Slug)
			require.Error(t, err)
			require.ErrorIs(t, err, tc.wantErrIs)
			assert.Nil(t, result)

			// No session/takeover: no OIDC provider link was created for the
			// pre-existing account, and it gained no new org membership.
			_, err = svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeOIDC, "oidc-user-123")
			require.Error(t, err)

			members, err := svc.db.ListMembersByOrg(ctx, org.UID)
			require.NoError(t, err)
			assert.Empty(t, members)

			// The existing account itself must be completely untouched.
			unchanged, err := svc.db.GetUserByEmail(ctx, tc.targetEmail)
			require.NoError(t, err)
			assert.Equal(t, existing.UID, unchanged.UID)
			assert.Equal(t, existing.PasswordHash, unchanged.PasswordHash)
		})
	}
}

// TestOIDCHandleCallback_VerifiedEmailStillLinksToExistingUser is the
// regression guard for the legitimate path: an IdP that properly asserts
// email_verified as a real JSON boolean `true` must still be able to link its
// asserted identity to a pre-existing local user by email (e.g. a user who
// signed up with a password and later enables SSO with a matching, verified
// IdP account).
func TestOIDCHandleCallback_VerifiedEmailStillLinksToExistingUser(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	org := setupOIDCTestOrg(ctx, t, svc)

	const email = "legit-user@example.com"

	existing := createExistingLocalUser(ctx, t, svc, email)

	idp.nextIDToken = idp.issueIDToken(t, func(c jwt.MapClaims) {
		c[keyEmail] = email
		c[oidcClaimEmailVerified] = true
	})

	result, err := svc.HandleCallback(ctx, "fake-code", org.Slug)
	require.NoError(t, err)
	assert.Equal(t, existing.UID, result.UserUID)

	provider, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeOIDC, "oidc-user-123")
	require.NoError(t, err)
	assert.Equal(t, existing.UID, provider.UserUID)

	member, err := svc.db.GetMemberByUserAndOrg(ctx, existing.UID, org.UID)
	require.NoError(t, err)
	assert.Equal(t, existing.UID, member.UserUID)
}
