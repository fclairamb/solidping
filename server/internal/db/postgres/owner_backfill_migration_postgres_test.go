package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portOwnerBackfill is distinct from every other _postgres_test.go embedded
// port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portOwnerBackfill = 15467

// ownerBackfillSQL is the backfill block of 011_owner_role.up.sql. Only this
// block is re-run here — the ALTER … DROP/ADD CONSTRAINT that precedes it in
// the migration has already been applied by Initialize(). The block is safe to
// re-run: it only touches orgs that have an admin and no owner.
// Keep in sync with migrations/011_owner_role.up.sql.
const ownerBackfillSQL = `
update organization_members as m
set role = 'owner',
    updated_at = now()
from (
  select distinct on (om.organization_uid) om.uid
  from organization_members om
  where om.role = 'admin'
    and om.deleted_at is null
    and not exists (
      select 1
      from organization_members owner_m
      where owner_m.organization_uid = om.organization_uid
        and owner_m.role = 'owner'
        and owner_m.deleted_at is null
    )
  order by om.organization_uid, om.created_at asc, om.uid asc
) as oldest
where m.uid = oldest.uid;
`

// TestOwnerBackfillPromotesOldestAdmin_Postgres is the Postgres twin of the
// SQLite backfill test. The two dialects express the "oldest admin per org"
// selection completely differently (DISTINCT ON + UPDATE…FROM here, a
// correlated scalar subquery there), so each needs its own proof.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestOwnerBackfillPromotesOldestAdmin_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portOwnerBackfill, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	mkOrg := func(slug string) *models.Organization {
		org := models.NewOrganization(slug, slug)
		r.NoError(svc.CreateOrganization(ctx, org))

		return org
	}

	mkMember := func(org *models.Organization, email string, role models.MemberRole, ageDays int,
	) *models.OrganizationMember {
		user := models.NewUser(email)
		r.NoError(svc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, role)
		created := base.AddDate(0, 0, ageDays)
		member.CreatedAt = created
		member.JoinedAt = &created
		r.NoError(svc.CreateOrganizationMember(ctx, member))

		return member
	}

	orgA := mkOrg("pg-owner-a")
	oldestA := mkMember(orgA, "old@pga.example", models.MemberRoleAdmin, 0)
	newA := mkMember(orgA, "new@pga.example", models.MemberRoleAdmin, 20)

	orgB := mkOrg("pg-owner-b")
	adminB := mkMember(orgB, "admin@pgb.example", models.MemberRoleAdmin, 0)
	ownerB := mkMember(orgB, "owner@pgb.example", models.MemberRoleOwner, 30)

	orgC := mkOrg("pg-owner-c")
	mkMember(orgC, "user@pgc.example", models.MemberRoleUser, 0)

	_, err = svc.DB().ExecContext(ctx, ownerBackfillSQL)
	r.NoError(err)

	roleOf := func(member *models.OrganizationMember) models.MemberRole {
		reloaded, getErr := svc.GetOrganizationMember(ctx, member.UID)
		r.NoError(getErr)

		return reloaded.Role
	}

	r.Equal(models.MemberRoleOwner, roleOf(oldestA))
	r.Equal(models.MemberRoleAdmin, roleOf(newA))

	ownersA, err := svc.CountOwnersByOrg(ctx, orgA.UID)
	r.NoError(err)
	r.Equal(1, ownersA, "exactly one owner per backfilled org")

	r.Equal(models.MemberRoleAdmin, roleOf(adminB))
	r.Equal(models.MemberRoleOwner, roleOf(ownerB))

	ownersB, err := svc.CountOwnersByOrg(ctx, orgB.UID)
	r.NoError(err)
	r.Equal(1, ownersB, "an org that already had an owner must not gain a second")

	ownersC, err := svc.CountOwnersByOrg(ctx, orgC.UID)
	r.NoError(err)
	r.Equal(0, ownersC, "an org with no admin has nobody to promote")

	// The widened CHECK constraint accepts owner and still rejects garbage.
	bogusUser := models.NewUser("bogus@pg.example")
	r.NoError(svc.CreateUser(ctx, bogusUser))
	r.Error(svc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(orgC.UID, bogusUser.UID, models.MemberRole("root"))))

	// Owners count toward the admin tally.
	admins, err := svc.CountAdminsByOrg(ctx, orgB.UID)
	r.NoError(err)
	r.Equal(2, admins)
}
