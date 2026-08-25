package integration_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/test/testdata"
)

// TestStartupSeedsAdminWithForcedRotation pins the exposure this spec closes
// (2026-08-23-04).
//
// The credentials themselves are DELIBERATELY unchanged and deliberately still
// published — the assertions below say so explicitly, so a well-meaning future
// change that randomizes or hides them trips this test and has to argue with
// the decision rather than quietly reverse it. What must hold is that they buy
// exactly one login: the seeded superadmin carries must_change_password.
//
//nolint:paralleltest // Integration test with database
func TestStartupSeedsAdminWithForcedRotation(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)

	defer func() { _ = dbSvc.Close() }()

	r.NoError(dbSvc.Initialize(ctx))

	jobDef := &jobtypes.StartupJobDefinition{}

	jobRun, err := jobDef.CreateJobRun(json.RawMessage("{}"))
	r.NoError(err)

	r.NoError(jobRun.Run(ctx, &jobdef.JobContext{
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}))

	admin, err := dbSvc.GetUserByEmail(ctx, defaults.Email)
	r.NoError(err)
	r.NotNil(admin)

	// The seed is unchanged: same address, same well-known password, still
	// superadmin. No generated secret, no mode-dependent branch.
	r.True(admin.SuperAdmin)
	r.NotNil(admin.PasswordHash)
	r.True(passwords.Verify(defaults.Password, *admin.PasswordHash),
		"the documented default password must still work — forced rotation, not secrecy, is the fix")

	// ...and that is exactly why it must be flagged.
	r.True(admin.MustChangePassword,
		"the seeded superadmin's credentials are public; the first login must be forced to rotate")
}

// TestTestModeUserIsNotFlagged is the guard rail for the E2E suites.
//
// `test@test.com` / `test` is seeded by a different path and is signed in with
// those fixed credentials by every dash0 and status0 Playwright spec. Flagging
// it "for consistency" would confine those sessions to the rotation screen and
// fail the entire browser suite — a failure that shows up far from its cause,
// which is why it is pinned here rather than left to be discovered in CI.
//
//nolint:paralleltest // Integration test with database
func TestTestModeUserIsNotFlagged(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)

	defer func() { _ = dbSvc.Close() }()

	r.NoError(dbSvc.Initialize(ctx))
	// The test-mode seed writes a screenshot attachment, so it needs a real
	// file-storage backend; a scratch directory is enough.
	localfs.Register()

	cfg := &config.Config{FileStorage: config.FileStorageConfig{
		Type:      "local",
		LocalRoot: t.TempDir(),
	}}
	r.NoError(testdata.CreateTestData(ctx, dbSvc, cfg))

	testUser, err := dbSvc.GetUserByEmail(ctx, "test@test.com")
	r.NoError(err)
	r.NotNil(testUser)
	r.False(testUser.MustChangePassword,
		"the test-mode user must never be flagged: the Playwright suites sign in with fixed credentials")
}
