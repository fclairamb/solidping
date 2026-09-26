// Package regionsweep is the per-minute region outage sweep (spec
// 2026-09-25-03).
//
// On 2026-09-24 a region lost its only worker and 12 checks stopped running
// for 8 hours. The only thing that could have noticed was the hourly platform
// watchdog, which was off, is operator-only, and ignores a region with fewer
// than 5 overdue jobs. This sweep runs every minute on the jobs node, calls
// checks.Service.RegionHealth (the single definition of "dark") and nothing
// else, and turns what it sees into transitions:
//
//   - the operator hears about a dark or stalled cloud region, and its
//     recovery, at the transition rather than at the next digest;
//   - every org with a check that no longer runs anywhere gets one notice when
//     the region goes dark and one when it comes back;
//   - solidping_workers_active and solidping_region_dark are published on
//     every run, whatever the watchdog config says.
//
// Private (`@`) regions are out of scope: the org owns that agent.
package regionsweep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// Recovery and stall thresholds.
const (
	// RecoveryStreak is how many consecutive healthy sweeps close an outage.
	// One would let a single worker beat in the middle of a crash loop
	// announce a recovery.
	RecoveryStreak = 2
	// StallFloor is the floor of the per-job lateness bar max(2 × period,
	// StallFloor): a job this late in a region with live workers means they
	// are alive but not claiming.
	StallFloor = 5 * time.Minute
	// stallPeriodMultiplier is the N of max(N × period, StallFloor).
	stallPeriodMultiplier = 2
)

// ErrRegionHealthUnavailable is returned when the sweep has no region health
// reporter to call.
var ErrRegionHealthUnavailable = errors.New("region health reporter is not wired")

// Reporter is the region health report plus the jobs it was aggregated from.
// Production passes the real *checks.Service; tests can make it fail.
type Reporter interface {
	RegionHealthWithJobs(ctx context.Context) (*checks.RegionHealthReport, []checks.RegionJob, error)
}

// Placer moves automatically placed checks off a dark region (spec
// 2026-09-25-06 A2). Production passes the same *checks.Service as Health.
type Placer interface {
	ReplaceAutoChecks(ctx context.Context, req checks.ReplacementRequest) ([]checks.PlacementChange, error)
}

// Deps is everything one sweep needs.
type Deps struct {
	// DB reads and writes markers, checks, orgs, members and events.
	DB db.Service
	// Health computes the region report. Required.
	Health Reporter
	// Placer re-places the auto checks of a dark region. Nil disables
	// re-placement (every check then behaves as pinned).
	Placer Placer
	// Jobs queues the org emails. Nil skips them (the events are still
	// written).
	Jobs jobsvc.Service
	// Operator delivers to the platform_watchdog recipients.
	Operator opsnotify.Deps
	// BaseURL builds dashboard links. Empty omits them.
	BaseURL string
	// Logger receives every transition. Defaults to slog.Default.
	Logger *slog.Logger
}

// TransitionKind is what happened to one region this sweep.
type TransitionKind string

// Transition kinds.
const (
	// TransitionDark is a region that just went dark (from healthy or
	// stalled).
	TransitionDark TransitionKind = "dark"
	// TransitionStalled is a region whose live workers stopped claiming.
	TransitionStalled TransitionKind = "stalled"
	// TransitionRecovered is a dark or stalled region healthy for
	// RecoveryStreak sweeps.
	TransitionRecovered TransitionKind = "recovered"
)

// Transition is one region's movement across this sweep.
type Transition struct {
	Region string
	Kind   TransitionKind
	// OrgsNotified are the orgs told about it this sweep.
	OrgsNotified []string
	// OperatorNotified reports whether the platform_watchdog recipients were
	// sent a notice for it this sweep.
	OperatorNotified bool
	// Replaced are the automatically placed checks moved off the region this
	// sweep (spec 2026-09-25-06).
	Replaced []checks.PlacementChange
}

// Result is the outcome of one sweep.
type Result struct {
	// CloudRegions is how many cloud regions were evaluated.
	CloudRegions int
	Transitions  []Transition
}

// observation is what one sweep sees of one cloud region.
type observation int

const (
	observedHealthy observation = iota
	observedStalled
	observedDark
)

// regionState is one cloud region's evaluation within a sweep.
type regionState struct {
	slug   string
	row    *checks.RegionHealthRow
	seen   observation
	marker *regionoutage.Marker
	// next is the phase after this sweep; empty means healthy (no marker).
	next regionoutage.Phase
	// streak is the healthy streak after this sweep.
	streak int
}

// Sweep evaluates every cloud region once.
func Sweep(ctx context.Context, deps *Deps) (*Result, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	if deps.Health == nil || deps.DB == nil {
		return nil, ErrRegionHealthUnavailable
	}

	report, jobs, err := deps.Health.RegionHealthWithJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("region health: %w", err)
	}

	if report == nil {
		return nil, ErrRegionHealthUnavailable
	}

	markers, err := regionoutage.List(ctx, deps.DB)
	if err != nil {
		return nil, err
	}

	// One time base: RegionHealth computed overdue and liveness against its
	// own clock, so every decision here reads that same instant.
	now := report.GeneratedAt

	states := evaluate(report, jobs, markers, now)

	publishMetrics(states)

	sweep := &sweepRun{
		deps:      deps,
		report:    report,
		jobs:      jobs,
		states:    states,
		now:       now,
		operators: operatorRecipients(ctx, deps),
	}

	transitions, err := sweep.apply(ctx)
	if err != nil {
		return nil, err
	}

	return &Result{CloudRegions: len(states), Transitions: transitions}, nil
}

