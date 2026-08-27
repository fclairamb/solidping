package entitlements_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// checkSpec describes one check to materialize for a demand-computation case.
type checkSpec struct {
	slug      string
	checkType string
	period    time.Duration
	regions   []string
	disabled  bool
	deleted   bool
	internal  bool
}

// createCheck materializes a checkSpec. Enabled/period/regions are set on the
// row before insert so the check_job the insert derives stays consistent.
func createCheck(t *testing.T, dbSvc *sqlite.Service, orgUID string, spec checkSpec) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	check := models.NewCheck(orgUID, spec.slug, spec.checkType)
	if spec.period > 0 {
		check.Period = timeutils.Duration(spec.period)
	}

	check.Regions = spec.regions
	check.Enabled = !spec.disabled
	check.Internal = spec.internal

	r.NoError(dbSvc.CreateCheck(ctx, check))

	if spec.deleted {
		now := time.Now()
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.Check)(nil)).
			Set("deleted_at = ?", now).
			Where("uid = ?", check.UID).
			Exec(ctx)
		r.NoError(err)
	}
}

// TestChecksPerMinuteDemand pins the demand formula the over-limit banner
// compares against MaxChecksPerMinute (spec 2026-08-26-03): the sum over
// enabled, non-deleted, non-internal, non-PASSIVE checks of
// max(1, regions) x 60s/period.
//
// The passive cases carry positive controls: the same check that contributes 0
// to demand still contributes to Usage.ChecksPerMinute, which describes what
// the org configured rather than what the rate gate meters. Without that pair
// an accidental "exclude everything" bug would pass the exclusion assertions.
func TestChecksPerMinuteDemand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		spec []checkSpec
		// want is the demand metered against the cap.
		want float64
		// wantUsage is the configured rate reported on the Usage page, which
		// deliberately keeps counting passive checks.
		wantUsage float64
	}{
		{
			name: "one single-region check per minute",
			spec: []checkSpec{
				{slug: "check-a", checkType: "http", period: time.Minute},
			},
			want: 1, wantUsage: 1,
		},
		{
			name: "a multi-region check costs one execution per region",
			spec: []checkSpec{
				{slug: "check-a", checkType: "http", period: time.Minute, regions: []string{"eu", "us", "jp"}},
			},
			want: 3, wantUsage: 3,
		},
		{
			name: "a slower period costs proportionally less",
			spec: []checkSpec{
				{slug: "check-a", checkType: "http", period: 5 * time.Minute},
			},
			want: 0.2, wantUsage: 0.2,
		},
		{
			name: "heartbeat checks are passive and consume no execution budget",
			spec: []checkSpec{
				{slug: "check-hb", checkType: "heartbeat", period: time.Minute},
			},
			want: 0, wantUsage: 1,
		},
		{
			name: "email checks are passive and consume no execution budget",
			spec: []checkSpec{
				{slug: "check-mail", checkType: "email", period: time.Minute},
			},
			want: 0, wantUsage: 1,
		},
		{
			name: "a multi-region passive check is excluded region count and all",
			spec: []checkSpec{
				{slug: "check-hb", checkType: "heartbeat", period: time.Minute, regions: []string{"eu", "us"}},
			},
			want: 0, wantUsage: 2,
		},
		{
			name: "disabled checks schedule nothing",
			spec: []checkSpec{
				{slug: "check-off", checkType: "http", period: time.Minute, disabled: true},
			},
			want: 0, wantUsage: 0,
		},
		{
			name: "soft-deleted checks schedule nothing",
			spec: []checkSpec{
				{slug: "check-gone", checkType: "http", period: time.Minute, deleted: true},
			},
			want: 0, wantUsage: 0,
		},
		{
			name: "internal checks are not the customer's quota",
			spec: []checkSpec{
				{slug: "check-sys", checkType: "http", period: time.Minute, internal: true},
			},
			want: 0, wantUsage: 0,
		},
		{
			name: "a mixed org sums only what actually probes",
			spec: []checkSpec{
				{slug: "check-a", checkType: "http", period: time.Minute, regions: []string{"eu", "us"}},
				{slug: "check-b", checkType: "tcp", period: 2 * time.Minute},
				{slug: "check-hb", checkType: "heartbeat", period: time.Minute},
				{slug: "check-off", checkType: "http", period: time.Minute, disabled: true},
			},
			// 2 (two regions) + 0.5 (every two minutes); the heartbeat and the
			// disabled check contribute nothing.
			want: 2.5, wantUsage: 3.5,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, org, dbSvc := setup(t)
			for _, spec := range testCase.spec {
				createCheck(t, dbSvc, org.UID, spec)
			}

			status, err := svc.ChecksPerMinuteStatus(t.Context(), org.UID)
			r.NoError(err)
			r.InDelta(testCase.want, status.Demand, 0.0001)

			usage, err := svc.Usage(t.Context(), org.UID)
			r.NoError(err)
			r.InDelta(testCase.wantUsage, usage.ChecksPerMinute, 0.0001,
				"the Usage page figure describes configured rate, not metered demand")
		})
	}
}

