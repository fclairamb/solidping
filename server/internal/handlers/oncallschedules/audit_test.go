package oncallschedules_test

// Overrides change who gets paged, so adding or removing one must leave an
// attributed row in the audit trail, and a refused delete must leave none.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/oncallschedules"
)

// actorCtx is the context a request carries once the auth middleware ran.
func actorCtx(t *testing.T, userUID string) context.Context {
	t.Helper()

	return audit.WithUser(t.Context(), userUID, models.ActorTypeUser)
}

// overrideAuditRows lists the oncall_schedule.updated rows about overrides,
// newest first.
func overrideAuditRows(t *testing.T, dbSvc db.Service, orgUID string) []*models.Event {
	t.Helper()

	rows, err := dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: orgUID,
		EventTypes:      []models.EventType{models.EventTypeOnCallScheduleUpdated},
		Limit:           100,
	})
	require.NoError(t, err)

	var overrides []*models.Event

	for _, row := range rows {
		if _, ok := row.Payload["override_action"]; ok {
			overrides = append(overrides, row)
		}
	}

	return overrides
}

func TestOverrideCreateAndDeleteAreAudited(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action string
		// run performs the change as the actor and returns the override.
		run func(t *testing.T, ctx context.Context, h *handlerSetup) *models.OnCallScheduleOverride
	}{
		{
			name:   "create",
			action: "created",
			run: func(t *testing.T, ctx context.Context, h *handlerSetup) *models.OnCallScheduleOverride {
				t.Helper()

				start := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
				override, err := h.svc.CreateOverride(ctx, &oncallschedules.CreateOverrideInput{
					OrganizationUID: h.acme.org.UID,
					ScheduleUID:     h.acme.schedule.UID,
					UserUID:         h.acme.user.UID,
					StartAt:         start,
					EndAt:           start.Add(2 * time.Hour),
					Reason:          "covering a shift",
				})
				require.NoError(t, err)

				return override
			},
		},
		{
			name:   "delete",
			action: "deleted",
			run: func(t *testing.T, ctx context.Context, h *handlerSetup) *models.OnCallScheduleOverride {
				t.Helper()

				require.NoError(t, h.svc.DeleteOverride(ctx,
					h.acme.org.UID, h.acme.schedule.UID, h.acme.override.UID))

				return h.acme.override
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			h := newHandlerSetup(t)
			before := len(overrideAuditRows(t, h.dbSvc, h.acme.org.UID))

			override := tc.run(t, actorCtx(t, h.acme.user.UID), h)

			rows := overrideAuditRows(t, h.dbSvc, h.acme.org.UID)
			r.Len(rows, before+1, "exactly one audit row per override change")

			var row *models.Event

			for _, candidate := range rows {
				if candidate.Payload["override_uid"] == override.UID && candidate.Payload["override_action"] == tc.action {
					row = candidate
				}
			}

			r.NotNil(row, "no %s audit row for the override", tc.action)
			r.NotNil(row.ActorUID)
			r.Equal(h.acme.user.UID, *row.ActorUID)
			r.Equal("oncall_schedule", row.Payload[audit.PayloadKeyTargetType])
			r.Equal(h.acme.schedule.UID, row.Payload[audit.PayloadKeyTargetUID])
			r.Contains(row.Payload[audit.PayloadKeyChangedFields], "overrides")
			r.Equal(tc.action, row.Payload["override_action"])
			r.Equal(override.UID, row.Payload["override_uid"])
			r.Equal(override.UserUID, row.Payload["override_user_uid"])
			r.Equal(override.StartAt.UTC().Format(time.RFC3339), row.Payload["override_start_at"])
			r.Equal(override.EndAt.UTC().Format(time.RFC3339), row.Payload["override_end_at"])
			r.NotContains(row.Payload, "reason", "the free-text reason stays out of the trail")
		})
	}
}

func TestOverrideRefusedDeleteIsNotAudited(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)
	ctx := actorCtx(t, h.other.user.UID)

	acmeBefore := len(overrideAuditRows(t, h.dbSvc, h.acme.org.UID))
	otherBefore := len(overrideAuditRows(t, h.dbSvc, h.other.org.UID))

	err := h.svc.DeleteOverride(ctx, h.other.org.UID, h.other.schedule.UID, h.acme.override.UID)
	r.ErrorIs(err, oncallschedules.ErrOverrideNotFound)
	r.Len(overrideAuditRows(t, h.dbSvc, h.acme.org.UID), acmeBefore)
	r.Len(overrideAuditRows(t, h.dbSvc, h.other.org.UID), otherBefore)

	// Positive control: the same caller deleting its own override is recorded.
	r.NoError(h.svc.DeleteOverride(ctx, h.other.org.UID, h.other.schedule.UID, h.other.override.UID))
	r.Len(overrideAuditRows(t, h.dbSvc, h.other.org.UID), otherBefore+1)
}