// evaluate turns the report into one state per cloud region, markers
// included: a region with a marker but no row any more (its jobs and workers
// are all gone) is evaluated as healthy — nothing is stranded there.
func evaluate(
	report *checks.RegionHealthReport, jobs []checks.RegionJob,
	markers map[string]*regionoutage.Marker, now time.Time,
) []*regionState {
	stalled := stalledRegions(jobs, now)

	bySlug := make(map[string]*regionState, len(report.Regions)+len(markers))

	for i := range report.Regions {
		row := &report.Regions[i]
		if !isCloudRow(row) {
			continue
		}

		state := &regionState{slug: row.Slug, row: row, marker: markers[row.Slug]}

		switch {
		case row.Jobs > 0 && row.LiveWorkers == 0:
			state.seen = observedDark
		case row.LiveWorkers > 0 && stalled[row.Slug]:
			state.seen = observedStalled
		default:
			state.seen = observedHealthy
		}

		bySlug[row.Slug] = state
	}

	for slug, marker := range markers {
		if _, ok := bySlug[slug]; ok || regions.IsPrivateRegion(slug) {
			continue
		}

		bySlug[slug] = &regionState{slug: slug, marker: marker, seen: observedHealthy}
	}

	out := make([]*regionState, 0, len(bySlug))
	for _, state := range bySlug {
		state.decide()
		out = append(out, state)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].slug < out[j].slug })

	return out
}

// isCloudRow keeps cloud regions only. Private (`@`) rows are org-relative
// and belong to the org's own agent (spec 2026-09-25-05); filtering them here
// explicitly means one can never be reported dark by this sweep.
func isCloudRow(row *checks.RegionHealthRow) bool {
	return row.Organization == "" && !row.IsPrivate()
}

// stalledRegions is the set of cloud regions holding at least one job overdue
// past max(2 × its period, StallFloor).
func stalledRegions(jobs []checks.RegionJob, now time.Time) map[string]bool {
	out := make(map[string]bool)

	for i := range jobs {
		job := &jobs[i]
		if job.ScheduledAt == nil || regions.IsPrivateRegion(job.Region) {
			continue
		}

		if now.Sub(*job.ScheduledAt) > stallBar(job.Period) {
			out[job.Region] = true
		}
	}

	return out
}

// stallBar is max(2 × period, StallFloor).
func stallBar(period time.Duration) time.Duration {
	return max(stallPeriodMultiplier*period, StallFloor)
}

// decide computes the phase and healthy streak after this sweep.
func (s *regionState) decide() {
	var previous regionoutage.Phase
	if s.marker != nil {
		previous = s.marker.Phase
	}

	switch s.seen {
	case observedDark:
		s.next = regionoutage.PhaseDark
	case observedStalled:
		// Workers came back to a dark region but are not draining it yet:
		// still not healthy, and not a new stall worth its own page either.
		if previous == regionoutage.PhaseDark {
			s.next = regionoutage.PhaseDark
		} else {
			s.next = regionoutage.PhaseStalled
		}
	case observedHealthy:
		if s.marker == nil {
			return
		}

		s.streak = s.marker.HealthyStreak + 1
		if s.streak < RecoveryStreak {
			s.next = previous
		}
	}
}

// isDarkAfter reports whether the region is dark once this sweep is applied.
func (s *regionState) isDarkAfter() bool {
	return s.next == regionoutage.PhaseDark
}

// publishMetrics replaces the two gauges with this sweep's values. Reset
// first, so a region that vanished from the report stops being exported
// instead of freezing at its last value.
func publishMetrics(states []*regionState) {
	prommetrics.WorkersActive.Reset()
	prommetrics.RegionDark.Reset()

	for _, state := range states {
		live := 0
		if state.row != nil {
			live = state.row.LiveWorkers
		}

		prommetrics.SetWorkersActive(state.slug, float64(live))
		prommetrics.SetRegionDark(state.slug, state.isDarkAfter())
	}
}

// operatorRecipients reads the platform_watchdog recipients. A disabled or
// unreadable watchdog means nobody to tell — the sweep still transitions,
// logs and meters, it just delivers nowhere.
func operatorRecipients(ctx context.Context, deps *Deps) *operatorTarget {
	cfg, err := watchdog.LoadConfig(ctx, deps.DB)
	if err != nil {
		deps.Logger.WarnContext(ctx,
			"Region sweep cannot read the platform_watchdog parameter; operator notices are logged only",
			"error", err)

		return &operatorTarget{}
	}

	if !cfg.Enabled {
		return &operatorTarget{}
	}

	return &operatorTarget{recipients: cfg.Recipients, minSeverity: cfg.Severity()}
}

// operatorTarget is who the operator notices go to, and from which severity.
type operatorTarget struct {
	recipients  []string
	minSeverity watchdog.Severity
}
