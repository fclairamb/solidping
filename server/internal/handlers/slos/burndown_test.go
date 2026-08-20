package slos_test

import (
	"encoding/json"
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

// newBurndownRouter wires the burn-down route over an in-memory database, and
// returns the org plus the check every fixture hangs off.
func newBurndownRouter(t *testing.T) (*httpx.Router, *sqlite.Service, *models.Organization, string) {
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
	// Created well before the window so nothing is clamped as "did not exist".
	check.CreatedAt = time.Now().AddDate(0, -6, 0)
	r.NoError(dbSvc.CreateCheck(ctx, check))

	cfg := &config.Config{}
	handler := slos.NewHandler(slos.NewService(dbSvc, cfg, nil), cfg)

	router := httpx.New()
	router.GET("/api/v1/orgs/:org/slos/:uid/burndown", handler.Burndown)

	return router, dbSvc, org, check.UID
}

// seedHourRollup writes an aggregated hour row inside the current month.
func seedHourRollup(t *testing.T, dbSvc *sqlite.Service, orgUID, checkUID string, at time.Time, total, success int) {
	t.Helper()

	end := at.Add(time.Hour)
	status := int(models.ResultStatusUp)

	require.NoError(t, dbSvc.CreateResult(t.Context(), &models.Result{
		UID:              uuid.Must(uuid.NewV7()).String(),
		OrganizationUID:  orgUID,
		CheckUID:         checkUID,
		PeriodType:       models.PeriodTypeHour,
		PeriodStart:      at,
		PeriodEnd:        &end,
		Status:           &status,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
		CreatedAt:        time.Now(),
	}))
}

func getBurndown(t *testing.T, router *httpx.Router, uid string) slos.BurndownResponse {
	t.Helper()

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/orgs/acme/slos/"+uid+"/burndown", nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var out slos.BurndownResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	return out
}

// TestBurndownIsCumulativeAndNeverGoesUp pins the defining property of a
// burn-down: each point re-evaluates the WHOLE window to date, so remaining
// budget is monotonically non-increasing. A per-step consumption chart would
// look similar and mean something else entirely.
func TestBurndownIsCumulativeAndNeverGoesUp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router, dbSvc, org, checkUID := newBurndownRouter(t)

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	// A failing hour early in the month, plus healthy hours after it.
	seedHourRollup(t, dbSvc, org.UID, checkUID, monthStart.Add(2*time.Hour), 60, 30)
	seedHourRollup(t, dbSvc, org.UID, checkUID, monthStart.Add(3*time.Hour), 60, 60)

	objective := models.NewSLO(org.UID, "API availability", "api-availability", 99.9)
	objective.CheckUID = &checkUID
	r.NoError(dbSvc.CreateSLO(t.Context(), objective))

	series := getBurndown(t, router, objective.UID)

	// Positive control: the series is non-empty and the budget is a real
	// number, so the monotonicity assertion below is not vacuous.
	r.NotEmpty(series.Data)
	r.Positive(series.BudgetTotalSeconds)
	r.InDelta(99.9, series.TargetPct, 0.0001)

	for i := 1; i < len(series.Data); i++ {
		r.LessOrEqual(series.Data[i].BudgetRemainingSeconds,
			series.Data[i-1].BudgetRemainingSeconds,
			"remaining budget must never increase within a window")
		r.True(series.Data[i].At.After(series.Data[i-1].At))
	}

	// The seeded outage really did consume budget.
	last := series.Data[len(series.Data)-1]
	r.True(last.HasData)
	r.NotNil(last.AttainmentPct)
	r.Less(last.BudgetRemainingSeconds, series.BudgetTotalSeconds)

	// The ideal line starts near the full budget and decays toward zero.
	r.LessOrEqual(series.Data[len(series.Data)-1].IdealRemainingSeconds,
		series.Data[0].IdealRemainingSeconds)
	r.GreaterOrEqual(series.Data[0].IdealRemainingSeconds, int64(0))
}

// A window with no results at all must produce points that report no data and
// an untouched budget — never a full-height line implying a perfect month.
func TestBurndownWithNoResultsReportsNoData(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router, dbSvc, org, checkUID := newBurndownRouter(t)

	objective := models.NewSLO(org.UID, "Quiet", "quiet", 99.9)
	objective.CheckUID = &checkUID
	r.NoError(dbSvc.CreateSLO(t.Context(), objective))

	series := getBurndown(t, router, objective.UID)

	r.NotEmpty(series.Data)
	r.Positive(series.BudgetTotalSeconds)

	for _, point := range series.Data {
		r.False(point.HasData)
		r.Nil(point.AttainmentPct, "no data must never be reported as an attainment")
		r.Equal(series.BudgetTotalSeconds, point.BudgetRemainingSeconds,
			"an unobserved window leaves the budget untouched, not spent")
	}
}
