package incidents

import (
	"context"
	"log/slog"
	"slices"
	"sort"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// failedComponentsDetailsKey holds, on a health check's incident, the names
	// of the components that were failing at the last result seen by the
	// incident. It is what a later result is compared against.
	failedComponentsDetailsKey = "failedComponents"

	// outputKeyFailedComponents is the health check's Output key listing the
	// failing component names.
	outputKeyFailedComponents = "failed"
)

// failedComponentsFromResult returns the sorted failing component names of a
// health result, and whether the result carries that list at all.
func failedComponentsFromResult(result *models.Result) ([]string, bool) {
	if result == nil || result.Output == nil {
		return nil, false
	}

	return stringList(result.Output[outputKeyFailedComponents])
}

// stringList reads a JSON-ish list of strings, whether it is still the
// checker's []string or came back from the database as []any. The names are
// returned sorted.
func stringList(raw any) ([]string, bool) {
	var names []string

	switch typed := raw.(type) {
	case []string:
		names = slices.Clone(typed)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				names = append(names, text)
			}
		}
	default:
		return nil, false
	}

	sort.Strings(names)

	return names, true
}

// withFailedComponents returns the incident details to store at opening: for
// a health check, the failing component names.
func withFailedComponents(check *models.Check, result *models.Result, details models.JSONMap) models.JSONMap {
	if check.Type != string(checkerdef.CheckTypeHealth) {
		return details
	}

	if names, ok := failedComponentsFromResult(result); ok {
		details[failedComponentsDetailsKey] = names
	}

	return details
}

// diffComponents returns what is in next and not in prev (added), and the
// reverse (removed).
func diffComponents(prev, next []string) ([]string, []string) {
	var added, removed []string

	for _, name := range next {
		if !slices.Contains(prev, name) {
			added = append(added, name)
		}
	}

	for _, name := range prev {
		if !slices.Contains(next, name) {
			removed = append(removed, name)
		}
	}

	return added, removed
}

// recordComponentChange puts an `incident.components_changed` event on an open
// health incident's timeline when the set of failing components differs from
// the one the incident last saw, and stores the new set. It never pages. The
// incident engine otherwise only counts failures, so without this a second
// component going down during an incident would be silent.
//
// It adds the new details to update when the set changed. Best-effort: a missing
// timeline line must not fail the result.
func (s *Service) recordComponentChange(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
	update *models.IncidentUpdate,
) {
	if check.Type != string(checkerdef.CheckTypeHealth) {
		return
	}

	next, ok := failedComponentsFromResult(result)
	if !ok {
		return
	}

	prev, _ := stringList(incident.Details[failedComponentsDetailsKey])
	if slices.Equal(prev, next) {
		return
	}

	details := make(models.JSONMap, len(incident.Details)+1)
	for key, value := range incident.Details {
		details[key] = value
	}

	details[failedComponentsDetailsKey] = next
	update.Details = &details

	// An incident that predates this tracking has nothing to compare with: the
	// set is recorded silently.
	if _, tracked := incident.Details[failedComponentsDetailsKey]; !tracked {
		return
	}

	added, removed := diffComponents(prev, next)
	payload := models.JSONMap{
		keyCheckUID:    check.UID,
		keyCheckSlug:   derefSlug(check.Slug),
		"added":        added,
		"removed":      removed,
		"failed":       next,
		keyResultUID:   result.UID,
		"failureError": failureReasonFromResult(result),
	}

	err := s.emitEvent(ctx, check.OrganizationUID, models.EventTypeIncidentComponentsChanged, incident, payload)
	if err != nil {
		slog.WarnContext(ctx, "Failed to record a component change on the incident timeline",
			"checkUID", check.UID, "incidentUid", incident.UID, "error", err)
	}
}
