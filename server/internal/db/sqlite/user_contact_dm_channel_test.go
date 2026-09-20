package sqlite

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestUserContactDMChannelColumn proves the user-contact-dm-channel-id section
// landed: the column exists after a full migrate, it round-trips, it can be
// cleared, and a channel-less upsert does not un-learn it.
func TestUserContactDMChannelColumn(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)

	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	var columns []string
	r.NoError(svc.db.NewRaw("select name from pragma_table_info('user_contacts')").Scan(ctx, &columns))
	r.Contains(columns, "dm_channel_id")

	org := models.NewOrganization("dmchan-sqlite", "DM Channel")
	r.NoError(svc.CreateOrganization(ctx, org))

	user := models.NewUser("adam@acme.test")
	r.NoError(svc.CreateUser(ctx, user))

	contact := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeDiscord, "SNOW-ADAM", "Discord")
	r.NoError(svc.UpsertUserContact(ctx, contact))

	// A brand-new contact has never had a DM opened.
	stored, err := svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.Nil(stored.DMChannelID, "a contact the bot has never messaged carries no channel")

	r.NoError(svc.SetUserContactDMChannel(ctx, contact.UID, "DM-1"))

	stored, err = svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(stored.DMChannelID)
	r.Equal("DM-1", *stored.DMChannelID)

	// An empty channelID CLEARS it — what the sender does when Discord 404s a
	// channel it once handed us, so the next send re-opens instead of retrying a
	// known-bad id forever.
	r.NoError(svc.SetUserContactDMChannel(ctx, contact.UID, ""))

	stored, err = svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.Nil(stored.DMChannelID)

	// A later upsert that does not know the channel (the re-connect path) must
	// not un-learn a cached one.
	r.NoError(svc.SetUserContactDMChannel(ctx, contact.UID, "DM-2"))

	blind := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeDiscord, "SNOW-ADAM", "Discord")
	r.NoError(svc.UpsertUserContact(ctx, blind))

	stored, err = svc.GetUserContact(ctx, blind.UID)
	r.NoError(err)
	r.NotNil(stored.DMChannelID, "a cached DM channel must survive a channel-less upsert")
	r.Equal("DM-2", *stored.DMChannelID)
}

// TestUserContactDMChannelMigrationsInLockstep is the dialect-agnostic guard the
// repo's DB rule asks for: the section must exist in BOTH engines' migration
// series under the same number, in both directions. A column that lands on SQLite
// only passes `make test` (which is -short) and breaks every Postgres deployment
// at the first DM.
func TestUserContactDMChannelMigrationsInLockstep(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	sqliteUp := migrationSection(t, "user-contact-dm-channel-id  (spec 2026-09-19-05)")
	r.Contains(sqliteUp, "add column dm_channel_id")

	for _, path := range []string{
		"../postgres/migrations/022_v0_30_0.up.sql",
		"../postgres/migrations/022_v0_30_0.down.sql",
		"migrations/022_v0_30_0.down.sql",
	} {
		body, err := os.ReadFile(path)
		r.NoError(err, "the twin migration must exist at %s", path)
		r.Contains(string(body), "SECTION: user-contact-dm-channel-id",
			"%s must carry the user-contact-dm-channel-id section", path)
		r.Contains(string(body), "dm_channel_id", "%s must name the column", path)
	}
}

// TestMigration022DownDropsSectionsInReverseOrder: the down file's own header
// promises reverse order, and a down migration that contradicts its header is a
// trap for whoever next reads it.
func TestMigration022DownDropsSectionsInReverseOrder(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, path := range []string{
		"migrations/022_v0_30_0.down.sql",
		"../postgres/migrations/022_v0_30_0.down.sql",
	} {
		body, err := os.ReadFile(path)
		r.NoError(err)

		text := string(body)
		dmIdx := indexOfSection(text, "user-contact-dm-channel-id")
		teamIdx := indexOfSection(text, "user-contact-team-id")

		r.Positive(dmIdx, "%s must carry the dm-channel section", path)
		r.Positive(teamIdx, "%s must carry the team-id section", path)
		r.Less(dmIdx, teamIdx,
			"%s must drop the LAST up-section first (reverse order, as its header promises)", path)
	}
}

func indexOfSection(text, name string) int {
	return strings.Index(text, "-- SECTION: "+name)
}
