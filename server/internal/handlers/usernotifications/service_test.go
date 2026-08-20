package usernotifications

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// setupUserNotificationsService creates an in-memory SQLite service + seed data.
func setupUserNotificationsService(t *testing.T) (*Service, *models.Organization, *models.User) {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("test-org", "Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("test@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, member))

	svc := NewService(dbSvc, nil)

	return svc, org, user
}

// TestCreateContact_WebPush_SetsVerifiedAt verifies that creating a webpush
// contact immediately marks it as verified (subscription is itself the proof).
func TestCreateContact_WebPush_SetsVerifiedAt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)

	subJSON := `{"endpoint":"https://fcm.googleapis.com/test","keys":{"p256dh":"abc","auth":"def"}}`

	route, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypeWebPush,
		Value: subJSON,
		Label: "Chrome on macOS",
	})
	r.NoError(err)
	r.NotNil(route)
	r.Equal("webpush", route.Contact.Type)
	r.NotNil(route.Contact.VerifiedAt, "webpush contact must be verified immediately on creation")
	r.Equal("Chrome on macOS", route.Contact.Label)
}

// TestCreateContact_WebPush_Idempotent verifies that creating a webpush contact
// with the same endpoint value is idempotent (returns the existing contact).
func TestCreateContact_WebPush_Idempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)

	subJSON := `{"endpoint":"https://fcm.googleapis.com/idempotent-test","keys":{"p256dh":"abc","auth":"def"}}`

	route1, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypeWebPush,
		Value: subJSON,
		Label: "Chrome on macOS",
	})
	r.NoError(err)
	r.NotNil(route1)

	// Re-subscribe with the same value — should succeed (upsert) and return
	// the same or updated contact.
	route2, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypeWebPush,
		Value: subJSON,
		Label: "Chrome on macOS (updated)",
	})
	r.NoError(err)
	r.NotNil(route2)
	r.Equal(route1.Contact.UID, route2.Contact.UID, "upsert must return the same contact UID")
}

// --- Contact deletion: no ghost routes (spec 2026-08-20-02) -----------------

// routeCount returns how many routes the list endpoint reports for the user,
// and asserts none of them carries an all-empty contact — the exact shape the
// dashboard renders as an undeletable ghost row.
func listRoutesNoGhosts(t *testing.T, svc *Service, orgSlug string, user *models.User) []*RouteResponse {
	t.Helper()

	resp, err := svc.ListRoutes(context.Background(), orgSlug, user)
	require.NoError(t, err, "ListRoutes must not fail")

	for _, route := range resp.Data {
		require.NotEmpty(t, route.Contact.UID,
			"route %s was returned with an empty contact — that is the ghost row", route.UID)
		require.NotEmpty(t, route.Contact.Type,
			"route %s was returned with an empty contact type", route.UID)
	}

	return resp.Data
}

// TestDeleteContact_RemovesTheRoute is the exact repro from spec 2026-08-20-02:
// deleting a contact used to soft-delete the contact but leave its route row
// behind, and the list query's tautological `contact.deleted_at IS NULL` guard
// (the predicate lives in bun's JOIN ON clause, so the column comes back NULL
// and the filter is TRUE) let that orphan through with a nil contact.
//
// This proves the negative: after the delete the route is GONE, not returned
// with an empty contact.
func TestDeleteContact_RemovesTheRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)

	// Seeds the default email route, then add a second contact.
	before := listRoutesNoGhosts(t, svc, org.Slug, user)
	r.Len(before, 1, "the seeded default email route")

	added, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypeEmail,
		Value: "second@example.com",
		Label: "Second",
	})
	r.NoError(err)

	r.Len(listRoutesNoGhosts(t, svc, org.Slug, user), 2, "both routes before the delete")

	r.NoError(svc.DeleteContact(ctx, org.Slug, user, added.Contact.UID))

	after := listRoutesNoGhosts(t, svc, org.Slug, user)
	r.Len(after, 1, "the deleted contact's route must be gone, not returned empty")
	r.Equal(user.Email, after[0].Contact.Value, "the surviving route is the seeded email")

	// And the row itself is gone from the table — not merely filtered out of
	// the read path, which would leave the escalation dispatcher iterating it.
	routeRows, err := svc.db.DB().NewSelect().
		Model((*models.UserNotificationRoute)(nil)).
		Where("contact_uid = ?", added.Contact.UID).
		Count(ctx)
	r.NoError(err)
	r.Equal(0, routeRows, "the route row must be hard-deleted, not left dangling")
}

// TestDeleteContact_SeededEmail_DoesNotBrickThePage covers defect 2 of the
// spec: deleting the auto-seeded default email used to make ListRoutes fail
// forever with `load default email contact: sql: no rows in result set` (the
// non-partial unique index means the soft-deleted row still wins the ON
// CONFLICT, and the follow-up SELECT was auto-filtered to live rows).
//
// It also pins the deliberate product decision: the deletion is respected, so
// a second call must NOT resurrect the email the user just removed.
func TestDeleteContact_SeededEmail_DoesNotBrickThePage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)

	seeded := listRoutesNoGhosts(t, svc, org.Slug, user)
	r.Len(seeded, 1)
	r.Equal(user.Email, seeded[0].Contact.Value)

	r.NoError(svc.DeleteContact(ctx, org.Slug, user, seeded[0].Contact.UID))

	// The page still loads (this is what used to return an error), and the
	// email is gone.
	first := listRoutesNoGhosts(t, svc, org.Slug, user)
	r.Empty(first, "no route survives deleting the only one")

	// A SECOND call must not re-seed it: re-seeding on every page load is what
	// would make the email method undeletable.
	second := listRoutesNoGhosts(t, svc, org.Slug, user)
	r.Empty(second, "the seeder must not resurrect a contact the user deleted")
}

