package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// TestAutoPersonalOrgs_Postgres runs the personal-org queries of spec
// 2026-10-30-04 on Postgres: the one-live-personal-org-per-user index, the
// owner lookup with and without deleted rows.
func TestAutoPersonalOrgs_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     testsupport.FreePort(t),
		RunMode:  runModeTest,
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	alice := models.NewUser("alice@acme.com")
	r.NoError(s.CreateUser(ctx, alice))

	none, err := s.GetAutoPersonalOrgByOwner(ctx, alice.UID, true)
	r.NoError(err)
	r.Nil(none)

	personal := models.NewOrganization("alice", "Alice's organization")
	personal.AutoPersonalOwnerUID = &alice.UID
	r.NoError(s.CreateOrganization(ctx, personal))
	r.NoError(s.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(personal.UID, alice.UID, models.MemberRoleOwner)))

	dup := models.NewOrganization("alice-dup", "dup")
	dup.AutoPersonalOwnerUID = &alice.UID
	r.Error(s.CreateOrganization(ctx, dup), "one live personal org per user")

	got, err := s.GetAutoPersonalOrgByOwner(ctx, alice.UID, false)
	r.NoError(err)
	r.NotNil(got)
	r.Equal(personal.UID, got.UID)

	// Deleted: still found with includeDeleted, and the index frees the slot.
	r.NoError(s.DeleteOrganization(ctx, personal.UID))

	live, err := s.GetAutoPersonalOrgByOwner(ctx, alice.UID, false)
	r.NoError(err)
	r.Nil(live)

	deleted, err := s.GetAutoPersonalOrgByOwner(ctx, alice.UID, true)
	r.NoError(err)
	r.NotNil(deleted)
	r.NotNil(deleted.DeletedAt)

	r.NoError(s.CreateOrganization(ctx, dup), "the unique index only covers live orgs")
}
