package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/test/testdata"
)

// seedFreshInstall runs the startup job once on a fresh in-memory database with
// cfg, and returns the database with every log line the seed wrote (JSON).
func seedFreshInstall(t *testing.T, cfg *config.Config) (*sqlite.Service, []map[string]any) {
	t.Helper()

	ctx := context.Background()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	jobRun, err := (&jobtypes.StartupJobDefinition{}).CreateJobRun(json.RawMessage("{}"))
	r.NoError(err)

	var out bytes.Buffer

	r.NoError(jobRun.Run(ctx, &jobdef.JobContext{
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		AppConfig: cfg,
		Logger:    slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}))

	var lines []map[string]any

	for _, raw := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}

		var line map[string]any
		r.NoError(json.Unmarshal(raw, &line))
		lines = append(lines, line)
	}

	return dbSvc, lines
}

// passwordsLogged returns the "password" attribute of every log line that has one.
func passwordsLogged(lines []map[string]any) []string {
	var found []string

	for _, line := range lines {
		if pw, ok := line["password"].(string); ok {
			found = append(found, pw)
		}
	}

	return found
}

func authConfig(cfg *config.Config) *config.Config {
	cfg.Auth.JWTSecret = "test-jwt-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour

	return cfg
}

// TestStartupSeedsAdminWithRandomPassword: in the default (production) run
// mode the seeded superadmin gets a generated password, never solidpass,
// different on every install, printed once in a WARN line, and good for one
// login that can only rotate it (specs 2026-08-23-04 and 2026-11-19-01).
//
//nolint:paralleltest // Integration test with database
func TestStartupSeedsAdminWithRandomPassword(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	seeded := make([]string, 0, 2)

	for range 2 {
		cfg := authConfig(&config.Config{})
		dbSvc, lines := seedFreshInstall(t, cfg)

		logged := passwordsLogged(lines)
		r.Len(logged, 1, "the generated password is printed exactly once")
		password := logged[0]
		r.NotEqual(defaults.Password, password)
		r.Len(password, passwords.GeneratedLength)

		var warned bool

		for _, line := range lines {
			if line["password"] == password {
				warned = line["level"] == "WARN" && line["email"] == defaults.Email
			}
		}

		r.True(warned, "the password line is a WARN carrying the email")

		admin, err := dbSvc.GetUserByEmail(ctx, defaults.Email)
		r.NoError(err)
		r.True(admin.SuperAdmin)
		r.True(admin.MustChangePassword, "the first login must still be forced to rotate")
		r.NotNil(admin.PasswordHash)
		r.NotContains(*admin.PasswordHash, password, "never stored in clear")
		r.False(passwords.Verify(defaults.Password, *admin.PasswordHash), "solidpass must not open a production install")

		svc := auth.NewService(dbSvc, cfg.Auth, cfg, nil, nil)
		login, err := svc.Login(ctx, defaults.Organization, defaults.Email, password, auth.Context{})
		r.NoError(err)
		r.True(login.User.MustChangePassword, "the session can only rotate the password")

		_, err = svc.Login(ctx, defaults.Organization, defaults.Email, defaults.Password, auth.Context{})
		r.Error(err)

		seeded = append(seeded, password)
	}

	r.NotEqual(seeded[0], seeded[1], "two installs never share a password")
}

// TestStartupSeedsConfiguredAdminPassword: SP_ADMIN_PASSWORD wins over the
// generated password and over the test-mode default, is never logged, and
// still forces the rotation.
//
//nolint:paralleltest // Integration test with database
func TestStartupSeedsConfiguredAdminPassword(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	for _, runMode := range []string{"", "test"} {
		cfg := authConfig(&config.Config{RunMode: runMode})
		cfg.Auth.AdminPassword = "acme-first-password"
		dbSvc, lines := seedFreshInstall(t, cfg)

		r.Empty(passwordsLogged(lines), "a configured password is never logged")

		admin, err := dbSvc.GetUserByEmail(ctx, defaults.Email)
		r.NoError(err)
		r.True(admin.MustChangePassword)
		r.True(passwords.Verify("acme-first-password", *admin.PasswordHash))
		r.False(passwords.Verify(defaults.Password, *admin.PasswordHash))
	}
}

