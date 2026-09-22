// Package degradedeval is the periodic sweep that turns the degraded-detection
// rules into incidents (spec 2026-09-22-03).
//
// It exists because a check that fails intermittently — seven failures in forty
// minutes, each one followed by a success before the confirmation period elapsed
// — opened no incident, sent no notification and left no history line, and
// because nothing user-facing read response time at all. The confirmation period
// is doing its job; this is the second, statistical detector beside it.
//
// Shaped deliberately like handlers/sloalerts: one bounded batch per sweep, read
// oldest-evaluated first, per-check failures logged and skipped rather than
// aborting the sweep. The worker, the result status and the check status are not
// touched by any of this.
package degradedeval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/degraded"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// evaluationBatchSize bounds one sweep. Checks are read oldest-evaluated first,
// so a very large install still gives every check a turn instead of starving the
// tail — and one sweep can never become an unbounded scan.
const evaluationBatchSize = 500

// lookBack is how far back the probe query reaches: 24 h, because that is the
// `raw` retention measured in production (~24 h raw, 5 days hour, 5 weeks day).
// The rules need the per-probe sequence, which only `raw` carries, so a longer
// look-back would read buckets that cannot answer the question. Do not extend it.
const lookBack = 24 * time.Hour

// probeFetchSlack over-fetches relative to the largest window so the
// maintenance/abandoned/lifecycle rows that are skipped rather than counted
// cannot starve the window of countable probes.
const probeFetchSlack = 3

// maxProbeFetch caps one check's probe query however large its windows are.
const maxProbeFetch = 1000

// Service evaluates the degraded rules for every check.
type Service struct {
	db        db.Service
	incidents *incidents.Service
	clock     clock.Clock
	logger    *slog.Logger
}

// NewService builds the degraded evaluator.
func NewService(
	dbService db.Service, incidentSvc *incidents.Service, clk clock.Clock, logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}

	return &Service{db: dbService, incidents: incidentSvc, clock: clk, logger: logger}
}

// EvaluateDegraded runs one sweep and returns how many checks were evaluated.
//
// Per-check failures are logged and skipped rather than aborting the sweep: one
// check whose results query failed must not stop every other organization's
// detection for the rest of the minute.
func (s *Service) EvaluateDegraded(ctx context.Context, now time.Time) (int, error) {
	checks, err := s.db.ListChecksForDegradedEval(ctx, evaluationBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list checks for degraded eval: %w", err)
	}

	evaluated := 0

	for _, check := range checks {
		if evalErr := s.evaluateCheck(ctx, check, now); evalErr != nil {
			s.logger.WarnContext(ctx, "degraded evaluation failed",
				"checkUid", check.UID, "error", evalErr)

			continue
		}

		evaluated++
	}

	return evaluated, nil
}

// EvaluateCheck evaluates one check. Exported for the tests and for any caller
// that wants a synchronous decision on a single check.
func (s *Service) EvaluateCheck(ctx context.Context, check *models.Check, now time.Time) error {
	return s.evaluateCheck(ctx, check, now)
}

// evaluateCheck is the per-check state machine: suppress, evaluate, then either
// open/update/resolve an incident or stamp the dry run.
func (s *Service) evaluateCheck(ctx context.Context, check *models.Check, now time.Time) error {
	params := paramsFor(check)

	// Always stamp the rotation cursor, whatever else happens: a check that is
	// skipped must still move to the back of the queue, or it would be the only
	// thing the sweep ever looks at.
	update := models.CheckUpdate{DegradedEvaluatedAt: &now}
	defer func() { s.applyCheckUpdate(ctx, check, &update) }()

	// Enabling the feature retires the dry-run stamp: "would have fired" and "is
	// allowed to fire" must never both look true, or the banner keeps asking the
	// operator to enable something already enabled.
	if check.DegradedEnabled && check.DegradedWouldFireAt != nil {
		update.ClearDegradedWouldFireAt = true
	}

	if !params.FailureRuleActive() && !params.SlowRuleActive() {
		return nil
	}

	// THE suppression rule. Without it the fleet produces 114 degraded incidents
	// a day instead of 25 (measured): a check that is genuinely down fails every
	// probe, which trips any failure rule instantly and would double every outage
	// with an amber twin. Do not weaken it.
	suppressed, err := s.checkIncidentOpen(ctx, check.UID)
	if err != nil {
		return err
	}

	if suppressed {
		return nil
	}

	probes, err := s.probes(ctx, check, params, now)
	if err != nil {
		return err
	}

	outcome := degraded.Evaluate(probes, params, now)

	open, err := s.incidents.FindActiveDegradedIncident(ctx, check.UID)
	if err != nil {
		return err
	}

	return s.applyLifecycle(ctx, check, outcome, open, now, &update)
}

