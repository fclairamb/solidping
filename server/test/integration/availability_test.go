package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// availabilityPeriod mirrors the availability handler's AvailabilityPeriod DTO.
// We decode the raw JSON rather than use the generated client because this
// endpoint isn't part of the hand-maintained pkg/client surface.
type availabilityPeriod struct {
	Period           string   `json:"period"`
	WindowStart      string   `json:"windowStart"`
	WindowEnd        string   `json:"windowEnd"`
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

type availabilityResponse struct {
	Data []availabilityPeriod `json:"data"`
}

const (
	availCheckUID = "30000000-0000-0000-0000-000000000001"
	availOrgUID   = "10000000-0000-0000-0000-000000000001" // matches the test org in testhelper
)

// seedAvailabilityData creates one check (created ~40 days ago) with a known mix
// of raw + rolled-up rows and a couple of incidents, so the per-period numbers
// are exactly predictable.
//
// Layout (all timestamps relative to now):
//   - Today window (since local midnight): raw rows, 3 up + 1 down → 75% over the
//     probes that landed today.
//   - 7d window: today's raw rows + an hour-tier rollup 2 days ago
//     (100 total / 90 up). Disjoint tiers, so they simply sum.
//   - 30d window: above + a day-tier rollup 10 days ago (1000 / 900).
//   - Incidents: one resolved 1h outage today; one resolved 2h outage 5 days ago.
func seedAvailabilityData(ctx context.Context, t *testing.T, ts *TestServer) {
	t.Helper()

	r := require.New(t)
	dbService := ts.Server.DBService()

	now := time.Now().UTC()

	name := "Avail Check"
	slug := "avail-check"
	check := &models.Check{
		UID:             availCheckUID,
		OrganizationUID: availOrgUID,
		Name:            &name,
		Slug:            &slug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://example.com"},
		Enabled:         true,
		CreatedAt:       now.Add(-40 * 24 * time.Hour),
		UpdatedAt:       now,
	}
	r.NoError(dbService.CreateCheck(ctx, check))

	region := testRegionUSEast1

	// Raw rows landing "today" (within the last few hours, so they're in every
	// window): 3 up + 1 down.
	rawStatuses := []struct {
		status models.ResultStatus
		ago    time.Duration
	}{
		{models.ResultStatusUp, 1 * time.Hour},
		{models.ResultStatusUp, 2 * time.Hour},
		{models.ResultStatusUp, 3 * time.Hour},
		{models.ResultStatusDown, 4 * time.Hour},
	}
	for i, rs := range rawStatuses {
		status := int(rs.status)
		r.NoError(dbService.CreateResult(ctx, &models.Result{
			UID:             newAvailUID(40 + i),
			OrganizationUID: availOrgUID,
			CheckUID:        availCheckUID,
			Region:          &region,
			PeriodType:      models.PeriodTypeRaw,
			PeriodStart:     now.Add(-rs.ago),
			Status:          &status,
			CreatedAt:       now,
		}))
	}

	// Hour-tier rollup 2 days ago: 100 checks, 90 up. In 7d/30d windows only.
	hourEnd := now.Add(-2*24*time.Hour + time.Hour)
	r.NoError(dbService.CreateResult(ctx, aggResult(
		newAvailUID(50), now.Add(-2*24*time.Hour), &hourEnd, models.PeriodTypeHour, 100, 90, region,
	)))

	// Day-tier rollup 10 days ago: 1000 checks, 900 up. In the 30d window only.
	dayEnd := now.Add(-10*24*time.Hour + 24*time.Hour)
	r.NoError(dbService.CreateResult(ctx, aggResult(
		newAvailUID(51), now.Add(-10*24*time.Hour), &dayEnd, models.PeriodTypeDay, 1000, 900, region,
	)))

	// Incident 1: resolved 1h outage today.
	incStart1 := now.Add(-5 * time.Hour)
	incEnd1 := now.Add(-4 * time.Hour)
	r.NoError(dbService.CreateIncident(ctx, &models.Incident{
		UID:             newAvailUID(60),
		Kind:            models.IncidentKindCheck,
		OrganizationUID: availOrgUID,
		CheckUID:        availCheckUID,
		State:           models.IncidentStateResolved,
		StartedAt:       incStart1,
		ResolvedAt:      &incEnd1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	// Incident 2: resolved 2h outage 5 days ago (in 7d/30d, not today).
	incStart2 := now.Add(-5 * 24 * time.Hour)
	incEnd2 := now.Add(-5*24*time.Hour + 2*time.Hour)
	r.NoError(dbService.CreateIncident(ctx, &models.Incident{
		UID:             newAvailUID(61),
		Kind:            models.IncidentKindCheck,
		OrganizationUID: availOrgUID,
		CheckUID:        availCheckUID,
		State:           models.IncidentStateResolved,
		StartedAt:       incStart2,
		ResolvedAt:      &incEnd2,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))
}

func aggResult(
	uid string, start time.Time, end *time.Time, periodType string,
	total, successful int, region string,
) *models.Result {
	reg := region

	return &models.Result{
		UID:              uid,
		OrganizationUID:  availOrgUID,
		CheckUID:         availCheckUID,
		Region:           &reg,
		PeriodType:       periodType,
		PeriodStart:      start,
		PeriodEnd:        end,
		TotalChecks:      &total,
		SuccessfulChecks: &successful,
		CreatedAt:        time.Now().UTC(),
	}
}

func newAvailUID(n int) string {
	// Deterministic, unique UUID-shaped strings in a private namespace.
	const base = "30000000-0000-0000-0000-0000000000"

	return base + string(rune('0'+(n/10))) + string(rune('0'+(n%10)))
}

// midDayTZ returns an IANA fixed-offset timezone in which the wall clock is
// currently around mid-day, so "today" started ~11-12 hours ago in that zone.
// The availability fixtures place raw rows and an incident 1-5 hours in the
// past and expect them inside "today" — resolved against UTC that breaks for
// any suite run within ~5h after UTC midnight. Note the Etc/GMT sign
// convention is inverted (Etc/GMT+5 means UTC−5).
func midDayTZ(now time.Time) string {
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

func getAvailability(
	t *testing.T, ts *TestServer, token, checkIdent, periods string,
) (int, *availabilityResponse) {
	t.Helper()

	r := require.New(t)

	path := "/api/v1/orgs/" + TestOrgSlug + "/checks/" + checkIdent + "/availability?periods=" + periods +
		"&tz=" + url.QueryEscape(midDayTZ(time.Now()))
	status, body := rawRequest(t, ts, http.MethodGet, path, token, "", nil)

	if status != http.StatusOK {
		return status, nil
	}

	var resp availabilityResponse
	r.NoError(json.Unmarshal(body, &resp))

	return status, &resp
}

func periodByToken(resp *availabilityResponse, token string) *availabilityPeriod {
	for i := range resp.Data {
		if resp.Data[i].Period == token {
			return &resp.Data[i]
		}
	}

	return nil
}

func TestAvailability_PerPeriodStats(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	seedAvailabilityData(ctx, t, ts)
	token := loginToken(t, ts, TestOrgSlug)

	status, resp := getAvailability(t, ts, token, "avail-check", "today,7d,30d,365d")
	r.Equal(http.StatusOK, status)
	r.Len(resp.Data, 4)

	// Today: only the 4 raw rows → 3 up / 4 total = 75%.
	today := periodByToken(resp, "today")
	r.NotNil(today)
	r.True(today.HasData)
	r.Equal(4, today.TotalChecks)
	r.Equal(3, today.SuccessfulChecks)
	r.NotNil(today.AvailabilityPct)
	r.InDelta(75.0, *today.AvailabilityPct, 0.001)
	// downtimeSeconds = (1 − 0.75) × monitoredSeconds.
	r.Equal(int64(float64(today.MonitoredSeconds)*0.25+0.5), today.DowntimeSeconds)
	// Today incident: a single 1h resolved outage.
	r.Equal(1, today.Incidents.Count)
	r.Equal(int64(3600), today.Incidents.TotalDowntimeSeconds)
	r.Equal(int64(3600), today.Incidents.LongestSeconds)

	// 7d: raw (3/4) + hour rollup (90/100) = 93 up / 104 total.
	d7 := periodByToken(resp, "7d")
	r.NotNil(d7)
	r.Equal(104, d7.TotalChecks)
	r.Equal(93, d7.SuccessfulChecks)
	r.NotNil(d7.AvailabilityPct)
	r.InDelta(93.0/104.0*100, *d7.AvailabilityPct, 0.001)
	// 7d incidents: today's 1h + the 2h one 5 days ago = 2 incidents, 3h total.
	r.Equal(2, d7.Incidents.Count)
	r.Equal(int64(3*3600), d7.Incidents.TotalDowntimeSeconds)
	r.Equal(int64(2*3600), d7.Incidents.LongestSeconds)
	r.Equal(int64(3*3600/2), d7.Incidents.AverageSeconds)
	// A 40-day-old check is older than 7d, so this window is fully monitored.
	r.False(d7.Partial)

	// 30d: raw (3/4) + hour (90/100) + day (900/1000) = 993 up / 1104 total.
	d30 := periodByToken(resp, "30d")
	r.NotNil(d30)
	r.Equal(1104, d30.TotalChecks)
	r.Equal(993, d30.SuccessfulChecks)
	r.InDelta(993.0/1104.0*100, *d30.AvailabilityPct, 0.001)

	// 365d: the check is younger than the window (40d old) → partial, and
	// monitoredSeconds caps at ~40 days, not 365.
	d365 := periodByToken(resp, "365d")
	r.NotNil(d365)
	r.True(d365.Partial)
	r.Less(d365.MonitoredSeconds, int64(365*24*3600))
	r.InDelta(float64(40*24*3600), float64(d365.MonitoredSeconds), float64(24*3600))
}

func TestAvailability_ByUID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	seedAvailabilityData(ctx, t, ts)
	token := loginToken(t, ts, TestOrgSlug)

	// Resolving by UID must match resolving by slug.
	status, resp := getAvailability(t, ts, token, availCheckUID, "today")
	r.Equal(http.StatusOK, status)
	r.Len(resp.Data, 1)
	r.Equal("today", resp.Data[0].Period)
	r.True(resp.Data[0].HasData)
}

func TestAvailability_NoDataPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	// A check with no results at all: availabilityPct must be null, hasData false.
	dbService := ts.Server.DBService()
	now := time.Now().UTC()
	name := "Empty Check"
	slug := "empty-check"
	r.NoError(dbService.CreateCheck(ctx, &models.Check{
		UID:             "30000000-0000-0000-0000-0000000000ff",
		OrganizationUID: availOrgUID,
		Name:            &name,
		Slug:            &slug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://example.com"},
		Enabled:         true,
		CreatedAt:       now.Add(-2 * time.Hour),
		UpdatedAt:       now,
	}))

	token := loginToken(t, ts, TestOrgSlug)

	status, resp := getAvailability(t, ts, token, "empty-check", "today,7d")
	r.Equal(http.StatusOK, status)
	r.Len(resp.Data, 2)
	for _, p := range resp.Data {
		r.False(p.HasData, "period %s should have no data", p.Period)
		r.Nil(p.AvailabilityPct)
		r.Zero(p.DowntimeSeconds)
		r.Equal(0, p.Incidents.Count)
	}
}

func TestAvailability_InvalidPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	seedAvailabilityData(ctx, t, ts)
	token := loginToken(t, ts, TestOrgSlug)

	// Unknown token → 400 VALIDATION_ERROR.
	path := "/api/v1/orgs/" + TestOrgSlug + "/checks/avail-check/availability?periods=7d,5x"
	status, body := rawRequest(t, ts, http.MethodGet, path, token, "", nil)
	r.Equal(http.StatusBadRequest, status)
	r.Contains(string(body), "VALIDATION_ERROR")
}

func TestAvailability_Unauthorized(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	seedAvailabilityData(ctx, t, ts)

	// No token → 401.
	path := "/api/v1/orgs/" + TestOrgSlug + "/checks/avail-check/availability?periods=today"
	status, _ := rawRequest(t, ts, http.MethodGet, path, "", "", nil)
	r.Equal(http.StatusUnauthorized, status)
}

func TestAvailability_CheckNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	seedAvailabilityData(ctx, t, ts)
	token := loginToken(t, ts, TestOrgSlug)

	status, _ := getAvailability(t, ts, token, "does-not-exist", "today")
	r.Equal(http.StatusNotFound, status)
}
