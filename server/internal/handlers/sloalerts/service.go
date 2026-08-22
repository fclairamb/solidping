// Package sloalerts turns an SLO's computed burn rate into a page.
//
// The burn rate has existed since the SLO/SLA feature shipped and was consumed
// by nobody: an org that spent a month of error budget in an hour found out
// from the dashboard or the monthly digest, never from a phone. This package is
// the missing consumer — a Google-SRE-style multiwindow, multi-burn-rate
// evaluator that opens ORDINARY INCIDENTS, so ack, snooze, manual resolve,
// escalation policies, severity-gated channel routing and group correlation all
// apply with no parallel alerting path to keep in step.
package sloalerts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/slo"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Service errors.
var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrSLONotFound is returned when an SLO is not found.
	ErrSLONotFound = errors.New("slo not found")
	// ErrPolicyNotFound is returned when an alert policy is not found.
	ErrPolicyNotFound = errors.New("alert policy not found")
	// ErrInvalidWindows is returned when the window pair is not orderable.
	ErrInvalidWindows = errors.New("shortWindowSeconds must be > 0 and <= longWindowSeconds")
	// ErrInvalidThreshold is returned when the burn-rate multiple is not positive.
	ErrInvalidThreshold = errors.New("threshold must be greater than 0")
	// ErrInvalidSeverity is returned when the severity is not a known value.
	ErrInvalidSeverity = errors.New("severity must be one of: critical, warning")
	// ErrInvalidMinSamples is returned when the sample floor is below 1.
	ErrInvalidMinSamples = errors.New("minSamples must be at least 1")
)

// evaluationBatchSize bounds one sweep. Policies are read oldest-evaluated
// first, so a very large install still gives every policy a turn instead of
// starving the tail — and one sweep can never become an unbounded scan.
const evaluationBatchSize = 500

// maxWindowSeconds caps a configurable window at seven days. Beyond that the
// short-window query stops being answerable from the raw-retention band and the
// alert stops being about "right now" anyway.
const maxWindowSeconds = 7 * 24 * 3600

// Service evaluates burn-rate alert policies and exposes their CRUD.
type Service struct {
	db        db.Service
	slos      *slos.Service
	incidents *incidents.Service
	clock     clock.Clock
	logger    *slog.Logger
}

// NewService builds the burn-rate alerting service.
func NewService(
	dbService db.Service, sloSvc *slos.Service, incidentSvc *incidents.Service,
	clk clock.Clock, logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}

	return &Service{db: dbService, slos: sloSvc, incidents: incidentSvc, clock: clk, logger: logger}
}

// windowSample is one evaluated rolling window.
type windowSample struct {
	// BurnRate is nil when the window carries no countable probe, or when the
	// objective is 100% (no allowance to divide by).
	BurnRate *float64
	Samples  int
	// Conclusive is false when the window is too thin to make a claim. An
	// inconclusive window never fires AND never counts as "below threshold" for
	// the resolution hysteresis — sparse data must not fabricate an alert, and
	// it must not silently close one either.
	Conclusive bool
}

func (w windowSample) over(threshold float64) bool {
	return w.Conclusive && w.BurnRate != nil && *w.BurnRate > threshold
}

func (w windowSample) under(threshold float64) bool {
	return w.Conclusive && w.BurnRate != nil && *w.BurnRate <= threshold
}

// evaluation is everything one pass learned about one policy.
type evaluation struct {
	Long  windowSample
	Short windowSample
	// Month is the calendar-window budget state, carried purely so the page
	// can say how much budget is left and when it runs out.
	Month slos.StatusRow
}

// firing reports the multiwindow rule: BOTH windows over threshold. The long
// window proves the burn is significant; the short one proves it is still
// happening, which is exactly what stops a spike that ended forty minutes ago
// from paging for the rest of the hour.
func (e evaluation) firing(threshold float64) bool {
	return e.Long.over(threshold) && e.Short.over(threshold)
}

// cooled reports that both windows are conclusively below threshold — the
// precondition for starting the resolution hysteresis clock.
func (e evaluation) cooled(threshold float64) bool {
	return e.Long.under(threshold) && e.Short.under(threshold)
}

// EvaluateBurnRates runs one sweep over every enabled policy. It returns how
// many policies were evaluated.
//
// Per-policy failures are logged and skipped rather than aborting the sweep:
// one SLO whose scope was deleted mid-flight must not stop every other org's
// alerting for the rest of the minute.
func (s *Service) EvaluateBurnRates(ctx context.Context, now time.Time) (int, error) {
	policies, err := s.db.ListEnabledSLOAlertPolicies(ctx, evaluationBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list enabled alert policies: %w", err)
	}

	evaluated := 0

	for _, policy := range policies {
		if evalErr := s.evaluatePolicy(ctx, policy, now); evalErr != nil {
			s.logger.WarnContext(ctx, "SLO burn-rate evaluation failed",
				"sloUid", policy.SLOUID, "policyUid", policy.UID, "error", evalErr)

			continue
		}

		evaluated++
	}

	return evaluated, nil
}

