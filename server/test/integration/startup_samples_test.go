package integration_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

//nolint:paralleltest // Integration test with database
func TestStartupJobCreatesSampleChecks(t *testing.T) {
	ctx := context.Background()

	// Create in-memory database
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	defer func() { _ = dbSvc.Close() }()

	// Initialize database
	err = dbSvc.Initialize(ctx)
	require.NoError(t, err)

	// Create startup job
	jobDef := &jobtypes.StartupJobDefinition{}
	config := json.RawMessage("{}")

	jobRun, err := jobDef.CreateJobRun(config)
	require.NoError(t, err)

	// Create logger for the job
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Execute the startup job
	jctx := &jobdef.JobContext{
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    logger,
	}

	err = jobRun.Run(ctx, jctx)
	require.NoError(t, err)

	// Get the default organization
	orgs, err := dbSvc.ListOrganizations(ctx)
	require.NoError(t, err)
	require.Len(t, orgs, 1, "Should have created default organization")

	defaultOrg := orgs[0]
	require.Equal(t, "default", defaultOrg.Slug)

	// Verify sample checks were created
	checks, _, err := dbSvc.ListChecks(ctx, defaultOrg.UID, nil)
	require.NoError(t, err)

	// Verify we have sample checks from all checker types that provide samples
	require.GreaterOrEqual(t, len(checks), 10, "Should have at least 10 sample checks")

	// Verify check types distribution (at minimum the original 4 types)
	checksByType := make(map[string]int)
	for _, check := range checks {
		checksByType[check.Type]++
	}

	require.GreaterOrEqual(t, checksByType["http"], 1, "Should have at least 1 HTTP sample check")
	require.GreaterOrEqual(t, checksByType["dns"], 3, "Should have at least 3 DNS sample checks")
	require.GreaterOrEqual(t, checksByType["icmp"], 3, "Should have at least 3 ICMP sample checks")
	require.GreaterOrEqual(t, checksByType["tcp"], 3, "Should have at least 3 TCP sample checks")

	// Verify all checks are enabled and have periods
	for _, check := range checks {
		require.True(t, check.Enabled, "Sample check %s should be enabled", check.Slug)
		require.NotNil(t, check.Name, "Sample check %s should have a name", check.Slug)
		require.NotEmpty(t, check.Period, "Sample check %s should have a period", check.Slug)
	}

	// Verify specific periods for HTTP check
	checksMap := make(map[string]*models.Check)
	for i := range checks {
		checksMap[*checks[i].Slug] = checks[i]
	}

	// HTTP test-api check has a 20 second period
	if httpTestAPI, ok := checksMap["http-test-api"]; ok {
		require.Equal(t, timeutils.Duration(20*time.Second), httpTestAPI.Period, "http-test-api should have 20 second period")
	}

	// DNS, ICMP, and TCP checks should have 5 minute period
	for _, check := range checks {
		if check.Type == "dns" || check.Type == "icmp" || check.Type == "tcp" {
			require.Equal(
				t, timeutils.Duration(5*time.Minute), check.Period,
				"Check %s (type %s) should have 5 minute period", check.Slug, check.Type,
			)
		}
	}

	// Verify some specific samples exist
	slugs := make(map[string]bool)
	for _, check := range checks {
		slugs[*check.Slug] = true
	}

	// Check for expected sample slugs
	require.True(t, slugs["http-test-api"], "Should have HTTP test-api sample")
	require.True(t, slugs["dns-google"], "Should have DNS Google sample")
	require.True(t, slugs["icmp-google-dns"], "Should have ICMP Google DNS sample")
	require.True(t, slugs["tcp-google"], "Should have TCP Google HTTPS sample")

	t.Logf("Successfully created %d sample checks", len(checks))
	for _, check := range checks {
		t.Logf("  - %s: %s (type: %s)", *check.Slug, *check.Name, check.Type)
	}

	// Verify that check_jobs were created for all checks
	var checkJobCount int
	checkJobCount, err = dbSvc.DB().NewSelect().
		Model((*models.CheckJob)(nil)).
		Where("organization_uid = ?", defaultOrg.UID).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, len(checks), checkJobCount, "Should have created check_jobs for all checks")

	t.Logf("Successfully verified %d check_jobs were created", checkJobCount)
}

