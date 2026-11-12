package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestUserContactDeliveryFailure: the flag round-trips, clears, and a re-link
// (upsert) clears it too.
func TestUserContactDeliveryFailure(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)

	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	org := models.NewOrganization("delfail-sqlite", "Delivery Failure")
	r.NoError(svc.CreateOrganization(ctx, org))

	user := models.NewUser("adam@acme.test")
	r.NoError(svc.CreateUser(ctx, user))

	contact := models.NewUserContact(user.UID, org.UID, models.UserContactTypeSlackUser, "U123", "Slack")
	r.NoError(svc.UpsertUserContact(ctx, contact))

	now := time.Now()
	r.NoError(svc.SetUserContactDeliveryFailure(ctx, contact.UID, &now, "user_not_found"))

	stored, err := svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(stored.DeliveryFailedAt)
	r.Equal("user_not_found", *stored.DeliveryFailureCode)

	// Re-linking the same contact clears the flag.
	relink := models.NewUserContact(user.UID, org.UID, models.UserContactTypeSlackUser, "U123", "Slack")
	r.NoError(svc.UpsertUserContact(ctx, relink))

	stored, err = svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.Nil(stored.DeliveryFailedAt)
	r.Nil(stored.DeliveryFailureCode)

	r.NoError(svc.SetUserContactDeliveryFailure(ctx, contact.UID, &now, "user_not_found"))
	r.NoError(svc.SetUserContactDeliveryFailure(ctx, contact.UID, nil, ""))

	stored, err = svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.Nil(stored.DeliveryFailedAt)
}
