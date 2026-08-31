package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// availabilityPeriod mirrors the availability handler's AvailabilityPeriod DTO.
type availabilityPeriod struct {
	Period           string   `json:"period"`
	MonitoredSeconds int64    `json:"monitoredSeconds"`
	Partial          bool     `json:"partial"`
	HasData          bool     `json:"hasData"`
	TotalChecks      int      `json:"totalChecks"`
	SuccessfulChecks int      `json:"successfulChecks"`
	AvailabilityPct  *float64 `json:"availabilityPct"`
	DowntimeSeconds  int64    `json:"downtimeSeconds"`
	Incidents        struct {
		Count                int   `json:"count"`
		LongestSeconds       int64 `json:"longestSeconds"`
		AverageSeconds       int64 `json:"averageSeconds"`
		TotalDowntimeSeconds int64 `json:"totalDowntimeSeconds"`
	} `json:"incidents"`
}

// TestAvailability_PostgresParity seeds the same fixture as the SQLite
// integration test (server/test/integration/availability_test.go) against a real
// Postgres-backed server and asserts the identical per-period probe-ratio and
// incident numbers — proving the endpoint behaves the same on both stores
// (it goes through Bun via db.Service, no raw SQL).
func TestAvailability_PostgresParity(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := NewPostgresScenario(t)
	ctx := t.Context()

	dbSvc := s.DBService()
	org, err := dbSvc.GetOrganizationBySlug(ctx, s.OrgSlug)
	r.NoError(err)
	orgUID := org.UID

	now := time.Now().UTC()

	// Check created 40 days ago.
	name := "Avail Check"
	slug := "avail-check"
	checkUID := randUID()
	r.NoError(dbSvc.CreateCheck(ctx, &models.Check{
		UID:             checkUID,
		OrganizationUID: orgUID,
		Name:            &name,
		Slug:            &slug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://example.com"},
		Enabled:         true,
		CreatedAt:       now.Add(-40 * 24 * time.Hour),
		UpdatedAt:       now,
	}))

	region := "us-east-1"

	// Raw rows today: 3 up + 1 down.
	raws := []struct {
		st  models.ResultStatus
		ago time.Duration
	}{
		{models.ResultStatusUp, 1 * time.Hour},
		{models.ResultStatusUp, 2 * time.Hour},
		{models.ResultStatusUp, 3 * time.Hour},
		{models.ResultStatusDown, 4 * time.Hour},
	}
	for _, rw := range raws {
		st := int(rw.st)
		r.NoError(dbSvc.CreateResult(ctx, &models.Result{
			UID:             randUID(),
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			Region:          &region,
			PeriodType:      models.PeriodTypeRaw,
			PeriodStart:     now.Add(-rw.ago),
			Status:          &st,
			CreatedAt:       now,
		}))
	}

	// Hour-tier rollup 2 days ago: 100 / 90.
	r.NoError(dbSvc.CreateResult(ctx, pgAgg(orgUID, checkUID, region, models.PeriodTypeHour,
		now.Add(-2*24*time.Hour), now.Add(-2*24*time.Hour+time.Hour), 100, 90)))

	// Day-tier rollup 10 days ago: 1000 / 900.
	r.NoError(dbSvc.CreateResult(ctx, pgAgg(orgUID, checkUID, region, models.PeriodTypeDay,
		now.Add(-10*24*time.Hour), now.Add(-10*24*time.Hour+24*time.Hour), 1000, 900)))

	// Incident today: 1h resolved.
	incEnd1 := now.Add(-4 * time.Hour)
	r.NoError(dbSvc.CreateIncident(ctx, &models.Incident{
		UID: randUID(), OrganizationUID: orgUID, CheckUID: checkUID,
		Kind:  models.IncidentKindCheck,
		State: models.IncidentStateResolved, StartedAt: now.Add(-5 * time.Hour),
		ResolvedAt: &incEnd1, CreatedAt: now, UpdatedAt: now,
	}))

	// Incident 5 days ago: 2h resolved.
	incEnd2 := now.Add(-5*24*time.Hour + 2*time.Hour)
	r.NoError(dbSvc.CreateIncident(ctx, &models.Incident{
		UID: randUID(), OrganizationUID: orgUID, CheckUID: checkUID,
		Kind:  models.IncidentKindCheck,
		State: models.IncidentStateResolved, StartedAt: now.Add(-5 * 24 * time.Hour),
		ResolvedAt: &incEnd2, CreatedAt: now, UpdatedAt: now,
	}))

	data := getPgAvailability(ctx, t, s, "avail-check", "today,7d,30d,365d")
	r.Len(data, 4)

	today := pgPeriod(data, "today")
	r.True(today.HasData)
	r.Equal(4, today.TotalChecks)
	r.Equal(3, today.SuccessfulChecks)
	r.InDelta(75.0, *today.AvailabilityPct, 0.001)
	r.Equal(1, today.Incidents.Count)
	r.Equal(int64(3600), today.Incidents.TotalDowntimeSeconds)

	d7 := pgPeriod(data, "7d")
	r.Equal(104, d7.TotalChecks)
	r.Equal(93, d7.SuccessfulChecks)
	r.InDelta(93.0/104.0*100, *d7.AvailabilityPct, 0.001)
	r.Equal(2, d7.Incidents.Count)
	r.Equal(int64(3*3600), d7.Incidents.TotalDowntimeSeconds)

	d30 := pgPeriod(data, "30d")
	r.Equal(1104, d30.TotalChecks)
	r.Equal(993, d30.SuccessfulChecks)

	d365 := pgPeriod(data, "365d")
	r.True(d365.Partial)
	r.Less(d365.MonitoredSeconds, int64(365*24*3600))
}

