package incidents_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// TestTelegramWritesEnforceTheMemberFloor: a Telegram ack or comment is
// attributed to the linked SolidPing user, so it gets the same floor as the
// web routes: a live, joined membership at role `user` or above. A viewer or a
// non-member changes nothing.
func TestTelegramWritesEnforceTheMemberFloor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		role    models.MemberRole // "" means no membership at all
		joined  bool
		removed bool
		wantErr error
	}{
		{name: "viewer is read-only", role: models.MemberRoleViewer, joined: true, wantErr: incidents.ErrRoleReadOnly},
		{name: "non-member", wantErr: incidents.ErrNotOrgMember},
		{name: "removed member", role: models.MemberRoleUser, joined: true, removed: true,
			wantErr: incidents.ErrNotOrgMember},
		{name: "pending invite", role: models.MemberRoleUser, wantErr: incidents.ErrNotOrgMember},
		{name: "user may write", role: models.MemberRoleUser, joined: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := t.Context()
			s := newResolveSetup(t)

			user := models.NewUser("floor@acme.com")
			r.NoError(s.dbSvc.CreateUser(ctx, user))

			if tc.role != "" {
				member := models.NewOrganizationMember(s.org.UID, user.UID, tc.role)
				if !tc.joined {
					member.JoinedAt = nil
				}

				r.NoError(s.dbSvc.CreateOrganizationMember(ctx, member))

				if tc.removed {
					r.NoError(s.dbSvc.DeleteOrganizationMember(ctx, member.UID))
				}
			}

			_, ackErr := s.svc.AcknowledgeIncidentFromTelegram(
				ctx, s.org.UID, s.incident.UID, user.UID, "via Telegram")
			_, commentErr := s.svc.AddCommentFromTelegram(
				ctx, s.org.UID, s.incident.UID, "on it", user.UID, "via Telegram", "4242")

			got, err := s.dbSvc.GetIncident(ctx, s.org.UID, s.incident.UID)
			r.NoError(err)

			comments, err := s.dbSvc.ListEvents(ctx, &models.ListEventsFilter{
				OrganizationUID: s.org.UID,
				IncidentUID:     &s.incident.UID,
				EventTypes:      []models.EventType{models.EventTypeIncidentComment},
			})
			r.NoError(err)

			if tc.wantErr != nil {
				r.ErrorIs(ackErr, tc.wantErr)
				r.ErrorIs(commentErr, tc.wantErr)
				r.Nil(got.AcknowledgedAt, "a refused ack must leave the incident unacknowledged")
				r.Empty(comments, "a refused comment must not reach the timeline")

				return
			}

			r.NoError(ackErr)
			r.NoError(commentErr)
			r.NotNil(got.AcknowledgedAt)
			r.Len(comments, 1)
		})
	}
}
