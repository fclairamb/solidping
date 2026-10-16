package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

func setupMicrosoftTestService(t *testing.T) (*MicrosoftOAuthService, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() {
		_ = dbService.Close()
	})

	cfg := &config.Config{
		Microsoft: config.MicrosoftOAuthConfig{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			TenantID:     "test-tenant-id",
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

	authService := NewService(dbService, cfg.Auth, cfg, nil, nil)
	svc := NewMicrosoftOAuthService(dbService, cfg, authService)

	return svc, ctx
}

func setupMicrosoftTestOrg(ctx context.Context, t *testing.T, svc *MicrosoftOAuthService) *models.Organization {
	t.Helper()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	return org
}

func TestMicrosoftOAuthState(t *testing.T) {
	t.Parallel()

	svc, ctx := setupMicrosoftTestService(t)

	t.Run("generate and validate state", func(t *testing.T) {
		t.Parallel()

		nonce, err := svc.GenerateOAuthState(ctx, "/dashboard", "test-org")
		require.NoError(t, err)
		assert.NotEmpty(t, nonce)

		state, err := svc.ValidateOAuthState(ctx, nonce)
		require.NoError(t, err)
		assert.Equal(t, "/dashboard", state.RedirectURI)
		assert.Equal(t, "test-org", state.OrgSlug)
	})

	t.Run("state is one-time use", func(t *testing.T) {
		t.Parallel()

		nonce, err := svc.GenerateOAuthState(ctx, "/", "test-org")
		require.NoError(t, err)

		_, err = svc.ValidateOAuthState(ctx, nonce)
		require.NoError(t, err)

		// Second use should fail
		_, err = svc.ValidateOAuthState(ctx, nonce)
		assert.ErrorIs(t, err, ErrInvalidOAuthState)
	})

	t.Run("invalid state returns error", func(t *testing.T) {
		t.Parallel()

		_, err := svc.ValidateOAuthState(ctx, "invalid-nonce")
		assert.ErrorIs(t, err, ErrInvalidOAuthState)
	})
}

func TestMicrosoftHandleCallback(t *testing.T) {
	t.Parallel()

	t.Run("full flow creates user and returns tokens", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		// Mock Microsoft token and user endpoints
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftTokenResponse{
				AccessToken: "mock-access-token",
				TokenType:   "Bearer",
				Scope:       "openid email profile User.Read",
				ExpiresIn:   3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftUserInfo{
				ID:                "ms-user-12345",
				DisplayName:       "Test User",
				Mail:              "test@example.com",
				UserPrincipalName: "test@example.onmicrosoft.com",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		svc.httpClient = tokenServer.Client()

		result := testMicrosoftCallbackWithMockServers(ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL)

		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, org.Slug, result.OrgSlug)
	})

	t.Run("fallback to userPrincipalName when mail is empty", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftTokenResponse{
				AccessToken: "mock-access-token",
				TokenType:   "Bearer",
				Scope:       "openid email profile User.Read",
				ExpiresIn:   3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftUserInfo{
				ID:                "ms-user-67890",
				DisplayName:       "UPN User",
				Mail:              "",
				UserPrincipalName: "upn@example.onmicrosoft.com",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		svc.httpClient = tokenServer.Client()

		result := testMicrosoftCallbackWithMockServers(ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL)

		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, org.Slug, result.OrgSlug)
	})

	t.Run("existing user login via provider ID", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		// Pre-create user and provider link
		user := models.NewUser("existing-ms@example.com")
		user.Name = "Existing MS User"
		require.NoError(t, svc.db.CreateUser(ctx, user))

		provider := models.NewUserProvider(user.UID, models.ProviderTypeMicrosoft, "ms-user-99999")
		require.NoError(t, svc.db.CreateUserProvider(ctx, provider))

		// Create membership
		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		now := time.Now()
		member.JoinedAt = &now
		require.NoError(t, svc.db.CreateOrganizationMember(ctx, member))

		// Test findOrCreateUser returns existing user
		userInfo := &MicrosoftUserInfo{
			ID:                "ms-user-99999",
			DisplayName:       "Existing MS User",
			Mail:              "existing-ms@example.com",
			UserPrincipalName: "existing-ms@example.onmicrosoft.com",
		}

		// Untrusted email: an already-linked Microsoft ID still signs in.
		foundUser, created, err := svc.findOrCreateUser(ctx, userInfo, "existing-ms@example.com", false)
		require.False(t, created, "an existing account must not be reported as newly created")
		require.NoError(t, err)
		assert.Equal(t, user.UID, foundUser.UID)
	})

	t.Run("email-based user matching", func(t *testing.T) {
		t.Parallel()

		for _, trusted := range []bool{true, false} {
			svc, ctx := setupMicrosoftTestService(t)

			// Pre-create user (e.g., from password login)
			user := models.NewUser("match-ms@example.com")
			user.Name = "Match MS User"
			require.NoError(t, svc.db.CreateUser(ctx, user))

			// Microsoft login with same email but new provider ID
			userInfo := &MicrosoftUserInfo{
				ID:                "ms-user-55555",
				DisplayName:       "Match MS User",
				Mail:              "match-ms@example.com",
				UserPrincipalName: "match-ms@example.onmicrosoft.com",
			}

			foundUser, created, err := svc.findOrCreateUser(ctx, userInfo, "match-ms@example.com", trusted)
			require.False(t, created, "an existing account matched by email must not be reported as newly created")

			linked, linkErr := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeMicrosoft, "ms-user-55555")

			if !trusted {
				require.ErrorIs(t, err, ErrEmailNotVerified)
				assert.Nil(t, foundUser)
				require.Error(t, linkErr, "an untrusted email must not link the Microsoft ID")

				continue
			}

			require.NoError(t, err)
			assert.Equal(t, user.UID, foundUser.UID)
			require.NoError(t, linkErr)
			assert.Equal(t, user.UID, linked.UserUID)
		}
	})

	t.Run("new user creation", func(t *testing.T) {
		t.Parallel()

		for _, trusted := range []bool{true, false} {
			svc, ctx := setupMicrosoftTestService(t)

			userInfo := &MicrosoftUserInfo{
				ID:                "ms-user-77777",
				DisplayName:       "Brand New User",
				Mail:              "brand-new@example.com",
				UserPrincipalName: "brand-new@example.onmicrosoft.com",
			}

			user, created, err := svc.findOrCreateUser(ctx, userInfo, "brand-new@example.com", trusted)

			if !trusted {
				require.ErrorIs(t, err, ErrEmailNotVerified)
				assert.False(t, created)
				assert.Nil(t, user)

				stored, _ := svc.db.GetUserByEmail(ctx, "brand-new@example.com")
				assert.Nil(t, stored, "an untrusted email must not create an account")

				continue
			}

			require.True(t, created, "a brand-new account must be reported as newly created")
			require.NoError(t, err)
			assert.Equal(t, "brand-new@example.com", user.Email)
			assert.Equal(t, "Brand New User", user.Name)
			assert.NotNil(t, user.EmailVerifiedAt)
		}
	})

	t.Run("ensure membership first user gets owner", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		user := models.NewUser("first@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, user))

		member, pending, err := svc.authService.JoinOrgViaLogin(ctx, org, user)
		require.NoError(t, err)
		require.False(t, pending)
		require.NotNil(t, member)
		// First member of an empty org owns it (spec 2026-08-08-11).
		assert.Equal(t, models.MemberRoleOwner, member.Role)
	})

	t.Run("ensure membership second user gets user role", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		// Create first user as admin
		firstUser := models.NewUser("first@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, firstUser))

		_, _, err := svc.authService.JoinOrgViaLogin(ctx, org, firstUser)
		require.NoError(t, err)

		// The org admits @example.com, so the second user joins as a
		// plain user (without the pattern they would be left pending —
		// see TestJoinOrgViaLogin).
		require.NoError(t, svc.db.SetOrgParameter(
			ctx, org.UID, "registration.email_pattern", `@example\.com$`, false))

		secondUser := models.NewUser("second@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, secondUser))

		member, pending, err := svc.authService.JoinOrgViaLogin(ctx, org, secondUser)
		require.NoError(t, err)
		require.False(t, pending)
		require.NotNil(t, member)
		assert.Equal(t, models.MemberRoleUser, member.Role)
	})

	t.Run("no email returns error", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftTokenResponse{AccessToken: "mock-token", TokenType: "Bearer", ExpiresIn: 3600}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		// User endpoint returns no email fields
		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftUserInfo{
				ID:                "ms-user-11111",
				DisplayName:       "No Email User",
				Mail:              "",
				UserPrincipalName: "",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		svc.httpClient = tokenServer.Client()

		_, err := testMicrosoftCallbackWithMockServersErr(
			ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL,
		)
		assert.ErrorIs(t, err, ErrEmailNotVerified)
	})
}

