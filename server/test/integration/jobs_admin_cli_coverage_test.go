package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// cliCovSuperAdminClient seeds a super-admin user (member of the primary test
// org) and returns a client logged in as them. The /system job endpoints are
// gated by RequireSuperAdmin, so the ordinary admin test user is insufficient.
func cliCovSuperAdminClient(ctx context.Context, t *testing.T, ts *TestServer) *client.SolidPingClient {
	t.Helper()

	passwordHash, err := passwords.Hash(TestUserPassword)
	require.NoError(t, err)

	now := time.Now()
	admin := &models.User{
		UID:          "40000000-0000-0000-0000-0000000000aa",
		Email:        "cli-cov-superadmin@example.com",
		Name:         "CLI Coverage Super Admin",
		PasswordHash: &passwordHash,
		SuperAdmin:   true,
		CreatedAt:    now, UpdatedAt: now,
	}
	require.NoError(t, ts.Server.DBService().CreateUser(ctx, admin))

	member := &models.OrganizationMember{
		UID:             "40000000-0000-0000-0000-0000000000ab",
		UserUID:         admin.UID,
		OrganizationUID: cliCovOrgUID,
		Role:            models.MemberRoleAdmin,
		JoinedAt:        &now, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, ts.Server.DBService().CreateOrganizationMember(ctx, member))

	apiClient := ts.NewClient()
	_, err = apiClient.Login(ctx, TestOrgSlug, admin.Email, TestUserPassword)
	require.NoError(t, err)

	return apiClient
}

// cliCovSeedJob inserts a background job for the given org (nil for a system job).
func cliCovSeedJob(ctx context.Context, t *testing.T, ts *TestServer, orgUID *string, jobType string) *models.Job {
	t.Helper()

	job := models.NewJob(orgUID, jobType)
	require.NoError(t, ts.Server.DBService().CreateJob(ctx, job))

	return job
}

// cliCovSeedCheckJob inserts a check-schedule row referencing the given check.
func cliCovSeedCheckJob(
	ctx context.Context, t *testing.T, ts *TestServer, orgUID, checkUID string,
) *models.CheckJob {
	t.Helper()

	checkJob := models.NewCheckJob(orgUID, checkUID, timeutils.Duration(60*time.Second))
	checkJob.Type = "http"
	require.NoError(t, ts.Server.DBService().CreateCheckJob(ctx, checkJob))

	return checkJob
}

