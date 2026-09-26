package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// portRegisterAntiEnumPG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// internal/db/incident_number_test.go).
const portRegisterAntiEnumPG = 15600

// newRegisterAntiEnumPostgresService boots one embedded Postgres and wires a
// real jobs service on top of it — the Postgres twin of setupAuthTestServiceWithJobs,
// used because the registration confirmation token is now only recoverable
// from the queued confirmation email (spec 2026-09-25-30), not from the
// pending state entry.
func newRegisterAntiEnumPostgresService(t *testing.T) (*Service, db.Service) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbService, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portRegisterAntiEnumPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbService.Close() })

	if initErr := dbService.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	fullCfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "https://example.com"},
		Auth: config.AuthConfig{
			JWTSecret:                "test-jwt-secret",
			AccessTokenExpiry:        time.Hour,
			RefreshTokenExpiry:       7 * 24 * time.Hour,
			RegistrationEmailPattern: ".*",
		},
	}

	jobs := jobsvc.NewService(dbService.DB(), dbService, notifier.NewLocalEventNotifier(), nil)
	svc := NewService(dbService, fullCfg.Auth, fullCfg, jobs, nil)

	return svc, dbService
}

// TestRegisterAntiEnumeration_Postgres is the real-engine twin of the SQLite
// contracts in register_anti_enum_test.go: registration entries are hashed
// at rest, confirmation resolves the real token by hash, and a taken email
// is indistinguishable from a new signup. Run on the same embedded instance
// sequentially (unique emails per sub-test) to keep the (slow) engine
// start-up to one boot for the whole spec.
//
//nolint:paralleltest,tparallel // one embedded PG instance shared by every sub-test
func TestRegisterAntiEnumeration_Postgres(t *testing.T) {
	t.Parallel()

	svc, dbSvc := newRegisterAntiEnumPostgresService(t)

	t.Run("stores a hashed token, confirms once, then generic error on reuse", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()

		const email = "pg-hash-check@example.com"

		_, err := svc.Register(ctx, RegisterRequest{Name: "Hash Check", Email: email, Password: "supersecret123"})
		r.NoError(err)

		entries, err := dbSvc.ListStateEntries(ctx, nil, registrationKeyPrefix)
		r.NoError(err)

		var entry *models.StateEntry

		for _, e := range entries {
			if e.Value == nil {
				continue
			}

			if got, _ := (*e.Value)[keyEmail].(string); got == email {
				entry = e

				break
			}
		}

		r.NotNil(entry, "precondition: a pending entry must exist for this email")

		val := *entry.Value
		r.NotContains(val, "token", "the plaintext token key must never be stored")
		r.NotContains(val, keyToken, "the plaintext token key must never be stored")

		storedHash, ok := val[keyTokenHash].(string)
		r.True(ok, "tokenHash must decode as a string, got %T", val[keyTokenHash])
		r.Len(storedHash, 64, "sha256 hex digest is 64 chars")

		token := extractRegistrationConfirmToken(t, ctx, dbSvc, email)
		r.NotEmpty(token, "precondition: a confirmation email must have been queued")
		r.Equal(hashPendingToken(token), storedHash, "stored hash must be sha256(token)")

		_, err = svc.ConfirmRegistration(ctx, token)
		r.NoError(err)

		user, err := dbSvc.GetUserByEmail(ctx, email)
		r.NoError(err)
		r.NotNil(user)

		_, err = svc.ConfirmRegistration(ctx, token)
		r.ErrorIs(err, ErrRegistrationExpired, "a consumed token must not confirm twice")
	})

	t.Run("taken email behaves exactly like a new signup", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()

		const existingEmail = "pg-already-registered@example.com"

		hash, err := passwords.Hash("originalpass1")
		r.NoError(err)
		existingUser := models.NewUser(existingEmail)
		existingUser.PasswordHash = &hash
		r.NoError(dbSvc.CreateUser(ctx, existingUser))

		resp, err := svc.Register(ctx, RegisterRequest{
			Name: "Impersonator", Email: existingEmail, Password: "supersecret123",
		})
		r.NoError(err)
		r.Equal(registerSuccessResponse, resp, "a taken email must get the exact success response, not an error")

		entries, err := dbSvc.ListStateEntries(ctx, nil, registrationKeyPrefix)
		r.NoError(err)

		for _, e := range entries {
			if e.Value == nil {
				continue
			}

			got, _ := (*e.Value)[keyEmail].(string)
			r.NotEqual(existingEmail, got, "no pending entry must be created for a taken email")
		}

		r.Empty(extractRegistrationConfirmToken(t, ctx, dbSvc, existingEmail),
			"no confirmation email must be sent for a taken email")

		unchanged, err := dbSvc.GetUserByEmail(ctx, existingEmail)
		r.NoError(err)
		r.True(passwords.Verify("originalpass1", *unchanged.PasswordHash), "the original account must be untouched")
	})

	t.Run("wrong token and expired entry get the same generic error as unknown", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()

		_, errUnknown := svc.ConfirmRegistration(ctx, "pg-a-token-that-was-never-issued")
		r.ErrorIs(errUnknown, ErrRegistrationExpired)

		const knownToken = "pg-expired-registration-token"
		pwHash, err := passwords.Hash("supersecret123")
		r.NoError(err)

		stateValue := &models.JSONMap{
			keyTokenHash:   hashPendingToken(knownToken),
			keyEmail:       "pg-expired-reg@example.com",
			keyName:        "Expired Reg",
			"passwordHash": pwHash,
		}
		alreadyExpired := -time.Minute
		r.NoError(dbSvc.SetStateEntry(ctx, nil,
			registrationKeyPrefix+"pg-expired-reg@example.com", stateValue, &alreadyExpired))

		_, errExpired := svc.ConfirmRegistration(ctx, knownToken)
		r.ErrorIs(errExpired, ErrRegistrationExpired)
		r.Equal(errUnknown, errExpired, "an expired entry and an unknown token must be indistinguishable")
	})
}
