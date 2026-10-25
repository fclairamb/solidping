package incidents

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ResolveOnCheckDisable closes every active incident (outage and degraded) of a
// check the user just disabled with resolveOpenIncidents (spec 2026-10-23-01).
// Without it they would stay active forever: a disabled check has no jobs, so
// no result ever arrives to auto-resolve them.
//
// Incidents resolve with resolution_type = "check_disabled" and resolved_by =
// the caller when known, so history and notifications say the check was
// disabled rather than that it recovered. Returns the UIDs it resolved.
func (s *Service) ResolveOnCheckDisable(ctx context.Context, orgUID, checkUID string) ([]string, error) {
	active, _, err := s.db.ListIncidents(ctx, &models.ListIncidentsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       []string{checkUID},
		States:          []models.IncidentState{models.IncidentStateActive},
	})
	if err != nil {
		return nil, fmt.Errorf("list active incidents: %w", err)
	}

	if len(active) == 0 {
		return nil, nil
	}

	check, checkErr := s.db.GetCheck(ctx, orgUID, checkUID)
	if checkErr != nil {
		check = nil
	}

	actorUID := audit.ActorFromContext(ctx).UserUID
	resolved := make([]string, 0, len(active))

	for _, incident := range active {
		if err := s.resolveForCheckDisable(ctx, orgUID, incident, check, actorUID); err != nil {
			return resolved, err
		}

		resolved = append(resolved, incident.UID)
	}

	return resolved, nil
}

func (s *Service) resolveForCheckDisable(
	ctx context.Context, orgUID string, incident *models.Incident, check *models.Check, actorUID string,
) error {
	now := s.clock.Now()
	state := models.IncidentStateResolved
	resolutionType := models.ResolutionTypeCheckDisabled
	update := models.IncidentUpdate{
		State:          &state,
		ResolvedAt:     &now,
		ResolutionType: &resolutionType,
	}

	if actorUID != "" {
		update.ResolvedBy = &actorUID
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("resolve incident %s: %w", incident.UID, err)
	}

	incident.State = state
	incident.ResolvedAt = &now
	incident.ResolutionType = &resolutionType
	incident.ResolvedBy = update.ResolvedBy

	// Cancel BEFORE emitting, or the sweep also drops the resolved
	// notifications about to be queued.
	s.cancelPendingNotifications(ctx, incident.UID, nil)

	payload := models.JSONMap{
		keyCheckUID:        incident.CheckUID,
		keyResolutionType:  resolutionType,
		keyResolvedAt:      now,
		keyDurationSeconds: int64(now.Sub(incident.StartedAt).Seconds()),
	}
	if check != nil {
		payload[keyCheckSlug] = check.Slug
		payload[keyCheckName] = check.Name
	}

	if actorUID != "" {
		payload[payloadKeyActorUID] = actorUID
	}

	if err := s.emitEvent(ctx, orgUID, models.EventTypeIncidentResolved, incident, payload); err != nil {
		slog.WarnContext(ctx, "Failed to emit check-disabled resolve event",
			"incidentUid", incident.UID, "error", err)
	}

	s.publishResolved(ctx, incident)

	return nil
}
