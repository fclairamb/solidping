package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func newSLODB(t *testing.T) *Service {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

func seedSLOOrg(ctx context.Context, t *testing.T, svc *Service) *models.Organization {
	t.Helper()

	org := &models.Organization{
		UID:       uuid.New().String(),
		Slug:      "acme",
		Name:      "acme",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, svc.CreateOrganization(ctx, org))

	return org
}

func seedSLOCheck(ctx context.Context, t *testing.T, svc *Service, orgUID, slug string) string {
	t.Helper()

	check := models.NewCheck(orgUID, slug, "http")
	require.NoError(t, svc.CreateCheck(ctx, check))

	return check.UID
}

func seedSLOGroup(ctx context.Context, t *testing.T, svc *Service, orgUID, slug string) string {
	t.Helper()

	group := models.NewCheckGroup(orgUID, slug, slug)
	require.NoError(t, svc.CreateCheckGroup(ctx, group))

	return group.UID
}

// The schema's XOR constraint is the whole guarantee that an SLO has exactly
// one defensible denominator. It must be enforced by the DATABASE, not merely
// by whichever handler happens to write the row.
func TestSLOScopeXorIsEnforcedBySchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc := newSLODB(t)
	org := seedSLOOrg(ctx, t, svc)

	check := seedSLOCheck(ctx, t, svc, org.UID, "api")
	group := seedSLOGroup(ctx, t, svc, org.UID, "edge")

	// Positive control: a single-scoped SLO inserts fine, so a failure below
	// really is the constraint and not a broken fixture.
	ok := models.NewSLO(org.UID, "api", "api", 99.9)
	ok.CheckUID = &check
	r.NoError(svc.CreateSLO(ctx, ok))

	both := models.NewSLO(org.UID, "both", "both", 99.9)
	both.CheckUID = &check
	both.CheckGroupUID = &group
	r.Error(svc.CreateSLO(ctx, both), "an SLO scoped to both a check and a group must be rejected")

	neither := models.NewSLO(org.UID, "neither", "neither", 99.9)
	r.Error(svc.CreateSLO(ctx, neither), "an unscoped SLO must be rejected")
}

func TestSLOTargetRangeIsEnforcedBySchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc := newSLODB(t)
	org := seedSLOOrg(ctx, t, svc)

	check := seedSLOCheck(ctx, t, svc, org.UID, "api")

	valid := models.NewSLO(org.UID, "ok", "ok", 100)
	valid.CheckUID = &check
	r.NoError(svc.CreateSLO(ctx, valid))

	tooHigh := models.NewSLO(org.UID, "high", "high", 100.5)
	tooHigh.CheckUID = &check
	r.Error(svc.CreateSLO(ctx, tooHigh))

	zero := models.NewSLO(org.UID, "zero", "zero", 0)
	zero.CheckUID = &check
	r.Error(svc.CreateSLO(ctx, zero))
}

func TestSLOCrudRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc := newSLODB(t)
	org := seedSLOOrg(ctx, t, svc)

	check := seedSLOCheck(ctx, t, svc, org.UID, "api")
	group := seedSLOGroup(ctx, t, svc, org.UID, "edge")

	row := models.NewSLO(org.UID, "API availability", "api-availability", 99.9)
	row.CheckUID = &check
	r.NoError(svc.CreateSLO(ctx, row))

	count, err := svc.CountSLOs(ctx, org.UID)
	r.NoError(err)
	r.Equal(1, count)

	fetched, err := svc.GetSLOBySlug(ctx, org.UID, "api-availability")
	r.NoError(err)
	r.Equal(row.UID, fetched.UID)
	r.True(fetched.ExcludeMaintenance)
	r.InDelta(99.9, fetched.TargetPct, 0.0001)

	byCheck, err := svc.ListSLOsForChecks(ctx, org.UID, []string{check})
	r.NoError(err)
	r.Len(byCheck, 1)

	// Re-scoping to a group must clear check_uid in the same statement, or the
	// XOR constraint rejects the write.
	target := 99.5
	r.NoError(svc.UpdateSLO(ctx, row.UID, models.SLOUpdate{
		CheckGroupUID: &group,
		TargetPct:     &target,
	}))

	updated, err := svc.GetSLO(ctx, org.UID, row.UID)
	r.NoError(err)
	r.Nil(updated.CheckUID)
	r.NotNil(updated.CheckGroupUID)
	r.InDelta(99.5, updated.TargetPct, 0.0001)

	r.NoError(svc.DeleteSLO(ctx, org.UID, row.UID))

	count, err = svc.CountSLOs(ctx, org.UID)
	r.NoError(err)
	r.Zero(count)
}

