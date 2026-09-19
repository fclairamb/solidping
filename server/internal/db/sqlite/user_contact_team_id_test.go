package sqlite

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestUserContactTeamIDColumn proves the user-contact-team-id section landed:
// the column exists after a full migrate, and it round-trips.
func TestUserContactTeamIDColumn(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)

	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	var columns []string
	r.NoError(svc.db.NewRaw("select name from pragma_table_info('user_contacts')").Scan(ctx, &columns))
	r.Contains(columns, "team_id")

	org := models.NewOrganization("teamid-sqlite", "Team ID")
	r.NoError(svc.CreateOrganization(ctx, org))

	user := models.NewUser("adam@acme.test")
	r.NoError(svc.CreateUser(ctx, user))

	team := "T1"
	contact := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeSlackUser, "U-ADAM", "Slack DM")
	contact.TeamID = &team
	r.NoError(svc.UpsertUserContact(ctx, contact))

	stored, err := svc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(stored.TeamID)
	r.Equal("T1", *stored.TeamID)

	// A later upsert that does NOT know the workspace (the revive path, and the
	// generic POST) must not un-learn it — otherwise a member silently stops
	// being pinged after re-adding their Slack DM contact.
	blind := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeSlackUser, "U-ADAM", "Slack DM")
	r.NoError(svc.UpsertUserContact(ctx, blind))

	stored, err = svc.GetUserContact(ctx, blind.UID)
	r.NoError(err)
	r.NotNil(stored.TeamID, "a known workspace must survive a team-less upsert")
	r.Equal("T1", *stored.TeamID)
}

// TestUserContactTeamIDMigrationsInLockstep is the dialect-agnostic guard the
// repo's DB rule asks for: the section must exist in BOTH engines' migration
// series under the same number, in both directions. A column that lands on
// SQLite only passes `make test` (which is -short) and breaks every Postgres
// deployment at the first upsert.
func TestUserContactTeamIDMigrationsInLockstep(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	sqliteUp := migrationSection(t, "user-contact-team-id  (spec 2026-09-19-02)")
	r.Contains(sqliteUp, "add column team_id")

	for _, path := range []string{
		"../postgres/migrations/022_v0_30_0.up.sql",
		"../postgres/migrations/022_v0_30_0.down.sql",
		"migrations/022_v0_30_0.down.sql",
	} {
		body, err := os.ReadFile(path)
		r.NoError(err, "the twin migration must exist at %s", path)
		r.Contains(string(body), "SECTION: user-contact-team-id",
			"%s must carry the user-contact-team-id section", path)
		r.Contains(string(body), "team_id", "%s must name the column", path)
	}
}
