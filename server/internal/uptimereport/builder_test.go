package uptimereport

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/slo"
)

// builderEnv is a real db.Service on in-memory SQLite. The degenerate cases
// this spec is about (an empty previous window, a scope down end to end, a
// check created mid-period) are all about what the READ PATH returns, so they
// are exercised through the actual queries rather than through a stub that
// would have to reimplement the tier split.
type builderEnv struct {
	db      db.Service
	builder *Builder
	org     *models.Organization
	ctx     context.Context //nolint:containedctx // test fixture convenience
}

func newBuilderEnv(t *testing.T) *builderEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	// Org slugs are capped at 20 characters by a DB check constraint.
	org := models.NewOrganization(fmt.Sprintf("rep-%d", time.Now().UnixNano()%1_000_000_000), "Report Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	return &builderEnv{
		db:      dbSvc,
		builder: NewBuilder(dbSvc, &config.Config{}, nil),
		org:     org,
		ctx:     ctx,
	}
}

func (e *builderEnv) createCheck(t *testing.T, slug string) *models.Check {
	t.Helper()

	check := models.NewCheck(e.org.UID, slug, "http")
	name := slug
	check.Name = &name
	require.NoError(t, e.db.CreateCheck(e.ctx, check))

	return check
}

// addRaw writes `count` raw probes at `at`, `up` of them successful, each
// carrying `durationMillis`.
func (e *builderEnv) addRaw(t *testing.T, check *models.Check, at time.Time, up, count int, durationMillis float32) {
	t.Helper()

	for i := range count {
		status := models.ResultStatusDown
		if i < up {
			status = models.ResultStatusUp
		}

		result := models.NewResult(e.org.UID, check.UID, status, durationMillis)
		result.PeriodStart = at.Add(time.Duration(i) * time.Second)
		require.NoError(t, e.db.CreateResult(e.ctx, result))
	}
}

// addDayRollup writes one day-tier rollup row — the only way data older than
// the raw retention band is readable, and therefore what a previous-period
// comparison actually reads in production.
func (e *builderEnv) addDayRollup(
	t *testing.T, check *models.Check, day time.Time, up, total int, durAvg, durMin, durMax float32,
) {
	t.Helper()

	start := day.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)

	result := &models.Result{
		UID:              fmt.Sprintf("%s-%d", check.UID[:8], start.Unix()),
		OrganizationUID:  e.org.UID,
		CheckUID:         check.UID,
		PeriodType:       models.PeriodTypeDay,
		PeriodStart:      start,
		PeriodEnd:        &end,
		TotalChecks:      &total,
		SuccessfulChecks: &up,
		DurationAvg:      &durAvg,
		DurationMin:      &durMin,
		DurationMax:      &durMax,
		CreatedAt:        time.Now(),
	}
	require.NoError(t, e.db.CreateResult(e.ctx, result))
}

func monthlySchedule(orgUID string) *models.ReportSchedule {
	schedule := models.NewReportSchedule(orgUID, "Monthly digest", models.ReportFrequencyMonthly)
	schedule.Timezone = "UTC"
	schedule.IncludeSLOs = false

	return schedule
}

// currentMonth is the window the tests report on. It ends in the future, which
// is what lets raw rows written "an hour ago" fall inside uptimebar's raw
// retention clamp — exactly as they do for a real report on a period that just
// closed.
func currentMonth(now time.Time) slo.Window {
	return slo.MonthWindow(time.UTC, now)
}

