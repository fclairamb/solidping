package checks_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// availability24hCase is one seeded scenario for CheckStats.Availability24h
// (spec 2026-08-26-09) — the trailing-24h availability KPI that used to be
// fabricated (see the spec's Problem section). Shared by the SQLite suite
// below and its embedded-Postgres twin in availability24h_postgres_test.go,
// so both dialects are held to the same expectations.
type availability24hCase struct {
	name string
	// setup seeds whatever raw/hour/day rows the case needs, all placed
	// relative to `now` (the pinned clock svc.GetCheckStats reads "now"
	// through).
	setup func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time)
	// wantPct is nil to mean "expect Availability24h == nil" (no countable
	// data in the window) — never a fabricated percentage.
	wantPct *float64
	// want7d and want30d are the same expectation for Availability7d and
	// Availability30d (spec 2026-11-09-02).
	want7d  *float64
	want30d *float64
}

func ptrF64(f float64) *float64 { return &f }

// seedRawResult writes one raw result row at an exact PeriodStart, bypassing
// the check-worker pipeline — CreateResult is a plain insert.
func seedRawResult(
	ctx context.Context, t *testing.T, dbSvc db.Service,
	orgUID, checkUID string, status models.ResultStatus, at time.Time,
) {
	t.Helper()

	result := models.NewResult(orgUID, checkUID, status, 10)
	result.PeriodStart = at
	require.NoError(t, dbSvc.CreateResult(ctx, result))
}

// seedHourResult writes one `hour` rollup row with explicit
// total/successful counts, optionally tagged with a region.
func seedHourResult(
	ctx context.Context, t *testing.T, dbSvc db.Service,
	orgUID, checkUID string, at time.Time, total, success int, region *string,
) {
	t.Helper()

	row := &models.Result{
		UID:              uuid.Must(uuid.NewV7()).String(),
		OrganizationUID:  orgUID,
		CheckUID:         checkUID,
		PeriodType:       models.PeriodTypeHour,
		PeriodStart:      at,
		Region:           region,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
		CreatedAt:        time.Now(),
	}
	require.NoError(t, dbSvc.CreateResult(ctx, row))
}

// seedDayResult writes one `day` rollup row. With the default retention, day
// rows only exist past the 7-day hour retention, so they matter to the 7d and
// 30d windows.
func seedDayResult(
	ctx context.Context, t *testing.T, dbSvc db.Service,
	orgUID, checkUID string, at time.Time, total, success int,
) {
	t.Helper()

	row := &models.Result{
		UID:              uuid.Must(uuid.NewV7()).String(),
		OrganizationUID:  orgUID,
		CheckUID:         checkUID,
		PeriodType:       models.PeriodTypeDay,
		PeriodStart:      at,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
		CreatedAt:        time.Now(),
	}
	require.NoError(t, dbSvc.CreateResult(ctx, row))
}