// TestChecksPerMinuteStatusReportsTheResolvedLimit proves the payload carries
// the resolved cap, and that "unlimited" is a null limit that can never be
// over — not a large number.
func TestChecksPerMinuteStatusReportsTheResolvedLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, dbSvc := setup(t)
	// 4 executions/minute of demand.
	createCheck(t, dbSvc, org.UID, checkSpec{
		slug: "check-a", checkType: "http", period: time.Minute, regions: []string{"eu", "us", "jp", "au"},
	})

	// Self-hosted defaults leave MaxChecksPerMinute unset = unlimited.
	status, err := svc.ChecksPerMinuteStatus(t.Context(), org.UID)
	r.NoError(err)
	r.Nil(status.Limit, "an unset cap must stay null on the wire, not become a number")
	r.False(status.Over(), "an unlimited org is never over its cap")

	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{MaxChecksPerMinute: entitlements.Int(2)},
	}, "test", ""))

	status, err = svc.ChecksPerMinuteStatus(t.Context(), org.UID)
	r.NoError(err)
	r.NotNil(status.Limit)
	r.Equal(2, *status.Limit)
	r.True(status.Over(), "4/min of demand against a 2/min cap is over")

	r.NoError(svc.Set(t.Context(), org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{MaxChecksPerMinute: entitlements.Int(4)},
	}, "test", ""))

	status, err = svc.ChecksPerMinuteStatus(t.Context(), org.UID)
	r.NoError(err)
	r.False(status.Over(), "demand exactly AT the cap is not over it")
}

// TestRecordRateLimitedSkipCountsPerOrgPerDay pins the counter's identity: the
// right kind, the UTC DAY (not the month, which would keep the banner lit for
// weeks after an org came back under its cap), and no leakage across orgs.
func TestRecordRateLimitedSkipCountsPerOrgPerDay(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org, dbSvc := setup(t)

	other := models.NewOrganization("ent-org-other", "Other")
	r.NoError(dbSvc.CreateOrganization(ctx, other))

	status, err := svc.ChecksPerMinuteStatus(ctx, org.UID)
	r.NoError(err)
	r.Zero(status.SkippedToday, "an org that lost nothing reports zero, not a missing field")

	svc.RecordRateLimitedSkip(ctx, org.UID)
	svc.RecordRateLimitedSkip(ctx, org.UID)
	svc.RecordRateLimitedSkip(ctx, org.UID)
	svc.RecordRateLimitedSkip(ctx, other.UID)

	status, err = svc.ChecksPerMinuteStatus(ctx, org.UID)
	r.NoError(err)
	r.Equal(3, status.SkippedToday)

	otherStatus, err := svc.ChecksPerMinuteStatus(ctx, other.UID)
	r.NoError(err)
	r.Equal(1, otherStatus.SkippedToday, "skips must not leak between organizations")

	// The row itself: kind and a DAILY period_start. A monthly bucket would
	// read back identically today and wrongly tomorrow, so assert the key.
	var counter models.OrgUsageCounter
	r.NoError(dbSvc.DB().NewSelect().
		Model(&counter).
		Where("organization_uid = ?", org.UID).
		Scan(ctx))
	r.Equal(models.UsageCounterKindCheckRateLimited, counter.Kind)
	r.Equal(time.Now().UTC().Format("2006-01-02"), counter.PeriodStart)
	r.Equal(3, counter.Count)

	// Yesterday's tally must not be readable as today's.
	yesterday, err := dbSvc.GetMonthlyUsage(
		ctx, org.UID, models.UsageCounterKindCheckRateLimited,
		time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"),
	)
	r.NoError(err)
	r.Zero(yesterday, "the counter buckets by day, so yesterday reads back empty")
}

// TestRecordRateLimitedSkipNilServiceIsInert guards the agent-mode / disabled
// path: a nil entitlements service must be a no-op, never a panic on a hot
// claim path.
func TestRecordRateLimitedSkipNilServiceIsInert(t *testing.T) {
	t.Parallel()

	var svc *entitlements.Service

	require.NotPanics(t, func() {
		svc.RecordRateLimitedSkip(t.Context(), "any-org")
	})
}
