package watchdog

import (
	"context"
	"fmt"
	"time"
)

// SubjectFleet is the stable subject of the instance-wide execution-rate
// anomaly. Instance-wide detectors need a constant subject: a fingerprint
// that churned between runs would re-notify forever and never resolve.
const SubjectFleet = "instance"

// fleetWindowRow is the single-row projection of the conditional-aggregation
// query: two counts pulled out of one scan.
type fleetWindowRow struct {
	Current  int `bun:"current"`
	Baseline int `bun:"baseline"`
}

// detectFleetCollapse compares the results produced in the last COMPLETED
// hour against the same hour one day earlier.
//
// This is the catch-all detector — it fires for stranding causes this spec did
// not imagine, including ones no region-level view would show. On 2026-08-24
// the rate fell from ~5,700/10min to ~1,300/10min the instant the rename
// landed, which is exactly the shape this catches.
//
// Same hour YESTERDAY, not the previous hour, because the previous hour is not
// a baseline: check traffic has a daily shape, and a legitimate quiet stretch
// (overnight, weekend) would page every single night.
func (s *Service) detectFleetCollapse(ctx context.Context, cfg *Config) ([]Anomaly, error) {
	now := s.now().UTC()

	// The CURRENT hour is deliberately excluded: it is still filling, so it
	// would read as a collapse for the first 59 minutes of every hour.
	currentEnd := now.Truncate(time.Hour)
	currentStart := currentEnd.Add(-time.Hour)
	baselineStart := currentStart.AddDate(0, 0, -1)
	baselineEnd := currentEnd.AddDate(0, 0, -1)

	row := &fleetWindowRow{}

	// One scan, two conditional sums — the run must stay cheap, so this is a
	// single grouped query rather than two round trips.
	err := s.db.DB().NewSelect().
		TableExpr("results").
		ColumnExpr("SUM(CASE WHEN period_start >= ? AND period_start < ? THEN 1 ELSE 0 END) AS current",
			currentStart, currentEnd).
		ColumnExpr("SUM(CASE WHEN period_start >= ? AND period_start < ? THEN 1 ELSE 0 END) AS baseline",
			baselineStart, baselineEnd).
		Where("period_type = ?", "raw").
		Where("((period_start >= ? AND period_start < ?) OR (period_start >= ? AND period_start < ?))",
			currentStart, currentEnd, baselineStart, baselineEnd).
		Scan(ctx, row)
	if err != nil {
		return nil, fmt.Errorf("count fleet results: %w", err)
	}

	anomaly, ok := fleetCollapseAnomaly(row, cfg, currentStart, currentEnd)
	if !ok {
		return nil, nil
	}

	return []Anomaly{anomaly}, nil
}

// fleetCollapseAnomaly applies the drop bar to one window comparison.
func fleetCollapseAnomaly(
	row *fleetWindowRow, cfg *Config, windowStart, windowEnd time.Time,
) (Anomaly, bool) {
	// The floor is what keeps a quiet or brand-new instance silent: without
	// it, 4 results against 10 is a "60% collapse".
	if row.Baseline < cfg.FleetMinBaseline {
		return Anomaly{}, false
	}

	dropPct := (float64(row.Baseline-row.Current) / float64(row.Baseline)) * 100
	if dropPct <= cfg.FleetDropPercent {
		return Anomaly{}, false
	}

	severity := SeverityWarning
	if dropPct >= cfg.FleetCriticalDropPercent {
		severity = SeverityCritical
	}

	return Anomaly{
		Detector: DetectorFleetCollapse,
		Subject:  SubjectFleet,
		Severity: severity,
		Headline: fmt.Sprintf(
			"fleet execution collapsed %.1f%%: %d results in %s–%s vs %d in the same hour a day earlier",
			dropPct,
			row.Current,
			windowStart.Format("15:04"),
			windowEnd.Format("15:04 MST"),
			row.Baseline,
		),
		Detail: fmt.Sprintf("window=%s baseline=%s lost=%d",
			windowStart.Format(time.RFC3339),
			windowStart.AddDate(0, 0, -1).Format(time.RFC3339),
			row.Baseline-row.Current),
		Remediation: "GET /api/v1/system/regions/health — a dark region is the usual cause; " +
			"otherwise check worker liveness and the job queue backlog",
		Count: row.Baseline - row.Current,
	}, true
}