func TestMicrosoftGetCallbackURL(t *testing.T) {
	t.Parallel()

	svc, _ := setupMicrosoftTestService(t)

	assert.Equal(t, "http://localhost:4000/api/v1/auth/microsoft/callback", svc.getCallbackURL())
}

func TestMicrosoftGetTokenURL(t *testing.T) {
	t.Parallel()

	t.Run("with configured tenant", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupMicrosoftTestService(t)
		assert.Equal(t, "https://login.microsoftonline.com/test-tenant-id/oauth2/v2.0/token", svc.getTokenURL())
	})

	t.Run("defaults to common tenant", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupMicrosoftTestService(t)
		svc.cfg.Microsoft.TenantID = ""
		assert.Equal(t, "https://login.microsoftonline.com/common/oauth2/v2.0/token", svc.getTokenURL())
	})
}

// TestMicrosoftBuildAuthURL asserts the tenant configured on cfg.Microsoft.TenantID
// (which the systemconfig overlay populates from env/DB/UI) is composed into the
// authorize endpoint, and that an empty tenant falls back to "common". This is the
// authorize-side mirror of TestMicrosoftGetTokenURL and guards the spec's core
// requirement that a settable tenant reaches the Microsoft URL.
func TestMicrosoftBuildAuthURL(t *testing.T) {
	t.Parallel()

	t.Run("with configured tenant", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupMicrosoftTestService(t)
		handler := NewMicrosoftOAuthHandler(svc, svc.cfg)

		authURL := handler.buildMicrosoftAuthURL("state-123")
		assert.Contains(t, authURL, "https://login.microsoftonline.com/test-tenant-id/oauth2/v2.0/authorize?")
	})

	t.Run("defaults to common tenant", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupMicrosoftTestService(t)
		svc.cfg.Microsoft.TenantID = ""
		handler := NewMicrosoftOAuthHandler(svc, svc.cfg)

		authURL := handler.buildMicrosoftAuthURL("state-123")
		assert.Contains(t, authURL, "https://login.microsoftonline.com/common/oauth2/v2.0/authorize?")
	})
}

