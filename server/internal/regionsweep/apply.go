package regionsweep

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
)

// sweepRun carries one sweep's inputs through its side effects.
type sweepRun struct {
	deps      *Deps
	report    *checks.RegionHealthReport
	jobs      []checks.RegionJob
	states    []*regionState
	now       time.Time
	operators *operatorTarget

	// classifier is built lazily: most sweeps have no dark region and never
	// need to load a single check.
	classifier *classifier
	// moved is every check re-placed this sweep: it is no longer blind, so
	// the org notices leave it out.
	moved map[string]bool
}

// apply performs every transition's side effects and persists the markers.
func (r *sweepRun) apply(ctx context.Context) ([]Transition, error) {
	transitions := make([]Transition, 0)

	for _, state := range r.states {
		transition, err := r.applyOne(ctx, state)
		if err != nil {
			return transitions, err
		}

		if transition != nil {
			transitions = append(transitions, *transition)
		}
	}

	return transitions, nil
}

// applyOne handles one region.
func (r *sweepRun) applyOne(ctx context.Context, state *regionState) (*Transition, error) {
	var previous regionoutage.Phase
	if state.marker != nil {
		previous = state.marker.Phase
	}

	switch {
	case state.next == "" && state.marker == nil:
		// Healthy and nothing on record.
		return nil, nil //nolint:nilnil // no transition is not an error
	case state.next == "":
		return r.recover(ctx, state)
	case state.next == regionoutage.PhaseDark && previous != regionoutage.PhaseDark:
		return r.goDark(ctx, state)
	case state.next == regionoutage.PhaseStalled && previous == "":
		return r.goStalled(ctx, state)
	default:
		return nil, r.stayUnhealthy(ctx, state)
	}
}

// goDark records a region that just went dark and tells everyone.
func (r *sweepRun) goDark(ctx context.Context, state *regionState) (*Transition, error) {
	marker := r.nextMarker(state)
	marker.Phase = regionoutage.PhaseDark
	marker.Since = r.darkSince(state)

	// Re-place the automatic checks BEFORE telling anybody: a check moved to
	// a healthy region is not blind, and its org must not be told it is.
	replaced, err := r.replaceAutoChecks(ctx, state)
	if err != nil {
		return nil, err
	}

	notices, err := r.orgOfflineNotices(ctx, state, marker)
	if err != nil {
		return nil, err
	}

	operatorNotified := r.notifyOperatorDark(ctx, state, marker, notices, len(replaced))

	marker.NotifiedOrgs = append(marker.NotifiedOrgs, orgUIDsOf(notices)...)

	if err := regionoutage.Save(ctx, r.deps.DB, marker); err != nil {
		return nil, err
	}

	r.deps.Logger.WarnContext(ctx, "Region went dark: jobs assigned and no live worker",
		"region", state.slug, "since", marker.Since, "orgsNotified", len(notices),
		"operatorNotified", operatorNotified, "checksReplaced", len(replaced))

	return &Transition{
		Region: state.slug, Kind: TransitionDark,
		OrgsNotified: orgUIDsOf(notices), OperatorNotified: operatorNotified,
		Replaced: replaced,
	}, nil
}

// goStalled records a region whose live workers are not claiming.
func (r *sweepRun) goStalled(ctx context.Context, state *regionState) (*Transition, error) {
	marker := r.nextMarker(state)
	marker.Phase = regionoutage.PhaseStalled
	marker.Since = r.stalledSince(state)

	operatorNotified := r.notifyOperatorStalled(ctx, state, marker)

	if err := regionoutage.Save(ctx, r.deps.DB, marker); err != nil {
		return nil, err
	}

	r.deps.Logger.WarnContext(ctx, "Region stalled: live workers but jobs overdue",
		"region", state.slug, "since", marker.Since, "operatorNotified", operatorNotified)

	return &Transition{Region: state.slug, Kind: TransitionStalled, OperatorNotified: operatorNotified}, nil
}

