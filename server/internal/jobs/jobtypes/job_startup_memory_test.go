package jobtypes

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// runFreshStartup runs the startup job once on a fresh in-memory database.
func runFreshStartup(t *testing.T) *sqlite.Service {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(ctx))

	jobRun, err := (&StartupJobDefinition{}).CreateJobRun(json.RawMessage("{}"))
	require.NoError(t, err)

	require.NoError(t, jobRun.Run(ctx, &jobdef.JobContext{
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))

	return dbSvc
}

// TestStartupSeedsAdminWithBcrypt: the install-bootstrap admin's public
// password is hashed with bcrypt so first boot does no 64 MiB argon2
// derivation, it still verifies, it is flagged for rehash under the default
// argon2id policy, and the forced rotation is unchanged (spec 2026-10-12-04).
//
//nolint:paralleltest // integration test with database
func TestStartupSeedsAdminWithBcrypt(t *testing.T) {
	r := require.New(t)
	dbSvc := runFreshStartup(t)

	admin, err := dbSvc.GetUserByEmail(context.Background(), defaults.Email)
	r.NoError(err)
	r.NotNil(admin)
	r.NotNil(admin.PasswordHash)

	hash := *admin.PasswordHash
	r.True(strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$"), "seed hash must be bcrypt, got %q", hash[:8])
	r.True(passwords.Verify(defaults.Password, hash))
	r.True(passwords.ShouldRehash(hash), "the first login must re-mint it with the active argon2id policy")
	r.True(admin.MustChangePassword)
}

// TestStartupBrowserSamplesDisabled: samples that need a browser are created
// disabled so a native install with Chromium does not launch one every minute
// against a site that cannot pass; ordinary samples stay enabled
// (spec 2026-10-12-04).
//
//nolint:paralleltest // integration test with database
func TestStartupBrowserSamplesDisabled(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	dbSvc := runFreshStartup(t)

	orgs, err := dbSvc.ListOrganizations(ctx)
	r.NoError(err)
	r.Len(orgs, 1)

	checks, _, err := dbSvc.ListChecks(ctx, orgs[0].UID, nil)
	r.NoError(err)

	enabled := map[string]bool{}
	for _, c := range checks {
		enabled[*c.Slug] = c.Enabled
	}

	tests := []struct {
		slug    string
		enabled bool
	}{
		{"browser-example-com", false},
		{"js-browser-login", false},
		{"http-test-api", true},
	}

	for _, tt := range tests {
		got, found := enabled[tt.slug]
		r.True(found, "sample %s must exist", tt.slug)
		r.Equal(tt.enabled, got, "sample %s enabled", tt.slug)
	}
}
