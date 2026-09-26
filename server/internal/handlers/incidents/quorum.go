package incidents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regionquorum"
)

// resultSignal is what one result means for the check-level state machine.
//
// In legacy mode (one region, a regionless/passive check, or a quorum of every
// region — the N <= 2 default) it is the result's own status, exactly as
// ProcessCheckResult has always read it: isSuccess/isFailure/isWarning from
// the status, openable == isFailure, regional == false. Every expression that
// reads the two added fields reduces to its previous form with those values,
// which is what keeps single- and dual-region incident timing unchanged.
//
// In quorum mode (spec 2026-09-25-10) it is derived from the per-region
// states of the check's CURRENT regions instead:
//
//   - at least Q regions failing: a failure (arms / keeps the confirmation
//     clock, clears the recovery clock);
//   - some, but fewer than Q, failing: a success for the clocks and the
//     incident (so recovery mirrors the down rule), shown as `warning` —
//     the regional issue (regional == true);
//   - none failing: the result's own up / warning.
type resultSignal struct {
	isSuccess bool
	isFailure bool
	isWarning bool
	// regional marks the regional issue: counted as a success, shown as
	// warning.
	regional bool
	// openable is true when the result ITSELF failed. Only such a result may
	// open (or reopen) an incident, count toward failure_count, or flip the
	// check to `down`: a passing region's result while the quorum is failing
	// keeps the check validating, so the check never reads `down` with no
	// incident behind it. The next failing region's result opens it.
	openable bool
}

// known reports whether the result carries a status the state machine acts
// on (created/running and unknown statuses are skipped).
func (sig resultSignal) known() bool {
	return sig.isSuccess || sig.isFailure || sig.isWarning
}

// legacySignal is the per-result reading ProcessCheckResult has always used.
func legacySignal(status models.ResultStatus) resultSignal {
	isFailure := status.IsFailure()

	return resultSignal{
		isSuccess: status == models.ResultStatusUp,
		isFailure: isFailure,
		isWarning: status == models.ResultStatusWarning,
		openable:  isFailure,
	}
}

// resultRegion is the region a result ran in, "" for none.
func resultRegion(result *models.Result) string {
	if result.Region == nil {
		return ""
	}

	return *result.Region
}

// resultTime is the result's execution time, clamped to `now` (a result is
// never "from the future", and a zero PeriodStart reads as now).
func resultTime(result *models.Result, now time.Time) time.Time {
	if result.PeriodStart.IsZero() || result.PeriodStart.After(now) {
		return now
	}

	return result.PeriodStart
}

// tracksRegionStates reports whether per-region readings are kept for this
// check: a non-passive check with two or more regions. A single-region check
// has nothing to agree with and does not pay the extra write.
func tracksRegionStates(check *models.Check) bool {
	return regionquorum.RegionCount(check) >= 2
}

// recordRegionState stores the region's newest reading (spec 2026-09-25-10).
//
// Called after the freshness prelude (whose live-row read supplies the
// CURRENT regions) and before the maintenance return: a reading is an
// observation, not an incident decision, so a window must not freeze it.
// Best-effort — a failed write is logged, and the quorum evaluation of THIS
// result does not depend on it (deriveSignal overrides the result's own
// region with the result itself).
func (s *Service) recordRegionState(
	ctx context.Context, check *models.Check, result *models.Result, status models.ResultStatus,
) {
	region := resultRegion(result)
	if region == "" || !status.IsRealForFreshness() || !tracksRegionStates(check) {
		return
	}

	now := s.clock.Now()
	readingAt := resultTime(result, now)

	if err := s.db.UpsertCheckRegionState(ctx, &models.CheckRegionState{
		CheckUID:        check.UID,
		Region:          region,
		OrganizationUID: check.OrganizationUID,
		Status:          status,
		StatusSince:     readingAt,
		LastResultAt:    readingAt,
		UpdatedAt:       now,
	}); err != nil {
		slog.WarnContext(ctx, "Failed to record the region's reading",
			"checkUID", check.UID, "region", region, "error", err)
	}
}

// deriveSignal reads a result as a check-level signal: the result's own
// status in legacy mode, the quorum over the current regions otherwise.
func (s *Service) deriveSignal(
	ctx context.Context, check *models.Check, result *models.Result, status models.ResultStatus,
) (resultSignal, error) {
	legacy := legacySignal(status)
	if !legacy.known() {
		return legacy, nil
	}

	_, regionCount, quorum := regionquorum.ForCheck(check)
	if !regionquorum.UsesQuorum(quorum, regionCount) {
		return legacy, nil
	}

	// A result with no region cannot say which region it speaks for; read it
	// the only way it can be read.
	region := resultRegion(result)
	if region == "" {
		return legacy, nil
	}

	eval, err := s.evaluateQuorum(ctx, check, quorum, &regionquorum.RegionState{
		Region:       region,
		Status:       status,
		LastResultAt: resultTime(result, s.clock.Now()),
	})
	if err != nil {
		return resultSignal{}, err
	}

	sig := resultSignal{openable: legacy.isFailure}

	switch {
	case eval.QuorumFailing():
		sig.isFailure = true
	case eval.RegionalIssue():
		sig.isSuccess = true
		sig.regional = true
	case legacy.isWarning:
		sig.isWarning = true
	default:
		sig.isSuccess = true
	}

	return sig, nil
}

// evaluateQuorum evaluates the check's current regions from the stored
// readings, with `current` (the result being processed) standing in for its
// own region's row unless a newer reading is already stored.
func (s *Service) evaluateQuorum(
	ctx context.Context, check *models.Check, quorum int, current *regionquorum.RegionState,
) (regionquorum.Evaluation, error) {
	stored, err := s.db.ListCheckRegionStates(ctx, check.UID)
	if err != nil {
		return regionquorum.Evaluation{}, fmt.Errorf("failed to list region states: %w", err)
	}

	states := regionquorum.StatesOf(stored)
	if current != nil {
		states = append(states, *current)
	}

	return regionquorum.Evaluate(check.Regions, states, quorum, s.clock.Now(), check.StaleThreshold()), nil
}