// TestBuilderOmitsDeltasWithoutAPreviousPeriod is the new-org case: nothing was
// recorded before the reported period, so the digest must carry NO delta at
// all — no "±0.00%", no color, no "vs" line.
func TestBuilderOmitsDeltasWithoutAPreviousPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := newBuilderEnv(t)
	now := time.Now().UTC()
	window := currentMonth(now)

	check := env.createCheck(t, "api")
	env.addRaw(t, check, now.Add(-time.Hour), 100, 100, 150)

	data, err := env.builder.Build(env.ctx, env.org, monthlySchedule(env.org.UID), window, now)
	r.NoError(err)

	r.True(data.HasData, "the reported period itself must still have data")
	r.False(data.HasPreviousData)
	r.False(data.ShowAvailabilityDelta)
	r.False(data.ShowIncidentDelta)
	r.False(data.ShowResponseDelta)
	r.Empty(data.AvailabilityDeltaText)
	r.Empty(data.AvailabilityDeltaColor)
	r.Empty(data.PreviousAvailabilityPct)
	// The label is still resolved so the template can name the baseline once
	// one exists, but naming it is not showing a delta.
	r.NotEmpty(data.PreviousPeriodLabel)
}

// TestBuilderComparesAgainstThePreviousPeriod is the positive control for the
// test above AND for the zero-delta neutrality below: a real baseline read out
// of day rollups produces a real, colored delta.
func TestBuilderComparesAgainstThePreviousPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := newBuilderEnv(t)
	now := time.Now().UTC()
	window := currentMonth(now)
	previous := slo.PrecedingWindow(time.UTC, window, false)

	check := env.createCheck(t, "api")

	// This period: perfect, and slower.
	env.addRaw(t, check, now.Add(-time.Hour), 100, 100, 200)
	// Last period: 99%, and faster.
	env.addDayRollup(t, check, previous.Start.Add(48*time.Hour), 990, 1000, 100, 50, 400)

	data, err := env.builder.Build(env.ctx, env.org, monthlySchedule(env.org.UID), window, now)
	r.NoError(err)

	r.True(data.HasPreviousData)
	r.Equal(formatPct(99), data.PreviousAvailabilityPct)

	r.True(data.ShowAvailabilityDelta)
	r.Equal("+1.000 pts", data.AvailabilityDeltaText)
	r.Equal(deltaGoodColor, data.AvailabilityDeltaColor)

	r.True(data.ShowResponseDelta)
	r.Equal("+100.0%", data.ResponseDeltaText)
	r.Equal(deltaBadColor, data.ResponseDeltaColor, "getting slower is a regression, never green")

	r.True(data.ShowIncidentDelta)
	r.Equal(noChangeText, data.IncidentDeltaText)
	r.Equal(deltaNeutralColor, data.IncidentDeltaColor)
}

// TestBuilderZeroDeltaIsNeutralNeverGreen: two identical periods.
func TestBuilderZeroDeltaIsNeutralNeverGreen(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := newBuilderEnv(t)
	now := time.Now().UTC()
	window := currentMonth(now)
	previous := slo.PrecedingWindow(time.UTC, window, false)

	check := env.createCheck(t, "api")
	env.addRaw(t, check, now.Add(-time.Hour), 100, 100, 150)
	env.addDayRollup(t, check, previous.Start.Add(48*time.Hour), 1000, 1000, 150, 150, 150)

	data, err := env.builder.Build(env.ctx, env.org, monthlySchedule(env.org.UID), window, now)
	r.NoError(err)

	r.Equal(noChangeText, data.AvailabilityDeltaText)
	r.Equal(deltaNeutralColor, data.AvailabilityDeltaColor)
	r.Equal(noChangeText, data.ResponseDeltaText)
	r.Equal(deltaNeutralColor, data.ResponseDeltaColor)
	r.NotEqual(deltaGoodColor, data.AvailabilityDeltaColor)
	r.NotEqual(deltaGoodColor, data.ResponseDeltaColor)
}

