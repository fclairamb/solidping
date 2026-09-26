package auth

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portInviteHashPG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// internal/db/incident_number_test.go).
const portInviteHashPG = 15620

const inviteHashTestPassword = "correct-horse-battery"

// TestInviteTokenHashedAtRest runs the spec 2026-09-26-02 contract on SQLite.
//
//nolint:tparallel // sub-tests share one database and run sequentially
func TestInviteTokenHashedAtRest(t *testing.T) {
	t.Parallel()

	svc, dbSvc, _ := setupAuthTestService(t)
	runInviteHashContract(t, svc, dbSvc)
}

// TestInviteTokenHashedAtRest_Postgres is the real-engine twin. One embedded
// instance is shared by every sub-test; each sub-test uses its own org and
// emails.
//
//nolint:tparallel // sub-tests share one embedded instance and run sequentially
func TestInviteTokenHashedAtRest_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbService, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portInviteHashPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbService.Close() })

	if initErr := dbService.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	authCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	fullCfg := &config.Config{Auth: authCfg}
	svc := NewService(dbService, authCfg, fullCfg, nil, nil)

	runInviteHashContract(t, svc, dbService)
}

// inviteHashFixture creates an org with an admin inviter.
func inviteHashFixture(
	t *testing.T, dbSvc db.Service, slug string,
) (*models.Organization, *models.User) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	org := models.NewOrganization(slug, "Invite Hash "+slug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	inviter := models.NewUser("inviter-" + slug + "@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, inviter))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, inviter.UID, models.MemberRoleAdmin)))

	return org, inviter
}

func createHashTestInvite(
	t *testing.T, svc *Service, slug, inviterUID, email string,
) *InviteResponse {
	t.Helper()

	ctx := t.Context()

	resp, err := svc.CreateInvitation(ctx, slug, inviterUID, InviteRequest{
		Email:     email,
		Role:      string(models.MemberRoleUser),
		ExpiresIn: "24h",
		App:       appNameDash0,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Token)

	return resp
}

// writeLegacyInvite stores an invite the way CreateInvitation did before
// spec 2026-09-26-02: keyed by the plaintext token, token in the value.
func writeLegacyInvite(
	t *testing.T, dbSvc db.Service, org *models.Organization,
	inviterUID, email, token string,
) string {
	t.Helper()

	ctx := t.Context()
	key := inviteKeyPrefix + token
	ttl := 24 * time.Hour
	require.NoError(t, dbSvc.SetStateEntry(ctx, &org.UID, key, &models.JSONMap{
		keyToken:     token,
		keyEmail:     email,
		"role":       string(models.MemberRoleViewer),
		"inviterUID": inviterUID,
	}, &ttl))

	return key
}

//nolint:maintidx // one contract, one sub-test per requirement
func runInviteHashContract(t *testing.T, svc *Service, dbSvc db.Service) {
	t.Helper()

	t.Run("stored key is the token hash and the value holds no token", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()
		org, inviter := inviteHashFixture(t, dbSvc, "ih-store")

		resp := createHashTestInvite(t, svc, org.Slug, inviter.UID, "store@acme.com")

		entries, err := dbSvc.ListStateEntries(ctx, &org.UID, inviteKeyPrefix)
		r.NoError(err)
		r.Len(entries, 1)

		entry := entries[0]
		r.Equal(resp.UID, entry.UID)
		r.Equal(inviteKeyPrefix+hashPendingToken(resp.Token), entry.Key)

		suffix := strings.TrimPrefix(entry.Key, inviteKeyPrefix)
		r.Len(suffix, 64)
		_, decErr := hex.DecodeString(suffix)
		r.NoError(decErr, "key suffix must be hex")

		r.NotContains(entry.Key, resp.Token, "the plaintext token must not appear in the key")
		r.NotNil(entry.Value)
		r.NotContains(*entry.Value, keyToken, "the value must not carry a token field")

		for field, v := range *entry.Value {
			if s, ok := v.(string); ok {
				r.NotContains(s, resp.Token, "value field %q leaks the plaintext token", field)
			}
		}

		// The plaintext still reaches the admin and the invitee.
		r.Contains(resp.InviteURL, resp.Token)
	})

	t.Run("the plaintext token resolves and accepts, then the entry is gone", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()
		org, inviter := inviteHashFixture(t, dbSvc, "ih-accept")

		resp := createHashTestInvite(t, svc, org.Slug, inviter.UID, "accept@acme.com")

		info, err := svc.GetInviteInfo(ctx, resp.Token)
		r.NoError(err)
		r.Equal(org.Slug, info.OrgSlug)
		r.Equal(string(models.MemberRoleUser), info.Role)

		login, err := svc.AcceptInvite(ctx, AcceptInviteRequest{
			Token: resp.Token, Name: "Alice", Password: inviteHashTestPassword,
		})
		r.NoError(err)
		r.NotEmpty(login.AccessToken)
		r.Equal(org.Slug, login.Organization.Slug)

		entries, err := dbSvc.ListStateEntries(ctx, &org.UID, inviteKeyPrefix)
		r.NoError(err)
		r.Empty(entries, "an accepted invite must be deleted")

		_, err = svc.GetInviteInfo(ctx, resp.Token)
		r.ErrorIs(err, ErrInvitationNotFound)
	})

	t.Run("the stored hash is not usable as a token", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()
		org, inviter := inviteHashFixture(t, dbSvc, "ih-hashneg")

		resp := createHashTestInvite(t, svc, org.Slug, inviter.UID, "hashneg@acme.com")
		stored := hashPendingToken(resp.Token)

		_, err := svc.GetInviteInfo(ctx, stored)
		r.ErrorIs(err, ErrInvitationNotFound)

		_, err = svc.AcceptInvite(ctx, AcceptInviteRequest{
			Token: stored, Name: "Mallory", Password: inviteHashTestPassword,
		})
		r.ErrorIs(err, ErrInvitationNotFound)

		_, err = svc.GetInviteInfo(ctx, "")
		r.ErrorIs(err, ErrInvitationNotFound)

		// Positive control: the invite is still there and the real token works.
		entry, err := dbSvc.GetStateEntry(ctx, &org.UID, inviteKeyPrefix+stored)
		r.NoError(err)
		r.NotNil(entry, "a rejected hash must not consume the invite")

		_, err = svc.GetInviteInfo(ctx, resp.Token)
		r.NoError(err)

		user, err := dbSvc.GetUserByEmail(ctx, "hashneg@acme.com")
		r.True(err != nil || user == nil, "no account may be created from the hash")
	})

	t.Run("a plaintext-keyed row without its token in the value is not accepted", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()
		org, inviter := inviteHashFixture(t, dbSvc, "ih-notok")

		// Shape of a new-style row reached through the legacy key: no token
		// field. The legacy branch must refuse it.
		const presented = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

		ttl := 24 * time.Hour
		r.NoError(dbSvc.SetStateEntry(ctx, &org.UID, inviteKeyPrefix+presented, &models.JSONMap{
			keyEmail:     "notok@acme.com",
			"role":       string(models.MemberRoleUser),
			"inviterUID": inviter.UID,
		}, &ttl))

		_, err := svc.GetInviteInfo(ctx, presented)
		r.ErrorIs(err, ErrInvitationNotFound)

		_, err = svc.AcceptInvite(ctx, AcceptInviteRequest{
			Token: presented, Name: "Mallory", Password: inviteHashTestPassword,
		})
		r.ErrorIs(err, ErrInvitationNotFound)
	})

	t.Run("a legacy plaintext-keyed invite is still accepted and deleted", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()
		org, inviter := inviteHashFixture(t, dbSvc, "ih-legacy")

		const legacyToken = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

		key := writeLegacyInvite(t, dbSvc, org, inviter.UID, "legacy@acme.com", legacyToken)

		info, err := svc.GetInviteInfo(ctx, legacyToken)
		r.NoError(err)
		r.Equal(org.Slug, info.OrgSlug)
		r.Equal(string(models.MemberRoleViewer), info.Role)

		login, err := svc.AcceptInvite(ctx, AcceptInviteRequest{
			Token: legacyToken, Name: "Bob", Password: inviteHashTestPassword,
		})
		r.NoError(err)
		r.Equal(org.Slug, login.Organization.Slug)

		entry, err := dbSvc.GetStateEntry(ctx, &org.UID, key)
		r.NoError(err)
		r.Nil(entry, "the legacy entry must be deleted on acceptance")

		user, err := dbSvc.GetUserByEmail(ctx, "legacy@acme.com")
		r.NoError(err)
		member, err := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
		r.NoError(err)
		r.Equal(models.MemberRoleViewer, member.Role)
	})

	t.Run("already-member acceptance deletes the matched key, legacy and hashed", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()
		org, inviter := inviteHashFixture(t, dbSvc, "ih-member")

		existing := models.NewUser("member@acme.com")
		r.NoError(dbSvc.CreateUser(ctx, existing))
		r.NoError(dbSvc.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(org.UID, existing.UID, models.MemberRoleUser)))

		const legacyToken = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

		legacyKey := writeLegacyInvite(t, dbSvc, org, inviter.UID, existing.Email, legacyToken)

		_, err := svc.AcceptInvite(ctx, AcceptInviteRequest{Token: legacyToken})
		r.NoError(err)

		entry, err := dbSvc.GetStateEntry(ctx, &org.UID, legacyKey)
		r.NoError(err)
		r.Nil(entry, "the legacy entry must be deleted for an existing member")

		resp := createHashTestInvite(t, svc, org.Slug, inviter.UID, existing.Email)

		_, err = svc.AcceptInvite(ctx, AcceptInviteRequest{Token: resp.Token})
		r.NoError(err)

		entries, err := dbSvc.ListStateEntries(ctx, &org.UID, inviteKeyPrefix)
		r.NoError(err)
		r.Empty(entries, "the hashed entry must be deleted for an existing member")
	})

	t.Run("list and revoke work on a hashed-key invite", func(t *testing.T) {
		r := require.New(t)
		ctx := t.Context()
		org, inviter := inviteHashFixture(t, dbSvc, "ih-revoke")

		resp := createHashTestInvite(t, svc, org.Slug, inviter.UID, "revoke@acme.com")

		list, err := svc.ListInvitations(ctx, org.Slug)
		r.NoError(err)
		r.Len(list.Data, 1)
		r.Equal(resp.UID, list.Data[0].UID)
		r.Equal("revoke@acme.com", list.Data[0].Email)
		r.Equal(string(models.MemberRoleUser), list.Data[0].Role)

		r.NoError(svc.RevokeInvitation(ctx, org.Slug, resp.UID))

		list, err = svc.ListInvitations(ctx, org.Slug)
		r.NoError(err)
		r.Empty(list.Data)

		_, err = svc.GetInviteInfo(ctx, resp.Token)
		r.ErrorIs(err, ErrInvitationNotFound)
	})
}
