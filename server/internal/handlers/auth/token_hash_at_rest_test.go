package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// portTokenHashAtRestPG is distinct from every other _postgres_test.go
// embedded port in the repo.
const portTokenHashAtRestPG = 15561

// Session refresh tokens and PATs are stored as sha256 hex, never in clear
// (spec 2026-09-25-23). Every case runs on both engines.
type tokenHashCase struct {
	name string
	run  func(t *testing.T, svc *Service, dbSvc db.Service)
}

func sha256HexOf(value string) string {
	sum := sha256.Sum256([]byte(value))

	return hex.EncodeToString(sum[:])
}

// tokenHashFixture is an admin of a fresh org with a known password, unique
// per call so the Postgres cases can share one database.
func tokenHashFixture(ctx context.Context, t *testing.T, dbSvc db.Service) (*models.User, *models.Organization) {
	t.Helper()
	r := require.New(t)

	fixture := nextFixture()
	org := models.NewOrganization("tokhash-"+fixture, "Token Hash")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("tokhash-" + fixture + "@acme.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	return user, org
}

// requireNoRawToken fails if a stored text field of the user's tokens (the
// hash column or the properties document) carries the raw value.
func requireNoRawToken(ctx context.Context, t *testing.T, dbSvc db.Service, userUID, raw string) {
	t.Helper()

	rows, err := dbSvc.ListUserTokens(ctx, userUID)
	require.NoError(t, err)
	require.NotEmpty(t, rows)

	for _, row := range rows {
		require.NotContains(t, row.TokenHash, raw, "row %s stores the raw token", row.UID)

		properties, marshalErr := json.Marshal(row.Properties)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(properties), raw, "row %s stores the raw token in properties", row.UID)
	}
}

func tokenHashCases() []tokenHashCase {
	return []tokenHashCase{
		{
			name: "login stores only the hash and refresh still works",
			run: func(t *testing.T, svc *Service, dbSvc db.Service) {
				t.Helper()
				r := require.New(t)
				ctx := t.Context()
				user, org := tokenHashFixture(ctx, t, dbSvc)

				login, err := svc.Login(ctx, org.Slug, user.Email, "testpass1234", Context{})
				r.NoError(err)
				r.NotEmpty(login.RefreshToken)

				sessions, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
				r.NoError(err)
				r.Len(sessions, 1)
				r.Equal(sha256HexOf(login.RefreshToken), sessions[0].TokenHash)
				requireNoRawToken(ctx, t, dbSvc, user.UID, login.RefreshToken)

				refreshed, err := svc.Refresh(ctx, login.RefreshToken)
				r.NoError(err)
				r.Equal(login.RefreshToken, refreshed.RefreshToken, "the response shape is unchanged")

				claims, err := svc.ValidateToken(ctx, refreshed.AccessToken)
				r.NoError(err)
				r.Equal(user.UID, claims.UserUID)
				r.Equal(sessions[0].UID, claims.RefreshUID)
			},
		},
		{
			name: "a wrong refresh token and the stored hash get the same generic error",
			run: func(t *testing.T, svc *Service, dbSvc db.Service) {
				t.Helper()
				r := require.New(t)
				ctx := t.Context()
				user, org := tokenHashFixture(ctx, t, dbSvc)

				login, err := svc.Login(ctx, org.Slug, user.Email, "testpass1234", Context{})
				r.NoError(err)

				_, err = svc.Refresh(ctx, "not-a-real-refresh-token")
				r.ErrorIs(err, ErrInvalidToken)

				// Someone holding a database dump has only the hash; it is not a
				// credential.
				_, err = svc.Refresh(ctx, sha256HexOf(login.RefreshToken))
				r.ErrorIs(err, ErrInvalidToken)
			},
		},
		{
			name: "PAT create, validate and revoke with the cache keyed by hash",
			run: func(t *testing.T, svc *Service, dbSvc db.Service) {
				t.Helper()
				r := require.New(t)
				ctx := t.Context()
				user, org := tokenHashFixture(ctx, t, dbSvc)

				pat, err := svc.CreatePAT(ctx, org.Slug, user.UID, CreateTokenRequest{Name: "ci"})
				r.NoError(err)
				r.Contains(pat.Token, PATTokenPrefix, "the raw value is still returned once")

				row, err := dbSvc.GetUserToken(ctx, pat.UID)
				r.NoError(err)
				r.Equal(sha256HexOf(pat.Token), row.TokenHash)
				requireNoRawToken(ctx, t, dbSvc, user.UID, pat.Token)

				claims, err := svc.ValidatePATToken(ctx, pat.Token)
				r.NoError(err)
				r.Equal(user.UID, claims.UserUID)

				svc.cacheMux.RLock()
				_, cachedByHash := svc.patCache[sha256HexOf(pat.Token)]
				_, cachedByValue := svc.patCache[pat.Token]
				svc.cacheMux.RUnlock()
				r.True(cachedByHash, "the cache is keyed by the hash")
				r.False(cachedByValue, "no plaintext PAT sits in a map key")

				_, err = svc.ValidatePATToken(ctx, sha256HexOf(pat.Token))
				r.ErrorIs(err, ErrInvalidToken, "the stored hash is not a credential")

				_, err = svc.ValidatePATToken(ctx, pat.Token+"x")
				r.ErrorIs(err, ErrInvalidToken)

				r.NoError(svc.RevokeToken(ctx, user.UID, pat.UID))

				_, err = svc.ValidatePATToken(ctx, pat.Token)
				r.ErrorIs(err, ErrInvalidToken, "a revoked PAT is refused at once, cache or not")
			},
		},
	}
}

func TestTokenHashAtRest_SQLite(t *testing.T) {
	t.Parallel()

	for _, tc := range tokenHashCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, dbSvc, _ := setupAuthTestService(t)
			tc.run(t, svc, dbSvc)
		})
	}
}

//nolint:paralleltest,tparallel // one embedded PG instance shared by every sub-test
func TestTokenHashAtRest_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portTokenHashAtRestPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	authCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	svc := NewService(dbSvc, authCfg, &config.Config{Auth: authCfg}, nil, nil)

	for _, tc := range tokenHashCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, svc, dbSvc)
		})
	}
}
