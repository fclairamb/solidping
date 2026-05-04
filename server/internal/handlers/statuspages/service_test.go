package statuspages

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func ptrFloat64(v float64) *float64 { return &v }
func ptrFloat32(v float32) *float32 { return &v }
func ptrInt(v int) *int             { return &v }

func TestSynthesizeTodayAvailability_ReplacesStoredTodayRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	checkUID := "check-1"

	staleAvail := 100.0
	staleToday := &models.Result{
		CheckUID:        checkUID,
		PeriodStart:     todayStart,
		AvailabilityPct: &staleAvail,
		TotalChecks:     ptrInt(60),
	}
	yesterdayAvail := 95.0
	yesterday := &models.Result{
		CheckUID:        checkUID,
		PeriodStart:     yesterdayStart,
		AvailabilityPct: &yesterdayAvail,
	}

	resultsByCheck := map[string][]*models.Result{
		checkUID: {staleToday, yesterday},
	}

	hourlyByCheck := map[string][]*models.Result{
		checkUID: {
			{CheckUID: checkUID, AvailabilityPct: ptrFloat64(100), Duration: ptrFloat32(50), DurationP95: ptrFloat32(60)},
			{CheckUID: checkUID, AvailabilityPct: ptrFloat64(50), Duration: ptrFloat32(70), DurationP95: ptrFloat32(80)},
		},
	}

	synthesizeTodayAvailability(resultsByCheck, hourlyByCheck, []string{checkUID}, todayStart)

	results := resultsByCheck[checkUID]
	r.Len(results, 2, "expected yesterday + synthesized today")

	var todayRow *models.Result
	for _, row := range results {
		if row.PeriodStart.Equal(todayStart) {
			todayRow = row
		}
	}
	r.NotNil(todayRow, "today row should exist")
	r.NotEqual(staleToday, todayRow, "today row should be the synthesized one")
	r.NotNil(todayRow.AvailabilityPct)
	r.InDelta(75.0, *todayRow.AvailabilityPct, 0.01, "synthesized avail should average hourly rows")
}

func TestSynthesizeTodayAvailability_NoHourlyKeepsStored(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	checkUID := "check-1"

	staleAvail := 100.0
	storedToday := &models.Result{
		CheckUID:        checkUID,
		PeriodStart:     todayStart,
		AvailabilityPct: &staleAvail,
	}

	resultsByCheck := map[string][]*models.Result{
		checkUID: {storedToday},
	}
	hourlyByCheck := map[string][]*models.Result{}

	synthesizeTodayAvailability(resultsByCheck, hourlyByCheck, []string{checkUID}, todayStart)

	r.Len(resultsByCheck[checkUID], 1)
	r.Equal(storedToday, resultsByCheck[checkUID][0], "stored today row should be preserved when no hourly synth")
}

func TestSynthesizeTodayAvailability_PastDaysUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	dayMinus3 := todayStart.AddDate(0, 0, -3)
	dayMinus10 := todayStart.AddDate(0, 0, -10)
	checkUID := "check-1"

	avail3 := 99.0
	avail10 := 88.0
	past3 := &models.Result{CheckUID: checkUID, PeriodStart: dayMinus3, AvailabilityPct: &avail3}
	past10 := &models.Result{CheckUID: checkUID, PeriodStart: dayMinus10, AvailabilityPct: &avail10}

	resultsByCheck := map[string][]*models.Result{
		checkUID: {past10, past3},
	}
	hourlyByCheck := map[string][]*models.Result{
		checkUID: {{CheckUID: checkUID, AvailabilityPct: ptrFloat64(100)}},
	}

	synthesizeTodayAvailability(resultsByCheck, hourlyByCheck, []string{checkUID}, todayStart)

	r.Len(resultsByCheck[checkUID], 3, "past days kept + today synthesized")
	r.Contains(resultsByCheck[checkUID], past3)
	r.Contains(resultsByCheck[checkUID], past10)
}

func TestBuildTodaySynth_AveragesHourlyAvail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	todayStart := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)

	hourly := []*models.Result{
		{AvailabilityPct: ptrFloat64(100), Duration: ptrFloat32(10), DurationP95: ptrFloat32(20)},
		{AvailabilityPct: ptrFloat64(80), Duration: ptrFloat32(30), DurationP95: ptrFloat32(40)},
	}

	synth := buildTodaySynth("check-1", hourly, todayStart)

	r.NotNil(synth)
	r.Equal("check-1", synth.CheckUID)
	r.True(synth.PeriodStart.Equal(todayStart))
	r.NotNil(synth.AvailabilityPct)
	r.InDelta(90.0, *synth.AvailabilityPct, 0.01)
	r.NotNil(synth.Duration)
	r.InDelta(float32(20), *synth.Duration, 0.01)
	r.NotNil(synth.DurationP95)
	r.InDelta(float32(30), *synth.DurationP95, 0.01)
	r.NotNil(synth.TotalChecks)
	r.Equal(2, *synth.TotalChecks)
}

func TestBuildTodaySynth_ReturnsNilOnEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	todayStart := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)

	r.Nil(buildTodaySynth("check-1", nil, todayStart))
	r.Nil(buildTodaySynth("check-1", []*models.Result{}, todayStart))
	r.Nil(buildTodaySynth("check-1", []*models.Result{{}}, todayStart), "no avail in any row → nil")
}