// stayUnhealthy refreshes an ongoing outage. While dark, an org that became
// blind after the outage started (a check pinned to the region mid-outage, or
// a second region going dark) is told once; an org already told never is
// again.
func (r *sweepRun) stayUnhealthy(ctx context.Context, state *regionState) error {
	marker := r.nextMarker(state)

	if marker.Phase == regionoutage.PhaseDark && state.seen == observedDark {
		// Still dark: an automatic check that could not move last time (no
		// healthy candidate then), or that was placed here since, gets another
		// chance every sweep.
		replaced, err := r.replaceAutoChecks(ctx, state)
		if err != nil {
			return err
		}

		if len(replaced) > 0 {
			r.deps.Logger.InfoContext(ctx, "Region still dark: moved automatically placed checks off it",
				"region", state.slug, "checksReplaced", len(replaced))
		}

		notices, err := r.orgOfflineNotices(ctx, state, marker)
		if err != nil {
			return err
		}

		marker.NotifiedOrgs = append(marker.NotifiedOrgs, orgUIDsOf(notices)...)
	}

	return regionoutage.Save(ctx, r.deps.DB, marker)
}

// recover closes an outage: operator and exactly the orgs on record.
func (r *sweepRun) recover(ctx context.Context, state *regionState) (*Transition, error) {
	marker := state.marker

	orgs := r.orgRecoveredNotices(ctx, state, marker)
	operatorNotified := r.notifyOperatorRecovered(ctx, state, marker)

	if err := regionoutage.Delete(ctx, r.deps.DB, state.slug); err != nil {
		return nil, err
	}

	r.deps.Logger.InfoContext(ctx, "Region recovered",
		"region", state.slug, "phase", string(marker.Phase), "since", marker.Since,
		"orgsNotified", len(orgs), "operatorNotified", operatorNotified)

	return &Transition{
		Region: state.slug, Kind: TransitionRecovered,
		OrgsNotified: orgs, OperatorNotified: operatorNotified,
	}, nil
}

// nextMarker is the marker this sweep writes: the stored one carried forward
// (notified orgs, detection time), with this sweep's phase and streak.
func (r *sweepRun) nextMarker(state *regionState) *regionoutage.Marker {
	marker := &regionoutage.Marker{Region: state.slug, DetectedAt: r.now}

	if state.marker != nil {
		copied := *state.marker
		copied.NotifiedOrgs = append([]string(nil), state.marker.NotifiedOrgs...)
		marker = &copied
	}

	marker.Phase = state.next
	marker.HealthyStreak = state.streak
	marker.LastSeenAt = r.now

	return marker
}

// darkSince dates the outage from the last worker beat — when the region
// actually stopped, not when this sweep noticed. A region no worker ever
// served falls back to now.
func (r *sweepRun) darkSince(state *regionState) time.Time {
	if state.row != nil && state.row.LastWorkerSeenAt != nil {
		return *state.row.LastWorkerSeenAt
	}

	return r.now
}

// stalledSince dates a stall from its oldest overdue job.
func (r *sweepRun) stalledSince(state *regionState) time.Time {
	if state.row != nil && state.row.OldestOverdueAt != nil {
		return *state.row.OldestOverdueAt
	}

	return r.now
}

// replaceAutoChecks moves the automatically placed checks with a job in a
// dark region to a healthy one (spec 2026-09-25-06 A2), and remembers them so
// the org notices of this sweep leave them out. The candidate checks come from
// this sweep's own job snapshot — the rows that decided the region is dark.
func (r *sweepRun) replaceAutoChecks(ctx context.Context, state *regionState) ([]checks.PlacementChange, error) {
	if r.deps.Placer == nil {
		return nil, nil
	}

	uids := make([]string, 0)

	for i := range r.jobs {
		if r.jobs[i].Region == state.slug {
			uids = append(uids, r.jobs[i].CheckUID)
		}
	}

	if len(uids) == 0 {
		return nil, nil
	}

	replaced, err := r.deps.Placer.ReplaceAutoChecks(ctx, checks.ReplacementRequest{
		Region:    state.slug,
		CheckUIDs: uids,
		Healthy:   r.healthyRegions(),
		Reason:    models.PlacementReasonRegionOffline,
	})
	if err != nil {
		return nil, fmt.Errorf("re-place automatic checks off %s: %w", state.slug, err)
	}

	if r.moved == nil {
		r.moved = make(map[string]bool, len(replaced))
	}

	for i := range replaced {
		r.moved[replaced[i].CheckUID] = true
	}

	return replaced, nil
}

// healthyRegions is every cloud region that can take a check right now, as
// this sweep sees it: at least one live worker, and healthy after this sweep
// (no dark or stalled phase, recovering ones included).
func (r *sweepRun) healthyRegions() map[string]bool {
	out := make(map[string]bool, len(r.states))

	for _, state := range r.states {
		if state.next == "" && state.row != nil && state.row.LiveWorkers > 0 {
			out[state.slug] = true
		}
	}

	return out
}
