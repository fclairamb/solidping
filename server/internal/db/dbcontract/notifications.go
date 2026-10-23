package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runUserContacts(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	user := models.NewUser("contacts@acme.com")
	must.NoError(svc.CreateUser(ctx, user))

	// Default email route is idempotent.
	must.NoError(svc.EnsureDefaultEmailRoute(ctx, user.UID, fix.org.UID, "contacts@acme.com"))
	must.NoError(svc.EnsureDefaultEmailRoute(ctx, user.UID, fix.org.UID, "contacts@acme.com"))

	routes, err := svc.ListUserContactsWithRoutes(ctx, user.UID, fix.org.UID)
	must.NoError(err)
	must.Len(routes, 1)
	must.NotNil(routes[0].Contact)
	must.Equal("contacts@acme.com", routes[0].Contact.Value)
	must.NotNil(routes[0].Contact.VerifiedAt)

	// Deleting the default contact removes its route and is not resurrected
	// by the next EnsureDefaultEmailRoute.
	must.NoError(svc.DeleteUserContact(ctx, routes[0].ContactUID))
	routes, err = svc.ListUserContactsWithRoutes(ctx, user.UID, fix.org.UID)
	must.NoError(err)
	must.Empty(routes)
	must.NoError(svc.EnsureDefaultEmailRoute(ctx, user.UID, fix.org.UID, "contacts@acme.com"))
	routes, err = svc.ListUserContactsWithRoutes(ctx, user.UID, fix.org.UID)
	must.NoError(err)
	must.Empty(routes)

	// Explicit contact + route.
	contact := models.NewUserContact(user.UID, fix.org.UID, models.UserContactTypeTelegram, "chat-42", "tg")
	must.NoError(svc.UpsertUserContact(ctx, contact))
	must.NoError(svc.EnsureUserNotificationRoute(ctx, user.UID, fix.org.UID, contact.UID))
	must.NoError(svc.EnsureUserNotificationRoute(ctx, user.UID, fix.org.UID, contact.UID))

	routes, err = svc.ListUserContactsWithRoutes(ctx, user.UID, fix.org.UID)
	must.NoError(err)
	must.Len(routes, 1)

	must.NoError(svc.SetRouteEnabled(ctx, routes[0].UID, false))
	must.NoError(svc.ReorderRoutes(ctx, user.UID, fix.org.UID, []string{routes[0].UID}))

	got, err := svc.GetUserContact(ctx, contact.UID)
	must.NoError(err)
	must.Nil(got.VerifiedAt)

	hash := "code-hash"
	expires := time.Now().Add(time.Hour)
	must.NoError(svc.SetUserContactVerifyState(ctx, contact.UID, &hash, &expires, 2))
	got, err = svc.GetUserContact(ctx, contact.UID)
	must.NoError(err)
	must.Equal(2, got.VerifyAttempts)
	must.NotNil(got.VerifyCodeHash)

	must.NoError(svc.MarkUserContactVerified(ctx, contact.UID, time.Now()))
	got, err = svc.GetUserContact(ctx, contact.UID)
	must.NoError(err)
	must.NotNil(got.VerifiedAt)
	must.Nil(got.VerifyCodeHash)
	must.Zero(got.VerifyAttempts)

	must.NoError(svc.ClearUserContactVerified(ctx, contact.UID))
	got, err = svc.GetUserContact(ctx, contact.UID)
	must.NoError(err)
	must.Nil(got.VerifiedAt)

	must.NoError(svc.SetUserContactDMChannel(ctx, contact.UID, "dm-1"))

	byValue, err := svc.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, "chat-42")
	must.NoError(err)
	must.Len(byValue, 1)

	must.NoError(svc.DeleteUserContact(ctx, contact.UID))
	_, err = svc.GetUserContact(ctx, contact.UID)
	must.Error(err)

	byValue, err = svc.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, "chat-42")
	must.NoError(err)
	must.Empty(byValue)
}

