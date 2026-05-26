package usernotifications

import (
	"context"
	"testing"

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

	svc := NewService(dbSvc)

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