func pgAgg(
	orgUID, checkUID, region, periodType string,
	start, end time.Time, total, successful int,
) *models.Result {
	reg := region
	e := end

	return &models.Result{
		UID:              randUID(),
		OrganizationUID:  orgUID,
		CheckUID:         checkUID,
		Region:           &reg,
		PeriodType:       periodType,
		PeriodStart:      start,
		PeriodEnd:        &e,
		TotalChecks:      &total,
		SuccessfulChecks: &successful,
		CreatedAt:        time.Now().UTC(),
	}
}

// pgMidDayTZ returns an IANA fixed-offset timezone in which the wall clock is
// currently around mid-day, so "today" started ~11-12 hours ago in that zone.
// The fixture places raw rows and an incident 1-5 hours in the past and
// expects them inside "today" — resolved against UTC that breaks for any
// suite run within ~5h after UTC midnight. Note the Etc/GMT sign convention
// is inverted (Etc/GMT+5 means UTC−5).
func pgMidDayTZ(now time.Time) string {
	offset := 12 - now.UTC().Hour()

	switch {
	case offset == 0:
		return "UTC"
	case offset > 0:
		return fmt.Sprintf("Etc/GMT-%d", offset)
	default:
		return fmt.Sprintf("Etc/GMT+%d", -offset)
	}
}

func getPgAvailability(
	ctx context.Context, t *testing.T, s *Scenario, checkIdent, periods string,
) []availabilityPeriod {
	t.Helper()

	r := require.New(t)

	url := s.BaseURL + "/api/v1/orgs/" + s.OrgSlug + "/checks/" + checkIdent +
		"/availability?periods=" + periods + "&tz=" + neturl.QueryEscape(pgMidDayTZ(time.Now()))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer "+s.Token)

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	var out struct {
		Data []availabilityPeriod `json:"data"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&out))

	return out.Data
}

func pgPeriod(data []availabilityPeriod, token string) *availabilityPeriod {
	for i := range data {
		if data[i].Period == token {
			return &data[i]
		}
	}

	return nil
}