// TestBuilderDownAllPeriodSuppressesLatency is the competitor failure mode in
// full: a scope that was down end to end must state that as a fact, must NOT
// show a response-time grid built from error responses, and must not celebrate
// a "faster" response time against last month's healthy baseline.
func TestBuilderDownAllPeriodSuppressesLatency(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := newBuilderEnv(t)
	now := time.Now().UTC()
	window := currentMonth(now)
	previous := slo.PrecedingWindow(time.UTC, window, false)

	check := env.createCheck(t, "api")

	// Every probe failed this period, and failed FAST (a refused connection
	// answers in microseconds) — the shape that produced a celebrated
	// "-13% response time" in the report this spec reacts to.
	env.addRaw(t, check, now.Add(-time.Hour), 0, 100, 5)
	env.addDayRollup(t, check, previous.Start.Add(48*time.Hour), 1000, 1000, 300, 100, 900)

	data, err := env.builder.Build(env.ctx, env.org, monthlySchedule(env.org.UID), window, now)
	r.NoError(err)

	r.True(data.HasData)
	r.Equal(formatPct(0), data.AvailabilityPct)
	r.True(data.DownAllPeriod)
	r.Equal("api", data.DownAllPeriodLabel, "a single-check scope names the check itself")

	r.False(data.HasLatency, "no response-time block for a period of pure errors")
	r.Empty(data.AvgResponseTime)
	r.Empty(data.MinResponseTime)
	r.Empty(data.MaxResponseTime)
	r.Empty(data.SlowLine)

	r.False(data.ShowResponseDelta, "a 98% 'improvement' measured on errors must never ship")
	r.Empty(data.ResponseDeltaText)

	// The availability delta IS still shown — and it is red.
	r.True(data.ShowAvailabilityDelta)
	r.Equal(deltaBadColor, data.AvailabilityDeltaColor)

	// The row carries the same statement.
	r.Len(data.Checks, 1)
	r.True(data.Checks[0].DownAllPeriod)
}

// TestBuilderShowsLatencyForAHealthyPeriod is the positive control for the
// suppression above: identical fixture minus the outage, and the whole block
// appears.
func TestBuilderShowsLatencyForAHealthyPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := newBuilderEnv(t)
	now := time.Now().UTC()
	window := currentMonth(now)

	check := env.createCheck(t, "api")
	env.addRaw(t, check, now.Add(-time.Hour), 100, 100, 150)
	// One slow probe, so the slow-sample counter is exercised rather than
	// merely defaulting to zero.
	env.addRaw(t, check, now.Add(-30*time.Minute), 1, 1, 4200)

	data, err := env.builder.Build(env.ctx, env.org, monthlySchedule(env.org.UID), window, now)
	r.NoError(err)

	r.False(data.DownAllPeriod)
	r.True(data.HasLatency)
	r.NotEmpty(data.AvgResponseTime)
	r.Equal("150 ms", data.MinResponseTime)
	r.Equal("4.20 s", data.MaxResponseTime)
	r.Equal("1 sample above 1 s", data.SlowLine)
	r.Contains(data.LatencyNote, "failed samples")
}

// TestBuilderMidPeriodCheckGetsGrayDaysNotRed: a check created part-way
// through the month has no rows for the earlier days, and those cells must be
// gray. The positive control is the same strip's one day WITH data, which is
// green — so "gray" here cannot be "the strip is broken and everything is
// gray".
//
// The window is a fixed month in the past, fed entirely from day rollups: that
// is what a real monthly report reads (raw older than the retention band is
// gone), and it keeps the fixture independent of the wall clock.
func TestBuilderMidPeriodCheckGetsGrayDaysNotRed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := newBuilderEnv(t)

	window := slo.MonthWindow(time.UTC, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	now := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)

	check := env.createCheck(t, "new-check")
	// The check only started reporting on 20 July.
	env.addDayRollup(t, check, time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), 1440, 1440, 120, 90, 200)

	data, err := env.builder.Build(env.ctx, env.org, monthlySchedule(env.org.UID), window, now)
	r.NoError(err)

	r.Len(data.Checks, 1)

	cells := data.Checks[0].Days
	r.NotEmpty(cells, "the strip must be built")

	colors := make(map[string]int)
	days := 0

	for _, cell := range cells {
		colors[cell.Color] += cell.Span
		days += cell.Span
	}

	r.Equal(31, days, "the strip covers every day of July")
	r.Equal(30, colors[dayNoDataColor], "days before the check existed are gray")
	r.Equal(1, colors[dayGoodColor], "the one day with data is green")
	r.Zero(colors[dayBadColor], "a day the check did not exist is NOT a red day")
	r.NotEmpty(data.DayStripLabel)
	r.Contains(data.DayStripLabel, "(UTC)")

	// The check row itself is not "0%" either — it reports the day it has.
	r.True(data.Checks[0].HasData)
	r.False(data.Checks[0].DownAllPeriod)
}

