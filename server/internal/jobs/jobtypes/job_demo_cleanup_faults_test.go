package jobtypes

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

const demoTestEmail = "demo@solidping.example"

// tamperedDemo is a provisioned demo account bent in every way the sweep is
// meant to undo: a superadmin demo user, an owner role in the demo org, a
// membership in somebody else's org, a lifted session cap, and two visitor
// checks past their TTL.
type tamperedDemo struct {
	jctx    *jobdef.JobContext
	db      db.Service
	deleter *fakeCheckDeleter
	org     *models.Organization
	user    *models.User
	foreign *models.Organization
	expired []string
}

func newTamperedDemo(t *testing.T) *tamperedDemo {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	jctx, dbSvc, deleter := newDemoTestContext(t, true)

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	user, err := dbSvc.GetUserByEmail(ctx, demoTestEmail)
	r.NoError(err)

	yes := true
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{SuperAdmin: &yes}))

	member, err := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	r.NoError(err)

	owner := models.MemberRoleOwner
	r.NoError(dbSvc.UpdateOrganizationMember(ctx, member.UID, models.OrganizationMemberUpdate{Role: &owner}))

	foreign := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, foreign))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(foreign.UID, user.UID, models.MemberRoleAdmin)))

	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, string(systemconfig.KeySessionMaxDuration), 86400, false))

	old := time.Now().Add(-2 * time.Hour)

	return &tamperedDemo{
		jctx: jctx, db: dbSvc, deleter: deleter, org: org, user: user, foreign: foreign,
		expired: []string{
			seedDemoCheck(t, dbSvc, org.UID, "visitor-one", &user.UID, old),
			seedDemoCheck(t, dbSvc, org.UID, "visitor-two", &user.UID, old),
		},
	}
}

// demoHealth is what the sweep is responsible for, read back from the store.
type demoHealth struct {
	userDemoted    bool
	roleReset      bool
	foreignRemoved bool
	capRestored    bool
	deletedChecks  int
	rescheduled    bool
}

func (d *tamperedDemo) health(t *testing.T) demoHealth {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	user, err := d.db.GetUser(ctx, d.user.UID)
	r.NoError(err)

	members, err := d.db.ListMembersByUser(ctx, d.user.UID)
	r.NoError(err)

	health := demoHealth{userDemoted: !user.SuperAdmin, foreignRemoved: true, deletedChecks: len(d.deleter.deleted)}

	for _, member := range members {
		if member.OrganizationUID == d.foreign.UID {
			health.foreignRemoved = false
		}

		if member.OrganizationUID == d.org.UID {
			health.roleReset = member.Role == models.MemberRoleUser
		}
	}

	sessionCap, err := d.db.GetOrgParameter(ctx, d.org.UID, string(systemconfig.KeySessionMaxDuration))
	r.NoError(err)
	r.NotNil(sessionCap)

	seconds, ok := numericParamValue(sessionCap.Value["value"])
	r.True(ok)

	health.capRestored = seconds == demoSessionMaxDurationSeconds

	var next []*models.Job
	r.NoError(d.jctx.DB.NewSelect().Model(&next).
		Where("type = ?", string(jobdef.JobTypeDemoCleanup)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").Scan(ctx))
	r.Len(next, 1, "exactly one sweep is queued")

	// The seed queued it for now; a re-armed sweep is one cleanup interval
	// (30 minutes here) away.
	health.rescheduled = time.Until(next[0].ScheduledAt) > 15*time.Minute

	return health
}

// Every step of the sweep is independent: when one store call fails, the job
// still succeeds, heals everything else and re-arms itself. The control case
// shows a clean sweep heals all of it.
func TestDemoCleanup_OneFailingStepDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()

	healed := demoHealth{
		userDemoted: true, roleReset: true, foreignRemoved: true, capRestored: true,
		deletedChecks: 2, rescheduled: true,
	}

	tests := []struct {
		name string
		fail []string
		// want edits the fully healed state into what this failure leaves.
		want func(health *demoHealth)
	}{
		{name: "control: nothing fails", want: func(*demoHealth) {}},
		{
			name: "check listing fails",
			fail: []string{`FROM "checks"`},
			want: func(health *demoHealth) { health.deletedChecks = 0 },
		},
		{
			name: "user update fails",
			fail: []string{`UPDATE "users"`},
			want: func(health *demoHealth) { health.userDemoted = false },
		},
		{
			name: "membership listing fails",
			fail: []string{`FROM "organization_members"`},
			want: func(health *demoHealth) { health.roleReset, health.foreignRemoved = false, false },
		},
		{
			name: "role reset fails",
			fail: []string{`UPDATE "organization_members"`, "role = "},
			want: func(health *demoHealth) { health.roleReset = false },
		},
		{
			name: "foreign membership removal fails",
			fail: []string{`UPDATE "organization_members"`, "SET deleted_at"},
			want: func(health *demoHealth) { health.foreignRemoved = false },
		},
		{
			name: "entitlements re-pin fails",
			fail: []string{`INSERT INTO "org_entitlements"`},
			want: func(*demoHealth) {},
		},
		{
			name: "session cap write fails",
			fail: []string{`UPDATE "parameters"`, "3600"},
			want: func(health *demoHealth) { health.capRestored = false },
		},
		{
			name: "re-arming fails",
			fail: []string{`UPDATE "jobs"`},
			want: func(health *demoHealth) { health.rescheduled = false },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			demo := newTamperedDemo(t)

			var fault *queryFault
			if tt.fail != nil {
				fault = failQueries(demo.jctx.DB, tt.fail...)
			}

			require.NoError(t, (&DemoCleanupJobRun{}).Run(t.Context(), demo.jctx))

			if fault != nil {
				fault.requireFired(t)
			}

			want := healed
			tt.want(&want)
			require.Equal(t, want, demo.health(t))
		})
	}
}

