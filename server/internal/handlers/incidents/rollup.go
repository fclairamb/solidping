package incidents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// rollupDepthCap is how deep we walk the dependency graph when looking for
// a hard ancestor. 10 is plenty for any sane org topology.
const rollupDepthCap = 10

// rollupMinWindow is the floor on the correlation window — the lookback range
// inside which a parent's incident must already be open for the child to roll
// up under it.
const rollupMinWindow = 5 * time.Minute

// applyRollup walks the dependency graph for the supplied check and, if a
// hard ancestor has an open non-suppressed incident inside the correlation
// window, mutates `incident` in place to attach to it. Idempotent on the
// passed incident: safe to call multiple times. Any error is logged and
// produces a no-op (don't block the incident-open path on graph queries).
func (s *Service) applyRollup(
	ctx context.Context, check *models.Check, incident *models.Incident,
) {
	if incident == nil || check == nil {
		return
	}

	candidate, depth := s.findRollupRoot(ctx, check, incident.StartedAt)
	if candidate == nil {
		return
	}

	rootUID := candidate.UID
	suppressed := true

	incident.CausedByIncidentUID = &rootUID
	incident.PagingSuppressed = suppressed

	// INFO, not DEBUG: "why did this incident not page?" is the first question
	// asked during an outage post-mortem, and answering it used to require
	// re-running production at debug level (spec 2026-08-24-15).
	slog.InfoContext(ctx, "Rolling up incident under parent",
		"childIncidentUid", incident.UID,
		"childCheckUid", check.UID,
		"rootIncidentUid", rootUID,
		"depth", depth,
	)
}

// rollUpExistingChildren is the FORWARD mirror of applyRollup, and the fix for
// the ordering that actually happens in production: a core service's own probe
// routinely confirms AFTER the consumers that depend on it, so by the time the
// parent's incident opens its dependents are already open, already
// un-suppressed, and already paging. applyRollup only ever looks backward, at
// child-open time, so nothing revisited them.
//
// Called when an incident OPENS (created or reopened) for `parentCheck`: walks
// hard descendants downward — same BFS shape and depth cap as findRollupRoot —
// and attaches every active, non-suppressed check incident whose own
// correlation window still contains the parent's onset.
//
// `onset` is passed explicitly rather than read off the incident because a
// REOPENED incident keeps its original started_at; the relapse's period start
// is the onset that matters for correlation.
//
// Best-effort: any error is logged and stops the walk. Never blocks the
// incident-open path on dependency-graph queries.
func (s *Service) rollUpExistingChildren(
	ctx context.Context, parentCheck *models.Check, parent *models.Incident, onset time.Time,
) {
	if parent == nil || parentCheck == nil {
		return
	}

	type node struct {
		checkUID string
		depth    int
	}

	visited := map[string]int{parentCheck.UID: 0}
	queue := []node{{checkUID: parentCheck.UID, depth: 0}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.depth >= rollupDepthCap {
			continue
		}

		children, err := s.db.ListCheckDependencyChildren(ctx, cur.checkUID)
		if err != nil {
			slog.WarnContext(ctx, "Failed to list children during forward rollup walk",
				"checkUid", cur.checkUID, "error", err)

			return
		}

		hardChildren := make([]string, 0, len(children))

		for _, dep := range children {
			if dep.Kind != models.CheckDependencyKindHard {
				continue
			}

			if _, ok := visited[dep.ChildCheckUID]; ok {
				continue
			}

			visited[dep.ChildCheckUID] = cur.depth + 1
			hardChildren = append(hardChildren, dep.ChildCheckUID)
			queue = append(queue, node{checkUID: dep.ChildCheckUID, depth: cur.depth + 1})
		}

		if len(hardChildren) == 0 {
			continue
		}

		s.attachChildrenAtLevel(ctx, parent, onset, hardChildren, visited)
	}
}