// TestCLICoverage_JobsAdmin exercises the org-scoped getJobStats / listAdminJobs /
// getAdminJob / getAdminJobChain / listCheckJobs / getCheckJob operations.
func TestCLICoverage_JobsAdmin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	orgUID := cliCovOrgUID

	// Seed a retry chain: job2.previousJobUid = job1.uid.
	job1 := cliCovSeedJob(ctx, t, ts, &orgUID, "cli_cov_job")
	job2 := models.NewJob(&orgUID, "cli_cov_job_retry")
	job2.PreviousJobUID = &job1.UID
	r.NoError(ts.Server.DBService().CreateJob(ctx, job2))

	// Seed a check + its schedule row.
	checkUID := "40000000-0000-0000-0000-0000000000b1"
	cliCovSeedCheck(ctx, t, ts, checkUID, "cli-jobsadmin-check", "http",
		models.JSONMap{"url": "https://example.com"})
	checkJob := cliCovSeedCheckJob(ctx, t, ts, orgUID, checkUID)

	apiClient := cliCovAuthedClient(ctx, t, ts)

	// STATS — aggregate over queue + schedule.
	statsResp, err := apiClient.GetJobStatsWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, statsResp.StatusCode())
	r.NotNil(statsResp.JSON200)
	r.NotNil(statsResp.JSON200.Data)
	r.NotNil(statsResp.JSON200.Data.Jobs)
	r.NotNil(statsResp.JSON200.Data.Jobs.Pending)
	r.GreaterOrEqual(*statsResp.JSON200.Data.Jobs.Pending, 2)
	r.NotNil(statsResp.JSON200.Data.Checks)
	r.NotNil(statsResp.JSON200.Data.Checks.Total)
	r.GreaterOrEqual(*statsResp.JSON200.Data.Checks.Total, 1)

	// ADMIN LIST — both org jobs appear.
	listResp, err := apiClient.ListAdminJobsWithResponse(ctx, TestOrgSlug, &client.ListAdminJobsParams{})
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	r.GreaterOrEqual(len(*listResp.JSON200.Data), 2)

	// ADMIN GET — the seeded job round-trips.
	getResp, err := apiClient.GetAdminJobWithResponse(ctx, TestOrgSlug, uuid.MustParse(job1.UID))
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.NotNil(getResp.JSON200.Data)
	r.NotNil(getResp.JSON200.Data.Uid)
	r.Equal(job1.UID, getResp.JSON200.Data.Uid.String())
	r.NotNil(getResp.JSON200.Data.Type)
	r.Equal("cli_cov_job", *getResp.JSON200.Data.Type)

	// ADMIN CHAIN — walking from the ancestor returns both jobs, in order.
	chainResp, err := apiClient.GetAdminJobChainWithResponse(ctx, TestOrgSlug, uuid.MustParse(job1.UID))
	r.NoError(err)
	r.Equal(200, chainResp.StatusCode())
	r.NotNil(chainResp.JSON200)
	r.NotNil(chainResp.JSON200.Data)
	chain := *chainResp.JSON200.Data
	r.Len(chain, 2)
	r.Equal(job1.UID, chain[0].Uid.String())
	r.Equal(job2.UID, chain[1].Uid.String())

	// CHECK-JOBS LIST — the schedule row appears.
	cjListResp, err := apiClient.ListCheckJobsWithResponse(ctx, TestOrgSlug, &client.ListCheckJobsParams{})
	r.NoError(err)
	r.Equal(200, cjListResp.StatusCode())
	r.NotNil(cjListResp.JSON200)
	r.NotNil(cjListResp.JSON200.Data)
	foundCheckJob := false
	for _, row := range *cjListResp.JSON200.Data {
		if row.Uid != nil && row.Uid.String() == checkJob.UID {
			foundCheckJob = true
		}
	}
	r.True(foundCheckJob, "seeded check job should be listed")

	// CHECK-JOBS GET — single row, with resolved check name.
	cjGetResp, err := apiClient.GetCheckJobWithResponse(ctx, TestOrgSlug, uuid.MustParse(checkJob.UID))
	r.NoError(err)
	r.Equal(200, cjGetResp.StatusCode())
	r.NotNil(cjGetResp.JSON200)
	r.NotNil(cjGetResp.JSON200.Data)
	r.NotNil(cjGetResp.JSON200.Data.CheckUid)
	r.Equal(checkUID, cjGetResp.JSON200.Data.CheckUid.String())
	r.NotNil(cjGetResp.JSON200.Data.CheckName)
	r.Equal("cli-jobsadmin-check", *cjGetResp.JSON200.Data.CheckName)
}

