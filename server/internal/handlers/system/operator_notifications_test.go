package system

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// opsEnv is a system service backed by a real database.
type opsEnv struct {
	svc *Service
	db  db.Service
	org *models.Organization
}

func newOpsEnv(t *testing.T) *opsEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &opsEnv{svc: NewService(dbSvc), db: dbSvc, org: org}
}

func (e *opsEnv) user(t *testing.T, email string, superAdmin, withRoute bool) *models.User {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	user := models.NewUser(email)
	user.SuperAdmin = superAdmin
	r.NoError(e.db.CreateUser(ctx, user))
	r.NoError(e.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(e.org.UID, user.UID, models.MemberRoleAdmin)))

	if withRoute {
		r.NoError(e.db.EnsureDefaultEmailRoute(ctx, user.UID, e.org.UID, email))
	}

	return user
}

func recipientByEmail(resp *OperatorNotificationsResponse, email string) *OperatorNotificationRecipient {
	for i := range resp.Recipients {
		if resp.Recipients[i].Email == email {
			return &resp.Recipients[i]
		}
	}

	return nil
}

// TestOperatorNotificationsListsSuperAdminsAndTheirRoutes: the page must be
// able to show "subscribed, but unreachable" without extra calls, because a
// recipient with no route is the likeliest silent failure of the feature.
func TestOperatorNotificationsListsSuperAdminsAndTheirRoutes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newOpsEnv(t)

	env.user(t, "alice@acme.com", true, true)
	env.user(t, "bob@acme.com", true, false)
	env.user(t, "carol@acme.com", false, true)

	resp, err := env.svc.GetOperatorNotifications(t.Context())
	r.NoError(err)

	r.False(resp.Enabled, "an instance that never opted in is off")
	r.Equal(opsnotify.SubscribableEvents(), resp.Events)
	r.Len(resp.Recipients, 2, "only super admins are candidates")

	alice := recipientByEmail(resp, "alice@acme.com")
	r.NotNil(alice)
	r.Equal([]string{models.UserContactTypeEmail}, alice.Routes)

	bob := recipientByEmail(resp, "bob@acme.com")
	r.NotNil(bob)
	r.Empty(bob.Routes, "the no-routes warning case is visible in the payload")

	r.Nil(recipientByEmail(resp, "carol@acme.com"), "a regular user is not a candidate")
}

// TestOperatorNotificationsSurfacesAStaleSubscription: someone who lost
// super_admin while still named in the parameter is already being skipped at
// delivery. The row exists so an operator can see WHY.
func TestOperatorNotificationsSurfacesAStaleSubscription(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newOpsEnv(t)
	ctx := t.Context()

	demoted := env.user(t, "bob@acme.com", false, true)

	r.NoError(env.db.SetSystemParameter(ctx, opsnotify.ParamOperatorNotifications, map[string]any{
		"enabled": true,
		"recipients": []map[string]any{
			{"userUid": demoted.UID, "events": []string{opsnotify.EventSupportMessage}},
		},
	}, false))

	resp, err := env.svc.GetOperatorNotifications(ctx)
	r.NoError(err)

	bob := recipientByEmail(resp, "bob@acme.com")
	r.NotNil(bob, "a stale subscription must stay visible")
	r.False(bob.SuperAdmin)
	r.Equal([]string{opsnotify.EventSupportMessage}, bob.Events)
}

// TestSetOperatorNotificationsRoundTrips proves the document the dashboard
// saves is the one the delivery path reads.
func TestSetOperatorNotificationsRoundTrips(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newOpsEnv(t)
	ctx := t.Context()

	alice := env.user(t, "alice@acme.com", true, true)

	resp, err := env.svc.SetOperatorNotifications(ctx, &OperatorNotificationsRequest{
		Enabled: true,
		Recipients: []opsnotify.Recipient{
			{UserUID: alice.UID, Events: []string{opsnotify.EventSupportMessage}},
		},
	})
	r.NoError(err)
	r.True(resp.Enabled)

	cfg, err := opsnotify.LoadConfig(ctx, env.db)
	r.NoError(err)
	r.Equal([]string{alice.UID}, cfg.RecipientsFor(opsnotify.EventSupportMessage))
}