func runIncidentNotifications(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	incident := models.NewIncident(fix.org.UID, fix.check.UID, time.Now(), "notif incident")
	must.NoError(svc.CreateIncident(ctx, incident))

	newNotif := func(channel string) *models.IncidentNotification {
		n := models.NewIncidentNotificationForUser(
			fix.org.UID, incident.UID, "opened",
			models.IncidentNotificationSourceEscalationUser, fix.user.UID, channel, nil, nil)
		must.NoError(svc.CreateIncidentNotification(ctx, n))

		return n
	}

	sent := newNotif("email")
	must.NoError(svc.MarkIncidentNotificationSentByUID(ctx, sent.UID, time.Now(), "msg-1"))

	failed := newNotif("slack")
	must.NoError(svc.MarkIncidentNotificationFailedByUID(ctx, failed.UID, time.Now(), "boom"))

	// Job-keyed variants.
	jobSent := newNotif("webhook")
	jobUID := "11111111-1111-4111-8111-111111111111"
	jobSent.JobUID = &jobUID
	_, err := svc.DB().NewUpdate().Model(jobSent).Column("job_uid").WherePK().Exec(ctx)
	must.NoError(err)
	details := &models.DeliveryDetails{HTTPStatusCode: 200, RequestURL: "https://acme.com/hook"}
	must.NoError(svc.MarkIncidentNotificationSentByJob(ctx, jobUID, time.Now(), "wamid.1", details))

	jobFailed := newNotif("webhook")
	jobFailedUID := "22222222-2222-4222-8222-222222222222"
	jobFailed.JobUID = &jobFailedUID
	_, err = svc.DB().NewUpdate().Model(jobFailed).Column("job_uid").WherePK().Exec(ctx)
	must.NoError(err)
	must.NoError(svc.MarkIncidentNotificationFailedByJob(ctx, jobFailedUID, time.Now(), "retry", true, nil))
	must.NoError(svc.MarkIncidentNotificationFailedByJob(ctx, jobFailedUID, time.Now(), "dead", false, details))

	// Delivery details by message id, in-org and any-org.
	must.NoError(svc.UpdateIncidentNotificationDeliveryByMessageID(
		ctx, fix.org.UID, "wamid.1", &models.DeliveryDetails{HTTPStatusCode: 202}))
	must.NoError(svc.UpdateIncidentNotificationDeliveryByMessageIDAnyOrg(
		ctx, "wamid.1", &models.DeliveryDetails{HTTPStatusCode: 204}))
	must.NoError(svc.UpdateIncidentNotificationDeliveryByMessageID(ctx, fix.org.UID, "", details))
	must.NoError(svc.UpdateIncidentNotificationDeliveryByMessageIDAnyOrg(ctx, "unknown", nil))

	// Listing with each filter.
	all, err := svc.ListIncidentNotifications(ctx, fix.org.UID, db.ListIncidentNotificationsFilter{
		IncidentUID: incident.UID,
	})
	must.NoError(err)
	must.Len(all, 4)

	byStatus, err := svc.ListIncidentNotifications(ctx, fix.org.UID, db.ListIncidentNotificationsFilter{
		IncidentUID: incident.UID, Status: models.IncidentNotificationStatusFailed,
	})
	must.NoError(err)
	must.Len(byStatus, 2)

	byUser, err := svc.ListIncidentNotifications(ctx, fix.org.UID, db.ListIncidentNotificationsFilter{
		UserUID: fix.user.UID, Limit: 1000,
	})
	must.NoError(err)
	must.Len(byUser, 4)
	must.NotNil(byUser[0].UserName)

	none, err := svc.ListIncidentNotifications(ctx, fix.org.UID, db.ListIncidentNotificationsFilter{
		ConnectionUID: "00000000-0000-4000-8000-000000000000",
		Before:        time.Now().Add(-time.Hour),
	})
	must.NoError(err)
	must.Empty(none)

	row, err := svc.GetIncidentNotification(ctx, fix.org.UID, incident.UID, sent.UID)
	must.NoError(err)
	must.Equal(models.IncidentNotificationStatusSent, row.Status)

	orgRow, err := svc.GetOrgNotification(ctx, fix.org.UID, sent.UID)
	must.NoError(err)
	must.Equal(sent.UID, orgRow.UID)

	// Cancel only touches pending rows.
	pending := newNotif("sms")
	canceled, err := svc.CancelIncidentNotificationsForIncident(ctx, incident.UID, time.Now())
	must.NoError(err)
	must.EqualValues(1, canceled)

	row, err = svc.GetIncidentNotification(ctx, fix.org.UID, incident.UID, pending.UID)
	must.NoError(err)
	must.Equal(models.IncidentNotificationStatusCanceled, row.Status)
}