// attachChildrenAtLevel resolves one BFS level's hard children to their open
// incidents and attaches the ones still inside their own correlation window.
//
// The window belongs to the CHILD (max(2 * its period, 5min)), so children with
// different periods need different bounds — they are grouped by window and each
// group reuses the very same query the backward path uses, with
// `until = parent onset` instead of `until = child onset`. One correlation
// rule, one query, two directions.
func (s *Service) attachChildrenAtLevel(
	ctx context.Context, parent *models.Incident, onset time.Time,
	hardChildren []string, visited map[string]int,
) {
	checks, err := s.db.GetChecksByUIDs(ctx, parent.OrganizationUID, hardChildren)
	if err != nil {
		slog.WarnContext(ctx, "Failed to load child checks during forward rollup walk",
			"error", err)

		return
	}

	// Deterministic grouping: first-appearance order, never map order.
	var windows []time.Duration

	grouped := make(map[time.Duration][]string, len(hardChildren))

	for _, uid := range hardChildren {
		child, ok := checks[uid]
		if !ok || child == nil {
			continue
		}

		window := correlationWindow(child)
		if _, seen := grouped[window]; !seen {
			windows = append(windows, window)
		}

		grouped[window] = append(grouped[window], uid)
	}

	for _, window := range windows {
		incidents, err := s.db.FindActiveIncidentsForChecksInWindow(
			ctx, grouped[window], onset.Add(-window), onset,
		)
		if err != nil {
			slog.WarnContext(ctx, "Failed to query child incidents during forward rollup walk",
				"error", err)

			return
		}

		for _, child := range incidents {
			if child.UID == parent.UID {
				continue
			}

			s.attachChildToRollupParent(ctx, child, parent, visited[child.CheckUID])
		}
	}
}

// attachChildToRollupParent performs the guarded attachment and, only if this
// call is the one that flipped the row, records it. The guard lives in the
// UPDATE's WHERE clause (paging_suppressed = FALSE), so a forward walk racing
// the child's own backward evaluation converges on one attachment and one
// event instead of two.
func (s *Service) attachChildToRollupParent(
	ctx context.Context, child, parent *models.Incident, depth int,
) {
	attached, err := s.db.AttachIncidentToRollupParent(ctx, child.UID, parent.UID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to attach child incident to rollup parent",
			"childIncidentUid", child.UID, "parentIncidentUid", parent.UID, "error", err)

		return
	}

	if !attached {
		// Lost the race (or the child moved on): somebody else already owns
		// this attachment and already emitted its event.
		return
	}

	parentUID := parent.UID
	child.CausedByIncidentUID = &parentUID
	child.PagingSuppressed = true

	slog.InfoContext(ctx, "Retroactively rolled up child incident under a late-confirming parent",
		"childIncidentUid", child.UID,
		"childCheckUid", child.CheckUID,
		"parentIncidentUid", parent.UID,
		"parentCheckUid", parent.CheckUID,
		"depth", depth,
	)

	if err := s.emitEvent(ctx, child.OrganizationUID, models.EventTypeIncidentRolledUp, child, models.JSONMap{
		keyCheckUID:          child.CheckUID,
		keyParentIncidentUID: parent.UID,
		keyParentCheckUID:    parent.CheckUID,
		keyRollupDepth:       depth,
	}); err != nil {
		slog.WarnContext(ctx, "Failed to emit rollup event",
			"childIncidentUid", child.UID, "error", err)
	}
}

