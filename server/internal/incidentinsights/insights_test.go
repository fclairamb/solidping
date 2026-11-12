package incidentinsights_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/incidentinsights"
)

var (
	winStart = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	winEnd   = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	now      = winEnd
)

func at(minutes int) time.Time { return winStart.Add(time.Duration(minutes) * time.Minute) }

func tp(v time.Time) *time.Time { return &v }

func sp(v string) *string { return &v }

func inc(check string, startMin int) *models.Incident {
	return &models.Incident{UID: fmt.Sprintf("%s-%d", check, startMin), CheckUID: check, StartedAt: at(startMin)}
}

func resolved(i *models.Incident, endMin int, kind string) *models.Incident {
	i.ResolvedAt = tp(at(endMin))
	i.ResolutionType = sp(kind)

	return i
}

func acked(i *models.Incident, minute int) *models.Incident {
	i.AcknowledgedAt = tp(at(minute))

	return i
}

func TestPercentiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		minutes      []int
		median, p90  int64
		wantNoSample bool
	}{
		{name: "single", minutes: []int{5}, median: 300, p90: 300},
		{name: "odd", minutes: []int{1, 2, 3, 4, 5}, median: 180, p90: 300},
		{name: "even", minutes: []int{4, 1, 3, 2}, median: 120, p90: 240},
		{name: "empty", wantNoSample: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			var list []*models.Incident
			for i, m := range tc.minutes {
				list = append(list, acked(inc("c", i*100), i*100+m))
			}

			got := incidentinsights.Compute(list, winStart, winEnd, now).Summary.TimeToAcknowledge
			r.Equal(len(tc.minutes), got.SampleCount)

			if tc.wantNoSample {
				r.Nil(got.MedianSeconds)
				r.Nil(got.P90Seconds)

				return
			}

			r.Equal(tc.median, *got.MedianSeconds)
			r.Equal(tc.p90, *got.P90Seconds)
		})
	}
}

func TestCompute_EmptyInput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := incidentinsights.Compute(nil, winStart, winEnd, now)
	r.Zero(got.Summary.IncidentCount)
	r.Zero(got.Summary.AckRate)
	r.Nil(got.Summary.TimeToAcknowledge.MedianSeconds)
	r.Nil(got.Summary.TimeToResolve.MedianSeconds)
	r.Empty(got.Checks)
}

func TestCompute_NoAcknowledged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := incidentinsights.Compute(
		[]*models.Incident{resolved(inc("c", 0), 10, models.ResolutionTypeAuto)}, winStart, winEnd, now)
	r.Zero(got.Summary.AckRate)
	r.Zero(got.Summary.TimeToAcknowledge.SampleCount)
	r.Nil(got.Summary.TimeToAcknowledge.MedianSeconds)
	r.Equal(1, got.Summary.IncidentCount)
}

func TestCompute_ReopenedAckMeasuredFromReopen(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	i := acked(inc("c", 0), 130)
	i.LastReopenedAt = tp(at(120))

	got := incidentinsights.Compute([]*models.Incident{i}, winStart, winEnd, now)
	r.Equal(int64(600), *got.Summary.TimeToAcknowledge.MedianSeconds)
	r.InDelta(1.0, got.Summary.AckRate, 0)
}

func TestCompute_OpenIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	i := inc("c", 0)
	i.StartedAt = winEnd.Add(-time.Hour)

	got := incidentinsights.Compute([]*models.Incident{i}, winStart, winEnd, now)
	r.Equal(1, got.Summary.IncidentCount)
	r.Zero(got.Summary.TimeToResolve.SampleCount)
	r.Equal(int64(3600), got.Checks[0].DowntimeSeconds)
}

func TestCompute_ClippedToWindow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	i := inc("c", 0)
	i.StartedAt = winStart.Add(-time.Hour)
	resolved(i, 30, models.ResolutionTypeAuto)

	got := incidentinsights.Compute([]*models.Incident{i}, winStart, winEnd, now)
	r.Equal(int64(1800), got.Checks[0].DowntimeSeconds)
}

func TestCompute_ResolutionSplit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	list := []*models.Incident{
		resolved(inc("a", 0), 10, models.ResolutionTypeAuto),
		resolved(inc("a", 100), 130, models.ResolutionTypeManual),
		resolved(inc("a", 200), 1200, models.ResolutionTypeExpired),
		resolved(inc("a", 300), 1300, models.ResolutionTypeCheckDeleted),
	}

	got := incidentinsights.Compute(list, winStart, winEnd, now).Summary
	r.Equal(1, got.ByResolution.Auto.Count)
	r.Equal(1, got.ByResolution.Manual.Count)
	r.Equal(2, got.ByResolution.Other.Count)
	r.Equal(2, got.TimeToResolve.SampleCount)
	r.Equal(int64(600), *got.TimeToResolve.MedianSeconds)
}

func TestCompute_SuppressedLeftOutOfAck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rolled := resolved(inc("c", 0), 10, models.ResolutionTypeAuto)
	rolled.PagingSuppressed = true
	paged := acked(inc("c", 100), 105)

	got := incidentinsights.Compute([]*models.Incident{rolled, paged}, winStart, winEnd, now).Summary
	r.Equal(2, got.IncidentCount)
	r.Equal(1, got.PagedCount)
	r.InDelta(1.0, got.AckRate, 0)
	r.Equal(1, got.TimeToResolve.SampleCount)
}

func TestCompute_CheckRows(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	a1 := resolved(inc("a", 0), 10, models.ResolutionTypeAuto)
	a1.RelapseCount = 2
	a2 := resolved(inc("a", 100), 120, models.ResolutionTypeAuto)
	a2.RelapseCount = 1
	b1 := resolved(inc("b", 50), 250, models.ResolutionTypeAuto)
	b2 := resolved(inc("b", 300), 301, models.ResolutionTypeAuto)
	c1 := resolved(inc("c", 10), 11, models.ResolutionTypeAuto)

	got := incidentinsights.Compute([]*models.Incident{c1, a1, b1, a2, b2}, winStart, winEnd, now).Checks
	r.Len(got, 3)
	// a and b tie on 2 incidents: b has more downtime and ranks first.
	r.Equal("b", got[0].CheckUID)
	r.Equal(int64(200*60+60), got[0].DowntimeSeconds)
	r.Equal("a", got[1].CheckUID)
	r.Equal(3, got[1].RelapseCount)
	r.Equal(int64(30*60), got[1].DowntimeSeconds)
	r.Equal(at(100), *got[1].LastIncidentAt)
	r.Equal("c", got[2].CheckUID)
}
