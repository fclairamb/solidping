package incidents

import (
	"context"
	"database/sql"
	"errors"
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

	slog.DebugContext(ctx, "Rolling up incident under parent",
		"childIncidentUid", incident.UID,
		"rootIncidentUid", rootUID,
		"depth", depth,
	)
}

// findRollupRoot walks parents BFS up to depthCap, restricting to fully-hard
// paths. For every reached ancestor it queries open non-suppressed incidents
// inside the correlation window. Returns the deepest such incident (oldest
// startedAt as tiebreak) and its depth from the original child.
//
//nolint:cyclop // BFS with bookkeeping; splitting hurts readability.
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

// applyRollupToCheck loads the check from the DB and runs applyRollup. Used
// from paths that don't already have the check loaded.
func (s *Service) applyRollupToCheck(
	ctx context.Context, orgUID string, incident *models.Incident,
) {
	check, err := s.db.GetCheck(ctx, orgUID, incident.CheckUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return
		}

		slog.WarnContext(ctx, "Failed to load check for rollup", "error", err)

		return
	}

	s.applyRollup(ctx, check, incident)
}
