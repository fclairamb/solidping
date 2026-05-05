// Package activation tracks one-time activation milestones per organization
// and surfaces them as audit events. Each milestone fires at most once per
// org — the helper is safe to call from request paths and from background
// jobs without coordination.
package activation

import (
	"context"
	"errors"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// SourceLabel identifies the path that produced the activation event.
type SourceLabel string

// Standard source labels.
const (
	SourceEmptyState  SourceLabel = "empty_state"
	SourceRegularForm SourceLabel = "regular_form"
	SourceImport      SourceLabel = "import"
	SourceAPI         SourceLabel = "api"
	SourceSystem      SourceLabel = "system"
)

// ErrInvalidMilestone is returned when the supplied event type is not an
// org.activation.* type.
var ErrInvalidMilestone = errors.New("activation: not an activation milestone")

// Emit records a one-time activation milestone for org. If a row of the
// same (organizationUID, milestone) already exists the call is a no-op.
// Errors from the underlying store are logged but never returned — callers
// in hot paths must not bubble activation failures up.
func Emit(
	ctx context.Context,
	dbSvc db.Service,
	orgUID string,
	milestone models.EventType,
	source SourceLabel,
	actorUserUID string,
) {
	if !isActivationMilestone(milestone) {
		slog.WarnContext(ctx, "activation: refusing to emit non-activation event",
			"event_type", string(milestone))

		return
	}

	if orgUID == "" {
		return
	}

	already, err := alreadyEmitted(ctx, dbSvc, orgUID, milestone)
	if err != nil {
		slog.WarnContext(ctx, "activation: lookup failed", "error", err,
			"org_uid", orgUID, "milestone", string(milestone))
		// Best-effort: insertion below may still succeed.
	}
	if already {
		return
	}

	event := models.NewEvent(orgUID, milestone, models.ActorTypeSystem)
	if actorUserUID != "" {
		event.ActorType = models.ActorTypeUser
		event.ActorUID = &actorUserUID
	}
	event.Payload = models.JSONMap{
		"source": string(source),
	}

	if err := dbSvc.CreateEvent(ctx, event); err != nil {
		slog.WarnContext(ctx, "activation: emit failed", "error", err,
			"org_uid", orgUID, "milestone", string(milestone))
	}
}

// alreadyEmitted is true when an event of the given type already exists for
// the org. Activation milestones are monotonic so existence is enough.
func alreadyEmitted(
	ctx context.Context, dbSvc db.Service, orgUID string, milestone models.EventType,
) (bool, error) {
	events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: orgUID,
		EventTypes:      []models.EventType{milestone},
		Limit:           1,
	})
	if err != nil {
		return false, err
	}

	return len(events) > 0, nil
}

func isActivationMilestone(t models.EventType) bool {
	_, ok := activationMilestones[t]
	return ok
}

var activationMilestones = map[models.EventType]struct{}{ //nolint:gochecknoglobals
	models.EventTypeOrgActivationSignupCompleted:             {},
	models.EventTypeOrgActivationFirstCheckCreated:           {},
	models.EventTypeOrgActivationFirstResultReceived:         {},
	models.EventTypeOrgActivationFirstNotificationConfigured: {},
	models.EventTypeOrgActivationFirstIncidentPaged:          {},
}
