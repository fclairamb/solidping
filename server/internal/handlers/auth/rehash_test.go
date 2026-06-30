package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// argon2idDefaultPolicy reproduces the shipped argon2id default. Used to restore
// the process-wide policy after a test mutates it.
func argon2idDefaultPolicy() passwords.Policy {
	return passwords.Policy{
		Algorithm: config.PasswordAlgorithmArgon2id,
		Argon2: passwords.Argon2Params{
			Memory: 64 * 1024, Time: 3, Threads: 4, KeyLength: 32, SaltLength: 16,
		},
		Bcrypt: passwords.BcryptParams{Cost: 12},
	}
}

func bcryptTestPolicy(cost int) passwords.Policy {
	p := argon2idDefaultPolicy()
	p.Algorithm = config.PasswordAlgorithmBcrypt
	p.Bcrypt = passwords.BcryptParams{Cost: cost}
	return p
}

// usePolicy installs p as the process-wide password policy and restores the
// argon2id default on cleanup. Tests using it must NOT call t.Parallel(), since
// the policy is global.
func usePolicy(t *testing.T, p passwords.Policy) {
	t.Helper()
	passwords.SetDefaultPolicy(p)
	t.Cleanup(func() { passwords.SetDefaultPolicy(argon2idDefaultPolicy()) })
}

// hashUnderPolicy mints a hash of password under policy p without leaving p
// installed (restores the argon2id default afterwards).
func hashUnderPolicy(t *testing.T, p passwords.Policy, password string) string {
	t.Helper()
	passwords.SetDefaultPolicy(p)
	h, err := passwords.Hash(password)
	require.NoError(t, err)
	passwords.SetDefaultPolicy(argon2idDefaultPolicy())
	return h
}

// seedLoginUser creates an org + admin member with the supplied stored hash and
// returns the user.
func seedLoginUser(
	ctx context.Context, t *testing.T, dbSvc db.Service, orgSlug, email, storedHash string,
) *models.User {
	t.Helper()
	r := require.New(t)

	org := models.NewOrganization(orgSlug, orgSlug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser(email)
	user.PasswordHash = &storedHash
	r.NoError(dbSvc.CreateUser(ctx, user))

	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	return user
}

// TestRehashOnLogin exercises the transparent rehash-on-login upgrade. These
// subtests mutate the process-wide password policy, so the parent is not
// parallel.
//
//nolint:paralleltest // mutates the process-wide default policy via usePolicy
func TestRehashOnLogin(t *testing.T) {
	const password = "rehash-secret-pw"

	t.Run("stale algorithm is upgraded and persisted", func(t *testing.T) {
		r := require.New(t)
		svc, dbSvc, ctx := setupAuthTestService(t)

		// Stored hash is bcrypt; active policy is argon2id (default).
		usePolicy(t, argon2idDefaultPolicy())
		stored := hashUnderPolicy(t, bcryptTestPolicy(10), password)
		user := seedLoginUser(ctx, t, dbSvc, "rehash-up", "up@example.com", stored)

		resp, err := svc.Login(ctx, "rehash-up", "up@example.com", password, Context{})
		r.NoError(err)
		r.NotEmpty(resp.AccessToken)

		// Stored hash must have flipped to argon2id and now match the policy.
		updated, err := dbSvc.GetUser(ctx, user.UID)
		r.NoError(err)
		r.NotNil(updated.PasswordHash)
		r.NotEqual(stored, *updated.PasswordHash)
		r.True(passwords.Verify(password, *updated.PasswordHash))
		r.False(passwords.NeedsRehash(*updated.PasswordHash))
	})

	t.Run("matching hash is not rehashed", func(t *testing.T) {
		r := require.New(t)
		svc, dbSvc, ctx := setupAuthTestService(t)

		// Stored hash already matches the active policy.
		usePolicy(t, argon2idDefaultPolicy())
		stored := hashUnderPolicy(t, argon2idDefaultPolicy(), password)
		user := seedLoginUser(ctx, t, dbSvc, "rehash-noop", "noop@example.com", stored)

		resp, err := svc.Login(ctx, "rehash-noop", "noop@example.com", password, Context{})
		r.NoError(err)
		r.NotEmpty(resp.AccessToken)

		updated, err := dbSvc.GetUser(ctx, user.UID)
		r.NoError(err)
		r.NotNil(updated.PasswordHash)
		r.Equal(stored, *updated.PasswordHash, "matching hash must be left untouched")
	})

	t.Run("plaintext escape hatch is not rehashed", func(t *testing.T) {
		r := require.New(t)
		svc, dbSvc, ctx := setupAuthTestService(t)

		usePolicy(t, argon2idDefaultPolicy())
		stored := "$plaintext$" + password
		user := seedLoginUser(ctx, t, dbSvc, "rehash-plain", "plain@example.com", stored)

		resp, err := svc.Login(ctx, "rehash-plain", "plain@example.com", password, Context{})
		r.NoError(err)
		r.NotEmpty(resp.AccessToken)

		updated, err := dbSvc.GetUser(ctx, user.UID)
		r.NoError(err)
		r.NotNil(updated.PasswordHash)
		r.Equal(stored, *updated.PasswordHash, "plaintext hash must be left untouched")
	})

	t.Run("persist error still yields a successful login", func(t *testing.T) {
		r := require.New(t)
		svc, dbSvc, ctx := setupAuthTestService(t)

		usePolicy(t, argon2idDefaultPolicy())
		stored := hashUnderPolicy(t, bcryptTestPolicy(10), password)
		user := seedLoginUser(ctx, t, dbSvc, "rehash-err", "err@example.com", stored)

		// Swap in a db whose UpdateUser always fails; everything else delegates.
		svc.db = failingUpdateDB{Service: dbSvc}

		resp, err := svc.Login(ctx, "rehash-err", "err@example.com", password, Context{})
		r.NoError(err, "rehash persist failure must not fail the login")
		r.NotEmpty(resp.AccessToken)

		// The stored hash is unchanged because the write was rejected, but the
		// user remains authenticated.
		updated, err := dbSvc.GetUser(ctx, user.UID)
		r.NoError(err)
		r.NotNil(updated.PasswordHash)
		r.Equal(stored, *updated.PasswordHash)
	})
}

// errUpdateUserForced is returned by failingUpdateDB.UpdateUser.
var errUpdateUserForced = errors.New("forced UpdateUser failure")

// failingUpdateDB embeds a real db.Service but forces UpdateUser to fail, so the
// best-effort rehash persistence path can be exercised. All other methods
// delegate to the embedded service.
type failingUpdateDB struct {
	db.Service
}

func (failingUpdateDB) UpdateUser(_ context.Context, _ string, _ *models.UserUpdate) error {
	return errUpdateUserForced
}