func runPublicationsAndStatusUpdates(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc
	now := time.Now()

	page := models.NewStatusPage(fix.org.UID, "Pubs", "pubs")
	must.NoError(svc.CreateStatusPage(ctx, page))

	incident := models.NewIncident(fix.org.UID, fix.check.UID, now, "pub incident")
	must.NoError(svc.CreateIncident(ctx, incident))

	pub := models.NewIncidentPublication(fix.org.UID, page.UID, "Degraded", now)
	pub.IncidentUID = &incident.UID
	must.NoError(svc.CreateIncidentPublication(ctx, pub))

	got, err := svc.GetIncidentPublication(ctx, fix.org.UID, pub.UID)
	must.NoError(err)
	must.Equal("Degraded", got.PublicTitle)

	found, err := svc.FindIncidentPublication(ctx, incident.UID, page.UID)
	must.NoError(err)
	must.Equal(pub.UID, found.UID)

	count, err := svc.CountIncidentPublicationsForIncident(ctx, incident.UID)
	must.NoError(err)
	must.Equal(1, count)

	title := "Major outage"
	state := models.PublicationStateResolved
	severity := "major"
	resolved := now.Add(time.Minute)
	touched := now.Add(time.Second)
	windowCount := 3
	must.NoError(svc.UpdateIncidentPublication(ctx, pub.UID, &models.IncidentPublicationUpdate{
		PublicTitle: &title, PublicState: &state, Severity: &severity,
		HumanTouchedAt: &touched, ResolvedAt: &resolved,
		NotifyWindowStart: &now, NotifyWindowCount: &windowCount,
	}))

	got, err = svc.GetIncidentPublication(ctx, fix.org.UID, pub.UID)
	must.NoError(err)
	must.Equal(title, got.PublicTitle)
	must.Equal(state, got.PublicState)
	must.NotNil(got.Severity)
	must.NotNil(got.ResolvedAt)
	must.Equal(3, got.NotifyWindowCount)

	must.NoError(svc.UpdateIncidentPublication(ctx, pub.UID, &models.IncidentPublicationUpdate{
		ClearSeverity: true, ClearResolvedAt: true,
	}))
	got, err = svc.GetIncidentPublication(ctx, fix.org.UID, pub.UID)
	must.NoError(err)
	must.Nil(got.Severity)
	must.Nil(got.ResolvedAt)

	for _, f := range []*models.ListIncidentPublicationsFilter{
		{OrganizationUID: fix.org.UID},
		{OrganizationUID: fix.org.UID, StatusPageUID: page.UID, IncidentUID: incident.UID},
		{OrganizationUID: fix.org.UID, State: string(models.PublicationStateResolved), Limit: 500, Offset: 0},
		{OrganizationUID: fix.org.UID, ActiveOnly: true, Limit: 10, Offset: 0},
	} {
		list, listErr := svc.ListIncidentPublications(ctx, f)
		must.NoError(listErr)
		must.LessOrEqual(len(list), 1)
	}

	// Status updates threaded under the publication.
	update := models.NewStatusUpdate(fix.org.UID, page.UID, fix.user.UID)
	update.Title = "Investigating"
	update.BodyMarkdown = "We are looking"
	update.Kind = models.StatusUpdateKindInvestigating
	update.IncidentUID = &incident.UID
	update.IncidentPublicationUID = &pub.UID
	update.CheckUID = &fix.check.UID
	must.NoError(svc.CreateStatusUpdate(ctx, update))

	fetched, err := svc.GetStatusUpdateByUID(ctx, update.UID)
	must.NoError(err)
	must.Equal("Investigating", fetched.Title)

	fetched.Title = "Identified"
	fetched.Kind = models.StatusUpdateKindIdentified
	must.NoError(svc.UpdateStatusUpdate(ctx, fetched))

	filters := []models.StatusUpdatesFilter{
		{StatusPageUID: page.UID},
		{StatusPageUID: page.UID, CheckUID: &fix.check.UID, IncidentUID: &incident.UID, Limit: 1000},
		{IncidentPublicationUID: &pub.UID, Limit: 5, Offset: 0},
	}
	for i := range filters {
		list, listErr := svc.ListStatusUpdates(ctx, fix.org.UID, filters[i])
		must.NoError(listErr)
		must.Len(list, 1)
		must.Equal("Identified", list[0].Title)
	}

	empty, err := svc.ListStatusUpdates(ctx, fix.org.UID, models.StatusUpdatesFilter{Offset: 5})
	must.NoError(err)
	must.Empty(empty)

	must.NoError(svc.SoftDeleteStatusUpdate(ctx, update.UID))
	_, err = svc.GetStatusUpdateByUID(ctx, update.UID)
	must.Error(err)

	must.NoError(svc.SoftDeleteIncidentPublication(ctx, pub.UID))
	_, err = svc.GetIncidentPublication(ctx, fix.org.UID, pub.UID)
	must.Error(err)
	_, err = svc.FindIncidentPublication(ctx, incident.UID, page.UID)
	must.Error(err)
}
