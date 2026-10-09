package incidents

import (
	"context"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// NotifyCheckDeletedIncidents sends the one "resolved, check deleted"
// notification for each incident a single-check delete closed (spec
// 2026-10-08-02, resolved open question). The DB layer's DeleteCheck already
// resolved them with resolution_type = 'check_deleted' in the same transaction
// as the soft delete; this cancels their pending escalation steps and routes
// an incident.resolved event through the usual fan-out, so a pager that was
// alerted is told the incident is closed.
//
// Each incident is re-read first: only one still carrying check_deleted is
// notified, so an incident somebody else resolved in between (and notified for)
// is never announced twice. Org deletion does not call this: nobody is left to
// read it, and its integrations are being deleted too.
func (s *Service) NotifyCheckDeletedIncidents(
	ctx context.Context, orgUID string, check *models.Check, incidentUIDs []string,
) {
	for _, uid := range incidentUIDs {
		incident, err := s.db.GetIncident(ctx, orgUID, uid)
		if err != nil {
			slog.WarnContext(ctx, "Failed to load incident closed by check deletion",
				"incidentUid", uid, "error", err)

			continue
		}

		if !resolvedByCheckDeletion(incident) {
			continue
		}

		// Same ordering rule as the manual resolve path: cancel BEFORE emitting,
		// or the sweep also drops the resolved notifications about to be queued.
		s.cancelPendingNotifications(ctx, incident.UID, nil)

		payload := models.JSONMap{
			keyCheckUID:       incident.CheckUID,
			keyResolutionType: models.ResolutionTypeCheckDeleted,
		}
		if check != nil {
			payload[keyCheckSlug] = check.Slug
			payload[keyCheckName] = check.Name
		}
		if incident.ResolvedAt != nil {
			payload[keyResolvedAt] = *incident.ResolvedAt
			payload[keyDurationSeconds] = int64(incident.ResolvedAt.Sub(incident.StartedAt).Seconds())
		}

		if err := s.emitEvent(ctx, orgUID, models.EventTypeIncidentResolved, incident, payload); err != nil {
			slog.WarnContext(ctx, "Failed to emit check-deleted resolve event",
				"incidentUid", incident.UID, "error", err)
		}

		s.publishResolved(ctx, incident)
	}
}

func resolvedByCheckDeletion(incident *models.Incident) bool {
	return incident.State == models.IncidentStateResolved &&
		incident.ResolutionType != nil &&
		*incident.ResolutionType == models.ResolutionTypeCheckDeleted
}