// applyLifecycle is the fire / update / resolve / dry-run state machine.
func (s *Service) applyLifecycle(
	ctx context.Context, check *models.Check, outcome degraded.Outcome,
	open *models.Incident, now time.Time, update *models.CheckUpdate,
) error {
	snapshot := snapshotFor(check, outcome)

	if outcome.Firing() {
		if !check.DegradedEnabled {
			// The dry run. It opens NOTHING and only records the first moment the
			// rules would have fired, which is what the check page's banner and the
			// checks list's `wouldHaveFired` filter read. Earliest-wins: the banner
			// is past tense ("would have been flagged degraded at 14:37"), so a
			// later quiet sweep must not erase it.
			if check.DegradedWouldFireAt == nil {
				stamp := now
				update.DegradedWouldFireAt = &stamp
				update.ClearDegradedWouldFireAt = false
			}

			return nil
		}

		if open != nil {
			return s.incidents.UpdateDegradedIncident(ctx, open, snapshot)
		}

		startedAt := outcome.StartedAt()
		if startedAt.IsZero() {
			startedAt = now
		}

		_, err := s.incidents.OpenDegradedIncident(ctx, &incidents.OpenDegradedIncidentRequest{
			Check:     check,
			StartedAt: startedAt,
			Title:     Title(check, snapshot),
			Snapshot:  snapshot,
		})

		return err
	}

	if open == nil {
		return nil
	}

	// Resolution: the condition has been false for N consecutive countable
	// probes, N being the window recorded when the incident opened.
	if outcome.CleanStreak < resolveWindow(open, outcome) {
		return nil
	}

	return s.incidents.AutoResolveDegradedIncident(ctx, open, now, snapshot)
}

// checkIncidentOpen reports whether a kind='check' incident is open on the check.
func (s *Service) checkIncidentOpen(ctx context.Context, checkUID string) (bool, error) {
	incident, err := s.db.FindActiveIncidentByCheckUID(ctx, checkUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}

		return false, fmt.Errorf("find active check incident: %w", err)
	}

	return incident != nil, nil
}

// probes reads the check's recent raw results, newest first, as the rule
// primitive wants them.
//
// Every region is one stream, deliberately: the incident state machine is per
// check, so a 3-region check with one dead region has 1 failure in every 3 probes
// and trips 5-of-60 on its own. That is consistent with how outages already
// behave; per-region degradation is an explicit non-goal.
func (s *Service) probes(
	ctx context.Context, check *models.Check, params degraded.Params, now time.Time,
) ([]degraded.Probe, error) {
	window := params.FailuresWindow
	if params.SlowWindow > window {
		window = params.SlowWindow
	}

	limit := window * probeFetchSlack
	if limit > maxProbeFetch {
		limit = maxProbeFetch
	}

	since := now.Add(-lookBack)

	response, err := s.db.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID:  check.OrganizationUID,
		CheckUIDs:        []string{check.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: &since,
		Limit:            limit,
		SkipBlobs:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("list raw results: %w", err)
	}

	out := make([]degraded.Probe, 0, len(response.Results))

	for _, row := range response.Results {
		if row.Status == nil {
			continue
		}

		probe := degraded.Probe{
			At:          row.PeriodStart,
			Status:      models.ResultStatus(*row.Status),
			Maintenance: row.Maintenance,
		}

		if row.Duration != nil {
			probe.DurationMs = float64(*row.Duration)
		}

		out = append(out, probe)
	}

	return out, nil
}

