package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portUserContactTeamID is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portUserContactTeamID = 15498

// TestUserContactTeamID_Postgres exercises the user-contact-team-id migration
// section and the upsert that must preserve it, on the engine `make test`
// never reaches.
//
// Worth its own Postgres test: the section is NOT the same SQL in both engines
// (Postgres uses `add column if not exists` plus a column comment), and the
// upsert's preservation clause references the row bun aliases — a clause that
// parses on one engine and not the other would be invisible in -short mode and
// would break every Slack DM contact write in production.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestUserContactTeamID_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portUserContactTeamID, RunMode: runModeTest})
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
		 where table_name = 'user_contacts' and column_name = 'team_id'`).Scan(&columns))
	r.Equal(1, columns, "the user-contact-team-id section must have applied")

	org := models.NewOrganization("teamid-pg", "Team ID")
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

	// The preservation clause: a team-less upsert must not un-learn it.
	blind := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeSlackUser, "U-ADAM", "Slack DM")
	r.NoError(svc.UpsertUserContact(ctx, blind))

	stored, err = svc.GetUserContact(ctx, blind.UID)
	r.NoError(err)
	r.NotNil(stored.TeamID, "a known workspace must survive a team-less upsert")
	r.Equal("T1", *stored.TeamID)
}
