package incidentnotifications_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentnotifications"
)

// notifTestSetup spins up an in-memory sqlite, an org, a check, an active
// incident and a single sent notification row. Returns the bits the
// GetForIncident tests need.
type notifTestSetup struct {
	svc      *incidentnotifications.Service
	dbSvc    *sqlite.Service
	org      *models.Organization
	incident *models.Incident
	notif    *models.IncidentNotification
}

func newNotifTestSetup(t *testing.T) *notifTestSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("notif-test", "Notif Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-5*time.Minute), "api is down")
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	user := models.NewUser("alice@example.com")
	user.Name = "Alice"
	r.NoError(dbSvc.CreateUser(ctx, user))

	notif := models.NewIncidentNotificationForUser(
		org.UID, inc.UID, "incident.created", models.IncidentNotificationSourceEscalationUser,
		user.UID, "email", nil, nil,
	)
	sentAt := time.Now()
	notif.Status = models.IncidentNotificationStatusSent
	notif.SentAt = &sentAt
	msgID := "msg-123"
	notif.MessageID = &msgID
	job := "job-abc"
	notif.JobUID = &job
	r.NoError(dbSvc.CreateIncidentNotification(ctx, notif))

	return &notifTestSetup{
		svc:      incidentnotifications.NewService(dbSvc),
		dbSvc:    dbSvc,
		org:      org,
		incident: inc,
		notif:    notif,
	}
}

// TestGetForIncidentFound returns the full detail, including the fields that
// the list endpoint hides (jobUid) and the joined user sub-object.
func TestGetForIncidentFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newNotifTestSetup(t)

	detail, err := s.svc.GetForIncident(t.Context(), s.org.UID, s.incident.UID, s.notif.UID)
	r.NoError(err)
	r.NotNil(detail)
	r.Equal(s.notif.UID, detail.UID)
	r.Equal(s.incident.UID, detail.IncidentUID)
	r.Equal(models.IncidentNotificationStatusSent, detail.Status)
	r.NotNil(detail.SentAt)
	r.NotNil(detail.JobUID)
	r.Equal("job-abc", *detail.JobUID)
	r.NotNil(detail.MessageID)
	r.Equal("msg-123", *detail.MessageID)
	r.NotNil(detail.User)
	r.Equal("Alice", detail.User.Name)
	// A sent notification has no failed/canceled timestamps.
	r.Nil(detail.FailedAt)
	r.Nil(detail.CancelledAt)
}

// TestGetForIncidentNotFound maps an unknown notification UID to
// ErrNotificationNotFound (→ 404 in the handler).
func TestGetForIncidentNotFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newNotifTestSetup(t)

	_, err := s.svc.GetForIncident(t.Context(), s.org.UID, s.incident.UID, "does-not-exist")
	r.ErrorIs(err, incidentnotifications.ErrNotificationNotFound)
}

// TestGetForIncidentUnknownIncident maps an unknown incident UID to
// ErrIncidentNotFound (→ 404), never leaking the notification.
func TestGetForIncidentUnknownIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newNotifTestSetup(t)

	_, err := s.svc.GetForIncident(t.Context(), s.org.UID, "no-such-incident", s.notif.UID)
	r.ErrorIs(err, incidentnotifications.ErrIncidentNotFound)
}

// TestGetForIncidentWrongOrg returns 404 (not 403) so existence is not leaked
// across tenants: the notification exists, but not in the queried org.
func TestGetForIncidentWrongOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newNotifTestSetup(t)

	otherOrg := models.NewOrganization("other-org", "Other Org")
	r.NoError(s.dbSvc.CreateOrganization(t.Context(), otherOrg))

	// The incident does not belong to otherOrg → ErrIncidentNotFound, 404.
	_, err := s.svc.GetForIncident(t.Context(), otherOrg.UID, s.incident.UID, s.notif.UID)
	r.ErrorIs(err, incidentnotifications.ErrIncidentNotFound)
}

// TestGetForIncidentWrongIncident treats a notification queried under the wrong
// (but existing) incident as not found — the row exists, just not under that
// incident.
func TestGetForIncidentWrongIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newNotifTestSetup(t)

	check2 := models.NewCheck(s.org.UID, "api2", "http")
	r.NoError(s.dbSvc.CreateCheck(t.Context(), check2))

	otherInc := models.NewIncident(s.org.UID, check2.UID, time.Now().Add(-time.Minute), "other")
	r.NoError(s.dbSvc.CreateIncident(t.Context(), otherInc))

	_, err := s.svc.GetForIncident(t.Context(), s.org.UID, otherInc.UID, s.notif.UID)
	r.ErrorIs(err, incidentnotifications.ErrNotificationNotFound)
}
