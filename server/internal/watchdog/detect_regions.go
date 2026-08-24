package watchdog

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// detectDarkRegions reports every region whose assigned work is not being
// executed.
//
// It does NOT re-derive "dark": it calls checks.Service.RegionHealth — the
// spec-09 ghost detector — and applies a blast-radius bar on top of its rows.
// One definition of dark, one query set, one place to fix when the scheduler's
// matching rule changes.
func (s *Service) detectDarkRegions(ctx context.Context, cfg *Config) ([]Anomaly, error) {
	if s.regionHealth == nil {
		return nil, ErrRegionHealthUnavailable
	}

	report, err := s.regionHealth.RegionHealth(ctx)
	if err != nil {
		return nil, fmt.Errorf("region health: %w", err)
	}

	if report == nil {
		return nil, ErrRegionHealthUnavailable
	}

	now := s.now()

	rows := append([]checks.RegionHealthRow(nil), report.Regions...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Slug < rows[j].Slug })

	anomalies := make([]Anomaly, 0, len(rows))

	for i := range rows {
		if anomaly, ok := darkRegionAnomaly(&rows[i], cfg, now); ok {
			anomalies = append(anomalies, anomaly)
		}
	}

	return anomalies, nil
}

// darkRegionAnomaly applies the blast-radius bar to one region row.
//
// The bar exists because "overdue" alone is noise: a job 90 seconds late is
// the scheduler breathing. The reported condition is a backlog that is both
// WIDE (>= minOverdueJobs jobs) and OLD (oldest overdue >= minOverdueAge) —
// the shape of a stranding, not of a busy minute.
func darkRegionAnomaly(row *checks.RegionHealthRow, cfg *Config, now time.Time) (Anomaly, bool) {
	if row.JobsOverdue < cfg.DarkRegionMinOverdueJobs || row.OldestOverdueAt == nil {
		return Anomaly{}, false
	}

	age := now.Sub(*row.OldestOverdueAt)
	if age < cfg.DarkRegionMinOverdueAge() {
		return Anomaly{}, false
	}

	// LiveWorkers == 0 with work assigned is the genuinely dark case (spec
	// 2026-08-24-09's Ghost). A backlog served by live workers is still worth
	// reporting — something is stuck — but it is never critical on its own,
	// because the region is at least reachable.
	dark := row.LiveWorkers == 0 && row.Jobs > 0

	severity := SeverityWarning
	if dark && (row.JobsOverdue >= cfg.DarkRegionCriticalJobs || age >= cfg.DarkRegionCriticalAge()) {
		severity = SeverityCritical
	}

	return Anomaly{
		Detector:    DetectorDarkRegion,
		Subject:     row.Slug,
		Severity:    severity,
		Headline:    darkRegionHeadline(row, dark, age),
		Detail:      darkRegionDetail(row),
		Remediation: darkRegionRemediation(row, dark),
		Count:       row.JobsOverdue,
	}, true
}

// darkRegionHeadline carries the numbers an operator triages on.
func darkRegionHeadline(row *checks.RegionHealthRow, dark bool, age time.Duration) string {
	state := "backlogged"
	if dark {
		state = "DARK (no live worker)"
	}

	return fmt.Sprintf(
		"region %q is %s: %d job(s) assigned, %d overdue, oldest overdue by %s",
		row.Slug, state, row.Jobs, row.JobsOverdue, roundDuration(age),
	)
}

// darkRegionDetail dates when the region went dark — the first thing an
// operator wants during triage — and how many checks are pointed at it.
func darkRegionDetail(row *checks.RegionHealthRow) string {
	parts := []string{
		fmt.Sprintf("liveWorkers=%d", row.LiveWorkers),
		fmt.Sprintf("checksReferencing=%d", row.ChecksReferencing),
		"declared=" + strconv.FormatBool(row.Declared),
	}

	if row.LastWorkerSeenAt != nil {
		parts = append(parts, "lastWorkerSeenAt="+row.LastWorkerSeenAt.UTC().Format(time.RFC3339))
	} else {
		parts = append(parts, "lastWorkerSeenAt=never")
	}

	return strings.Join(parts, " ")
}

// darkRegionRemediation is the ready-to-run fix: the region-migration call
// from spec 2026-08-24-08 for a genuinely dark region, and the ghost listing
// from spec 2026-08-24-09 otherwise.
func darkRegionRemediation(row *checks.RegionHealthRow, dark bool) string {
	if !dark {
		return "GET /api/v1/system/regions/health — inspect why the backlog is not draining"
	}

	return fmt.Sprintf(
		"POST /api/v1/system/regions/migrate {\"from\":%q,\"to\":\"<live-region>\"} "+
			"— or GET /api/v1/system/regions/health to list every ghost first",
		row.Slug,
	)
}

// countFor sums the Count of every anomaly produced by one detector. It is
// what feeds the per-detector gauges: "419 stranded jobs" is the single
// number that told the story on 2026-08-24.
func countFor(anomalies []Anomaly, detector string) int {
	total := 0

	for _, anomaly := range anomalies {
		if anomaly.Detector == detector {
			total += anomaly.Count
		}
	}

	return total
}

// roundDuration renders a duration at a granularity a human reads at a glance.
func roundDuration(d time.Duration) time.Duration {
	if d >= time.Hour {
		return d.Round(time.Minute)
	}

	return d.Round(time.Second)
}