// testMicrosoftCallbackWithMockServers drives the REAL HandleCallback against
// httptest stand-ins for the Microsoft token and Graph endpoints — including
// the mail-empty → userPrincipalName fallback, which therefore stays covered
// by the production code rather than by a test-local copy of it.
func testMicrosoftCallbackWithMockServers(
	ctx context.Context,
	t *testing.T,
	svc *MicrosoftOAuthService,
	orgSlug, tokenURL, userURL string,
) *MicrosoftOAuthResult {
	t.Helper()

	result, err := testMicrosoftCallbackWithMockServersErr(ctx, t, svc, orgSlug, tokenURL, userURL)
	require.NoError(t, err)

	return result
}

func testMicrosoftCallbackWithMockServersErr(
	ctx context.Context,
	t *testing.T,
	svc *MicrosoftOAuthService,
	orgSlug, tokenURL, userURL string,
) (*MicrosoftOAuthResult, error) {
	t.Helper()

	svc.tokenURL = tokenURL
	svc.userURL = userURL

	return svc.HandleCallback(ctx, "mock-code", orgSlug)
}

const (
	nOAuthGraphID     = "ms-attacker-oid"
	nOAuthAliceEmail  = "alice@acme.com"
	nOAuthCommon      = "common"
	nOAuthPinnedGUID  = "8f1c2a3b-0000-4000-8000-000000000001"
	nOAuthInviteToken = "tok-bob"
)

