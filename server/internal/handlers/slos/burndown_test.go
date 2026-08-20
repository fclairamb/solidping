package slos_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// The burn-down fixtures live in a FIXED past month with an injected `now`, so
// the assertions do not depend on which day the suite happens to run.
func burndownWindowStartAt() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }

// burndownNowAt is exactly two full days into June: two steps, no partial
// trailing slice.
func burndownNowAt() time.Time { return time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC) }

// juneBudgetSeconds is a 99.9% objective over a 30-day UTC month.
const juneBudgetSeconds = int64(0.001 * 30 * 24 * 3600)

type burndownEnv struct {
	svc      *slos.Service
	dbSvc    *sqlite.Service
	org      *models.Organization
	checkUID string
}

func newBurndownEnv(t *testing.T) *burndownEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	// Well before the window, so nothing is clamped as "did not exist yet".
	check.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.NoError(dbSvc.CreateCheck(ctx, check))

	cfg := &config.Config{}

	return &burndownEnv{
		svc:      slos.NewService(dbSvc, cfg, nil),
		dbSvc:    dbSvc,
		org:      org,
		checkUID: check.UID,
	}
}

// seedHour writes one aggregated hour row.
func (e *burndownEnv) seedHour(t *testing.T, at time.Time, total, success int) {
	t.Helper()

	end := at.Add(time.Hour)
	status := int(models.ResultStatusUp)

	require.NoError(t, e.dbSvc.CreateResult(t.Context(), &models.Result{
		UID:              uuid.Must(uuid.NewV7()).String(),
		OrganizationUID:  e.org.UID,
		CheckUID:         e.checkUID,
		PeriodType:       models.PeriodTypeHour,
		PeriodStart:      at,
		PeriodEnd:        &end,
		Status:           &status,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
		CreatedAt:        at,
	}))
}

// burndownTargetPct is the objective every burn-down fixture uses: 99.9%
// monthly, which over a 30-day UTC month buys juneBudgetSeconds of budget.
const burndownTargetPct = 99.9

func (e *burndownEnv) seedSLO(t *testing.T, slug string) *models.SLO {
	t.Helper()

	objective := models.NewSLO(e.org.UID, slug, slug, burndownTargetPct)
	objective.CheckUID = &e.checkUID
	require.NoError(t, e.dbSvc.CreateSLO(t.Context(), objective))

	return objective
}

func (e *burndownEnv) burndown(t *testing.T, uid string) *slos.BurndownResponse {
	t.Helper()

	out, err := e.svc.GetBurndown(context.Background(), e.org.Slug, uid, burndownNowAt())
	require.NoError(t, err)

	return out
}

// TestBurndownStaysMonotonicWhenProbeDensityRises is the regression test for
// the defect the audit found: consumption used to be derived as
// (window-to-date failure ratio) x (window-to-date wall clock), which FALLS
// when probe density rises — so the burn-down climbed back up.
//
// Day 1 is sparse and half-failing (60 probes, 30 up). Day 2 is dense and
// perfect (1440 probes, all up). Under the old math the cumulative ratio
// collapses from 0.5 to 0.02 while the wall clock only doubles, so consumption
// drops from 43 200 s to 3 456 s and remaining budget rises by ~39 700 s.
// Under per-step accrual, day 2 adds nothing and the series stays flat.
func TestBurndownStaysMonotonicWhenProbeDensityRises(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newBurndownEnv(t)

	// Day 1: one sparse hour, half of it failing. Nothing else all day.
	env.seedHour(t, burndownWindowStartAt(), 60, 30)

	// Day 2: a full day of dense, perfect coverage.
	for hour := range 24 {
		env.seedHour(t, burndownWindowStartAt().AddDate(0, 0, 1).Add(time.Duration(hour)*time.Hour), 60, 60)
	}

	objective := env.seedSLO(t, "api-availability")
	series := env.burndown(t, objective.UID)

	// Positive controls: the window, the budget and the step count are what the
	// fixture intends, so the monotonicity assertion below is about the math
	// and not about an empty or mis-scoped series.
	r.Equal(juneBudgetSeconds, series.BudgetTotalSeconds)
	r.Len(series.Data, 2)
	r.Equal("2026-06-02T00:00:00Z", series.Data[0].At.Format(time.RFC3339))
	r.Equal("2026-06-03T00:00:00Z", series.Data[1].At.Format(time.RFC3339))

	// Day 1 spent half a day of budget: 0.5 x 86 400 s.
	const dayOneConsumed = int64(43200)
	r.Equal(juneBudgetSeconds-dayOneConsumed, series.Data[0].BudgetRemainingSeconds)

	// Day 2 was perfect, so it spends nothing — and crucially does NOT hand any
	// back. This is the assertion the old math failed.
	r.Equal(series.Data[0].BudgetRemainingSeconds, series.Data[1].BudgetRemainingSeconds)

	for i := 1; i < len(series.Data); i++ {
		r.LessOrEqual(series.Data[i].BudgetRemainingSeconds,
			series.Data[i-1].BudgetRemainingSeconds,
			"remaining budget must never increase within a window")
	}

	// The unclamped negative is load-bearing: this objective is catastrophically
	// breached, and flattening the series at zero would hide by how much. A
	// future clamp must fail here.
	r.Negative(series.Data[1].BudgetRemainingSeconds)
	r.Less(series.Data[1].BudgetRemainingSeconds, -dayOneConsumed/2)

	// Attainment stays window-to-date (cumulative), so it legitimately RISES as
	// the healthy day lands — that is a different quantity from the budget and
	// is not expected to be monotonic.
	r.NotNil(series.Data[0].AttainmentPct)
	r.InDelta(50.0, *series.Data[0].AttainmentPct, 0.0001)
	r.NotNil(series.Data[1].AttainmentPct)
	r.InDelta(98.0, *series.Data[1].AttainmentPct, 0.0001)
}