// TestMarkReportScheduleRunSuppressesDuplicates pins the whole multi-replica
// safety story: the FIRST claim of a closed period wins and every later one for
// the same (or an older) period is refused, so two replicas noticing the same
// closed month send exactly one report.
func TestMarkReportScheduleRunSuppressesDuplicates(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc := newSLODB(t)
	org := seedSLOOrg(ctx, t, svc)

	schedule := models.NewReportSchedule(org.UID, "Monthly digest", models.ReportFrequencyMonthly)
	schedule.Recipients = []string{"alice@acme.com"}
	r.NoError(svc.CreateReportSchedule(ctx, schedule))

	july := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	august := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	june := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	won, err := svc.MarkReportScheduleRun(ctx, schedule.UID, july, july.Add(time.Hour))
	r.NoError(err)
	r.True(won, "the first claim of a closed period must win")

	won, err = svc.MarkReportScheduleRun(ctx, schedule.UID, july, july.Add(2*time.Hour))
	r.NoError(err)
	r.False(won, "a second claim of the SAME period must be refused")

	// An OLDER period must never re-open: a replica with a lagging clock, or a
	// restart replaying an old sweep, must not re-mail a period already past.
	won, err = svc.MarkReportScheduleRun(ctx, schedule.UID, june, july.Add(3*time.Hour))
	r.NoError(err)
	r.False(won)

	// Positive control: the NEXT period is claimable, so the refusals above are
	// specific to the already-reported period rather than a dead code path.
	won, err = svc.MarkReportScheduleRun(ctx, schedule.UID, august, august.Add(time.Hour))
	r.NoError(err)
	r.True(won)

	stored, err := svc.GetReportSchedule(ctx, org.UID, schedule.UID)
	r.NoError(err)
	r.NotNil(stored.LastPeriodStart)
	r.Equal(august.UTC(), stored.LastPeriodStart.UTC())
	r.NotNil(stored.LastRunAt)
}

func TestReportScheduleRecipientsRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc := newSLODB(t)
	org := seedSLOOrg(ctx, t, svc)

	schedule := models.NewReportSchedule(org.UID, "Weekly", models.ReportFrequencyWeekly)
	schedule.Recipients = []string{"alice@acme.com", "bob@acme.com"}
	schedule.CheckUIDs = []string{"c1"}
	r.NoError(svc.CreateReportSchedule(ctx, schedule))

	stored, err := svc.GetReportSchedule(ctx, org.UID, schedule.UID)
	r.NoError(err)
	r.Equal([]string{"alice@acme.com", "bob@acme.com"}, stored.Recipients)
	r.Equal([]string{"c1"}, stored.CheckUIDs)
	r.False(stored.IsOrgWide())

	enabled, err := svc.ListEnabledReportSchedules(ctx)
	r.NoError(err)
	r.Len(enabled, 1)

	// Unsubscribing must remove the address from the SCHEDULE, not merely
	// suppress delivery: otherwise the operator sees the address in the UI and
	// can silently re-enable mail to someone who asked to stop.
	removed, err := svc.RemoveRecipientFromReportSchedules(ctx, org.UID, "ALICE@acme.com")
	r.NoError(err)
	r.Equal(1, removed, "matching must be case-insensitive")

	stored, err = svc.GetReportSchedule(ctx, org.UID, schedule.UID)
	r.NoError(err)
	r.Equal([]string{"bob@acme.com"}, stored.Recipients)

	// Negative control with a positive one attached: an address nobody has
	// changes nothing, and bob is still there to prove the list was non-empty.
	removed, err = svc.RemoveRecipientFromReportSchedules(ctx, org.UID, "carol@acme.com")
	r.NoError(err)
	r.Zero(removed)

	stored, err = svc.GetReportSchedule(ctx, org.UID, schedule.UID)
	r.NoError(err)
	r.Equal([]string{"bob@acme.com"}, stored.Recipients)

	// Emptying the list must persist as an empty list, never as "unset".
	removed, err = svc.RemoveRecipientFromReportSchedules(ctx, org.UID, "bob@acme.com")
	r.NoError(err)
	r.Equal(1, removed)

	stored, err = svc.GetReportSchedule(ctx, org.UID, schedule.UID)
	r.NoError(err)
	r.Empty(stored.Recipients)

	r.NoError(svc.DeleteReportSchedule(ctx, org.UID, schedule.UID))

	enabled, err = svc.ListEnabledReportSchedules(ctx)
	r.NoError(err)
	r.Empty(enabled)
}
