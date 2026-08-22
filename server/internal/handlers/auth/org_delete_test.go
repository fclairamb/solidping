package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
)

// seededOrg bundles everything seedDeletableOrg creates, so callers can pick
// what they need without a row of blank identifiers.
type seededOrg struct {
	org   *models.Organization
	owner *models.User
	check *models.Check
	job   *models.CheckJob
	token *models.UserToken
}

// seedDeletableOrg builds an org with one owner, one check with a scheduled
// job, and one org-scoped refresh token — everything DeleteOrg must take down.
func seedDeletableOrg(
	ctx context.Context, t *testing.T, dbService db.Service, slug string,
) seededOrg {
	t.Helper()

	org := models.NewOrganization(slug, slug)
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	user := models.NewUser("owner@" + slug + ".example")
	require.NoError(t, dbService.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleOwner)
	now := time.Now()
	member.JoinedAt = &now
	require.NoError(t, dbService.CreateOrganizationMember(ctx, member))

	check := models.NewCheck(org.UID, "api", "http")
	name := "API"
	check.Name = &name
	require.NoError(t, dbService.CreateCheck(ctx, check))

	// CreateCheck already provisions the scheduler row; adding a second one
	// would only muddy the "jobs are gone" assertions.
	jobs, err := dbService.ListCheckJobsByCheckUID(ctx, check.UID)
	require.NoError(t, err)
	require.Len(t, jobs, 1, "CreateCheck is expected to provision exactly one job")

	job := jobs[0]

	token := models.NewUserToken(user.UID, &org.UID, "refresh-"+slug, models.TokenTypeRefresh)
	require.NoError(t, dbService.CreateUserToken(ctx, token))

	return seededOrg{org: org, owner: user, check: check, job: job, token: token}
}

// deleteOrgAsOwner drives Service.DeleteOrg the way the handler does — with the
// seeded owner as the caller — and returns the replacement session.
func deleteOrgAsOwner(
	ctx context.Context, t *testing.T, svc *Service, seeded seededOrg, confirm string,
) (*LoginResponse, error) {
	t.Helper()

	return svc.DeleteOrg(ctx, seeded.org.Slug, seeded.owner.UID, DeleteOrgRequest{Slug: confirm}, Context{})
}

// TestDeleteOrgTearsEverythingDown is the happy path: after DeleteOrg the org no
// longer resolves by slug, its scheduler rows are gone (so no worker can keep
// probing), its memberships are gone, and its org-scoped tokens are revoked.
func TestDeleteOrgTearsEverythingDown(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupAuthTestService(t)
	seeded := seedDeletableOrg(ctx, t, dbService, "doomed")
	org, check, job, token := seeded.org, seeded.check, seeded.job, seeded.token

	// Controls: everything is alive before the delete.
	_, err := dbService.GetOrganizationBySlug(ctx, org.Slug)
	require.NoError(t, err)

	jobsBefore, err := dbService.ListCheckJobsByCheckUID(ctx, check.UID)
	require.NoError(t, err)
	require.Len(t, jobsBefore, 1)

	_, err = deleteOrgAsOwner(ctx, t, svc, seeded, org.Slug)
	require.NoError(t, err)

	// The org no longer resolves — this is what makes every surface 404.
	_, err = dbService.GetOrganizationBySlug(ctx, org.Slug)
	require.Error(t, err)

	_, err = dbService.GetOrganization(ctx, org.UID)
	require.Error(t, err)

	// The scheduler row is gone. This is the load-bearing one: the claim query
	// never joins organizations, so a surviving job would keep running checks
	// for a deleted org.
	jobsAfter, err := dbService.ListCheckJobsByCheckUID(ctx, check.UID)
	require.NoError(t, err)
	require.Empty(t, jobsAfter)

	stillThere, err := dbService.GetCheckJobByUID(ctx, job.UID)
	require.True(t, err != nil || stillThere == nil)

	// Memberships are gone, so the org disappears from the member's switcher.
	members, err := dbService.ListMembersByOrg(ctx, org.UID)
	require.NoError(t, err)
	require.Empty(t, members)

	// The org-scoped session is dead.
	_, err = dbService.GetUserTokenByToken(ctx, token.Token)
	require.Error(t, err)
}