// TestCLICoverage_SystemJobs exercises the system-scoped getSystemJobStats /
// listSystemJobs / getSystemJob / getSystemJobChain / listSystemCheckJobs /
// getSystemCheckJob operations (super-admin gated).
func TestCLICoverage_SystemJobs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	orgUID := cliCovOrgUID

	// Seed an org retry chain and a system (null-org) job.
	job1 := cliCovSeedJob(ctx, t, ts, &orgUID, "cli_cov_sys_job")
	job2 := models.NewJob(&orgUID, "cli_cov_sys_job_retry")
	job2.PreviousJobUID = &job1.UID
	r.NoError(ts.Server.DBService().CreateJob(ctx, job2))
	systemJob := cliCovSeedJob(ctx, t, ts, nil, "cli_cov_pure_system_job")

	// Seed a check + schedule row.
	checkUID := "40000000-0000-0000-0000-0000000000c1"
	cliCovSeedCheck(ctx, t, ts, checkUID, "cli-sysjobs-check", "http",
		models.JSONMap{"url": "https://example.com"})
	checkJob := cliCovSeedCheckJob(ctx, t, ts, orgUID, checkUID)

	// The ordinary admin user is refused on /system endpoints.
	adminClient := cliCovAuthedClient(ctx, t, ts)
	forbidden, err := adminClient.ListSystemJobsWithResponse(ctx, &client.ListSystemJobsParams{})
	r.NoError(err)
	r.Equal(403, forbidden.StatusCode())

	apiClient := cliCovSuperAdminClient(ctx, t, ts)

	// STATS across all orgs.
	statsResp, err := apiClient.GetSystemJobStatsWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, statsResp.StatusCode())
	r.NotNil(statsResp.JSON200)
	r.NotNil(statsResp.JSON200.Data)
	r.NotNil(statsResp.JSON200.Data.Jobs)
	r.NotNil(statsResp.JSON200.Data.Jobs.Pending)
	r.GreaterOrEqual(*statsResp.JSON200.Data.Jobs.Pending, 3)

	// LIST — includes the system (null-org) job.
	listResp, err := apiClient.ListSystemJobsWithResponse(ctx, &client.ListSystemJobsParams{})
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	foundSystemJob := false
	for _, row := range *listResp.JSON200.Data {
		if row.Uid != nil && row.Uid.String() == systemJob.UID {
			foundSystemJob = true
			r.Nil(row.OrganizationUid, "pure system job has no org")
		}
	}
	r.True(foundSystemJob, "system job should appear in the system-wide list")

	// GET a single job.
	getResp, err := apiClient.GetSystemJobWithResponse(ctx, uuid.MustParse(job1.UID))
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.NotNil(getResp.JSON200.Data)
	r.NotNil(getResp.JSON200.Data.Uid)
	r.Equal(job1.UID, getResp.JSON200.Data.Uid.String())

	// CHAIN across all orgs.
	chainResp, err := apiClient.GetSystemJobChainWithResponse(ctx, uuid.MustParse(job1.UID))
	r.NoError(err)
	r.Equal(200, chainResp.StatusCode())
	r.NotNil(chainResp.JSON200)
	r.NotNil(chainResp.JSON200.Data)
	chain := *chainResp.JSON200.Data
	r.Len(chain, 2)
	r.Equal(job1.UID, chain[0].Uid.String())
	r.Equal(job2.UID, chain[1].Uid.String())

	// CHECK-JOBS LIST across all orgs.
	cjListResp, err := apiClient.ListSystemCheckJobsWithResponse(ctx, &client.ListSystemCheckJobsParams{})
	r.NoError(err)
	r.Equal(200, cjListResp.StatusCode())
	r.NotNil(cjListResp.JSON200)
	r.NotNil(cjListResp.JSON200.Data)
	foundCheckJob := false
	for _, row := range *cjListResp.JSON200.Data {
		if row.Uid != nil && row.Uid.String() == checkJob.UID {
			foundCheckJob = true
		}
	}
	r.True(foundCheckJob, "seeded check job should be listed system-wide")

	// CHECK-JOBS GET.
	cjGetResp, err := apiClient.GetSystemCheckJobWithResponse(ctx, uuid.MustParse(checkJob.UID))
	r.NoError(err)
	r.Equal(200, cjGetResp.StatusCode())
	r.NotNil(cjGetResp.JSON200)
	r.NotNil(cjGetResp.JSON200.Data)
	r.NotNil(cjGetResp.JSON200.Data.CheckUid)
	r.Equal(checkUID, cjGetResp.JSON200.Data.CheckUid.String())
}
