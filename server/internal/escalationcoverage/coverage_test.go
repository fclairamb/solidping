package escalationcoverage_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/escalationcoverage"
)

type fixture struct {
	db     db.Service
	org    *models.Organization
	admin  *models.User
	member *models.User
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	mk := func(email string, role models.MemberRole) *models.User {
		user := models.NewUser(email)
		require.NoError(t, dbSvc.CreateUser(ctx, user))
		require.NoError(t, dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, role)))

		return user
	}

	return &fixture{
		db: dbSvc, org: org,
		admin:  mk("admin@acme.test", models.MemberRoleAdmin),
		member: mk("member@acme.test", models.MemberRoleUser),
	}
}

// policy creates a one-step policy with one target.
func (f *fixture) policy(t *testing.T, tt models.EscalationTargetType, targetUID *string) *models.EscalationPolicy {
	t.Helper()

	ctx := context.Background()
	pol := models.NewEscalationPolicy(f.org.UID, "p-"+string(tt))
	require.NoError(t, f.db.CreateEscalationPolicy(ctx, pol))

	step := models.NewEscalationPolicyStep(pol.UID, 0, 0)
	target := models.NewEscalationPolicyTarget(step.UID, tt, targetUID, 0)
	require.NoError(t, f.db.ReplaceEscalationPolicySteps(ctx, pol.UID,
		[]*models.EscalationPolicyStep{step}, map[int][]*models.EscalationPolicyTarget{0: {target}}))

	return pol
}

func (f *fixture) setDefault(t *testing.T, pol *models.EscalationPolicy) {
	t.Helper()
	require.NoError(t, f.db.UpdateOrganization(context.Background(), f.org.UID,
		models.OrganizationUpdate{DefaultEscalationPolicyUID: &pol.UID}))
}

func (f *fixture) coverage(t *testing.T, user *models.User) *escalationcoverage.Paging {
	t.Helper()

	paging, err := escalationcoverage.Compute(context.Background(), f.db, f.org.UID, user.UID)
	require.NoError(t, err)

	return paging
}

func TestCompute_NoPolicy(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	got := f.coverage(t, f.admin)
	require.Equal(t, escalationcoverage.CoveredByNone, got.CoveredBy)
	require.Equal(t, 0, got.PolicyCount)
}

func TestCompute_OrgDefaultAllAdmins(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.setDefault(t, f.policy(t, models.EscalationTargetAllAdmins, nil))

	require.Equal(t, escalationcoverage.CoveredByOrgDefault, f.coverage(t, f.admin).CoveredBy)
	require.Equal(t, escalationcoverage.CoveredByNone, f.coverage(t, f.member).CoveredBy)
}

func TestCompute_OrgDefaultUserTarget(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.setDefault(t, f.policy(t, models.EscalationTargetUser, &f.member.UID))

	require.Equal(t, escalationcoverage.CoveredByOrgDefault, f.coverage(t, f.member).CoveredBy)
	require.Equal(t, escalationcoverage.CoveredByNone, f.coverage(t, f.admin).CoveredBy)
}

func TestCompute_OrgDefaultScheduleMember(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	sched := models.NewOnCallSchedule(f.org.UID, "primary", "UTC", models.RotationTypeDaily)
	require.NoError(t, f.db.CreateOnCallSchedule(ctx, sched))
	require.NoError(t, f.db.ReplaceOnCallScheduleUsers(ctx, sched.UID, []string{f.member.UID}))
	f.setDefault(t, f.policy(t, models.EscalationTargetSchedule, &sched.UID))

	require.Equal(t, escalationcoverage.CoveredByOrgDefault, f.coverage(t, f.member).CoveredBy)
	require.Equal(t, escalationcoverage.CoveredByNone, f.coverage(t, f.admin).CoveredBy)
}

func TestCompute_PolicyOnlyOnOneCheck(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	pol := f.policy(t, models.EscalationTargetUser, &f.member.UID)

	// A policy that nothing uses does not page anybody.
	require.Equal(t, escalationcoverage.CoveredByNone, f.coverage(t, f.member).CoveredBy)
	require.Equal(t, 1, f.coverage(t, f.member).PolicyCount)

	check := models.NewCheck(f.org.UID, "api", "http")
	check.EscalationPolicyUID = &pol.UID
	require.NoError(t, f.db.CreateCheck(ctx, check))

	require.Equal(t, escalationcoverage.CoveredBySomeChecks, f.coverage(t, f.member).CoveredBy)
}

func TestCompute_SoftDeletedPolicyIgnored(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	pol := f.policy(t, models.EscalationTargetAllAdmins, nil)
	f.setDefault(t, pol)
	require.Equal(t, escalationcoverage.CoveredByOrgDefault, f.coverage(t, f.admin).CoveredBy)

	require.NoError(t, f.db.DeleteEscalationPolicy(context.Background(), pol.UID))

	got := f.coverage(t, f.admin)
	require.Equal(t, escalationcoverage.CoveredByNone, got.CoveredBy)
	require.Equal(t, 0, got.PolicyCount)
}