// TestDeleteOrgStopsInternalChecksToo is the regression test for the
// internal-check leak: stopOrgChecks enumerates the org's checks through
// ListChecks, whose filter DEFAULTS to `internal = FALSE` in both dialects
// (postgres.go / sqlite.go "Apply internal filter"). Enumerating with a filter
// that does not opt into internal checks therefore skipped them entirely — and
// because the scheduler's claim query never joins organizations, their
// check_jobs rows kept being claimed and executed forever after the org was
// deleted.
//
// `internal` is client-settable on check creation (handlers/checks/service.go),
// so this is reachable through the public API, not just by internal machinery.
//
// The non-internal check in the same org is the positive control: if the
// enumeration broke altogether, BOTH assertions would fail rather than just the
// internal one, and the test would stop being a detector for this specific bug.
func TestDeleteOrgStopsInternalChecksToo(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupAuthTestService(t)

	org := models.NewOrganization("internal-checks", "Internal Checks")
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	user := models.NewUser("owner@internal-checks.example")
	require.NoError(t, dbService.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleOwner)
	joined := time.Now()
	member.JoinedAt = &joined
	require.NoError(t, dbService.CreateOrganizationMember(ctx, member))

	mkCheck := func(slug string, internal bool) *models.Check {
		check := models.NewCheck(org.UID, slug, "http")
		name := slug
		check.Name = &name
		check.Internal = internal
		check.Enabled = true
		require.NoError(t, dbService.CreateCheck(ctx, check))

		return check
	}

	publicCheck := mkCheck("public-api", false)
	internalCheck := mkCheck("internal-probe", true)

	// Control: both checks are scheduled before the delete. An internal check
	// that never got a job in the first place would make this test vacuous.
	for _, check := range []*models.Check{publicCheck, internalCheck} {
		jobs, err := dbService.ListCheckJobsByCheckUID(ctx, check.UID)
		require.NoError(t, err)
		require.NotEmpty(t, jobs, "check %s must be scheduled before the delete", *check.Name)
	}

	_, err := svc.DeleteOrg(ctx, org.Slug, user.UID, DeleteOrgRequest{Slug: org.Slug}, Context{})
	require.NoError(t, err)

	// Both checks must be unscheduled. Deleting the jobs is what actually stops
	// the probes — soft-deleting the check row alone does not.
	for _, check := range []*models.Check{publicCheck, internalCheck} {
		jobs, err := dbService.ListCheckJobsByCheckUID(ctx, check.UID)
		require.NoError(t, err)
		require.Emptyf(t, jobs,
			"check %s still has a scheduler row after the org was deleted — workers would keep running it",
			*check.Name)
	}
}

// TestDeleteOrgSlugConfirmation pins the typed-confirmation guard.
func TestDeleteOrgSlugConfirmation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		confirm   string
		wantErr   error
		wantAlive bool
	}{
		{name: "matching slug deletes", confirm: "confirm-me"},
		{
			name: "mismatched slug is refused", confirm: "confirm-you",
			wantErr: ErrOrgSlugConfirmationMismatch, wantAlive: true,
		},
		{
			name: "empty slug is refused", confirm: "",
			wantErr: ErrOrgSlugConfirmationMismatch, wantAlive: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, dbService, ctx := setupAuthTestService(t)
			seeded := seedDeletableOrg(ctx, t, dbService, "confirm-me")

			_, err := deleteOrgAsOwner(ctx, t, svc, seeded, tc.confirm)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			_, getErr := dbService.GetOrganizationBySlug(ctx, seeded.org.Slug)
			if tc.wantAlive {
				require.NoError(t, getErr, "a refused delete must leave the org alone")
			} else {
				require.Error(t, getErr)
			}
		})
	}
}

// TestDeleteOrgUnknownSlug pins that deleting a slug that does not resolve is a
// not-found, not a silent success.
func TestDeleteOrgUnknownSlug(t *testing.T) {
	t.Parallel()

	svc, _, ctx := setupAuthTestService(t)

	_, err := svc.DeleteOrg(ctx, "never-existed", "", DeleteOrgRequest{Slug: "never-existed"}, Context{})
	require.ErrorIs(t, err, ErrOrganizationNotFound)
}

// TestDeletedOrgSlugIsFreedAndDoesNotResolve pins the two halves of the
// resolved open question: a deleted org's slug 404s immediately AND becomes
// claimable by a brand-new organization, with no alias or tombstone linking the
// new org to the old one.
func TestDeletedOrgSlugIsFreedAndDoesNotResolve(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupAuthTestService(t)
	seeded := seedDeletableOrg(ctx, t, dbService, "reusable")
	oldOrg, oldCheck := seeded.org, seeded.check

	_, delErr := deleteOrgAsOwner(ctx, t, svc, seeded, oldOrg.Slug)
	require.NoError(t, delErr)

	// Immediately unresolvable: this single lookup is what every surface uses
	// (dashboard API via RequireOrgAccess, status pages, badges, embed widget).
	_, err := dbService.GetOrganizationBySlug(ctx, "reusable")
	require.Error(t, err)

	// A different user may now claim the freed slug.
	claimant := models.NewUser("claimant@example.com")
	require.NoError(t, dbService.CreateUser(ctx, claimant))

	resp, err := svc.CreateOrg(ctx, claimant.UID,
		CreateOrgRequest{Name: "Reusable Again", Slug: "reusable"}, Context{})
	require.NoError(t, err)
	require.Equal(t, "reusable", resp.Slug)

	// The new org is genuinely new — not a resurrection of the old row.
	require.NotEqual(t, oldOrg.UID, resp.UID)

	fresh, err := dbService.GetOrganizationBySlug(ctx, "reusable")
	require.NoError(t, err)
	require.Equal(t, resp.UID, fresh.UID)

	// And nothing of the old org leaks into it: the deleted org's check does
	// not belong to the new org.
	checks, _, err := dbService.ListChecks(ctx, fresh.UID, &models.ListChecksFilter{})
	require.NoError(t, err)
	require.Empty(t, checks)

	stale, err := dbService.GetCheck(ctx, oldOrg.UID, oldCheck.UID)
	require.True(t, err != nil || stale == nil, "the deleted org's check must not resolve")
}

