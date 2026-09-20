package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portUserContactDMChannel is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portUserContactDMChannel = 15531

// TestUserContactDMChannel_Postgres exercises the user-contact-dm-channel-id
// migration section, the setter and the upsert preservation clause on the engine
// `make test` never reaches.
//
// Worth its own Postgres test for the same reason the team_id one is: the section
// is NOT the same SQL in both engines (Postgres uses `add column if not exists`
// plus a column comment), and both the setter's `set dm_channel_id = NULL` branch
// and the upsert's coalesce clause reference bun's row alias — SQL that parses on
// one engine and not the other would be invisible in -short mode and would break
// every Discord DM write in production.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestUserContactDMChannel_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portUserContactDMChannel, RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	var columns int
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from information_schema.columns
		 where table_name = 'user_contacts' and column_name = 'dm_channel_id'`).Scan(&columns))
	r.Equal(1, columns, "the user-contact-dm-channel-id section must have applied")

	org := models.NewOrganization("dmchan-pg", "DM Channel")
	r.NoError(svc.CreateOrganization(ctx, org))

	user := models.NewUser("adam@acme.test")
	r.NoError(svc.CreateUser(ctx, user))

	contact := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeDiscord, "SNOW-ADAM", "Discord")
	r.NoError(svc.UpsertUserContact(ctx, contact))

	stored, err := svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.Nil(stored.DMChannelID)

	r.NoError(svc.SetUserContactDMChannel(ctx, contact.UID, "DM-1"))

	stored, err = svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(stored.DMChannelID)
	r.Equal("DM-1", *stored.DMChannelID)

	// The clear branch: an empty channelID must produce a real NULL.
	r.NoError(svc.SetUserContactDMChannel(ctx, contact.UID, ""))

	stored, err = svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.Nil(stored.DMChannelID)

	// The preservation clause: a channel-less upsert must not un-learn it.
	r.NoError(svc.SetUserContactDMChannel(ctx, contact.UID, "DM-2"))

	blind := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeDiscord, "SNOW-ADAM", "Discord")
	r.NoError(svc.UpsertUserContact(ctx, blind))

	stored, err = svc.GetUserContact(ctx, blind.UID)
	r.NoError(err)
	r.NotNil(stored.DMChannelID, "a cached DM channel must survive a channel-less upsert")
	r.Equal("DM-2", *stored.DMChannelID)
}
