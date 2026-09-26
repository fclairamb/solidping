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
// executed. It also returns the RegionHealth report it evaluated, so the
// stale-checks detector can tell a check stale in a dark region apart.
//
// solidping_workers_active is NOT published from here any more: the
// per-minute region sweep (spec 2026-09-25-03, internal/regionsweep) owns it,
// runs whatever the watchdog config says, and is its only writer.
//
// It does NOT re-derive "dark": it calls checks.Service.RegionHealth — the
// spec-09 ghost detector — and applies a blast-radius bar on top of its rows.
// One definition of dark, one query set, one place to fix when the scheduler's
// matching rule changes.
//
// Private regions are org-relative (spec 2026-09-25-01): RegionHealth reports
// one row per (organization, slug), and each becomes its own anomaly with its
// own org-qualified Subject, so two orgs' `@paris` never share a fingerprint.
func (s *Service) detectDarkRegions(ctx context.Context, cfg *Config) ([]Anomaly, *checks.RegionHealthReport, error) {
	if s.regionHealth == nil {
		return nil, nil, ErrRegionHealthUnavailable
	}

	report, err := s.regionHealth.RegionHealth(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("region health: %w", err)
	}

	if report == nil {
		return nil, nil, ErrRegionHealthUnavailable
	}

	// The age bar is measured against the REPORT's own instant, not the
	// watchdog's clock. JobsOverdue and OldestOverdueAt were computed by
	// RegionHealth against its clock, so comparing them to a different `now`
	// would silently mix two time bases — the kind of skew that turns a
	// threshold test into a coin flip.
	now := report.GeneratedAt

	rows := append([]checks.RegionHealthRow(nil), report.Regions...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Slug != rows[j].Slug {
			return rows[i].Slug < rows[j].Slug
		}

		return rows[i].Organization < rows[j].Organization
	})

	anomalies := make([]Anomaly, 0, len(rows))
	seen := make(map[string]bool, len(rows))

	for i := range rows {
		// RegionHealth already yields one row per (organization, slug); this
		// guard only keeps a duplicate row from ever minting two anomalies
		// under one fingerprint.
		subject := darkRegionSubject(&rows[i])
		if seen[subject] {
			continue
		}

		seen[subject] = true

		if anomaly, ok := darkRegionAnomaly(&rows[i], cfg, now); ok {
			anomalies = append(anomalies, anomaly)
		}
	}

	return anomalies, report, nil
}

// darkRegionSubject is the anomaly Subject (and so the fingerprint) of one
// region row: the bare slug for a cloud region, `<org>/@<slug>` for a private
// one — private slugs are org-relative, so the slug alone would make two orgs'
// `@paris` share one anomaly and one anti-flood marker.
func darkRegionSubject(row *checks.RegionHealthRow) string {
	if row.Organization == "" {
		return row.Slug
	}

	return row.Organization + "/" + row.Slug
}

// darkRegionName is how a region reads in a headline: the quoted slug, plus
// the owning org for a private region.
func darkRegionName(row *checks.RegionHealthRow) string {
	if row.Organization == "" {
		return strconv.Quote(row.Slug)
	}

	return fmt.Sprintf("%q of org %q", row.Slug, row.Organization)
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
		Subject:     darkRegionSubject(row),
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
		"region %s is %s: %d job(s) assigned, %d overdue, oldest overdue by %s",
		darkRegionName(row), state, row.Jobs, row.JobsOverdue, roundDuration(age),
	)
}

// darkRegionDetail dates when the region went dark — the first thing an
// operator wants during triage — and how many checks are pointed at it.
func darkRegionDetail(row *checks.RegionHealthRow) string {
	parts := make([]string, 0, 5)

	if row.Organization != "" {
		parts = append(parts, "organization="+row.Organization)
	}

	parts = append(parts,
		fmt.Sprintf("liveWorkers=%d", row.LiveWorkers),
		fmt.Sprintf("checksReferencing=%d", row.ChecksReferencing),
		"declared="+strconv.FormatBool(row.Declared),
	)

	if row.LastWorkerSeenAt != nil {
		parts = append(parts, "lastWorkerSeenAt="+row.LastWorkerSeenAt.UTC().Format(time.RFC3339))
	} else {
		parts = append(parts, "lastWorkerSeenAt=never")
	}

	return strings.Join(parts, " ")
}

// darkRegionRemediation is the ready-to-run fix: the region-migration call
// from spec 2026-08-24-08 for a genuinely dark cloud region, the org's agent
// listing for a dark private region (only that org's agent can serve it), and
// the ghost listing from spec 2026-08-24-09 otherwise.
func darkRegionRemediation(row *checks.RegionHealthRow, dark bool) string {
	if !dark {
		return "GET /api/v1/system/regions/health — inspect why the backlog is not draining"
	}

	if row.Organization != "" {
		return fmt.Sprintf(
			"reconnect or re-enroll the agent serving %s in org %q — "+
				"GET /api/v1/orgs/%s/agents lists its agents and when each was last seen",
			row.Slug, row.Organization, row.Organization,
		)
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

	for i := range anomalies {
		if anomalies[i].Detector == detector {
			total += anomalies[i].Count
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