// availability24hCases is shared by the SQLite suite below and by the
// embedded-Postgres twin in availability24h_postgres_test.go.
func availability24hCases() []availability24hCase {
	return []availability24hCase{
		{
			name:    "empty org has no countable data in the window -> null",
			setup:   func(context.Context, *testing.T, db.Service, string, string, time.Time) {},
			wantPct: nil,
			want7d:  nil,
			want30d: nil,
		},
		{
			name: "raw-only org (young org): up/down/warning mix",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time) {
				t.Helper()
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusUp, now.Add(-1*time.Hour))
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusUp, now.Add(-2*time.Hour))
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusDown, now.Add(-3*time.Hour))
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusWarning, now.Add(-4*time.Hour))
			},
			// success = up(2) + warning(1) = 3; total = 4 -> 75%.
			wantPct: ptrF64(75),
			want7d:  ptrF64(75),
			want30d: ptrF64(75),
		},
		{
			name: "hour+raw mix combines both tiers in one figure",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time) {
				t.Helper()
				seedHourResult(ctx, t, dbSvc, orgUID, checkUID, now.Add(-20*time.Hour), 100, 95, nil)
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusUp, now.Add(-1*time.Hour))
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusDown, now.Add(-2*time.Hour))
			},
			// success = 95+1=96; total = 100+2=102.
			wantPct: ptrF64(96.0 / 102.0 * 100),
			want7d:  ptrF64(96.0 / 102.0 * 100),
			want30d: ptrF64(96.0 / 102.0 * 100),
		},
		{
			// The headline proof from the spec's Testing section: an org
			// whose only rows are excluded statuses must return null, NOT
			// 100 — 100 is the exact fabrication this spec fixes.
			name: "org with only excluded statuses (created, running, abandoned) returns null, not 100",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time) {
				t.Helper()
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusCreated, now.Add(-1*time.Hour))
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusRunning, now.Add(-2*time.Hour))
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusAbandoned, now.Add(-3*time.Hour))
			},
			wantPct: nil,
			want7d:  nil,
			want30d: nil,
		},
		{
			name: "warning counts as up",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time) {
				t.Helper()
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusWarning, now.Add(-1*time.Hour))
			},
			wantPct: ptrF64(100),
			want7d:  ptrF64(100),
			want30d: ptrF64(100),
		},
		{
			name: "multi-region hour rows are summed together, not kept separate",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time) {
				t.Helper()
				eu, us := "eu", "us"
				seedHourResult(ctx, t, dbSvc, orgUID, checkUID, now.Add(-10*time.Hour), 50, 50, &eu)
				seedHourResult(ctx, t, dbSvc, orgUID, checkUID, now.Add(-10*time.Hour), 50, 25, &us)
			},
			wantPct: ptrF64(75),
			want7d:  ptrF64(75),
			want30d: ptrF64(75),
		},
		{
			name: "rows older than the trailing 24h window are excluded from it, not from 7d",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time) {
				t.Helper()
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusDown, now.Add(-25*time.Hour))
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusUp, now.Add(-1*time.Hour))
			},
			wantPct: ptrF64(100),
			want7d:  ptrF64(50),
			want30d: ptrF64(50),
		},
		{
			name: "hour rows older than 24h count in the 7d window",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time) {
				t.Helper()
				seedHourResult(ctx, t, dbSvc, orgUID, checkUID, now.Add(-3*24*time.Hour), 100, 80, nil)
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusUp, now.Add(-1*time.Hour))
			},
			wantPct: ptrF64(100),
			want7d:  ptrF64(81.0 / 101.0 * 100),
			want30d: ptrF64(81.0 / 101.0 * 100),
		},
		{
			name: "a day rollup older than 7d counts in the 30d window only",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time) {
				t.Helper()
				seedDayResult(ctx, t, dbSvc, orgUID, checkUID, now.Add(-10*24*time.Hour), 100, 50)
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusUp, now.Add(-1*time.Hour))
			},
			wantPct: ptrF64(100),
			want7d:  ptrF64(100),
			want30d: ptrF64(51.0 / 101.0 * 100),
		},
		{
			name: "rows older than 30 days are excluded from every window",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string, now time.Time) {
				t.Helper()
				seedDayResult(ctx, t, dbSvc, orgUID, checkUID, now.Add(-31*24*time.Hour), 1000, 0)
				seedRawResult(ctx, t, dbSvc, orgUID, checkUID, models.ResultStatusUp, now.Add(-1*time.Hour))
			},
			wantPct: ptrF64(100),
			want7d:  ptrF64(100),
			want30d: ptrF64(100),
		},
	}
}

// requireAvailability24hEqual compares the computed availability24h against
// a case's expectation, treating nil specially so a fabricated non-nil value
// where "no data" was expected fails loudly rather than comparing floats.
func requireAvailability24hEqual(t *testing.T, want *float64, got *float64) {
	t.Helper()
	requireAvailabilityEqual(t, "availability24h", want, got)
}

// requireAvailabilityEqual is requireAvailability24hEqual for any field.
func requireAvailabilityEqual(t *testing.T, field string, want *float64, got *float64) {
	t.Helper()
	r := require.New(t)

	if want == nil {
		r.Nil(got, "expected null %s (no countable data), got a fabricated value: %v", field, got)
		return
	}

	r.NotNil(got, "expected a real %s value, got null", field)
	r.InDelta(*want, *got, 0.01, field)
}

// requireAvailabilityCase checks every availability field of stats against tc.
func requireAvailabilityCase(t *testing.T, tc availability24hCase, stats checks.CheckStatsResponse) {
	t.Helper()
	requireAvailability24hEqual(t, tc.wantPct, stats.Availability24h)
	requireAvailabilityEqual(t, "availability7d", tc.want7d, stats.Availability7d)
	requireAvailabilityEqual(t, "availability30d", tc.want30d, stats.Availability30d)
}

// TestGetCheckStatsAvailability24h_SQLite is the table-driven suite on
// SQLite. Its embedded-Postgres twin lives in
// availability24h_postgres_test.go and runs the same cases through
// availability24hCases().
func TestGetCheckStatsAvailability24h_SQLite(t *testing.T) {
	t.Parallel()

	for i, tc := range availability24hCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := t.Context()

			svc, dbSvc, org := newStatsService(t, fmt.Sprintf("avail24h-%d", i))

			now := time.Now().UTC()
			checks.SetRegionHealthNowForTest(svc, func() time.Time { return now })

			check := models.NewCheck(org.UID, fmt.Sprintf("avail24h-check-%d", i), "http")
			r.NoError(dbSvc.CreateCheck(ctx, check))

			tc.setup(ctx, t, dbSvc, org.UID, check.UID, now)

			stats, err := svc.GetCheckStats(ctx, org.Slug)
			r.NoError(err)

			requireAvailabilityCase(t, tc, stats)
		})
	}
}

// TestGetCheckStatsAvailability24hIsNullOnFreshOrg is a focused positive
// control on top of the table above: an org with no checks and no results at
// all — the actual "brand-new org" shape the dashboard sees the moment it
// first loads — must read null, not a fabricated number.
func TestGetCheckStatsAvailability24hIsNullOnFreshOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := newStatsService(t, "avail24h-fresh")

	stats, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Nil(stats.Availability24h)
	r.Nil(stats.Availability7d)
	r.Nil(stats.Availability30d)
}
