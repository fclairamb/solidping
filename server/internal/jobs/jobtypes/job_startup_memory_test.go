package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"runtime/metrics"
	"strings"
	"testing"
	"time"

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
		Logger:    slog.New(slog.DiscardHandler),
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
	r.True(strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$"),
		"seed hash must be bcrypt, got %q", hash[:8])
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

const (
	freshBootChildEnv = "SP_TEST_FRESH_BOOT_HEAP_CHILD"
	mib               = 1 << 20
	// freshBootSettle covers the passwords package's 1 s release delay plus margin.
	freshBootSettle = 2 * time.Second
)

// TestFreshBootHeapStaysSmall: running the startup job end to end on a fresh
// database leaves neither an inflated GC goal nor a large retained heap behind,
// without a forced GC. Before spec 2026-10-12-04 the seed admin's argon2id hash
// left a ~150 MB goal. Runs in a child process so other tests' heaps do not
// interfere.
//
//nolint:paralleltest // re-execs the test binary; the child measures process-wide memory
func TestFreshBootHeapStaysSmall(t *testing.T) {
	if os.Getenv(freshBootChildEnv) == "1" {
		freshBootChild(t)
		return
	}

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestFreshBootHeapStaysSmall$", "-test.v")
	cmd.Env = append(os.Environ(), freshBootChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	t.Logf("child output:\n%s", out)
	require.NoError(t, err)
}

func freshBootChild(t *testing.T) {
	t.Helper()

	runFreshStartup(t)
	time.Sleep(freshBootSettle)

	samples := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/gc/heap/goal:bytes"},
	}
	metrics.Read(samples)

	retained := samples[0].Value.Uint64() - samples[1].Value.Uint64()
	goal := samples[2].Value.Uint64()

	t.Logf("fresh boot: retained=%d MiB goal=%d MiB", retained/mib, goal/mib)

	require.LessOrEqual(t, goal, uint64(64*mib), "GC goal after a fresh boot")
	require.LessOrEqual(t, retained, uint64(64*mib), "retained heap after a fresh boot")
}
