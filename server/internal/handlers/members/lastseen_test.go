package members

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// newTestToken is a small helper mirroring the one in db/service_test.go: a
// user_tokens row with an explicit type, ready for the caller to set
// LastActiveAt/CreatedAt/DeletedAt directly before insertion.
func newTestToken(userUID string, tokenType models.TokenType) *models.UserToken {
	return models.NewUserToken(userUID, nil, uuid.New().String(), tokenType)
}

// seedOrgWithUser creates an organization with exactly one already-created
// user as an admin member, and returns the org plus that membership row.
func seedOrgWithUser(
	ctx context.Context, t *testing.T, dbService db.Service, slug string, user *models.User,
) (*models.Organization, *models.OrganizationMember) {
	t.Helper()

	org := models.NewOrganization(slug, slug)
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	now := time.Now()
	member.JoinedAt = &now
	require.NoError(t, dbService.CreateOrganizationMember(ctx, member))

	return org, member
}

// findMember returns the response row for the given member UID, or nil.
func findMember(resp *ListMembersResponse, memberUID string) *MemberResponse {
	for _, m := range resp.Data {
		if m.UID == memberUID {
			return m
		}
	}

	return nil
}

func TestListMembersLastSeen(t *testing.T) {
	t.Parallel()

	t.Run("SessionNewerThanLoginUsesSessionTime", func(t *testing.T) {
		t.Parallel()

		svc, dbService, ctx := setupMembersTest(t)
		r := require.New(t)
		now := time.Now().UTC().Truncate(time.Second)

		user := models.NewUser("session-newer@example.com")
		loginAt := now.Add(-48 * time.Hour)
		user.LastActiveAt = &loginAt
		r.NoError(dbService.CreateUser(ctx, user))

		org, member := seedOrgWithUser(ctx, t, dbService, "session-newer-org", user)

		session := newTestToken(user.UID, models.TokenTypeRefresh)
		sessionAt := now.Add(-1 * time.Hour)
		session.LastActiveAt = &sessionAt
		r.NoError(dbService.CreateUserToken(ctx, session))

		resp, err := svc.ListMembers(ctx, org.Slug)
		r.NoError(err)

		got := findMember(resp, member.UID)
		r.NotNil(got)
		r.NotNil(got.LastSessionActivityAt)
		r.WithinDuration(sessionAt, *got.LastSessionActivityAt, time.Second)
		r.Nil(got.LastTokenActivityAt)

		// GetMember must agree with ListMembers.
		single, err := svc.GetMember(ctx, org.Slug, member.UID)
		r.NoError(err)
		r.NotNil(single.LastSessionActivityAt)
		r.WithinDuration(sessionAt, *single.LastSessionActivityAt, time.Second)
	})

	t.Run("LoginOnlyNoSessionActivityUsesLoginTime", func(t *testing.T) {
		t.Parallel()

		svc, dbService, ctx := setupMembersTest(t)
		r := require.New(t)
		now := time.Now().UTC().Truncate(time.Second)

		user := models.NewUser("login-only@example.com")
		loginAt := now.Add(-2 * time.Hour)
		user.LastActiveAt = &loginAt
		r.NoError(dbService.CreateUser(ctx, user))

		org, member := seedOrgWithUser(ctx, t, dbService, "login-only-org", user)

		resp, err := svc.ListMembers(ctx, org.Slug)
		r.NoError(err)

		got := findMember(resp, member.UID)
		r.NotNil(got)
		r.NotNil(got.LastSessionActivityAt)
		r.WithinDuration(loginAt, *got.LastSessionActivityAt, time.Second)
		r.Nil(got.LastTokenActivityAt)
	})

	t.Run("PATUsedAfterSessionKeepsBothFieldsSeparate", func(t *testing.T) {
		t.Parallel()

		svc, dbService, ctx := setupMembersTest(t)
		r := require.New(t)
		now := time.Now().UTC().Truncate(time.Second)

		user := models.NewUser("pat-after-session@example.com")
		r.NoError(dbService.CreateUser(ctx, user))

		org, member := seedOrgWithUser(ctx, t, dbService, "pat-after-session", user)

		session := newTestToken(user.UID, models.TokenTypeRefresh)
		sessionAt := now.Add(-3 * time.Hour)
		session.LastActiveAt = &sessionAt
		r.NoError(dbService.CreateUserToken(ctx, session))

		pat := newTestToken(user.UID, models.TokenTypePAT)
		patAt := now.Add(-1 * time.Hour)
		pat.LastActiveAt = &patAt
		r.NoError(dbService.CreateUserToken(ctx, pat))

		resp, err := svc.ListMembers(ctx, org.Slug)
		r.NoError(err)

		got := findMember(resp, member.UID)
		r.NotNil(got)
		r.NotNil(got.LastSessionActivityAt)
		r.WithinDuration(sessionAt, *got.LastSessionActivityAt, time.Second)
		r.NotNil(got.LastTokenActivityAt)
		r.WithinDuration(patAt, *got.LastTokenActivityAt, time.Second)
	})

	t.Run("NeverSignedInNoTokensOmitsBothFields", func(t *testing.T) {
		t.Parallel()

		svc, dbService, ctx := setupMembersTest(t)
		r := require.New(t)

		user := models.NewUser("never-signed-in@example.com")
		r.NoError(dbService.CreateUser(ctx, user))

		org, member := seedOrgWithUser(ctx, t, dbService, "never-signed-in-org", user)

		resp, err := svc.ListMembers(ctx, org.Slug)
		r.NoError(err)

		got := findMember(resp, member.UID)
		r.NotNil(got)
		r.Nil(got.LastSessionActivityAt)
		r.Nil(got.LastTokenActivityAt)

		single, err := svc.GetMember(ctx, org.Slug, member.UID)
		r.NoError(err)
		r.Nil(single.LastSessionActivityAt)
		r.Nil(single.LastTokenActivityAt)
	})
}