// msTestIDToken returns an HS256-signed ID token: the connector does not
// verify the signature (back-channel token, see parseIDToken), so any key
// will do.
func msTestIDToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("any-test-key"))
	require.NoError(t, err)

	return signed
}

// msBaseIDClaims returns ID-token claims bound to the test client and to oid.
func msBaseIDClaims(oid string) jwt.MapClaims {
	return jwt.MapClaims{
		"aud": "test-client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
		"oid": oid,
		"tid": "attacker-tenant",
	}
}

// msRunCallback drives the real HandleCallback with a token endpoint returning
// idToken (omitted when empty) and a Graph /me returning userInfo.
func msRunCallback(
	ctx context.Context, t *testing.T, svc *MicrosoftOAuthService,
	orgSlug, idToken string, userInfo MicrosoftUserInfo,
) (*MicrosoftOAuthResult, error) {
	t.Helper()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(MicrosoftTokenResponse{
			AccessToken: "mock-access-token", TokenType: "Bearer", ExpiresIn: 3600, IDToken: idToken,
		})
	}))
	t.Cleanup(tokenServer.Close)

	userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(userInfo)
	}))
	t.Cleanup(userServer.Close)

	return testMicrosoftCallbackWithMockServersErr(ctx, t, svc, orgSlug, tokenServer.URL, userServer.URL)
}

// msSetupAlice creates alice@acme.com with a password, admin of org.
func msSetupAlice(ctx context.Context, t *testing.T, svc *MicrosoftOAuthService, org *models.Organization) *models.User {
	t.Helper()

	hash, err := passwords.Hash("alice-password-1234")
	require.NoError(t, err)

	alice := models.NewUser(nOAuthAliceEmail)
	alice.PasswordHash = &hash
	require.NoError(t, svc.db.CreateUser(ctx, alice))

	member := models.NewOrganizationMember(org.UID, alice.UID, models.MemberRoleAdmin)
	now := time.Now()
	member.JoinedAt = &now
	require.NoError(t, svc.db.CreateOrganizationMember(ctx, member))

	return alice
}

// msAssertNotLinked asserts no link exists for the Microsoft ID and that no
// session was minted for user.
func msAssertNotLinked(ctx context.Context, t *testing.T, svc *MicrosoftOAuthService, oid, userUID string) {
	t.Helper()

	_, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeMicrosoft, oid)
	require.Error(t, err, "no user_providers row may be written for an untrusted sign-in")

	tokens, err := svc.db.ListUserTokens(ctx, userUID)
	require.NoError(t, err)
	assert.Empty(t, tokens, "no session may be minted for the victim")
}