// TestSetOperatorNotificationsDropsUncheckedRows: the dashboard sends every row
// it rendered, so "not a recipient" arrives as an empty event list. Dropping it
// here is what makes unchecking the last box actually unsubscribe.
func TestSetOperatorNotificationsDropsUncheckedRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newOpsEnv(t)
	ctx := t.Context()

	alice := env.user(t, "alice@acme.com", true, true)
	bob := env.user(t, "bob@acme.com", true, true)

	_, err := env.svc.SetOperatorNotifications(ctx, &OperatorNotificationsRequest{
		Enabled: true,
		Recipients: []opsnotify.Recipient{
			{UserUID: alice.UID, Events: []string{opsnotify.EventSupportMessage}},
			{UserUID: bob.UID, Events: []string{}},
		},
	})
	r.NoError(err, "an unchecked row is not a validation error")

	cfg, err := opsnotify.LoadConfig(ctx, env.db)
	r.NoError(err)
	r.Equal([]string{alice.UID}, cfg.RecipientsFor(opsnotify.EventSupportMessage))
}

// TestSetOperatorNotificationsRejectsUnknownEventsAndNonAdmins is the
// write-time guard: a typo would silently subscribe someone to nothing, and a
// regular user would be a genuine authorization mistake.
func TestSetOperatorNotificationsRejectsUnknownEventsAndNonAdmins(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newOpsEnv(t)
	ctx := t.Context()

	alice := env.user(t, "alice@acme.com", true, true)
	carol := env.user(t, "carol@acme.com", false, true)

	_, err := env.svc.SetOperatorNotifications(ctx, &OperatorNotificationsRequest{
		Enabled:    true,
		Recipients: []opsnotify.Recipient{{UserUID: alice.UID, Events: []string{"support.mesage"}}},
	})
	r.ErrorIs(err, ErrInvalidParameter)
	r.ErrorIs(err, opsnotify.ErrInvalidEvent)

	_, err = env.svc.SetOperatorNotifications(ctx, &OperatorNotificationsRequest{
		Enabled: true,
		Recipients: []opsnotify.Recipient{
			{UserUID: carol.UID, Events: []string{opsnotify.EventSupportMessage}},
		},
	})
	r.ErrorIs(err, ErrInvalidParameter)
	r.ErrorIs(err, opsnotify.ErrNotSuperAdmin)

	// Neither attempt may have written anything.
	cfg, err := opsnotify.LoadConfig(ctx, env.db)
	r.NoError(err)
	r.False(cfg.Enabled)
}

// TestSetParameterRejectsAnInvalidOperatorNotificationsDocument closes the back
// door: the raw parameter CRUD must apply the same validation as the dedicated
// endpoint, or the dashboard's guard is one curl away from being bypassed.
func TestSetParameterRejectsAnInvalidOperatorNotificationsDocument(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newOpsEnv(t)

	_, err := env.svc.SetParameter(t.Context(), opsnotify.ParamOperatorNotifications, map[string]any{
		"enabled":    true,
		"recipients": []map[string]any{{"userUid": "nobody", "events": []string{"support.message"}}},
	}, false)
	r.ErrorIs(err, ErrInvalidParameter)
}

// TestSendOperatorNoticeTestReportsWhatHappened: a bare 200 would be a lie —
// the transport succeeds as a call even when every route was skipped, and the
// whole value of the button is the answer.
func TestSendOperatorNoticeTestReportsWhatHappened(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newOpsEnv(t)

	alice := env.user(t, "alice@acme.com", true, true)
	bob := env.user(t, "bob@acme.com", true, false)

	var sentTo []string

	env.svc.SetOperatorNoticeDeps(opsnotify.Deps{
		DB: env.db,
		EnqueueEmail: func(_ context.Context, _, to, _, _ string) error {
			sentTo = append(sentTo, to)

			return nil
		},
	})

	report, err := env.svc.SendOperatorNoticeTest(t.Context(), alice, "https://solidping.example")
	r.NoError(err)
	r.Equal(1, report.Delivered)
	r.Equal([]string{"alice@acme.com"}, sentTo)

	routeless, err := env.svc.SendOperatorNoticeTest(t.Context(), bob, "https://solidping.example")
	r.NoError(err)
	r.Zero(routeless.Delivered, "a recipient with no route must be reported as undelivered")
	r.Zero(routeless.Routes)
}

// TestSendOperatorNoticeTestWithoutATransport fails loudly rather than
// reporting a delivery that never happened.
func TestSendOperatorNoticeTestWithoutATransport(t *testing.T) {
	t.Parallel()

	env := newOpsEnv(t)
	alice := env.user(t, "alice@acme.com", true, true)

	_, err := env.svc.SendOperatorNoticeTest(t.Context(), alice, "")
	require.ErrorIs(t, err, ErrOperatorNoticeUnavailable)
}