// A step with no countable probe must spend nothing: no data is "we were not
// watching", not downtime. The burn-down flattens across the gap rather than
// dropping through it.
func TestBurndownGapSpendsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newBurndownEnv(t)

	// Day 1 has a partial outage; day 2 has no results at all.
	env.seedHour(t, burndownWindowStartAt(), 100, 90)

	objective := env.seedSLO(t, "gappy")
	series := env.burndown(t, objective.UID)

	r.Len(series.Data, 2)

	// Positive control: day 1 really did spend budget (10% of 86 400 s).
	r.Equal(juneBudgetSeconds-int64(8640), series.Data[0].BudgetRemainingSeconds)

	// Day 2 is silent: identical remaining budget, and attainment stays at the
	// window-to-date value rather than being dragged anywhere.
	r.Equal(series.Data[0].BudgetRemainingSeconds, series.Data[1].BudgetRemainingSeconds)
	r.NotNil(series.Data[1].AttainmentPct)
	r.InDelta(90.0, *series.Data[1].AttainmentPct, 0.0001)
}

// The ideal line is drawn against the SAME window-level budget the response
// reports, decaying linearly to zero at the window end.
func TestBurndownIdealLineTracksTheReportedBudget(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newBurndownEnv(t)

	env.seedHour(t, burndownWindowStartAt(), 100, 100)

	objective := env.seedSLO(t, "healthy")
	series := env.burndown(t, objective.UID)

	r.Len(series.Data, 2)
	r.Equal(juneBudgetSeconds, series.BudgetTotalSeconds)

	// 1/30th and 2/30ths of June elapsed at the two sample points.
	r.Equal(int64(math.Round(float64(juneBudgetSeconds)*(1-1.0/30))), series.Data[0].IdealRemainingSeconds)
	r.Equal(int64(math.Round(float64(juneBudgetSeconds)*(1-2.0/30))), series.Data[1].IdealRemainingSeconds)

	// A perfect window leaves the budget untouched, so the actual line sits
	// above the ideal one throughout — which is what "healthy" looks like.
	for _, point := range series.Data {
		r.Equal(series.BudgetTotalSeconds, point.BudgetRemainingSeconds)
		r.Greater(point.BudgetRemainingSeconds, point.IdealRemainingSeconds)
	}
}

// Maintenance-tagged time neither spends budget nor counts toward the window's
// wall clock, and turning the exclusion off is the positive control.
func TestBurndownExcludesMaintenance(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newBurndownEnv(t)

	// Day 1: 100 probes, 20 of them under maintenance and all 20 failing.
	end := burndownWindowStartAt().Add(time.Hour)
	status := int(models.ResultStatusUp)
	total, success, maintTotal, maintSuccess := 100, 80, 20, 0

	r.NoError(env.dbSvc.CreateResult(t.Context(), &models.Result{
		UID:                         uuid.Must(uuid.NewV7()).String(),
		OrganizationUID:             env.org.UID,
		CheckUID:                    env.checkUID,
		PeriodType:                  models.PeriodTypeHour,
		PeriodStart:                 burndownWindowStartAt(),
		PeriodEnd:                   &end,
		Status:                      &status,
		TotalChecks:                 &total,
		SuccessfulChecks:            &success,
		MaintenanceChecks:           &maintTotal,
		MaintenanceSuccessfulChecks: &maintSuccess,
		CreatedAt:                   burndownWindowStartAt(),
	}))

	excluded := env.seedSLO(t, "excluded")
	excludedSeries := env.burndown(t, excluded.UID)

	// Every failure was planned work, so nothing is spent.
	r.Len(excludedSeries.Data, 2)
	r.Equal(excludedSeries.BudgetTotalSeconds, excludedSeries.Data[0].BudgetRemainingSeconds)

	// Positive control: the same data with the exclusion off is a hard breach.
	included := models.NewSLO(env.org.UID, "included", "included", burndownTargetPct)
	included.CheckUID = &env.checkUID
	included.ExcludeMaintenance = false
	r.NoError(env.dbSvc.CreateSLO(t.Context(), included))

	includedSeries := env.burndown(t, included.UID)
	r.Less(includedSeries.Data[0].BudgetRemainingSeconds, excludedSeries.Data[0].BudgetRemainingSeconds)
	r.Negative(includedSeries.Data[0].BudgetRemainingSeconds)
}

// A window with no results at all must report no data and an untouched budget —
// never a full-height line implying a perfect month. Driven through the HTTP
// route so the wiring is covered too.
func TestBurndownRouteWithNoResultsReportsNoData(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newBurndownEnv(t)
	objective := env.seedSLO(t, "quiet")

	cfg := &config.Config{}
	router := httpx.New()
	router.GET("/api/v1/orgs/:org/slos/:uid/burndown",
		slos.NewHandler(slos.NewService(env.dbSvc, cfg, nil), cfg).Burndown)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/orgs/acme/slos/"+objective.UID+"/burndown", nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var series slos.BurndownResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &series))

	r.NotEmpty(series.Data)
	r.Positive(series.BudgetTotalSeconds)

	for _, point := range series.Data {
		r.False(point.HasData)
		r.Nil(point.AttainmentPct, "no data must never be reported as an attainment")
		r.Equal(series.BudgetTotalSeconds, point.BudgetRemainingSeconds,
			"an unobserved window leaves the budget untouched, not spent")
	}
}