// findRollupRoot walks parents BFS up to depthCap, restricting to fully-hard
// paths. For every reached ancestor it queries open non-suppressed incidents
// inside the correlation window. Returns the deepest such incident (oldest
// startedAt as tiebreak) and its depth from the original child.
func (s *Service) findRollupRoot(
	ctx context.Context, child *models.Check, childStartedAt time.Time,
) (*models.Incident, int) {
	type node struct {
		checkUID string
		depth    int
	}

	window := correlationWindow(child)
	since := childStartedAt.Add(-window)
	until := childStartedAt

	visited := map[string]int{child.UID: 0}
	queue := []node{{checkUID: child.UID, depth: 0}}

	var (
		best      *models.Incident
		bestDepth int
	)

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.depth >= rollupDepthCap {
			continue
		}

		parents, err := s.db.ListCheckDependencyParents(ctx, cur.checkUID)
		if err != nil {
			slog.WarnContext(ctx, "Failed to list parents during rollup walk",
				"checkUid", cur.checkUID, "error", err)

			return nil, 0
		}

		hardParents := make([]string, 0, len(parents))

		for _, dep := range parents {
			if dep.Kind != models.CheckDependencyKindHard {
				continue
			}

			if _, ok := visited[dep.ParentCheckUID]; ok {
				continue
			}

			visited[dep.ParentCheckUID] = cur.depth + 1
			hardParents = append(hardParents, dep.ParentCheckUID)
			queue = append(queue, node{checkUID: dep.ParentCheckUID, depth: cur.depth + 1})
		}

		if len(hardParents) == 0 {
			continue
		}

		incidents, err := s.db.FindActiveIncidentsForChecksInWindow(ctx, hardParents, since, until)
		if err != nil {
			slog.WarnContext(ctx, "Failed to query parent incidents during rollup walk",
				"error", err)

			return nil, 0
		}

		for _, inc := range incidents {
			candidateDepth := visited[inc.CheckUID]
			if best == nil || candidateDepth > bestDepth ||
				(candidateDepth == bestDepth && inc.StartedAt.Before(best.StartedAt)) {
				best = inc
				bestDepth = candidateDepth
			}
		}
	}

	return best, bestDepth
}

// correlationWindow returns the lookback window for a child's check; the spec
// uses max(2 * period, 5min).
func correlationWindow(check *models.Check) time.Duration {
	period := time.Duration(check.Period)
	doubled := 2 * period

	if doubled < rollupMinWindow {
		return rollupMinWindow
	}

	return doubled
}

// reEvaluateRollupChildren is called after a parent incident resolves. It
// walks suppressed children and either:
//   - the child has recovered → emit a rollup_detached event and clear the
//     attribution (don't page).
//   - the child is still down → flip paging_suppressed = false and queue
//     the notifications now.
func (s *Service) reEvaluateRollupChildren(
	ctx context.Context, parent *models.Incident,
) error {
	if parent == nil {
		return nil
	}

	children, err := s.db.ListSuppressedChildIncidents(ctx, parent.UID)
	if err != nil {
		return fmt.Errorf("list suppressed children: %w", err)
	}

	for _, child := range children {
		if err := s.reEvaluateChild(ctx, child); err != nil {
			slog.WarnContext(ctx, "Failed to re-evaluate rollup child",
				"childIncidentUid", child.UID, "error", err)
		}
	}

	return nil
}

func (s *Service) reEvaluateChild(ctx context.Context, child *models.Incident) error {
	check, err := s.db.GetCheck(ctx, child.OrganizationUID, child.CheckUID)
	if err != nil {
		return fmt.Errorf("get child check: %w", err)
	}

	if check.Status != models.CheckStatusDown {
		return s.markRollupDetached(ctx, child)
	}

	suppressed := false
	update := models.IncidentUpdate{PagingSuppressed: &suppressed}

	if err := s.db.UpdateIncident(ctx, child.UID, &update); err != nil {
		return fmt.Errorf("clear suppressed: %w", err)
	}

	child.PagingSuppressed = false

	payload := models.JSONMap{
		keyCheckUID:  check.UID,
		keyCheckSlug: derefSlug(check.Slug),
	}

	if err := s.emitEvent(ctx, child.OrganizationUID, models.EventTypeIncidentReopened, child, payload); err != nil {
		return fmt.Errorf("emit reopened event: %w", err)
	}

	return nil
}

func (s *Service) markRollupDetached(ctx context.Context, child *models.Incident) error {
	suppressed := false
	update := models.IncidentUpdate{
		PagingSuppressed:         &suppressed,
		ClearCausedByIncidentUID: true,
	}

	if err := s.db.UpdateIncident(ctx, child.UID, &update); err != nil {
		return fmt.Errorf("clear rollup attribution: %w", err)
	}

	return nil
}

// derefSlug returns "" for a nil slug pointer; checks always have a slug in
// practice but the model is nullable.
func derefSlug(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
