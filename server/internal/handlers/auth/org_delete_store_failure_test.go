package auth

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// deletionWorld is an organization with everything DeleteOrg takes down: an
// owner, a check with its job, a session and a provider link.
type deletionWorld struct {
	svc    *Service
	db     db.Service
	ctx    context.Context //nolint:containedctx // test fixture
	fault  *queryFault
	seeded seededOrg
}

func newDeletionWorld(t *testing.T) *deletionWorld {
	t.Helper()

	svc, dbSvc, ctx := setupAuthTestService(t)
	seeded := seedDeletableOrg(ctx, t, dbSvc, "doomed")
	require.NoError(t, dbSvc.CreateOrganizationProvider(ctx,
		models.NewOrganizationProvider(seeded.org.UID, models.ProviderTypeSlack, "T-DOOMED")))

	return &deletionWorld{svc: svc, db: dbSvc, ctx: ctx, fault: installQueryFault(dbSvc), seeded: seeded}
}

func (w *deletionWorld) delete() (*LoginResponse, error) {
	return w.svc.DeleteOrg(w.ctx, w.seeded.org.Slug, w.seeded.owner.UID,
		DeleteOrgRequest{Slug: w.seeded.org.Slug}, Context{})
}

// exists reports whether the organization still resolves by its slug.
func (w *deletionWorld) exists(t *testing.T) bool {
	t.Helper()

	_, err := w.db.GetOrganizationBySlug(w.ctx, w.seeded.org.Slug)
	if err != nil {
		require.ErrorIs(t, err, sql.ErrNoRows)

		return false
	}

	return true
}

// secondOrg makes the owner a member of another organization.
func (w *deletionWorld) secondOrg(t *testing.T) *models.Organization {
	t.Helper()

	org := models.NewOrganization("survivor", "Survivor")
	require.NoError(t, w.db.CreateOrganization(w.ctx, org))
	require.NoError(t, w.db.CreateOrganizationMember(w.ctx,
		models.NewOrganizationMember(org.UID, w.seeded.owner.UID, models.MemberRoleAdmin)))

	return org
}

// TestDeleteOrgTeardownIsRetryable fails each teardown step in turn. The
// organization row is deleted last, so a failed teardown leaves it resolvable
// and the owner can run the deletion again.
func TestDeleteOrgTeardownIsRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fault   sqlFault
		wantMsg string
	}{
		{"reading the organization", failingSQL(`FROM "organizations"`), ""},
		{"listing the checks", failingSQL(`FROM "checks"`), "failed to list organization checks"},
		{"listing the check jobs", failingSQL(`FROM "check_jobs"`), "failed to list check jobs"},
		{"deleting a check job", failingSQL(`DELETE FROM "check_jobs"`), "failed to delete check job"},
		{"deleting a check", failingSQL(`UPDATE "checks"`), "failed to delete check"},
		{"listing the members", failingSQL(`FROM "organization_members"`), "failed to list organization members"},
		{
			"deleting a membership", failingSQL(`UPDATE "organization_members"`),
			"failed to delete organization member",
		},
		{"revoking the tokens", failingSQL(`UPDATE "user_tokens"`), "failed to revoke organization tokens"},
		{
			"releasing the previous slugs", failingSQL(`UPDATE "organization_previous_slugs"`),
			"failed to release organization previous slugs",
		},
		{
			"listing the provider links", failingSQL(`FROM "organization_providers"`),
			"failed to list organization provider links",
		},
		{
			"releasing a provider link", failingSQL(`UPDATE "organization_providers"`),
			"failed to release organization provider link",
		},
		{"deleting the organization", failingSQL(`UPDATE "organizations"`), "failed to delete organization"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newDeletionWorld(t)

			var (
				resp *LoginResponse
				err  error
			)

			w.fault.during(t, tt.fault, func() { resp, err = w.delete() })
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, tt.wantMsg)
			require.Nil(t, resp)
			require.True(t, w.exists(t), "the organization row goes last")

			resp, err = w.delete()
			require.NoError(t, err)
			require.Equal(t, LoginActionNoOrg, resp.LoginAction)
			require.False(t, w.exists(t))

			jobs, err := w.db.ListCheckJobsByCheckUID(w.ctx, w.seeded.check.UID)
			require.NoError(t, err)
			require.Empty(t, jobs, "the retried deletion finished the teardown")
		})
	}
}

// TestDeleteOrgSessionFailures: once the organization is gone, a failure to
// mint the caller's replacement session is reported, and the deletion stands.
func TestDeleteOrgSessionFailures(t *testing.T) {
	t.Parallel()

	t.Run("remaining organizations cannot be listed", func(t *testing.T) {
		t.Parallel()

		w := newDeletionWorld(t)

		var err error

		// The first membership read is the teardown's own listing.
		w.fault.during(t, failingSQL(`FROM "organization_members"`).after(1), func() { _, err = w.delete() })
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "failed to resolve remaining organizations")
		require.False(t, w.exists(t))
	})

	t.Run("session on the remaining organization cannot be stored", func(t *testing.T) {
		t.Parallel()

		w := newDeletionWorld(t)
		w.secondOrg(t)

		var err error

		w.fault.during(t, failingSQL(`INSERT INTO "user_tokens"`), func() { _, err = w.delete() })
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "failed to mint a session for the remaining organization")
		require.False(t, w.exists(t))
	})

	t.Run("caller cannot be read", func(t *testing.T) {
		t.Parallel()

		w := newDeletionWorld(t)

		var err error

		w.fault.during(t, failingSQL(`FROM "users"`), func() { _, err = w.delete() })
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, w.exists(t))
	})

	t.Run("caller no longer exists", func(t *testing.T) {
		t.Parallel()

		w := newDeletionWorld(t)

		_, err := w.svc.DeleteOrg(w.ctx, w.seeded.org.Slug, uuidV7(t),
			DeleteOrgRequest{Slug: w.seeded.org.Slug}, Context{})
		require.ErrorIs(t, err, ErrUserNotFound)
		require.False(t, w.exists(t))
	})
}

// TestDeleteOrgReplacementSession: who deletes their last organization keeps
// an org-less session, with the super admin role when they hold it.
func TestDeleteOrgReplacementSession(t *testing.T) {
	t.Parallel()

	t.Run("super admin", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newDeletionWorld(t)
		superAdmin := true
		r.NoError(w.db.UpdateUser(w.ctx, w.seeded.owner.UID, &models.UserUpdate{SuperAdmin: &superAdmin}))

		resp, err := w.delete()
		r.NoError(err)
		r.Equal(LoginActionNoOrg, resp.LoginAction)
		r.Equal(RoleSuperAdmin, resp.User.Role)
		r.Empty(resp.RefreshToken)
	})

	t.Run("attachments cannot be reaped: the deletion still succeeds", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newDeletionWorld(t)

		var (
			resp *LoginResponse
			err  error
		)

		w.fault.during(t, failingSQL(`UPDATE "files"`), func() { resp, err = w.delete() })
		r.NoError(err)
		r.NotEmpty(resp.AccessToken)
		r.False(w.exists(t))
	})
}
