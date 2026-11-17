package members

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestRemoveMemberEndsContactsInThatOrg: removing a member deletes their
// contacts and notification routes in the org they were removed from, keeps
// the ones in an org they still belong to, and records the count in the audit
// entry of the removal.
func TestRemoveMemberEndsContactsInThatOrg(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupMembersTest(t)
	r := require.New(t)

	orgA, membersA := seedOrg(ctx, t, dbService, "acme-a", map[string]models.MemberRole{
		"owner@acme.com": models.MemberRoleOwner,
		"user@acme.com":  models.MemberRoleUser,
	})
	removed := membersA["user@acme.com"]

	orgB := models.NewOrganization("acme-b", "acme-b")
	r.NoError(dbService.CreateOrganization(ctx, orgB))
	r.NoError(dbService.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(orgB.UID, removed.UserUID, models.MemberRoleUser)))

	contactA := models.NewUserContact(removed.UserUID, orgA.UID, models.UserContactTypeTelegram, "4242", "tg")
	r.NoError(dbService.UpsertUserContact(ctx, contactA))
	r.NoError(dbService.EnsureUserNotificationRoute(ctx, removed.UserUID, orgA.UID, contactA.UID))

	contactB := models.NewUserContact(removed.UserUID, orgB.UID, models.UserContactTypeTelegram, "4242", "tg")
	r.NoError(dbService.UpsertUserContact(ctx, contactB))
	r.NoError(dbService.EnsureUserNotificationRoute(ctx, removed.UserUID, orgB.UID, contactB.UID))

	r.NoError(svc.RemoveMember(ctx, orgA.Slug, removed.UID, callerFor(membersA["owner@acme.com"])))

	_, err := dbService.GetUserContact(ctx, contactA.UID)
	r.Error(err, "the contact in the org the member left must be deleted")

	routesA, err := dbService.ListUserContactsWithRoutes(ctx, removed.UserUID, orgA.UID)
	r.NoError(err)
	r.Empty(routesA)

	// Positive control: org B membership is untouched, and so is its contact.
	_, err = dbService.GetUserContact(ctx, contactB.UID)
	r.NoError(err)

	routesB, err := dbService.ListUserContactsWithRoutes(ctx, removed.UserUID, orgB.UID)
	r.NoError(err)
	r.Len(routesB, 1)

	events, err := dbService.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: orgA.UID,
		EventTypes:      []models.EventType{models.EventTypeMemberRemoved},
	})
	r.NoError(err)
	r.Len(events, 1)
	r.EqualValues(1, events[0].Payload[fieldContactsRemoved])
}
