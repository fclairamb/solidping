// Package watchdog is the platform's self-monitoring detector set (spec
// 2026-08-24-10).
//
// The worst failure mode of a monitoring product is going blind, and that is
// exactly the failure that produces zero signal: a check that stops being
// executed keeps its last state forever, so the dashboard shows green rows
// with quietly aging timestamps. Check-level alerting (incidents → escalation
// policies) cannot report on itself. This package is the channel that can.
//
// It holds only the parts that are testable without a job runner: the
// detectors, the anti-flood transition state machine, and the digest
// renderer. Scheduling and delivery live in the `platform_watchdog` job
// (internal/jobs/jobtypes/job_platform_watchdog.go).
package watchdog

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// Detector names. They are the stable half of an anomaly fingerprint and of
// the per-detector metric label, so they must never be reworded casually — a
// rename resets every operator's anti-flood state to "new".
const (
	// DetectorDarkRegion reports regions whose assigned work nothing live can
	// claim (the 2026-08-24 stranding).
	DetectorDarkRegion = "dark-region"
	// DetectorFleetCollapse reports an instance-wide drop in produced results
	// against the same hour a day earlier — the catch-all for stranding
	// causes this spec did not imagine.
	DetectorFleetCollapse = "fleet-collapse"
	// DetectorStaleIncidents reports active incidents whose check has stopped
	// producing results (the "frozen incident" symptom).
	DetectorStaleIncidents = "stale-incidents"
)

// Severity orders the three levels an anomaly can carry. Ordered so
// minSeverity filtering is a plain comparison.
type Severity int

// Severity tokens, as they appear in the system parameter and the digest.
const (
	// SeverityTokenInfo is the "info" token.
	SeverityTokenInfo = "info"
	// SeverityTokenWarning is the "warning" token, and the default bar.
	SeverityTokenWarning = "warning"
	// SeverityTokenCritical is the "critical" token.
	SeverityTokenCritical = "critical"
)

// Severity levels, ascending.
const (
	// SeverityInfo is informational — never pages, always logged.
	SeverityInfo Severity = iota
	// SeverityWarning is the default bar: something is wrong but bounded.
	SeverityWarning
	// SeverityCritical is blast-radius territory: hundreds of stranded jobs,
	// a halved fleet, a wall of frozen incidents.
	SeverityCritical
)

// String renders the severity as the lowercase token used in the system
// parameter and in the digest.
func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return SeverityTokenInfo
	case SeverityWarning:
		return SeverityTokenWarning
	case SeverityCritical:
		return SeverityTokenCritical
	default:
		return SeverityTokenWarning
	}
}

// ParseSeverity maps a configured token to a Severity. Anything unrecognized
// (including the empty string) falls back to warning — the documented default
// bar, never "off".
func ParseSeverity(value string) Severity {
	switch value {
	case SeverityTokenInfo:
		return SeverityInfo
	case SeverityTokenCritical:
		return SeverityCritical
	case SeverityTokenWarning:
		return SeverityWarning
	default:
		return SeverityWarning
	}
}

// Anomaly is one thing the platform found wrong with itself.
type Anomaly struct {
	// Detector is one of the Detector* constants.
	Detector string
	// Subject identifies WHAT is broken within the detector's domain — a
	// region slug, or the sentinel "fleet"/"global" for instance-wide
	// detectors. Together with Detector it forms the fingerprint, so it must
	// be stable across runs for the same underlying condition: an anomaly
	// whose subject churns would re-notify forever.
	Subject  string
	Severity Severity
	// Headline is the one-line summary carrying the numbers that matter.
	Headline string
	// Detail is optional extra context (the three oldest frozen incidents,
	// the last-worker-seen timestamp, …).
	Detail string
	// Remediation is the ready-to-run fix, when there is one.
	Remediation string
	// Count is the detector's headline magnitude — overdue jobs for a dark
	// region, frozen incidents for stale-incidents, lost results for a fleet
	// collapse. Kept as a first-class field rather than re-parsed out of the
	// rendered headline so the gauges and the digest read the same number.
	Count int
}

// Fingerprint is the anti-flood identity of an anomaly: `<detector>:<subject>`.
func (a *Anomaly) Fingerprint() string {
	return a.Detector + ":" + a.Subject
}

// Report is the outcome of one watchdog evaluation.
type Report struct {
	GeneratedAt time.Time
	Anomalies   []Anomaly
	// Failed maps a detector name to the error it returned. A detector that
	// errored is explicitly NOT reported as "clean": its fingerprints are
	// excluded from resolution reconciliation, so a transient query failure
	// can never be laundered into a false recovery notice.
	Failed map[string]error
	// StrandedJobs is the total overdue-job count across every dark region,
	// exported as its own gauge because it is the number that told the story
	// on 2026-08-24.
	StrandedJobs int
	// StaleIncidents is the count of frozen active incidents.
	StaleIncidents int
}