// applyCheckUpdate writes the evaluator-owned columns. Best-effort: a stamp that
// failed to write costs one out-of-order sweep, while returning the error would
// make a cosmetic write failure look like a detection failure.
func (s *Service) applyCheckUpdate(ctx context.Context, check *models.Check, update *models.CheckUpdate) {
	if err := s.db.UpdateCheck(ctx, check.UID, update); err != nil {
		s.logger.WarnContext(ctx, "degraded: failed to write evaluator state",
			"checkUid", check.UID, "error", err)
	}
}

// paramsFor projects a check onto the rule primitive's inputs.
func paramsFor(check *models.Check) degraded.Params {
	return degraded.Params{
		Failures:       check.DegradedFailures,
		FailuresWindow: check.DegradedFailuresWindow,
		Slow:           check.DegradedSlow,
		SlowWindow:     check.DegradedSlowWindow,
		SlowThreshold:  check.SlowThresholdMs,
		Period:         time.Duration(check.Period),
	}
}

// snapshotFor packs an outcome into the incident/notification payload.
func snapshotFor(check *models.Check, outcome degraded.Outcome) *incidents.DegradedSnapshot {
	snapshot := &incidents.DegradedSnapshot{
		Failures:       outcome.Failure.Threshold,
		FailuresWindow: outcome.Failure.Window,
		FailureSlots:   outcome.Failure.Slots,
		FailureMatches: outcome.Failure.Matches,
		FailuresFired:  outcome.Failure.Fired,
		Slow:           outcome.Slow.Threshold,
		SlowWindow:     outcome.Slow.Window,
		SlowSlots:      outcome.Slow.Slots,
		SlowMatches:    outcome.Slow.Matches,
		SlowFired:      outcome.Slow.Fired,
		SlowThreshold:  check.SlowThresholdMs,
		ResolveWindow:  outcome.ResolveWindow,
		WindowFrom:     outcome.WindowStart,
		WindowTo:       outcome.WindowEnd,
		CurrentlyUp:    check.Status == models.CheckStatusUp || check.Status == models.CheckStatusWarning,
	}

	if outcome.Failure.Active && outcome.Failure.Slots > 0 {
		pct := 100 * float64(outcome.Failure.Slots-outcome.Failure.Matches) / float64(outcome.Failure.Slots)
		snapshot.AvailabilityPct = &pct
	}

	return snapshot
}

// resolveWindow prefers the window recorded on the open incident over the one the
// current evaluation would pick: the rule that closes an incident is the rule it
// opened under, or a config edit mid-episode would silently change the exit
// condition.
func resolveWindow(open *models.Incident, outcome degraded.Outcome) int {
	if window, ok := incidents.DegradedResolveWindow(open); ok && window > 0 {
		return window
	}

	if outcome.ResolveWindow > 0 {
		return outcome.ResolveWindow
	}

	return 1
}

// Title is what a notification and the incident list lead with. It must not read
// like an outage: "is down" would get muted alongside the real thing, and the
// target is usually up at this very moment.
func Title(check *models.Check, snapshot *incidents.DegradedSnapshot) string {
	name := "Check"

	switch {
	case check.Slug != nil && *check.Slug != "":
		name = *check.Slug
	case check.Name != nil && *check.Name != "":
		name = *check.Name
	}

	switch {
	case snapshot.FailuresFired && snapshot.AvailabilityPct != nil:
		return fmt.Sprintf("%s is degraded: %d failures in the last %d probes (%.1f%%)",
			name, snapshot.FailureMatches, snapshot.FailureSlots, *snapshot.AvailabilityPct)
	case snapshot.FailuresFired:
		return fmt.Sprintf("%s is degraded: %d failures in the last %d probes",
			name, snapshot.FailureMatches, snapshot.FailureSlots)
	default:
		return fmt.Sprintf("%s is degraded: %d of the last %d probes were slower than %dms",
			name, snapshot.SlowMatches, snapshot.SlowSlots, snapshot.SlowThreshold)
	}
}