// TestCreateContact_RevivesDeletedEmail is the positive control for the seeder
// change: refusing to re-seed must not make the address unusable. Re-adding the
// same email explicitly has to bring back both the contact and its route.
func TestCreateContact_RevivesDeletedEmail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)

	seeded := listRoutesNoGhosts(t, svc, org.Slug, user)
	r.Len(seeded, 1)

	r.NoError(svc.DeleteContact(ctx, org.Slug, user, seeded[0].Contact.UID))
	r.Empty(listRoutesNoGhosts(t, svc, org.Slug, user))

	revived, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypeEmail,
		Value: user.Email,
		Label: "Back again",
	})
	r.NoError(err, "re-adding a deleted email must work")
	r.Equal(user.Email, revived.Contact.Value)

	after := listRoutesNoGhosts(t, svc, org.Slug, user)
	r.Len(after, 1, "the revived contact must have exactly one route")
	r.Equal(revived.Contact.UID, after[0].Contact.UID)
}

// TestDeleteContact_ThenReAdd_LeavesExactlyOneRoute is the revive positive
// control for a non-seeded contact: DeleteUserContact now hard-deletes the
// route, and UpsertUserContact resurrects the contact under its ORIGINAL uid,
// so a re-add must produce one route — not zero (route never re-created) and
// not two (a second route minted alongside a surviving one).
func TestDeleteContact_ThenReAdd_LeavesExactlyOneRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)

	const value = "+15551239876"

	first, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypePhone,
		Value: value,
		Label: "mobile",
	})
	r.NoError(err)

	r.NoError(svc.DeleteContact(ctx, org.Slug, user, first.Contact.UID))

	again, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypePhone,
		Value: value,
		Label: "mobile again",
	})
	r.NoError(err)
	r.Equal(first.Contact.UID, again.Contact.UID, "the revived contact keeps its uid")

	routes := listRoutesNoGhosts(t, svc, org.Slug, user)

	phoneRoutes := 0

	for _, route := range routes {
		if route.Contact.Value == value {
			phoneRoutes++
		}
	}

	r.Equal(1, phoneRoutes, "delete + re-add must leave exactly one route")
}

// TestListRoutes_SkipsRouteWithMissingContact is the API-boundary defense in
// depth (spec proposal 5). It bypasses DeleteUserContact entirely and dangles a
// route by hand — the historical state, and whatever a future code path might
// do — then proves neither the query nor the serializer surfaces it.
func TestListRoutes_SkipsRouteWithMissingContact(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)

	live := listRoutesNoGhosts(t, svc, org.Slug, user)
	r.Len(live, 1)

	// A route pointing at a soft-deleted contact (what every pre-fix deletion
	// left behind), created without going through DeleteUserContact.
	orphaned := models.NewUserContact(user.UID, org.UID, models.UserContactTypePhone, "+15550001111", "orphan")
	err := svc.db.UpsertUserContact(ctx, orphaned)
	r.NoError(err)
	r.NoError(svc.db.EnsureUserNotificationRoute(ctx, user.UID, org.UID, orphaned.UID))

	deletedAt := time.Now()
	_, err = svc.db.DB().NewUpdate().
		Model((*models.UserContact)(nil)).
		Where("uid = ?", orphaned.UID).
		Set("deleted_at = ?", deletedAt).
		Exec(ctx)
	r.NoError(err)

	// The dangling row really is still in the table…
	dangling, err := svc.db.DB().NewSelect().
		Model((*models.UserNotificationRoute)(nil)).
		Where("contact_uid = ?", orphaned.UID).
		Count(ctx)
	r.NoError(err)
	r.Equal(1, dangling, "the test must actually have dangled a route")

	// …and the endpoint still refuses to emit it.
	after := listRoutesNoGhosts(t, svc, org.Slug, user)
	r.Len(after, 1, "a route whose contact is soft-deleted must never be listed")
	r.Equal(live[0].UID, after[0].UID)
}

// TestToRouteResponses_DropsNilContact pins the serializer guard on its own,
// with no database involved: a nil contact must be dropped, never serialized
// as a zero-value ContactResponse.
func TestToRouteResponses_DropsNilContact(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	good := &models.UserNotificationRoute{UID: "route-ok", Contact: &models.UserContact{UID: "c1", Type: "email"}}
	ghost := &models.UserNotificationRoute{UID: "route-ghost"}

	out := toRouteResponses([]*models.UserNotificationRoute{good, ghost})

	r.Len(out, 1, "the nil-contact route must be dropped")
	r.Equal("route-ok", out[0].UID)
}
