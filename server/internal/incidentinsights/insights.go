// Package incidentinsights aggregates incident rows into the numbers a team
// asks for after a while on call: which checks fail the most, how long it
// takes to acknowledge an incident and how long incidents last. It is pure
// (no database access) so the incident insights endpoint and the uptime
// report share one definition of every number.
package incidentinsights

import (
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/availability"
)

// Resolution buckets of Summary.ByResolution.
const (
	ResolutionAuto   = "auto"
	ResolutionManual = "manual"
	ResolutionOther  = "other"
)

const percentile90 = 90

// Stats is a duration sample summary. The durations are nil, never zero, when
// the sample is empty.
type Stats struct {
	MedianSeconds *int64 `json:"medianSeconds"`
	P90Seconds    *int64 `json:"p90Seconds"`
	SampleCount   int    `json:"sampleCount"`
}

// ResolutionStats is a Stats plus the number of incidents in the bucket.
type ResolutionStats struct {
	Count         int    `json:"count"`
	MedianSeconds *int64 `json:"medianSeconds"`
	P90Seconds    *int64 `json:"p90Seconds"`
}

// Summary holds the org-wide numbers.
type Summary struct {
	IncidentCount int `json:"incidentCount"`
	// PagedCount is the incidents someone was paged for: every incident but the
	// rolled-up (paging-suppressed) ones. It is the denominator of AckRate.
	PagedCount        int     `json:"pagedCount"`
	AcknowledgedCount int     `json:"acknowledgedCount"`
	AckRate           float64 `json:"ackRate"`
	TimeToAcknowledge Stats   `json:"timeToAcknowledge"`
	// TimeToResolve covers auto and manual resolutions only: the other
	// closures (expired, check deleted...) say nothing about recovery.
	TimeToResolve Stats                      `json:"timeToResolve"`
	ByResolution  map[string]ResolutionStats `json:"byResolution"`
}

// CheckRow is the per-check line of the failing checks table.
type CheckRow struct {
	CheckUID        string     `json:"checkUid"`
	IncidentCount   int        `json:"incidentCount"`
	DowntimeSeconds int64      `json:"downtimeSeconds"`
	RelapseCount    int        `json:"relapseCount"`
	LastIncidentAt  *time.Time `json:"lastIncidentAt"`
}

// Insights is the result of Compute. Checks is sorted by incident count, then
// downtime, both descending, then check UID.
type Insights struct {
	Summary Summary    `json:"summary"`
	Checks  []CheckRow `json:"checks"`
}

// Compute aggregates the incidents over [windowStart, windowEnd). An open
// incident counts as down until now. It never reads acknowledged_by.
func Compute(incidents []*models.Incident, windowStart, windowEnd, now time.Time) Insights {
	var ackSample, resolveSample []int64

	byResolution := map[string][]int64{ResolutionAuto: nil, ResolutionManual: nil, ResolutionOther: nil}
	counts := map[string]int{}
	perCheck := map[string][]*models.Incident{}
	summary := Summary{IncidentCount: len(incidents)}

	for _, inc := range incidents {
		perCheck[inc.CheckUID] = append(perCheck[inc.CheckUID], clipToWindow(inc, windowStart))

		if !inc.PagingSuppressed {
			summary.PagedCount++

			if inc.AcknowledgedAt != nil {
				summary.AcknowledgedCount++
				ackSample = append(ackSample, seconds(inc.AcknowledgedAt.Sub(currentOnset(inc))))
			}
		}

		if inc.ResolvedAt == nil {
			continue
		}

		bucket := resolutionBucket(inc)
		counts[bucket]++

		duration := seconds(inc.ResolvedAt.Sub(inc.StartedAt))
		byResolution[bucket] = append(byResolution[bucket], duration)

		if bucket != ResolutionOther {
			resolveSample = append(resolveSample, duration)
		}
	}

	if summary.PagedCount > 0 {
		summary.AckRate = float64(summary.AcknowledgedCount) / float64(summary.PagedCount)
	}

	summary.TimeToAcknowledge = summarize(ackSample)
	summary.TimeToResolve = summarize(resolveSample)
	summary.ByResolution = make(map[string]ResolutionStats, len(byResolution))

	for bucket, sample := range byResolution {
		stats := summarize(sample)
		summary.ByResolution[bucket] = ResolutionStats{
			Count: counts[bucket], MedianSeconds: stats.MedianSeconds, P90Seconds: stats.P90Seconds,
		}
	}

	return Insights{Summary: summary, Checks: checkRows(perCheck, incidents, windowStart, windowEnd, now)}
}

// currentOnset is the start of the incident's current episode: reopening
// clears acknowledged_at, so a surviving ack belongs to the last episode.
func currentOnset(inc *models.Incident) time.Time {
	if inc.LastReopenedAt != nil && inc.LastReopenedAt.After(inc.StartedAt) {
		return *inc.LastReopenedAt
	}

	return inc.StartedAt
}

func resolutionBucket(inc *models.Incident) string {
	if inc.ResolutionType == nil {
		return ResolutionOther
	}

	switch *inc.ResolutionType {
	case models.ResolutionTypeAuto:
		return ResolutionAuto
	case models.ResolutionTypeManual:
		return ResolutionManual
	default:
		return ResolutionOther
	}
}

// clipToWindow returns a copy that starts no earlier than the window, because
// availability.IncidentBlock ignores incidents that began before it.
func clipToWindow(inc *models.Incident, windowStart time.Time) *models.Incident {
	if !inc.StartedAt.Before(windowStart) {
		return inc
	}

	clipped := *inc
	clipped.StartedAt = windowStart

	return &clipped
}

func checkRows(
	perCheck map[string][]*models.Incident, all []*models.Incident, windowStart, windowEnd, now time.Time,
) []CheckRow {
	relapses := map[string]int{}
	last := map[string]time.Time{}

	for _, inc := range all {
		relapses[inc.CheckUID] += inc.RelapseCount

		if inc.StartedAt.After(last[inc.CheckUID]) {
			last[inc.CheckUID] = inc.StartedAt
		}
	}

	rows := make([]CheckRow, 0, len(perCheck))

	for checkUID, incs := range perCheck {
		lastAt := last[checkUID]
		rows = append(rows, CheckRow{
			CheckUID:        checkUID,
			IncidentCount:   len(incs),
			DowntimeSeconds: availability.IncidentBlock(incs, windowStart, windowEnd, now).TotalDowntimeSeconds,
			RelapseCount:    relapses[checkUID],
			LastIncidentAt:  &lastAt,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].IncidentCount != rows[j].IncidentCount {
			return rows[i].IncidentCount > rows[j].IncidentCount
		}

		if rows[i].DowntimeSeconds != rows[j].DowntimeSeconds {
			return rows[i].DowntimeSeconds > rows[j].DowntimeSeconds
		}

		return rows[i].CheckUID < rows[j].CheckUID
	})

	return rows
}

func seconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}

	return int64(d.Seconds())
}

func summarize(sample []int64) Stats {
	if len(sample) == 0 {
		return Stats{}
	}

	sorted := append([]int64(nil), sample...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	mid := median(sorted)
	p90 := nearestRank(sorted, percentile90)

	return Stats{MedianSeconds: &mid, P90Seconds: &p90, SampleCount: len(sorted)}
}

// median is the middle value, or the mean of the two middle ones.
func median(sorted []int64) int64 {
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}

	return (sorted[mid-1] + sorted[mid]) / 2
}

// nearestRank is the value at rank ceil(p/100 * n) of the sorted sample.
func nearestRank(sorted []int64, percent int) int64 {
	rank := (percent*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}

	return sorted[rank-1]
}