// TestMicrosoftNOAuth covers spec 2026-10-16-01: on a multi-tenant
// registration, a Graph `mail` set by a foreign tenant admin must never link
// or create an account.
func TestMicrosoftNOAuth(t *testing.T) {
	t.Parallel()

	attackerProfile := MicrosoftUserInfo{
		ID: nOAuthGraphID, DisplayName: "Mallory", Mail: nOAuthAliceEmail,
		UserPrincipalName: "mallory@evil.onmicrosoft.com",
	}

	t.Run("unverified email does not link an existing account", func(t *testing.T) {
		t.Parallel()

		withClaim := func(v any) jwt.MapClaims {
			c := msBaseIDClaims(nOAuthGraphID)
			c["email"] = nOAuthAliceEmail

			if v != nil {
				c["xms_edov"] = v
			}

			return c
		}

		tests := []struct {
			name   string
			claims jwt.MapClaims
		}{
			{name: "no id_token"},
			{name: "no xms_edov", claims: withClaim(nil)},
			{name: "xms_edov false", claims: withClaim(false)},
			{name: "xms_edov string true", claims: withClaim("true")},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				svc, ctx := setupMicrosoftTestService(t)
				svc.cfg.Microsoft.TenantID = nOAuthCommon
				org := setupMicrosoftTestOrg(ctx, t, svc)
				alice := msSetupAlice(ctx, t, svc, org)

				var idToken string
				if tt.claims != nil {
					idToken = msTestIDToken(t, tt.claims)
				}

				result, err := msRunCallback(ctx, t, svc, org.Slug, idToken, attackerProfile)
				require.ErrorIs(t, err, ErrEmailNotVerified)
				assert.Nil(t, result)
				msAssertNotLinked(ctx, t, svc, nOAuthGraphID, alice.UID)
			})
		}
	})

	t.Run("unverified email does not create an account or consume an invitation", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		svc.cfg.Microsoft.TenantID = nOAuthCommon
		org := setupMicrosoftTestOrg(ctx, t, svc)

		inviteKey := inviteKeyPrefix + nOAuthInviteToken
		ttl := time.Hour
		require.NoError(t, svc.db.SetStateEntry(ctx, &org.UID, inviteKey, &models.JSONMap{
			keyEmail: "bob@acme.com",
			"role":   string(models.MemberRoleAdmin),
		}, &ttl))

		profile := MicrosoftUserInfo{ID: "ms-bob-oid", DisplayName: "Bob", Mail: "bob@acme.com"}

		_, err := msRunCallback(ctx, t, svc, org.Slug, "", profile)
		require.ErrorIs(t, err, ErrEmailNotVerified)

		bob, _ := svc.db.GetUserByEmail(ctx, "bob@acme.com")
		assert.Nil(t, bob, "no account may be created from an unverified email")

		entry, err := svc.db.GetStateEntry(ctx, &org.UID, inviteKey)
		require.NoError(t, err)
		assert.NotNil(t, entry, "the invitation must not be consumed")

		_, err = svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeMicrosoft, "ms-bob-oid")
		require.Error(t, err)
	})

	t.Run("id_token must be bound to our client and to the Graph user", func(t *testing.T) {
		t.Parallel()

		verified := func(mutate func(jwt.MapClaims)) jwt.MapClaims {
			c := msBaseIDClaims(nOAuthGraphID)
			c["email"] = nOAuthAliceEmail
			c["xms_edov"] = true
			mutate(c)

			return c
		}

		tests := []struct {
			name   string
			tenant string
			claims jwt.MapClaims
		}{
			{name: "foreign audience", tenant: nOAuthCommon,
				claims: verified(func(c jwt.MapClaims) { c["aud"] = "someone-else" })},
			{name: "oid differs from Graph id", tenant: nOAuthCommon,
				claims: verified(func(c jwt.MapClaims) { c["oid"] = "other-oid" })},
			{name: "expired", tenant: nOAuthCommon,
				claims: verified(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() })},
			{name: "pinned tenant, foreign audience", tenant: nOAuthPinnedGUID,
				claims: verified(func(c jwt.MapClaims) { c["aud"] = "someone-else" })},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				svc, ctx := setupMicrosoftTestService(t)
				svc.cfg.Microsoft.TenantID = tt.tenant
				org := setupMicrosoftTestOrg(ctx, t, svc)
				alice := msSetupAlice(ctx, t, svc, org)

				_, err := msRunCallback(ctx, t, svc, org.Slug, msTestIDToken(t, tt.claims), attackerProfile)
				require.ErrorIs(t, err, ErrMicrosoftAPI)
				msAssertNotLinked(ctx, t, svc, nOAuthGraphID, alice.UID)
			})
		}

		t.Run("malformed id_token", func(t *testing.T) {
			t.Parallel()

			svc, ctx := setupMicrosoftTestService(t)
			svc.cfg.Microsoft.TenantID = nOAuthCommon
			org := setupMicrosoftTestOrg(ctx, t, svc)

			_, err := msRunCallback(ctx, t, svc, org.Slug, "not-a-jwt", attackerProfile)
			require.ErrorIs(t, err, ErrMicrosoftAPI)
		})
	})

	t.Run("verified ID-token email links the existing account", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		svc.cfg.Microsoft.TenantID = "Organizations"
		org := setupMicrosoftTestOrg(ctx, t, svc)
		alice := msSetupAlice(ctx, t, svc, org)

		claims := msBaseIDClaims("ms-alice-oid")
		claims["email"] = nOAuthAliceEmail
		claims["xms_edov"] = true

		// Graph `mail` differs: the verified ID-token email is the one used.
		profile := MicrosoftUserInfo{ID: "ms-alice-oid", DisplayName: "Alice", Mail: "alias@acme.com"}

		result, err := msRunCallback(ctx, t, svc, org.Slug, msTestIDToken(t, claims), profile)
		require.NoError(t, err)
		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, alice.UID, result.UserUID)

		linked, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeMicrosoft, "ms-alice-oid")
		require.NoError(t, err)
		assert.Equal(t, alice.UID, linked.UserUID)

		alias, _ := svc.db.GetUserByEmail(ctx, "alias@acme.com")
		assert.Nil(t, alias, "Graph mail must not be used on a multi-tenant registration")
	})

	t.Run("pinned tenant trusts Graph mail without xms_edov", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		svc.cfg.Microsoft.TenantID = nOAuthPinnedGUID
		org := setupMicrosoftTestOrg(ctx, t, svc)
		alice := msSetupAlice(ctx, t, svc, org)

		profile := MicrosoftUserInfo{ID: "ms-alice-oid", DisplayName: "Alice", Mail: nOAuthAliceEmail}
		idToken := msTestIDToken(t, msBaseIDClaims("ms-alice-oid"))

		result, err := msRunCallback(ctx, t, svc, org.Slug, idToken, profile)
		require.NoError(t, err)
		assert.Equal(t, alice.UID, result.UserUID)

		linked, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeMicrosoft, "ms-alice-oid")
		require.NoError(t, err)
		assert.Equal(t, alice.UID, linked.UserUID)
	})

	t.Run("already-linked Microsoft ID signs in with an unverified email", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		svc.cfg.Microsoft.TenantID = nOAuthCommon
		org := setupMicrosoftTestOrg(ctx, t, svc)
		alice := msSetupAlice(ctx, t, svc, org)
		require.NoError(t, svc.db.CreateUserProvider(ctx,
			models.NewUserProvider(alice.UID, models.ProviderTypeMicrosoft, "ms-alice-oid")))

		profile := MicrosoftUserInfo{ID: "ms-alice-oid", DisplayName: "Alice", Mail: nOAuthAliceEmail}

		result, err := msRunCallback(ctx, t, svc, org.Slug, "", profile)
		require.NoError(t, err)
		assert.Equal(t, alice.UID, result.UserUID)
		assert.NotEmpty(t, result.AccessToken)
	})
}

func TestMicrosoftIsPinnedTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tenant string
		want   bool
	}{
		{"", false},
		{"common", false},
		{"COMMON", false},
		{"organizations", false},
		{"Consumers", false},
		{nOAuthPinnedGUID, true},
		{"acme.onmicrosoft.com", true},
	}

	for _, tt := range tests {
		svc := &MicrosoftOAuthService{cfg: &config.Config{
			Microsoft: config.MicrosoftOAuthConfig{TenantID: tt.tenant},
		}}
		assert.Equal(t, tt.want, svc.isPinnedTenant(), "tenant %q", tt.tenant)
	}
}
