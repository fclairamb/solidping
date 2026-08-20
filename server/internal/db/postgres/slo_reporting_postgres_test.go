package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portSLOReporting is distinct from every other _postgres_test.go embedded
// port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portSLOReporting = 15482

func newSLOPG(t *testing.T) *Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := New(ctx, &Config{Embedded: true, Port: portSLOReporting, RunMode: "test"})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return svc
}

func seedSLOPGOrg(ctx context.Context, t *testing.T, svc *Service, slug string) *models.Organization {
	t.Helper()

	org := &models.Organization{
		UID:       uuid.New().String(),
		Slug:      slug,
		Name:      slug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, svc.CreateOrganization(ctx, org))

	return org
}

func seedSLOPGCheck(ctx context.Context, t *testing.T, svc *Service, orgUID, slug string) string {
	t.Helper()

	check := models.NewCheck(orgUID, slug, "http")
	require.NoError(t, svc.CreateCheck(ctx, check))

	return check.UID
}

// TestSLOSchemaConstraintsPostgres is the Postgres-dialect twin of
// db/sqlite/slo_reporting_test.go's constraint tests.
//
// The XOR is the schema's whole guarantee that an objective has exactly one
// defensible denominator, and the range check is what keeps a nonsense target
// out of the budget arithmetic. Both are stated separately in each dialect's
// migration, so proving them on SQLite alone proves nothing about the dialect
// production actually runs on.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestSLOSchemaConstraintsPostgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)
	svc := newSLOPG(t)

	org := seedSLOPGOrg(ctx, t, svc, "acme-slo")
	checkUID := seedSLOPGCheck(ctx, t, svc, org.UID, "api")

	group := models.NewCheckGroup(org.UID, "edge", "edge")
	r.NoError(svc.CreateCheckGroup(ctx, group))

	// Positive control first: a single-scoped objective inserts cleanly, so a
	// rejection below is the constraint doing its job rather than a broken
	// fixture or a migration that never ran.
	ok := models.NewSLO(org.UID, "api", "api", 99.9)
	ok.CheckUID = &checkUID
	r.NoError(svc.CreateSLO(ctx, ok))

	byGroup := models.NewSLO(org.UID, "edge", "edge", 99.5)
	byGroup.CheckGroupUID = &group.UID
	r.NoError(svc.CreateSLO(ctx, byGroup))

	both := models.NewSLO(org.UID, "both", "both", 99.9)
	both.CheckUID = &checkUID
	both.CheckGroupUID = &group.UID
	r.Error(svc.CreateSLO(ctx, both), "an SLO scoped to both a check and a group must be rejected")

	neither := models.NewSLO(org.UID, "neither", "neither", 99.9)
	r.Error(svc.CreateSLO(ctx, neither), "an unscoped SLO must be rejected")

	// Target range: 100 is a legitimate (brutal) objective; anything above it,
	// or at/below zero, is arithmetic nonsense.
	perfect := models.NewSLO(org.UID, "perfect", "perfect", 100)
	perfect.CheckUID = &checkUID
	r.NoError(svc.CreateSLO(ctx, perfect))

	tooHigh := models.NewSLO(org.UID, "high", "high", 100.5)
	tooHigh.CheckUID = &checkUID
	r.Error(svc.CreateSLO(ctx, tooHigh))

	zero := models.NewSLO(org.UID, "zero", "zero", 0)
	zero.CheckUID = &checkUID
	r.Error(svc.CreateSLO(ctx, zero))

	negative := models.NewSLO(org.UID, "negative", "negative", -1)
	negative.CheckUID = &checkUID
	r.Error(svc.CreateSLO(ctx, negative))

	// The per-org slug uniqueness index is what the API's 409 rests on.
	duplicate := models.NewSLO(org.UID, "api again", "api", 99.9)
	duplicate.CheckUID = &checkUID
	r.Error(svc.CreateSLO(ctx, duplicate), "a slug is unique per organization")

	count, err := svc.CountSLOs(ctx, org.UID)
	r.NoError(err)
	r.Equal(3, count, "only the three valid objectives landed")
}

// TestReportScheduleRoundTripPostgres proves the JSONB columns round-trip on
// the Postgres dialect (they are TEXT on SQLite, so the encoding differs), and
// that the conditional period claim behaves identically there.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestReportScheduleRoundTripPostgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)
	svc := newSLOPG(t)

	org := seedSLOPGOrg(ctx, t, svc, "acme-reports")

	schedule := models.NewReportSchedule(org.UID, "Monthly digest", models.ReportFrequencyMonthly)
	schedule.Recipients = []string{"alice@acme.com", "bob@acme.com"}
	schedule.CheckUIDs = []string{"c1", "c2"}
	r.NoError(svc.CreateReportSchedule(ctx, schedule))

	stored, err := svc.GetReportSchedule(ctx, org.UID, schedule.UID)
	r.NoError(err)
	r.Equal([]string{"alice@acme.com", "bob@acme.com"}, stored.Recipients)
	r.Equal([]string{"c1", "c2"}, stored.CheckUIDs)
	r.False(stored.IsOrgWide())

	july := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	won, err := svc.MarkReportScheduleRun(ctx, schedule.UID, july, july.Add(time.Hour))
	r.NoError(err)
	r.True(won)

	won, err = svc.MarkReportScheduleRun(ctx, schedule.UID, july, july.Add(2*time.Hour))
	r.NoError(err)
	r.False(won, "a second claim of the same closed period must be refused")

	removed, err := svc.RemoveRecipientFromReportSchedules(ctx, org.UID, "ALICE@acme.com")
	r.NoError(err)
	r.Equal(1, removed)

	stored, err = svc.GetReportSchedule(ctx, org.UID, schedule.UID)
	r.NoError(err)
	r.Equal([]string{"bob@acme.com"}, stored.Recipients)

	// An invalid frequency must be refused by the DDL, not merely by the API.
	bad := models.NewReportSchedule(org.UID, "bad", models.ReportFrequencyMonthly)
	bad.Frequency = "hourly"
	r.Error(svc.CreateReportSchedule(ctx, bad))
}