// evaluatePolicy evaluates one policy and drives its incident lifecycle.
func (s *Service) evaluatePolicy(ctx context.Context, policy *models.SLOAlertPolicy, now time.Time) error {
	row, err := s.db.GetSLO(ctx, policy.OrganizationUID, policy.SLOUID)
	if err != nil {
		return fmt.Errorf("get slo: %w", err)
	}

	result, err := s.evaluate(ctx, row, policy, now)
	if err != nil {
		return err
	}

	open, err := s.incidents.FindActiveBurnIncident(ctx, policy.SLOUID, policy.UID)
	if err != nil {
		return err
	}

	update := models.SLOAlertPolicyUpdate{LastEvaluatedAt: &now}
	applyBurnReadout(&update, result)

	if lifecycleErr := s.applyLifecycle(ctx, row, policy, result, open, now, &update); lifecycleErr != nil {
		return lifecycleErr
	}

	if err := s.db.UpdateSLOAlertPolicy(ctx, policy.UID, update); err != nil {
		return fmt.Errorf("update alert policy state: %w", err)
	}

	return nil
}

// applyLifecycle is the fire / update / hysteresis-resolve state machine.
//
//nolint:cyclop // the whole alert lifecycle in one readable ladder; splitting it hides the rule.
func (s *Service) applyLifecycle(
	ctx context.Context, row *models.SLO, policy *models.SLOAlertPolicy,
	result evaluation, open *models.Incident, now time.Time,
	update *models.SLOAlertPolicyUpdate,
) error {
	switch {
	case result.firing(policy.Threshold):
		// Any burn at all resets the cool-down clock.
		update.ClearBelowThresholdSince = true

		snapshot := s.snapshot(row, policy, result)

		if open != nil {
			// Dedup: at most one open incident per (SLO, policy). A worsening
			// burn updates it rather than paging again.
			return s.incidents.UpdateSLOBurnIncident(ctx, open, snapshot)
		}

		// A manual resolve while the SLO is still burning lands here on the
		// very next sweep and opens a fresh incident — the same semantic check
		// incidents have, where a manually-resolved incident is never revived
		// but a still-failing target opens a new one.
		return s.openIncident(ctx, row, policy, snapshot, now)

	case open == nil:
		// Nothing burning and nothing open: no clock to keep.
		update.ClearBelowThresholdSince = true

		return nil

	case !result.cooled(policy.Threshold):
		// Either a window is still over threshold, or one turned inconclusive.
		// Neither is evidence the burn stopped, so the incident stays open and
		// the hysteresis anchor is left exactly where it was.
		return nil

	case policy.BelowThresholdSince == nil:
		// First conclusively-cool sweep: start the clock, do not resolve.
		update.BelowThresholdSince = &now

		return nil

	case now.Sub(*policy.BelowThresholdSince) < policy.ShortWindow():
		// Cool, but not for long enough yet. This is the anti-flap rule: an
		// alert that resolves the instant the rate dips re-fires a minute later
		// and pages twice for one outage.
		return nil

	default:
		if err := s.incidents.AutoResolveSLOBurnIncident(
			ctx, open, now, s.snapshot(row, policy, result),
		); err != nil {
			return err
		}

		update.ClearBelowThresholdSince = true

		return nil
	}
}

// openIncident resolves the routing anchor and opens the burn incident.
func (s *Service) openIncident(
	ctx context.Context, row *models.SLO, policy *models.SLOAlertPolicy,
	snapshot incidents.BurnSnapshot, now time.Time,
) error {
	anchor, err := s.anchorCheck(ctx, row)
	if err != nil {
		return err
	}

	if anchor == nil {
		// An SLO whose scope has no live check cannot page anybody. Silently
		// not alerting is the right outcome — there is nothing being measured
		// — but it is worth a line, because "my SLO never alerted" is exactly
		// the question this produces.
		s.logger.InfoContext(ctx, "SLO burn alert has no live check to route through",
			"sloUid", row.UID, "policyUid", policy.UID)

		return nil
	}

	_, err = s.incidents.OpenSLOBurnIncident(ctx, &incidents.OpenSLOBurnIncidentRequest{
		OrganizationUID: row.OrganizationUID,
		AnchorCheck:     anchor,
		StartedAt:       now,
		Title:           burnTitle(row, policy, snapshot),
		Snapshot:        snapshot,
	})

	return err
}

