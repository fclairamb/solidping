package incidentinsights_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/incidentinsights"
)

//nolint:gochecknoglobals // fixed clock shared by the table tests
var now = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

//nolint:gochecknoglobals // fixed window shared by the table tests
var windowStart = now.Add(-30 * 24 * time.Hour)

func incident(checkUID string, started time.Time) *models.Incident {
	return &models.Incident{CheckUID: checkUID, StartedAt: started, State: models.IncidentStateActive}
}

func resolved(inc *models.Incident, after time.Duration, resolution string) *models.Incident {
	at := inc.StartedAt.Add(after)
	inc.ResolvedAt = &at
	inc.ResolutionType = &resolution
	inc.State = models.IncidentStateResolved

	return inc
}

func acked(inc *models.Incident, after time.Duration) *models.Incident {
	at := inc.StartedAt.Add(after)
	inc.AcknowledgedAt = &at

	return inc
}

func TestCompute_Percentiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		minutes []int
		median  int64
		p90     int64
	}{
		{"single", []int{5}, 300, 300},
		{"odd", []int{1, 2, 3}, 120, 180},
		{"even", []int{4, 1, 2, 3}, 150, 240},
		{"ten", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 330, 540},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			incs := make([]*models.Incident, 0, len(tt.minutes))
			for i, m := range tt.minutes {
				inc := incident("c1", now.Add(-time.Duration(i+2)*time.Hour))
				incs = append(incs, resolved(acked(inc, time.Duration(m)*time.Minute), time.Duration(m)*time.Minute, "auto"))
			}

			got := incidentinsights.Compute(incs, windowStart, now, now)
			r := require.New(t)
			r.Equal(tt.median, *got.Summary.TimeToAcknowledge.MedianSeconds)
			r.Equal(tt.p90, *got.Summary.TimeToAcknowledge.P90Seconds)
			r.Equal(len(tt.minutes), got.Summary.TimeToAcknowledge.SampleCount)
			r.Equal(tt.median, *got.Summary.TimeToResolve.MedianSeconds)
		})
	}
}

func TestCompute_Empty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	got := incidentinsights.Compute(nil, windowStart, now, now)

	r.Zero(got.Summary.IncidentCount)
	r.Zero(got.Summary.AckRate)
	r.Nil(got.Summary.TimeToAcknowledge.MedianSeconds)
	r.Nil(got.Summary.TimeToResolve.P90Seconds)
	r.Zero(got.Summary.TimeToResolve.SampleCount)
	r.Empty(got.Checks)
	r.Len(got.Summary.ByResolution, 3)
}

func TestCompute_NoAcknowledged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	incs := []*models.Incident{resolved(incident("c1", now.Add(-time.Hour)), time.Minute, "auto")}
	got := incidentinsights.Compute(incs, windowStart, now, now)

	r.Zero(got.Summary.AckRate)
	r.Zero(got.Summary.AcknowledgedCount)
	r.Zero(got.Summary.TimeToAcknowledge.SampleCount)
	r.Nil(got.Summary.TimeToAcknowledge.MedianSeconds)
	r.Equal(1, got.Summary.PagedCount)
}

func TestCompute_ReopenedAckMeasuredFromReopen(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	inc := incident("c1", now.Add(-10*time.Hour))
	reopened := now.Add(-2 * time.Hour)
	inc.LastReopenedAt = &reopened
	ack := reopened.Add(3 * time.Minute)
	inc.AcknowledgedAt = &ack

	got := incidentinsights.Compute([]*models.Incident{inc}, windowStart, now, now)

	r.Equal(int64(180), *got.Summary.TimeToAcknowledge.MedianSeconds)
	r.InDelta(1.0, got.Summary.AckRate, 0.0001)
}

func TestCompute_OpenIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	got := incidentinsights.Compute(
		[]*models.Incident{incident("c1", now.Add(-time.Hour))}, windowStart, now, now)

	r.Equal(1, got.Summary.IncidentCount)
	r.Zero(got.Summary.TimeToResolve.SampleCount)
	r.Nil(got.Summary.TimeToResolve.MedianSeconds)
	r.Equal(int64(3600), got.Checks[0].DowntimeSeconds)
}

func TestCompute_StartedBeforeWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	inc := resolved(incident("c1", windowStart.Add(-2*time.Hour)), 3*time.Hour, "auto")
	got := incidentinsights.Compute([]*models.Incident{inc}, windowStart, now, now)

	r.Equal(1, got.Checks[0].IncidentCount)
	r.Equal(int64(3600), got.Checks[0].DowntimeSeconds, "clipped to the window")
}

func TestCompute_ResolutionSplit(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	incs := []*models.Incident{
		resolved(incident("c1", now.Add(-5*time.Hour)), 10*time.Minute, models.ResolutionTypeAuto),
		resolved(incident("c1", now.Add(-4*time.Hour)), 30*time.Minute, models.ResolutionTypeManual),
		resolved(incident("c2", now.Add(-3*time.Hour)), 20*time.Hour, models.ResolutionTypeExpired),
		resolved(incident("c2", now.Add(-2*time.Hour)), 20*time.Hour, models.ResolutionTypeCheckDeleted),
	}
	got := incidentinsights.Compute(incs, windowStart, now, now).Summary

	r.Equal(2, got.TimeToResolve.SampleCount)
	r.Equal(int64(1200), *got.TimeToResolve.MedianSeconds)
	r.Equal(1, got.ByResolution["auto"].Count)
	r.Equal(1, got.ByResolution["manual"].Count)
	r.Equal(2, got.ByResolution["other"].Count)
	r.Equal(int64(600), *got.ByResolution["auto"].MedianSeconds)
}

func TestCompute_SuppressedLeftOutOfAck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	paged := acked(incident("c1", now.Add(-3*time.Hour)), time.Minute)
	rolled := acked(incident("c2", now.Add(-2*time.Hour)), time.Minute)
	rolled.PagingSuppressed = true

	got := incidentinsights.Compute([]*models.Incident{paged, rolled}, windowStart, now, now)

	r.Equal(2, got.Summary.IncidentCount)
	r.Equal(1, got.Summary.PagedCount)
	r.Equal(1, got.Summary.AcknowledgedCount)
	r.InDelta(1.0, got.Summary.AckRate, 0.0001)
	r.Len(got.Checks, 2)
}

func TestCompute_CheckRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	mk := func(check string, hoursAgo int, down time.Duration, relapses int) *models.Incident {
		inc := resolved(incident(check, now.Add(-time.Duration(hoursAgo)*time.Hour)), down, "auto")
		inc.RelapseCount = relapses

		return inc
	}
	incs := []*models.Incident{
		mk("b", 10, time.Minute, 1),
		mk("b", 5, time.Minute, 2),
		mk("a", 9, 5*time.Minute, 0),
		mk("a", 8, 5*time.Minute, 0),
		mk("c", 7, time.Hour, 4),
	}
	got := incidentinsights.Compute(incs, windowStart, now, now).Checks

	r.Len(got, 3)
	r.Equal("a", got[0].CheckUID, "tie on count broken by downtime")
	r.Equal(int64(600), got[0].DowntimeSeconds)
	r.Equal("b", got[1].CheckUID)
	r.Equal(3, got[1].RelapseCount)
	r.Equal(now.Add(-5*time.Hour), *got[1].LastIncidentAt)
	r.Equal("c", got[2].CheckUID)
	r.Equal(1, got[2].IncidentCount)
}
