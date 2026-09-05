package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestCLICoverage_ServerMgmt exercises the mgmt introspection operations backing
// `sp server limits/memory/cost-distribution`: the public limits endpoint, plus
// the super-admin gated memory snapshot and cost distribution.
func TestCLICoverage_ServerMgmt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	// LIMITS — public, no authentication required.
	limitsResp, err := ts.NewClient().GetLimitsWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, limitsResp.StatusCode())
	r.NotNil(limitsResp.JSON200)
	r.NotEmpty(limitsResp.JSON200.CallerBucket)
	// Rate limiting is disabled in the zero-value test config.
	r.False(limitsResp.JSON200.RateLimit.Enabled)
	r.False(limitsResp.JSON200.Concurrency.Enabled)

	// The ordinary admin user is refused on the super-admin mgmt endpoints.
	adminClient := cliCovAuthedClient(ctx, t, ts)

	forbiddenMem, err := adminClient.GetMemoryWithResponse(ctx, nil)
	r.NoError(err)
	r.Equal(403, forbiddenMem.StatusCode())

	forbiddenCost, err := adminClient.GetCostDistributionWithResponse(ctx, &client.GetCostDistributionParams{})
	r.NoError(err)
	r.Equal(403, forbiddenCost.StatusCode())

	apiClient := cliCovSuperAdminClient(ctx, t, ts)

	// MEMORY — snapshot with runtime + build facts.
	memResp, err := apiClient.GetMemoryWithResponse(ctx, nil)
	r.NoError(err)
	r.Equal(200, memResp.StatusCode())
	r.NotNil(memResp.JSON200)
	r.NotNil(memResp.JSON200.Data.Runtime)
	r.NotNil(memResp.JSON200.Data.Runtime.NumGoroutine)
	r.Positive(*memResp.JSON200.Data.Runtime.NumGoroutine)
	r.NotNil(memResp.JSON200.Data.Build)
	r.NotNil(memResp.JSON200.Data.Build.GoVersion)
	r.NotEmpty(*memResp.JSON200.Data.Build.GoVersion)

	// COST DISTRIBUTION — default threshold.
	costResp, err := apiClient.GetCostDistributionWithResponse(ctx, &client.GetCostDistributionParams{})
	r.NoError(err)
	r.Equal(200, costResp.StatusCode())
	r.NotNil(costResp.JSON200)
	r.NotNil(costResp.JSON200.Data)
	r.NotNil(costResp.JSON200.Data.TotalJobs)

	// COST DISTRIBUTION — explicit threshold echoed back in the response.
	threshold := 500
	costResp2, err := apiClient.GetCostDistributionWithResponse(ctx, &client.GetCostDistributionParams{
		ThresholdMs: &threshold,
	})
	r.NoError(err)
	r.Equal(200, costResp2.StatusCode())
	r.NotNil(costResp2.JSON200)
	r.NotNil(costResp2.JSON200.Data)
	r.NotNil(costResp2.JSON200.Data.ThresholdMs)
	r.Equal(threshold, *costResp2.JSON200.Data.ThresholdMs)
}

// TestCLICoverage_SystemOps exercises the system-action operations backing
// `sp system test-email/email-inbox/activation/lane-load` (all super-admin
// gated).
func TestCLICoverage_SystemOps(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	// The ordinary admin user is refused on the super-admin system actions.
	adminClient := cliCovAuthedClient(ctx, t, ts)
	forbidden, err := adminClient.GetActivationFunnelWithResponse(ctx)
	r.NoError(err)
	r.Equal(403, forbidden.StatusCode())

	apiClient := cliCovSuperAdminClient(ctx, t, ts)

	// TEST-EMAIL — email is disabled in the test config, so the send is
	// reported as not sent (200 with an explanatory message).
	emailResp, err := apiClient.SendTestEmailWithResponse(ctx, client.SendTestEmailJSONRequestBody{
		Recipient: "someone@example.com",
	})
	r.NoError(err)
	r.Equal(200, emailResp.StatusCode())
	r.NotNil(emailResp.JSON200)
	r.False(emailResp.JSON200.Sent)
	r.NotEmpty(emailResp.JSON200.Message)

	// TEST-EMAIL validation — a missing recipient is rejected.
	badEmail, err := apiClient.SendTestEmailWithResponse(ctx, client.SendTestEmailJSONRequestBody{
		Recipient: "",
	})
	r.NoError(err)
	r.Equal(422, badEmail.StatusCode())

	// EMAIL-INBOX CONFIG — never configured, so a zero-valued config is returned.
	cfgResp, err := apiClient.GetEmailInboxConfigWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, cfgResp.StatusCode())
	r.NotNil(cfgResp.JSON200)
	r.NotNil(cfgResp.JSON200.Enabled)
	r.False(*cfgResp.JSON200.Enabled)

	// EMAIL-INBOX STATUS — manager wired but not configured/connected.
	statusResp, err := apiClient.GetEmailInboxStatusWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, statusResp.StatusCode())
	r.NotNil(statusResp.JSON200)
	r.NotNil(statusResp.JSON200.Connected)
	r.False(*statusResp.JSON200.Connected)

	// EMAIL-INBOX TEST — not configured yields a 400.
	testResp, err := apiClient.TestEmailInboxWithResponse(ctx)
	r.NoError(err)
	r.Equal(400, testResp.StatusCode())

	// EMAIL-INBOX SYNC — not configured yields a 400.
	syncResp, err := apiClient.SyncEmailInboxWithResponse(ctx)
	r.NoError(err)
	r.Equal(400, syncResp.StatusCode())

	// ACTIVATION — one row per organization (two seeded by the test harness).
	actResp, err := apiClient.GetActivationFunnelWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, actResp.StatusCode())
	r.NotNil(actResp.JSON200)
	r.GreaterOrEqual(len(actResp.JSON200.Data), 2)
	for _, row := range actResp.JSON200.Data {
		r.NotEmpty(row.Slug)
	}

	// LANE-LOAD — no workers seeded, so the report is present but empty.
	laneResp, err := apiClient.GetSchedulingLaneLoadWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, laneResp.StatusCode())
	r.NotNil(laneResp.JSON200)
	r.NotNil(laneResp.JSON200.Data)
}