// anchorCheck picks the check a burn incident routes through.
//
// Deterministic (lowest UID) rather than arbitrary: the same SLO must resolve
// the same channels and the same escalation policy on every evaluation, or two
// consecutive alerts for one objective would page two different rotas.
func (s *Service) anchorCheck(ctx context.Context, row *models.SLO) (*models.Check, error) {
	checkUIDs, err := s.slos.ScopeCheckUIDs(ctx, row.OrganizationUID, row)
	if err != nil {
		return nil, fmt.Errorf("resolve slo scope: %w", err)
	}

	if len(checkUIDs) == 0 {
		return nil, nil //nolint:nilnil // "no live check" is a normal answer here
	}

	sorted := make([]string, len(checkUIDs))
	copy(sorted, checkUIDs)
	sort.Strings(sorted)

	for _, uid := range sorted {
		check, lookupErr := s.db.GetCheck(ctx, row.OrganizationUID, uid)
		if lookupErr == nil && check != nil {
			return check, nil
		}
	}

	return nil, nil //nolint:nilnil // every candidate was deleted; same normal answer
}

// evaluate measures the long window, the short window and the calendar budget
// in ONE pass over the SLO's scope.
func (s *Service) evaluate(
	ctx context.Context, row *models.SLO, policy *models.SLOAlertPolicy, now time.Time,
) (evaluation, error) {
	loc, err := slo.LoadLocation(row.Timezone)
	if err != nil {
		loc = time.UTC
	}

	windows := []slo.Window{
		{Start: now.Add(-policy.LongWindow()), End: now},
		{Start: now.Add(-policy.ShortWindow()), End: now},
		slo.MonthWindow(loc, now),
	}

	// The SAME shared path GetStatus / GetHistory use, so group aggregation,
	// the coverage clamp and the results.maintenance exclusion cannot drift
	// from the objective's own denominator. Without the maintenance half of
	// that, planned maintenance would page.
	rows, err := s.slos.EvaluateWindows(ctx, row.OrganizationUID, row, windows, now)
	if err != nil {
		return evaluation{}, fmt.Errorf("evaluate windows: %w", err)
	}

	if len(rows) != len(windows) {
		return evaluation{}, fmt.Errorf("evaluate windows: %w", sql.ErrNoRows)
	}

	return evaluation{
		Long:  toSample(rows[0], policy.MinSamples),
		Short: toSample(rows[1], policy.MinSamples),
		Month: rows[2],
	}, nil
}

// toSample applies the minimum-sample floor.
func toSample(row slos.StatusRow, minSamples int) windowSample {
	return windowSample{
		BurnRate:   row.BurnRate,
		Samples:    row.TotalChecks,
		Conclusive: row.HasData && row.BurnRate != nil && row.TotalChecks >= minSamples,
	}
}

// snapshot packs an evaluation into the incident/notification payload.
func (s *Service) snapshot(
	row *models.SLO, policy *models.SLOAlertPolicy, result evaluation,
) incidents.BurnSnapshot {
	return incidents.BurnSnapshot{
		SLOUID:                 row.UID,
		SLOName:                row.Name,
		SLOSlug:                row.Slug,
		TargetPct:              row.TargetPct,
		PolicyUID:              policy.UID,
		PolicyKind:             policy.Kind,
		Severity:               policy.Severity,
		Threshold:              policy.Threshold,
		LongWindowSeconds:      policy.LongWindowSeconds,
		ShortWindowSeconds:     policy.ShortWindowSeconds,
		LongBurnRate:           deref(result.Long.BurnRate),
		ShortBurnRate:          deref(result.Short.BurnRate),
		BudgetRemainingSeconds: result.Month.BudgetRemainingSeconds,
		ProjectedExhaustionAt:  result.Month.ProjectedExhaustionAt,
	}
}

// applyBurnReadout records the live rates so the dashboard and the evaluator
// cannot disagree about what the burn rate was a minute ago.
func applyBurnReadout(update *models.SLOAlertPolicyUpdate, result evaluation) {
	if result.Long.BurnRate == nil && result.Short.BurnRate == nil {
		// Null rather than stale: "no data" is not "the same as last time".
		update.ClearLastBurnRates = true

		return
	}

	update.LastLongBurnRate = result.Long.BurnRate
	update.LastShortBurnRate = result.Short.BurnRate
}

// burnTitle is what a phone shows at 3am, so it leads with the severity and the
// rate rather than with the word "incident".
func burnTitle(row *models.SLO, policy *models.SLOAlertPolicy, snapshot incidents.BurnSnapshot) string {
	label := "Slow burn"
	if policy.Kind == models.SLOAlertPolicyKindFast {
		label = "Fast burn"
	}

	return fmt.Sprintf("%s: %s error budget burning at %.1fx", label, row.Name, snapshot.LongBurnRate)
}

func deref(value *float64) float64 {
	if value == nil {
		return 0
	}

	return *value
}