// flakyDeleter refuses one check and deletes the others.
type flakyDeleter struct {
	inner  *fakeCheckDeleter
	refuse string
}

func (f *flakyDeleter) DeleteCheck(ctx context.Context, orgSlug, identifier string) error {
	if identifier == f.refuse {
		return errTestBoom
	}

	return f.inner.DeleteCheck(ctx, orgSlug, identifier)
}

func TestDemoCleanup_CheckExpiry(t *testing.T) {
	t.Parallel()

	t.Run("a check that cannot be deleted does not block the next one", func(t *testing.T) {
		t.Parallel()

		demo := newTamperedDemo(t)
		demo.jctx.Services.Checks = &flakyDeleter{inner: demo.deleter, refuse: demo.expired[0]}

		require.NoError(t, (&DemoCleanupJobRun{}).Run(t.Context(), demo.jctx))
		require.Equal(t, []string{demo.expired[1]}, demo.deleter.deleted)
	})

	t.Run("without a check service nothing expires, the identity is still healed", func(t *testing.T) {
		t.Parallel()

		demo := newTamperedDemo(t)
		demo.jctx.Services = &services.Registry{Jobs: demo.jctx.Services.Jobs}

		require.NoError(t, (&DemoCleanupJobRun{}).Run(t.Context(), demo.jctx))

		health := demo.health(t)
		require.Zero(t, health.deletedChecks)
		require.True(t, health.userDemoted)
		require.True(t, health.rescheduled)
	})

	t.Run("without any service the identity is still healed", func(t *testing.T) {
		t.Parallel()

		demo := newTamperedDemo(t)
		jobs := demo.jctx.Services.Jobs
		demo.jctx.Services = nil

		require.NoError(t, (&DemoCleanupJobRun{}).Run(t.Context(), demo.jctx))

		demo.jctx.Services = &services.Registry{Jobs: jobs}
		health := demo.health(t)
		require.True(t, health.userDemoted)
		require.True(t, health.roleReset)
		require.False(t, health.rescheduled, "nothing can be queued without a job service")
	})
}

// The demo user lost its membership in the demo org (an operator removed it):
// the sweep puts it back, with the `user` role.
func TestDemoCleanup_RecreatesAMissingMembership(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	demo := newTamperedDemo(t)
	ctx := t.Context()

	member, err := demo.db.GetMemberByUserAndOrg(ctx, demo.user.UID, demo.org.UID)
	r.NoError(err)
	r.NoError(demo.db.DeleteOrganizationMember(ctx, member.UID))

	r.NoError((&DemoCleanupJobRun{}).Run(ctx, demo.jctx))

	restored, err := demo.db.GetMemberByUserAndOrg(ctx, demo.user.UID, demo.org.UID)
	r.NoError(err)
	r.NotNil(restored)
	r.Equal(models.MemberRoleUser, restored.Role)
}

// With the demo enabled but its org or its user absent there is nothing to
// sweep. The job succeeds, deletes nothing and stays scheduled, so a demo
// provisioned later is picked up.
func TestDemoCleanup_NothingToSweepStillReArms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		arrange func(t *testing.T, dbSvc db.Service)
	}{
		{"no demo org", func(*testing.T, db.Service) {}},
		{
			"flagged demo org without its user",
			func(t *testing.T, dbSvc db.Service) {
				t.Helper()

				org := models.NewOrganization("demo", "Demo")
				require.NoError(t, dbSvc.CreateOrganization(t.Context(), org))
				require.NoError(t, dbSvc.SetOrgParameter(t.Context(), org.UID, ParamDemoEnabled, true, false))
				seedDemoCheck(t, dbSvc, org.UID, "old", nil, time.Now().Add(-2*time.Hour))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jctx, dbSvc, deleter := newDemoTestContext(t, true)
			jobs := &recordingJobs{}
			jctx.Services.Jobs = jobs
			tt.arrange(t, dbSvc)

			require.NoError(t, (&DemoCleanupJobRun{}).Run(t.Context(), jctx))
			require.Empty(t, deleter.deleted)
			require.Equal(t, []string{string(jobdef.JobTypeDemoCleanup)}, jobs.calls)
			require.InDelta(t, (30 * time.Minute).Seconds(), jobs.delay.Seconds(), 60)
		})
	}
}