// TestCreateOrgMakesTheCallerOwner pins the creator→owner rule end to end.
func TestCreateOrgMakesTheCallerOwner(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupAuthTestService(t)

	user := models.NewUser("founder@example.com")
	require.NoError(t, dbService.CreateUser(ctx, user))

	resp, err := svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "New Co", Slug: "new-co"}, Context{})
	require.NoError(t, err)

	member, err := dbService.GetMemberByUserAndOrg(ctx, user.UID, resp.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleOwner, member.Role)

	// The minted session carries the owner role, so the first org-scoped call
	// after creation is already owner-authorized.
	claims, err := svc.ValidateToken(ctx, resp.AccessToken)
	require.NoError(t, err)
	require.Equal(t, string(models.MemberRoleOwner), claims.Role)

	// The creator is the ONLY member — creating an org for somebody else is
	// impossible: the user UID comes from the validated claims.
	members, err := dbService.ListMembersByOrg(ctx, resp.UID)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Equal(t, user.UID, members[0].UserUID)
}

// TestDeleteOrgLeavesOtherOrgsAlone pins the blast radius: deleting one org
// must not touch another org's checks, jobs, members or tokens.
func TestDeleteOrgLeavesOtherOrgsAlone(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupAuthTestService(t)
	victim := seedDeletableOrg(ctx, t, dbService, "victim")
	bystanderSeed := seedDeletableOrg(ctx, t, dbService, "bystander")
	bystander, bystanderCheck, bystanderToken := bystanderSeed.org, bystanderSeed.check, bystanderSeed.token

	_, delErr := deleteOrgAsOwner(ctx, t, svc, victim, victim.org.Slug)
	require.NoError(t, delErr)

	survivor, err := dbService.GetOrganizationBySlug(ctx, bystander.Slug)
	require.NoError(t, err)
	require.Equal(t, bystander.UID, survivor.UID)

	jobs, err := dbService.ListCheckJobsByCheckUID(ctx, bystanderCheck.UID)
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	members, err := dbService.ListMembersByOrg(ctx, bystander.UID)
	require.NoError(t, err)
	require.Len(t, members, 1)

	_, err = dbService.GetUserTokenByToken(ctx, bystanderToken.Token)
	require.NoError(t, err)
}

// TestClaimsHasOrgRole pins the JWT-claim side of the hierarchy: an owner token
// passes every admin gate, an admin token does not pass an owner gate, and a
// super-admin claim passes everything.
func TestClaimsHasOrgRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		claimRole string
		minRole   models.MemberRole
		want      bool
	}{
		{"owner claim clears the admin gate", "owner", models.MemberRoleAdmin, true},
		{"owner claim clears the owner gate", "owner", models.MemberRoleOwner, true},
		{"admin claim clears the admin gate", "admin", models.MemberRoleAdmin, true},
		{"admin claim fails the owner gate", "admin", models.MemberRoleOwner, false},
		{"user claim fails the admin gate", "user", models.MemberRoleAdmin, false},
		{"viewer claim fails the admin gate", "viewer", models.MemberRoleAdmin, false},
		{"superadmin claim clears the owner gate", RoleSuperAdmin, models.MemberRoleOwner, true},
		{"empty claim fails the viewer gate", "", models.MemberRoleViewer, false},
		{"garbage claim fails the viewer gate", "root", models.MemberRoleViewer, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			claims := &Claims{Role: tc.claimRole}
			require.Equal(t, tc.want, claims.HasOrgRole(tc.minRole))
		})
	}
}

// TestDeleteOrgReapsItsPublicLogo is a reaping obligation of spec 2026-08-22-03,
// not housekeeping.
//
// An org logo is publicly readable on /pub/assets/:uid exactly while its file
// row is LIVE and carries the `organizations/<uid>/logo` topic. Nothing about
// the org row deleting is visible to that check, so without this reap a deleted
// organization's logo stays world-readable forever.
func TestDeleteOrgReapsItsPublicLogo(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbService, ctx := setupAuthTestService(t)
	seeded := seedDeletableOrg(ctx, t, dbService, "logoed")

	logo := models.NewFile(seeded.org.UID, "logo.png", "image/png", "local://x", 10, nil)
	topic := files.OrganizationLogoTopic(seeded.org.UID)
	logo.Topic = &topic
	r.NoError(dbService.CreateFile(ctx, logo))

	// Positive control: it is live (and therefore public) before the delete.
	live, err := dbService.GetFileAny(ctx, logo.UID)
	r.NoError(err)
	r.True(files.IsPublicTopic(*live.Topic))

	_, err = deleteOrgAsOwner(ctx, t, svc, seeded, seeded.org.Slug)
	r.NoError(err)

	_, err = dbService.GetFileAny(ctx, logo.UID)
	r.Error(err, "a deleted org's logo must stop being publicly readable")
}
