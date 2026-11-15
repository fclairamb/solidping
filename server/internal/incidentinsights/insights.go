// Package incidentinsights aggregates incident rows into the numbers a team
// asks for after a month of on-call: which checks fail the most, how long it
// takes to acknowledge, how long incidents last. Everything here is a pure
// function over already-loaded incidents so that the insights endpoint and the
// uptime report share one definition.
package incidentinsights

import (
	"math"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// DurationStats is the median and p90 of a sample of durations. The durations
// are nil, not zero, when the sample is empty.
type DurationStats struct {
	MedianSeconds *int64 `json:"medianSeconds"`
	P90Seconds    *int64 `json:"p90Seconds"`
	SampleCount   int    `json:"sampleCount"`
}

// ResolutionStats is the DurationStats of one resolution bucket plus its count.
type ResolutionStats struct {
	Count         int    `json:"count"`
	MedianSeconds *int64 `json:"medianSeconds"`
	P90Seconds    *int64 `json:"p90Seconds"`
}

// ByResolution splits resolved incidents by how they were closed.
type ByResolution struct {
	Auto   ResolutionStats `json:"auto"`
	Manual ResolutionStats `json:"manual"`
	Other  ResolutionStats `json:"other"`
}

// Summary is the window-wide part of the insights.
type Summary struct {
	IncidentCount int `json:"incidentCount"`
	// PagedCount is the incidents somebody could have acknowledged: every one
	// except the rolled-up (paging-suppressed) ones. It is the ackRate base.
	PagedCount        int           `json:"pagedCount"`
	AcknowledgedCount int           `json:"acknowledgedCount"`
	AckRate           float64       `json:"ackRate"`
	TimeToAcknowledge DurationStats `json:"timeToAcknowledge"`
	TimeToResolve     DurationStats `json:"timeToResolve"`
	ByResolution      ByResolution  `json:"byResolution"`
}

// CheckRow aggregates the incidents of one check.
type CheckRow struct {
	CheckUID        string     `json:"checkUid"`
	IncidentCount   int        `json:"incidentCount"`
	DowntimeSeconds int64      `json:"downtimeSeconds"`
	RelapseCount    int        `json:"relapseCount"`
	LastIncidentAt  *time.Time `json:"lastIncidentAt"`
}

// Insights is the result of Compute.
type Insights struct {
	Summary Summary    `json:"summary"`
	Checks  []CheckRow `json:"checks"`
}

// Compute aggregates incidents over [windowStart, windowEnd). An open incident
// runs to now for downtime. Incidents entirely outside the window are ignored.
func Compute(incidents []*models.Incident, windowStart, windowEnd, now time.Time) Insights {
	var (
		ackSample     []int64
		resolveSample []int64
		autoSample    []int64
		manualSample  []int64
		otherSample   []int64
		summary       Summary
	)

	rows := map[string]*CheckRow{}

	for _, inc := range incidents {
		downtime, inWindow := clippedDowntime(inc, windowStart, windowEnd, now)
		if !inWindow {
			continue
		}

		summary.IncidentCount++

		row := rows[inc.CheckUID]
		if row == nil {
			row = &CheckRow{CheckUID: inc.CheckUID}
			rows[inc.CheckUID] = row
		}

		row.IncidentCount++
		row.DowntimeSeconds += downtime
		row.RelapseCount += inc.RelapseCount

		if row.LastIncidentAt == nil || inc.StartedAt.After(*row.LastIncidentAt) {
			startedAt := inc.StartedAt
			row.LastIncidentAt = &startedAt
		}

		if !inc.PagingSuppressed {
			summary.PagedCount++

			if inc.AcknowledgedAt != nil {
				summary.AcknowledgedCount++
				ackSample = append(ackSample, nonNegative(inc.AcknowledgedAt.Sub(onset(inc))))
			}
		}

		if inc.ResolvedAt == nil {
			continue
		}

		dur := nonNegative(inc.ResolvedAt.Sub(inc.StartedAt))

		switch resolutionBucket(inc) {
		case models.ResolutionTypeAuto:
			autoSample = append(autoSample, dur)
			resolveSample = append(resolveSample, dur)
		case models.ResolutionTypeManual:
			manualSample = append(manualSample, dur)
			resolveSample = append(resolveSample, dur)
		default:
			otherSample = append(otherSample, dur)
		}
	}

	if summary.PagedCount > 0 {
		summary.AckRate = float64(summary.AcknowledgedCount) / float64(summary.PagedCount)
	}

	summary.TimeToAcknowledge = stats(ackSample)
	summary.TimeToResolve = stats(resolveSample)
	summary.ByResolution = ByResolution{
		Auto:   resolutionStats(autoSample),
		Manual: resolutionStats(manualSample),
		Other:  resolutionStats(otherSample),
	}

	return Insights{Summary: summary, Checks: sortedRows(rows)}
}

// onset is the start of the incident's current episode: reopening clears the
// acknowledgement, so the surviving one belongs to the last episode (same rule
// as incidentClockFloor in the incidents service).
func onset(inc *models.Incident) time.Time {
	if inc.LastReopenedAt != nil && inc.LastReopenedAt.After(inc.StartedAt) {
		return *inc.LastReopenedAt
	}

	return inc.StartedAt
}

// resolutionBucket maps a resolution type to auto, manual or "other".
func resolutionBucket(inc *models.Incident) string {
	if inc.ResolutionType == nil {
		return "other"
	}

	switch *inc.ResolutionType {
	case models.ResolutionTypeAuto, models.ResolutionTypeManual:
		return *inc.ResolutionType
	default:
		return "other"
	}
}

// clippedDowntime is the incident's wall-clock outage clamped to the window,
// an open incident running to now. The bool is false when it does not overlap
// the window at all. (availability.IncidentBlock drops incidents that started
// before the window, which would lose the tail of one that straddles it.)
func clippedDowntime(inc *models.Incident, windowStart, windowEnd, now time.Time) (int64, bool) {
	start := inc.StartedAt
	end := now

	if inc.ResolvedAt != nil {
		end = *inc.ResolvedAt
	}

	if !start.Before(windowEnd) || end.Before(windowStart) {
		return 0, false
	}

	if start.Before(windowStart) {
		start = windowStart
	}

	if end.After(windowEnd) {
		end = windowEnd
	}

	return nonNegative(end.Sub(start)), true
}

func nonNegative(d time.Duration) int64 {
	if d < 0 {
		return 0
	}

	return int64(d.Seconds())
}

func sortedRows(rows map[string]*CheckRow) []CheckRow {
	out := make([]CheckRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].IncidentCount != out[j].IncidentCount {
			return out[i].IncidentCount > out[j].IncidentCount
		}

		if out[i].DowntimeSeconds != out[j].DowntimeSeconds {
			return out[i].DowntimeSeconds > out[j].DowntimeSeconds
		}

		return out[i].CheckUID < out[j].CheckUID
	})

	return out
}

func stats(sample []int64) DurationStats {
	median, p90 := percentiles(sample)

	return DurationStats{MedianSeconds: median, P90Seconds: p90, SampleCount: len(sample)}
}

func resolutionStats(sample []int64) ResolutionStats {
	median, p90 := percentiles(sample)

	return ResolutionStats{Count: len(sample), MedianSeconds: median, P90Seconds: p90}
}

func percentiles(sample []int64) (*int64, *int64) {
	if len(sample) == 0 {
		return nil, nil
	}

	sorted := append([]int64(nil), sample...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	median := nearestRank(sorted, 0.5)
	p90 := nearestRank(sorted, 0.9)

	return &median, &p90
}

// nearestRank returns the nearest-rank percentile of an ascending sample.
func nearestRank(sorted []int64, pct float64) int64 {
	rank := int(math.Ceil(pct * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}

	return sorted[rank-1]
}
