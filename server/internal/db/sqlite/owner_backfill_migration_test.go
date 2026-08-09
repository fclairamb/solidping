package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ownerBackfillSQL is the backfill block of 011_owner_role.up.sql. The table
// rebuild that precedes it in the migration is NOT idempotent (it drops and
// recreates organization_members), so tests that re-run the backfill after
// Initialize() has already applied the whole file inline just this block —
// exactly like aggregation_dedupe_migration_test.go does for 006. It is safe to
// re-run: it only touches orgs that have an admin and no owner.
// Keep in sync with migrations/011_owner_role.up.sql.
const ownerBackfillSQL = `
update organization_members
set role = 'owner',
    updated_at = datetime('now')
where uid in (
  select (
    select inner_m.uid
    from organization_members inner_m
    where inner_m.organization_uid = om.organization_uid
      and inner_m.role = 'admin'
      and inner_m.deleted_at is null
    order by inner_m.created_at asc, inner_m.uid asc
    limit 1
  )
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
);
`

// TestOwnerBackfillPromotesOldestAdmin pins the 011 backfill: every org that
// had admins but no owner ends up with exactly one owner — its OLDEST admin —
// while orgs that already have an owner, orgs with no admin at all, and
// non-admin members are left untouched.
func TestOwnerBackfillPromotesOldestAdmin(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	mkOrg := func(slug string) *models.Organization {
		org := models.NewOrganization(slug, slug)
		r.NoError(svc.CreateOrganization(ctx, org))

		return org
	}

	// mkMember creates a membership with an explicit created_at so "oldest"
	// is deterministic rather than dependent on insertion timing.
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

	// Org A: three admins of different ages plus a viewer. The oldest admin wins.
	orgA := mkOrg("org-a")
	oldestA := mkMember(orgA, "old@a.example", models.MemberRoleAdmin, 0)
	midA := mkMember(orgA, "mid@a.example", models.MemberRoleAdmin, 10)
	newA := mkMember(orgA, "new@a.example", models.MemberRoleAdmin, 20)
	viewerA := mkMember(orgA, "viewer@a.example", models.MemberRoleViewer, -5)

	// Org B: already has an owner (younger than its admin) — must be left alone,
	// and its admin must NOT be promoted to a second owner.
	orgB := mkOrg("org-b")
	adminB := mkMember(orgB, "admin@b.example", models.MemberRoleAdmin, 0)
	ownerB := mkMember(orgB, "owner@b.example", models.MemberRoleOwner, 30)

	// Org C: no admin at all — nobody to promote, so it stays ownerless.
	orgC := mkOrg("org-c")
	userC := mkMember(orgC, "user@c.example", models.MemberRoleUser, 0)

	// Org D: its only admin is soft-deleted — a dead row must not be promoted.
	orgD := mkOrg("org-d")
	deletedD := mkMember(orgD, "gone@d.example", models.MemberRoleAdmin, 0)
	r.NoError(svc.DeleteOrganizationMember(ctx, deletedD.UID))

	// Re-run the backfill (Initialize already ran it against an empty table).
	_, err = svc.DB().ExecContext(ctx, ownerBackfillSQL)
	r.NoError(err)

	roleOf := func(member *models.OrganizationMember) models.MemberRole {
		reloaded, getErr := svc.GetOrganizationMember(ctx, member.UID)
		r.NoError(getErr)

		return reloaded.Role
	}

	// Org A: exactly the oldest admin was promoted.
	r.Equal(models.MemberRoleOwner, roleOf(oldestA))
	r.Equal(models.MemberRoleAdmin, roleOf(midA))
	r.Equal(models.MemberRoleAdmin, roleOf(newA))
	r.Equal(models.MemberRoleViewer, roleOf(viewerA))

	ownersA, err := svc.CountOwnersByOrg(ctx, orgA.UID)
	r.NoError(err)
	r.Equal(1, ownersA, "org-a must end up with exactly one owner")

	// Org B: untouched, still exactly one owner.
	r.Equal(models.MemberRoleAdmin, roleOf(adminB))
	r.Equal(models.MemberRoleOwner, roleOf(ownerB))

	ownersB, err := svc.CountOwnersByOrg(ctx, orgB.UID)
	r.NoError(err)
	r.Equal(1, ownersB, "an org that already had an owner must not gain a second")

	// Org C: nobody promoted.
	r.Equal(models.MemberRoleUser, roleOf(userC))

	ownersC, err := svc.CountOwnersByOrg(ctx, orgC.UID)
	r.NoError(err)
	r.Equal(0, ownersC)

	// Org D: the soft-deleted admin stays an admin (GetOrganizationMember hides
	// soft-deleted rows, so read it raw).
	var deletedRole string
	r.NoError(svc.DB().QueryRowContext(ctx,
		"select role from organization_members where uid = ?", deletedD.UID).Scan(&deletedRole))
	r.Equal(string(models.MemberRoleAdmin), deletedRole)

	ownersD, err := svc.CountOwnersByOrg(ctx, orgD.UID)
	r.NoError(err)
	r.Equal(0, ownersD)

	// Re-running the backfill is a no-op — orgs now have owners.
	_, err = svc.DB().ExecContext(ctx, ownerBackfillSQL)
	r.NoError(err)

	ownersA, err = svc.CountOwnersByOrg(ctx, orgA.UID)
	r.NoError(err)
	r.Equal(1, ownersA, "the backfill must be idempotent")
}

// TestOwnerRoleIsAcceptedByTheSchema pins the widened CHECK constraint: `owner`
// is storable and a garbage role is still rejected by the database.
func TestOwnerRoleIsAcceptedByTheSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	org := models.NewOrganization("check-org", "Check Org")
	r.NoError(svc.CreateOrganization(ctx, org))

	user := models.NewUser("owner@check.example")
	r.NoError(svc.CreateUser(ctx, user))

	r.NoError(svc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleOwner)))

	other := models.NewUser("bogus@check.example")
	r.NoError(svc.CreateUser(ctx, other))

	bogus := models.NewOrganizationMember(org.UID, other.UID, models.MemberRole("root"))
	r.Error(svc.CreateOrganizationMember(ctx, bogus),
		"the role CHECK constraint must still reject unknown roles")
}

// TestCountAdminsByOrgCountsOwners pins that an owner satisfies the last-admin
// accounting — an org whose only privileged member is its owner must not read
// as "zero admins".
func TestCountAdminsByOrgCountsOwners(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	org := models.NewOrganization("counting", "Counting")
	r.NoError(svc.CreateOrganization(ctx, org))

	add := func(email string, role models.MemberRole) {
		user := models.NewUser(email)
		r.NoError(svc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, role)
		now := time.Now()
		member.JoinedAt = &now
		r.NoError(svc.CreateOrganizationMember(ctx, member))
	}

	add("owner@counting.example", models.MemberRoleOwner)

	admins, err := svc.CountAdminsByOrg(ctx, org.UID)
	r.NoError(err)
	r.Equal(1, admins, "an owner counts as an admin")

	add("admin@counting.example", models.MemberRoleAdmin)
	add("user@counting.example", models.MemberRoleUser)
	add("viewer@counting.example", models.MemberRoleViewer)

	admins, err = svc.CountAdminsByOrg(ctx, org.UID)
	r.NoError(err)
	r.Equal(2, admins, "owner + admin, and nobody below")

	owners, err := svc.CountOwnersByOrg(ctx, org.UID)
	r.NoError(err)
	r.Equal(1, owners)
}
