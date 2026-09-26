package checks

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// resolveRegionsForType is ResolveRegionsForCheck with the passive rule in
// front of it (spec 2026-09-25-04): a passive check (heartbeat, email) has no
// regions. It makes no outbound request, its evaluation reads the database on
// the jobs node, and a region only gave its evaluator a place to die (a dark
// region silenced the dead-man's switch) or an agent to fail on (every
// evaluation an error and an incident).
//
// An explicit list is ACCEPTED AND DROPPED, never rejected, and the org's
// default regions are never applied: an existing config-as-code file that
// names a region on a heartbeat keeps applying, and the stored row, the
// response and the diff all show the empty list.
//
// Every create/update/plan/diff path resolves regions through this helper, so
// the API, the MCP tools and config-as-code cannot disagree.
func (s *Service) resolveRegionsForType(
	ctx context.Context, checkType string, requested []string, orgUID string,
) ([]string, error) {
	if checkerdef.CheckType(checkType).IsPassive() {
		return []string{}, nil
	}

	return s.regions.ResolveRegionsForCheck(ctx, requested, orgUID)
}
