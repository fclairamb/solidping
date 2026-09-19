package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// newUsersSearchTestDB is a small in-memory SQLite instance dedicated to
// SearchUsers/ListMembersByUsers tests.
func newUsersSearchTestDB(t *testing.T) *Service {
	t.Helper()

	ctx := context.Background()
	r := require.New(t)

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// TestSearchUsers_QueryMatchesEmailOrNameCaseInsensitive covers the search
// contract on both target columns and case-insensitivity.
func TestSearchUsers_QueryMatchesEmailOrNameCaseInsensitive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)
	s := newUsersSearchTestDB(t)

	alice := models.NewUser("alice@acme.com")
	alice.Name = "Alice"
	r.NoError(s.CreateUser(ctx, alice))

	bob := models.NewUser("bob@example.com")
	bob.Name = "Bobby Tables"
	r.NoError(s.CreateUser(ctx, bob))

	// Substring match on email, case-insensitive.
	rows, total, err := s.SearchUsers(ctx, models.UserSearchFilter{Query: "ALICE@ACME"})
	r.NoError(err)
	r.Equal(1, total)
	r.Len(rows, 1)
	r.Equal(alice.UID, rows[0].UID)

	// Substring match on name, case-insensitive.
	rows, total, err = s.SearchUsers(ctx, models.UserSearchFilter{Query: "bobby"})
	r.NoError(err)
	r.Equal(1, total)
	r.Len(rows, 1)
	r.Equal(bob.UID, rows[0].UID)

	// No match.
	rows, total, err = s.SearchUsers(ctx, models.UserSearchFilter{Query: "zzz-nomatch"})
	r.NoError(err)
	r.Equal(0, total)
	r.Empty(rows)
}

// TestSearchUsers_LiteralPercentIsEscaped proves a literal '%' in Query is
// matched literally rather than as a SQL LIKE wildcard.
func TestSearchUsers_LiteralPercentIsEscaped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)
	s := newUsersSearchTestDB(t)

	literal := models.NewUser("percent@example.com")
	literal.Name = "Fifty%Percent"
	r.NoError(s.CreateUser(ctx, literal))

	other := models.NewUser("other@example.com")
	other.Name = "FiftyXPercent"
	r.NoError(s.CreateUser(ctx, other))

	// The literal query, with its '%' escaped, must match only the row that
	// actually contains a literal '%' character.
	rows, total, err := s.SearchUsers(ctx, models.UserSearchFilter{Query: "fifty%percent"})
	r.NoError(err)
	r.Equal(1, total)
	r.Len(rows, 1)
	r.Equal(literal.UID, rows[0].UID)
}

// TestSearchUsers_PagingAndTotal covers Limit/Offset slicing and the total
// unpaged match count.
func TestSearchUsers_PagingAndTotal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)
	s := newUsersSearchTestDB(t)

	for i := range 5 {
		u := models.NewUser("paging" + string(rune('a'+i)) + "@example.com")
		r.NoError(s.CreateUser(ctx, u))
	}

	rows, total, err := s.SearchUsers(ctx, models.UserSearchFilter{Limit: 2, Offset: 0})
	r.NoError(err)
	r.Equal(5, total)
	r.Len(rows, 2)

	rows, total, err = s.SearchUsers(ctx, models.UserSearchFilter{Limit: 2, Offset: 4})
	r.NoError(err)
	r.Equal(5, total)
	r.Len(rows, 1)
}

// TestSearchUsers_ExcludesSoftDeleted proves deleted_at IS NULL always applies.
func TestSearchUsers_ExcludesSoftDeleted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)
	s := newUsersSearchTestDB(t)

	live := models.NewUser("live@example.com")
	r.NoError(s.CreateUser(ctx, live))

	deleted := models.NewUser("deleted@example.com")
	r.NoError(s.CreateUser(ctx, deleted))
	r.NoError(s.DeleteUser(ctx, deleted.UID))

	rows, total, err := s.SearchUsers(ctx, models.UserSearchFilter{})
	r.NoError(err)
	r.Equal(1, total)
	r.Len(rows, 1)
	r.Equal(live.UID, rows[0].UID)
}

// TestListMembersByUsers_ExcludesSoftDeletedMembershipsAndOrgs covers both
// exclusion rules: a soft-deleted membership, and a membership whose org got
// soft-deleted.
func TestListMembersByUsers_ExcludesSoftDeletedMembershipsAndOrgs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)
	s := newUsersSearchTestDB(t)

	liveOrg := models.NewOrganization("live-org", "Live Org")
	r.NoError(s.CreateOrganization(ctx, liveOrg))
	deadOrg := models.NewOrganization("dead-org", "Dead Org")
	r.NoError(s.CreateOrganization(ctx, deadOrg))

	userA := models.NewUser("member-a@example.com")
	r.NoError(s.CreateUser(ctx, userA))
	activeMembership := models.NewOrganizationMember(liveOrg.UID, userA.UID, models.MemberRoleAdmin)
	r.NoError(s.CreateOrganizationMember(ctx, activeMembership))

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
	r.Len(members, 1, "only userA's live membership in a live org must survive")
	r.Equal(userA.UID, members[0].UserUID)
	r.NotNil(members[0].Organization)
	r.Equal(liveOrg.UID, members[0].Organization.UID)
}

// TestListMembersByUsers_EmptyInputSkipsQuery proves the stated contract: an
// empty slice returns empty without querying.
func TestListMembersByUsers_EmptyInputSkipsQuery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)
	s := newUsersSearchTestDB(t)

	members, err := s.ListMembersByUsers(ctx, nil)
	r.NoError(err)
	r.Empty(members)

	members, err = s.ListMembersByUsers(ctx, []string{})
	r.NoError(err)
	r.Empty(members)
}