// TestStartupTestModeKeepsDefaultPassword: the test and demo run modes still
// seed the public defaults.Password, which the dash0 E2E suite logs in with,
// and print nothing at seed time.
//
//nolint:paralleltest // Integration test with database
func TestStartupTestModeKeepsDefaultPassword(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	for _, runMode := range []string{"test", "demo"} {
		cfg := authConfig(&config.Config{RunMode: runMode})
		dbSvc, lines := seedFreshInstall(t, cfg)

		r.Empty(passwordsLogged(lines))

		admin, err := dbSvc.GetUserByEmail(ctx, defaults.Email)
		r.NoError(err)
		r.True(admin.SuperAdmin)
		r.True(passwords.Verify(defaults.Password, *admin.PasswordHash), runMode)
		r.True(admin.MustChangePassword, "the public pair still buys exactly one login")
	}
}

// TestStartupAdminPasswordRecovery: a boot with SP_ADMIN_PASSWORD set on an
// existing database re-hashes the seeded admin's password while it still has
// to be rotated, and never once it was rotated or for another account.
//
//nolint:paralleltest // Integration test with database
func TestStartupAdminPasswordRecovery(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	dbSvc, _ := seedFreshInstall(t, authConfig(&config.Config{}))

	rerun := func(cfg *config.Config) {
		jobRun, err := (&jobtypes.StartupJobDefinition{}).CreateJobRun(json.RawMessage("{}"))
		r.NoError(err)
		r.NoError(jobRun.Run(ctx, &jobdef.JobContext{
			DB: dbSvc.DB(), DBService: dbSvc, AppConfig: cfg, Logger: slog.New(slog.DiscardHandler),
		}))
	}

	// Lost the first-boot line: restart with SP_ADMIN_PASSWORD.
	cfg := authConfig(&config.Config{})
	cfg.Auth.AdminPassword = "acme-recovery-password"
	rerun(cfg)

	admin, err := dbSvc.GetUserByEmail(ctx, defaults.Email)
	r.NoError(err)
	r.True(passwords.Verify("acme-recovery-password", *admin.PasswordHash))
	r.True(admin.MustChangePassword)

	login, err := auth.NewService(dbSvc, cfg.Auth, cfg, nil, nil).
		Login(ctx, defaults.Organization, defaults.Email, "acme-recovery-password", auth.Context{})
	r.NoError(err)
	r.True(login.User.MustChangePassword)

	// Once rotated, SP_ADMIN_PASSWORD is never applied again.
	rotatedHash, err := passwords.Hash("acme-rotated-password")
	r.NoError(err)

	cleared := false
	r.NoError(dbSvc.UpdateUser(ctx, admin.UID, &models.UserUpdate{
		PasswordHash: &rotatedHash, MustChangePassword: &cleared,
	}))

	cfg.Auth.AdminPassword = "acme-another-password"
	rerun(cfg)

	admin, err = dbSvc.GetUserByEmail(ctx, defaults.Email)
	r.NoError(err)
	r.True(passwords.Verify("acme-rotated-password", *admin.PasswordHash))
	r.False(passwords.Verify("acme-another-password", *admin.PasswordHash))

	// Another flagged account (not the seeded email) is never touched.
	otherHash, err := passwords.HashBcrypt("acme-other-password")
	r.NoError(err)

	other := models.NewUser("other@acme.com")
	other.PasswordHash = &otherHash
	other.MustChangePassword = true
	r.NoError(dbSvc.CreateUser(ctx, other))

	rerun(cfg)

	other, err = dbSvc.GetUserByEmail(ctx, "other@acme.com")
	r.NoError(err)
	r.True(passwords.Verify("acme-other-password", *other.PasswordHash))
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