//nolint:paralleltest // Integration test with database
func TestStartupJobIdempotency(t *testing.T) {
	ctx := context.Background()

	// Create in-memory database
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	defer func() { _ = dbSvc.Close() }()

	// Initialize database
	err = dbSvc.Initialize(ctx)
	require.NoError(t, err)

	// Create startup job
	jobDef := &jobtypes.StartupJobDefinition{}
	config := json.RawMessage("{}")

	jobRun, err := jobDef.CreateJobRun(config)
	require.NoError(t, err)

	// Create logger for the job
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Execute the startup job
	jctx := &jobdef.JobContext{
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    logger,
	}

	// Run startup job first time
	err = jobRun.Run(ctx, jctx)
	require.NoError(t, err)

	// Get the default organization
	orgs, err := dbSvc.ListOrganizations(ctx)
	require.NoError(t, err)
	require.Len(t, orgs, 1)
	defaultOrg := orgs[0]

	// Count checks after first run
	checksFirstRun, _, err := dbSvc.ListChecks(ctx, defaultOrg.UID, nil)
	require.NoError(t, err)
	firstRunCount := len(checksFirstRun)

	// Run startup job second time (should be idempotent)
	jobRun2, err := jobDef.CreateJobRun(config)
	require.NoError(t, err)

	err = jobRun2.Run(ctx, jctx)
	require.NoError(t, err)

	// Count checks after second run - should be the same
	checksSecondRun, _, err := dbSvc.ListChecks(ctx, defaultOrg.UID, nil)
	require.NoError(t, err)

	require.Len(t, checksSecondRun, firstRunCount,
		"Startup job should be idempotent - no duplicate checks should be created")

	t.Logf("Idempotency verified: %d checks after first run, %d after second run",
		firstRunCount, len(checksSecondRun))
}

func TestStartupJobCreatesSampleChecksTestMode(t *testing.T) {
	ctx := context.Background()

	// Set test mode
	t.Setenv("SP_RUN_MODE", "test")

	// Create in-memory database
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	defer func() { _ = dbSvc.Close() }()

	err = dbSvc.Initialize(ctx)
	require.NoError(t, err)

	// Create and run startup job
	jobDef := &jobtypes.StartupJobDefinition{}
	jobRun, err := jobDef.CreateJobRun(json.RawMessage("{}"))
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	jctx := &jobdef.JobContext{
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    logger,
	}

	err = jobRun.Run(ctx, jctx)
	require.NoError(t, err)

	// Get the default organization
	orgs, err := dbSvc.ListOrganizations(ctx)
	require.NoError(t, err)
	require.Len(t, orgs, 1)

	defaultOrg := orgs[0]

	// Verify checks were created
	checks, _, err := dbSvc.ListChecks(ctx, defaultOrg.UID, nil)
	require.NoError(t, err)

	// In test mode: at least 14 sample checks (5 HTTP + 3 DNS + 3 ICMP + 3 TCP + new types)
	require.GreaterOrEqual(t, len(checks), 14, "Test mode should create at least 14 sample checks")

	checksByType := make(map[string]int)
	for _, check := range checks {
		checksByType[check.Type]++
	}

	require.GreaterOrEqual(t, checksByType["http"], 5, "Should have at least 5 HTTP test checks")
	require.GreaterOrEqual(t, checksByType["dns"], 3, "Should have at least 3 DNS sample checks")
	require.GreaterOrEqual(t, checksByType["icmp"], 3, "Should have at least 3 ICMP sample checks")
	require.GreaterOrEqual(t, checksByType["tcp"], 3, "Should have at least 3 TCP sample checks")

	// Build slug map
	slugs := make(map[string]*models.Check)
	for i := range checks {
		slugs[*checks[i].Slug] = checks[i]
	}

	// Verify all expected HTTP test slugs exist
	expectedHTTPSlugs := []string{
		"http-fake-stable",
		"http-fake-flaky",
		"http-fake-unstable",
		"http-fake-slow",
		"http-fake-503",
	}
	for _, slug := range expectedHTTPSlugs {
		require.Contains(t, slugs, slug, "Should have %s check", slug)
	}

	// Verify periods
	require.Equal(t, timeutils.Duration(10*time.Second), slugs["http-fake-stable"].Period)
	require.Equal(t, timeutils.Duration(15*time.Second), slugs["http-fake-flaky"].Period)
	require.Equal(t, timeutils.Duration(15*time.Second), slugs["http-fake-unstable"].Period)
	require.Equal(t, timeutils.Duration(20*time.Second), slugs["http-fake-slow"].Period)
	require.Equal(t, timeutils.Duration(15*time.Second), slugs["http-fake-503"].Period)

	// Verify all checks are enabled
	for _, check := range checks {
		require.True(t, check.Enabled, "Check %s should be enabled", *check.Slug)
	}

	// Verify URLs use the default base URL
	for _, slug := range expectedHTTPSlugs {
		check := slugs[slug]
		url, ok := check.Config["url"].(string)
		require.True(t, ok, "Check %s should have a URL config", slug)
		require.Contains(t, url, "http://localhost:4000/api/v1/fake", "Check %s URL should use default base URL", slug)
	}

	t.Logf("Test mode: created %d checks", len(checks))
	for _, check := range checks {
		t.Logf("  - %s: %s (type: %s, period: %v)", *check.Slug, *check.Name, check.Type, check.Period)
	}
}