// TestBuilderSortsWorstFirstAndReportsTruncation covers the ordering end to
// end, including that the cap keeps the worst rows and says so.
func TestBuilderSortsWorstFirstAndReportsTruncation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := newBuilderEnv(t)
	now := time.Now().UTC()
	window := currentMonth(now)

	// 55 healthy checks named so they sort alphabetically BEFORE the broken
	// one — under the old name sort the broken check would be row 56 of a
	// 50-row table, i.e. invisible.
	for i := range maxCheckRows + 5 {
		healthy := env.createCheck(t, fmt.Sprintf("aaa-%03d", i))
		env.addRaw(t, healthy, now.Add(-time.Hour), 10, 10, 100)
	}

	broken := env.createCheck(t, "zzz-broken")
	env.addRaw(t, broken, now.Add(-time.Hour), 2, 10, 100)

	silent := env.createCheck(t, "aaa-silent-no-data")
	_ = silent

	data, err := env.builder.Build(env.ctx, env.org, monthlySchedule(env.org.UID), window, now)
	r.NoError(err)

	r.True(data.Truncated)
	r.Equal(maxCheckRows, data.TruncatedShown)
	r.Equal(maxCheckRows+7, data.TruncatedTotal)
	r.Len(data.Checks, maxCheckRows)

	r.Equal("zzz-broken", data.Checks[0].Name, "the worst check leads the table")

	// The no-data check is not in the shipped rows at all (it sorts last), and
	// it certainly is not treated as 0%.
	for _, row := range data.Checks {
		r.NotEqual("aaa-silent-no-data", row.Name)
		r.True(row.HasData)
	}
}

// TestBuilderReportsAverageIncidentDuration covers the derived stat, and pins
// that it is absent (not "0s") when there were no incidents.
func TestBuilderReportsAverageIncidentDuration(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := newBuilderEnv(t)
	now := time.Now().UTC()
	window := currentMonth(now)

	check := env.createCheck(t, "api")
	env.addRaw(t, check, now.Add(-time.Hour), 90, 100, 150)

	// No incidents yet: the average must be absent.
	data, err := env.builder.Build(env.ctx, env.org, monthlySchedule(env.org.UID), window, now)
	r.NoError(err)
	r.Zero(data.IncidentCount)
	r.Empty(data.AverageIncident)

	// Two incidents of 10 and 30 minutes -> average 20 minutes.
	for i, minutes := range []int{10, 30} {
		startedAt := window.Start.Add(time.Duration(i+1) * 24 * time.Hour)
		resolvedAt := startedAt.Add(time.Duration(minutes) * time.Minute)

		incident := models.NewIncident(env.org.UID, check.UID, startedAt, "down")
		incident.State = models.IncidentStateResolved
		incident.ResolvedAt = &resolvedAt
		r.NoError(env.db.CreateIncident(env.ctx, incident))
	}

	data, err = env.builder.Build(env.ctx, env.org, monthlySchedule(env.org.UID), window, now)
	r.NoError(err)

	r.Equal(2, data.IncidentCount)
	r.Equal("30m", data.LongestIncident)
	r.Equal("20m", data.AverageIncident)
	r.Equal("40m", data.TotalDowntime)
}