// HasFailures reports whether any detector errored this run.
func (r *Report) HasFailures() bool {
	return len(r.Failed) > 0
}

// DetectorSucceeded reports whether a given detector completed without error.
func (r *Report) DetectorSucceeded(detector string) bool {
	_, failed := r.Failed[detector]

	return !failed
}

// Filtered returns a copy of the report keeping only the anomalies at or
// above minSeverity, with the detector-failure map (and therefore the
// resolution-reconciliation guard) carried over untouched.
//
// The anti-flood ledger tracks the FILTERED set on purpose: a marker written
// for an anomaly the digest never mentions would record a notification that
// never happened, and would then suppress the real one when the anomaly
// escalates past the bar.
func (r *Report) Filtered(minSeverity Severity) *Report {
	out := *r
	out.Anomalies = Filter(r.Anomalies, minSeverity)

	return &out
}

// RegionHealthReporter is the spec-09 ghost-detection function, injected as an
// interface for one reason only: tests need to make it fail on demand (to
// prove detector independence) without a broken database. Production always
// passes the real *checks.Service, so "dark" has exactly one definition —
// checks.Service.RegionHealth — and this package never re-derives it.
type RegionHealthReporter interface {
	RegionHealth(ctx context.Context) (*checks.RegionHealthReport, error)
}

// Service evaluates the platform's own vitals.
type Service struct {
	db           db.Service
	regionHealth RegionHealthReporter
	// now is the injectable clock every detector and the transition state
	// machine read time through. Wall-clock arithmetic in a detector whose
	// window is "the last completed hour" would make its tests fail during
	// one particular minute of the day; this makes them deterministic.
	now func() time.Time
}

// NewService builds a watchdog service. regionHealth may be nil, in which case
// the dark-region detector reports a failure rather than silently skipping.
func NewService(dbService db.Service, regionHealth RegionHealthReporter) *Service {
	return &Service{
		db:           dbService,
		regionHealth: regionHealth,
		now:          time.Now,
	}
}

// SetNow overrides the service clock. Exported because the watchdog's windows
// ("the last completed hour", "the same hour yesterday", "re-notify after
// 24h") are only testable against a pinned clock — the same reason
// checks.Service carries one for RegionHealth.
func (s *Service) SetNow(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Now returns the service's current time.
func (s *Service) Now() time.Time {
	return s.now()
}

// Evaluate runs every detector and collects their anomalies.
//
// Detector independence is the contract: each runs in its own call, and a
// detector that returns an error contributes to Report.Failed while the
// others still produce their anomalies. A watchdog that goes silent because
// one query broke would reproduce the exact failure mode it exists to catch.
func (s *Service) Evaluate(ctx context.Context, cfg *Config) *Report {
	report := &Report{
		GeneratedAt: s.now(),
		Failed:      make(map[string]error),
	}

	type detectorFn struct {
		name string
		run  func(context.Context, *Config) ([]Anomaly, error)
	}

	for _, detector := range []detectorFn{
		{DetectorDarkRegion, s.detectDarkRegions},
		{DetectorFleetCollapse, s.detectFleetCollapse},
		{DetectorStaleIncidents, s.detectStaleIncidents},
	} {
		anomalies, err := s.runDetector(ctx, cfg, detector.run)
		if err != nil {
			report.Failed[detector.name] = err

			continue
		}

		report.Anomalies = append(report.Anomalies, anomalies...)
	}

	report.StrandedJobs = countFor(report.Anomalies, DetectorDarkRegion)
	report.StaleIncidents = countFor(report.Anomalies, DetectorStaleIncidents)

	return report
}

// runDetector isolates one detector, turning a panic into an error so a
// detector bug cannot take the whole watchdog — and, with it, the platform's
// only self-report — down with it.
func (s *Service) runDetector(
	ctx context.Context, cfg *Config, run func(context.Context, *Config) ([]Anomaly, error),
) ([]Anomaly, error) {
	var (
		anomalies []Anomaly
		err       error
	)

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				anomalies, err = nil, errDetectorPanicked(recovered)
			}
		}()

		anomalies, err = run(ctx, cfg)
	}()

	if err != nil {
		return nil, err
	}

	return anomalies, nil
}

// Filter returns the anomalies at or above minSeverity. Filtering applies to
// DELIVERY only — every anomaly is logged and metered regardless, because the
// out-of-band Prometheus path must see everything.
func Filter(anomalies []Anomaly, minSeverity Severity) []Anomaly {
	out := make([]Anomaly, 0, len(anomalies))

	for i := range anomalies {
		if anomalies[i].Severity >= minSeverity {
			out = append(out, anomalies[i])
		}
	}

	return out
}
