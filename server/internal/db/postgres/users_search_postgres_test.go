package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portUsersSearch is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portUsersSearch = 15530

func newUsersSearchPostgresDB(t *testing.T) *Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portUsersSearch,
		RunMode:  runModeTest,
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	return s
}

// TestSearchUsers_Postgres is the Postgres counterpart of the SQLite
// SearchUsers tests: substring match on email/name (case-insensitive), a
// literal '%' matched literally, paging + total, and soft-delete exclusion —
// the same contract, on the dialect that actually ships to most deployments.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go pwfile extraction) with siblings
func TestSearchUsers_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)
	s := newUsersSearchPostgresDB(t)

	alice := models.NewUser("alice@acme.com")
	alice.Name = "Alice"
	r.NoError(s.CreateUser(ctx, alice))

	bob := models.NewUser("bob@example.com")
	bob.Name = "Fifty%Percent"
	r.NoError(s.CreateUser(ctx, bob))

	deleted := models.NewUser("deleted@example.com")
	r.NoError(s.CreateUser(ctx, deleted))
	r.NoError(s.DeleteUser(ctx, deleted.UID))

	// Case-insensitive substring on email.
	rows, total, err := s.SearchUsers(ctx, models.UserSearchFilter{Query: "ALICE@ACME"})
	r.NoError(err)
	r.Equal(1, total)
	r.Len(rows, 1)
	r.Equal(alice.UID, rows[0].UID)

	// Literal '%' matched literally, not as a wildcard.
	rows, total, err = s.SearchUsers(ctx, models.UserSearchFilter{Query: "fifty%percent"})
	r.NoError(err)
	r.Equal(1, total)
	r.Len(rows, 1)
	r.Equal(bob.UID, rows[0].UID)

	rows, total, err = s.SearchUsers(ctx, models.UserSearchFilter{Query: "fiftyXpercent"})
	r.NoError(err)
	r.Equal(0, total)
	r.Empty(rows)

	// Soft-deleted user is absent from an unfiltered listing.
	rows, total, err = s.SearchUsers(ctx, models.UserSearchFilter{Limit: 200})
	r.NoError(err)
	r.Equal(2, total)

	emails := make([]string, 0, len(rows))
	for _, row := range rows {
		emails = append(emails, row.Email)
	}

	r.NotContains(emails, "deleted@example.com")
}

// TestListMembersByUsers_Postgres is the Postgres counterpart of the SQLite
// ListMembersByUsers exclusion tests.
//
//nolint:paralleltest // shares dev-machine resources with siblings in this package
func TestListMembersByUsers_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)
	s := newUsersSearchPostgresDB(t)

	liveOrg := models.NewOrganization("live-org", "Live Org")
	r.NoError(s.CreateOrganization(ctx, liveOrg))
	deadOrg := models.NewOrganization("dead-org", "Dead Org")
	r.NoError(s.CreateOrganization(ctx, deadOrg))

	userA := models.NewUser("member-a@example.com")
	r.NoError(s.CreateUser(ctx, userA))
	r.NoError(s.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(liveOrg.UID, userA.UID, models.MemberRoleAdmin)))

	userB := models.NewUser("member-b@example.com")
	r.NoError(s.CreateUser(ctx, userB))
	deletedMembership := models.NewOrganizationMember(liveOrg.UID, userB.UID, models.MemberRoleUser)
	r.NoError(s.CreateOrganizationMember(ctx, deletedMembership))
	r.NoError(s.DeleteOrganizationMember(ctx, deletedMembership.UID))

	userC := models.NewUser("member-c@example.com")
	r.NoError(s.CreateUser(ctx, userC))
	r.NoError(s.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(deadOrg.UID, userC.UID, models.MemberRoleUser)))
	r.NoError(s.DeleteOrganization(ctx, deadOrg.UID))

	members, err := s.ListMembersByUsers(ctx, []string{userA.UID, userB.UID, userC.UID})
	r.NoError(err)
	r.Len(members, 1)
	r.Equal(userA.UID, members[0].UserUID)
	r.NotNil(members[0].Organization)

	// Empty input skips the query entirely.
	empty, err := s.ListMembersByUsers(ctx, nil)
	r.NoError(err)
	r.Empty(empty)
}
