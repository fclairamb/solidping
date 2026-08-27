package agentws_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

// jobFor loads the single check_job row materialized for a check.
func (e *env) jobFor(checkUID string) *models.CheckJob {
	e.t.Helper()

	job := new(models.CheckJob)
	require.NoError(e.t, e.dbSvc.DB().NewSelect().
		Model(job).
		Where("check_uid = ?", checkUID).
		Scan(e.t.Context()))

	return job
}

// TestAgentClaimRateLimitDeferralKeepsOrderingKey covers the deported-agent
// half of spec 2026-08-26-02, end to end through the real WS handler.
//
// The agent-side worker loop has no in-process entitlements service, so the
// per-org MaxChecksPerMinute cap is enforced at dispatch in handleClaim. That
// deferral must behave exactly like the in-process one: advance scheduled_at,
// but leave effective_scheduled_at (the claim's ORDER BY key) at the tick the
// job missed, so the same checks cannot lose every window forever.
//
// The cap is pinned at 0 so the bucket is drained deterministically, with no
// dependence on refill timing.
func TestAgentClaimRateLimitDeferralKeepsOrderingKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newEnvWith(t, entitlementsWithLimits(t, entitlements.Limits{
		MaxChecksPerMinute: entitlements.Int(0),
	}))

	check := env.createCheck("starved", []string{testRegion})

	before := env.jobFor(check.UID)
	r.NotNil(before.ScheduledAt)
	r.NotNil(before.EffectiveScheduledAt, "a materialized job is created with its ordering key anchored")

	token := env.mintToken()
	conn, _, _ := env.enroll(token, "dc1-agent")

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10,
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)
	r.Empty(resp.Jobs, "a drained org bucket must dispatch no work at all")

	after := env.jobFor(check.UID)

	r.Nil(after.LeaseWorkerUID, "the deferral must release the lease the claim just took")
	r.Nil(after.LeaseExpiresAt, "the deferral must clear the lease expiry")

	r.NotNil(after.ScheduledAt)
	r.True(after.ScheduledAt.After(*before.ScheduledAt),
		"scheduled_at must advance so the job is not re-claimed inside the same window")

	r.NotNil(after.EffectiveScheduledAt)
	r.WithinDuration(*before.EffectiveScheduledAt, *after.EffectiveScheduledAt, time.Second,
		"the agent dispatch gate must NOT re-anchor effective_scheduled_at — that is what "+
			"starved the late-phase checks of an over-cap org indefinitely")
}

// TestAgentClaimRateLimitDeferralCountsTheSkip covers the agent half of spec
// 2026-08-26-03. A deported agent's executions are dropped by a gate that lives
// in the server, not in the agent, so without an explicit tally here an org
// running entirely on private locations would be throttled with the banner
// staying dark — the exact failure mode the spec exists to close.
func TestAgentClaimRateLimitDeferralCountsTheSkip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newEnvWith(t, entitlementsWithLimits(t, entitlements.Limits{
		MaxChecksPerMinute: entitlements.Int(0),
	}))

	env.createCheck("starved", []string{testRegion})

	today := time.Now().UTC().Format("2006-01-02")

	before, err := env.dbSvc.GetMonthlyUsage(
		t.Context(), env.org.UID, models.UsageCounterKindCheckRateLimited, today,
	)
	r.NoError(err)
	r.Zero(before, "nothing skipped before the agent claims")

	token := env.mintToken()
	conn, _, _ := env.enroll(token, "dc1-agent")

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10,
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)
	r.Empty(resp.Jobs)

	after, err := env.dbSvc.GetMonthlyUsage(
		t.Context(), env.org.UID, models.UsageCounterKindCheckRateLimited, today,
	)
	r.NoError(err)
	r.Equal(1, after, "the agent dispatch gate must count its skip in the org's daily tally")
}

// TestAgentClaimUnderCapCountsNothing is the positive control: an agent claim
// that actually dispatches work must leave the skip counter alone. Without it,
// a gate that counted unconditionally would pass the test above and light the
// banner for every healthy org.
func TestAgentClaimUnderCapCountsNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newEnvWith(t, entitlementsWithLimits(t, entitlements.Limits{
		MaxChecksPerMinute: entitlements.Int(600),
	}))

	env.createCheck("healthy", []string{testRegion})

	token := env.mintToken()
	conn, _, _ := env.enroll(token, "dc1-agent")

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10,
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)
	r.NotEmpty(resp.Jobs, "an org well inside its cap must receive its work")

	skipped, err := env.dbSvc.GetMonthlyUsage(
		t.Context(), env.org.UID, models.UsageCounterKindCheckRateLimited,
		time.Now().UTC().Format("2006-01-02"),
	)
	r.NoError(err)
	r.Zero(skipped, "a dispatched job is not a skip")
}

// createInternalCheck materializes a check the way the SERVER's own plumbing
// does — `internal` is no longer settable through any API (spec
// 2026-08-27-01), so the db service is the only way in. Left enabled so a
// check_job row exists to be claimed: the point under test is what the gate
// does with an internal job it is handed, not whether one is normally
// scheduled (production's internal checks are disabled).
func (e *env) createInternalCheck(slug string, regions []string) *models.Check {
	e.t.Helper()

	check := e.createCheck(slug, regions)
	internal := true
	require.NoError(e.t, e.dbSvc.UpdateCheck(e.t.Context(), check.UID, &models.CheckUpdate{
		Internal: &internal,
	}))
	check.Internal = true

	return check
}

// TestAgentClaimExemptsInternalChecks is spec 2026-08-27-01 on the dispatch
// path: an internal check costs the org no MaxChecks slot and is absent from
// its demand figure, so it must not spend the org's per-minute budget either.
// With the cap pinned at 0, dispatching at all is only possible via the
// exemption.
func TestAgentClaimExemptsInternalChecks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newEnvWith(t, entitlementsWithLimits(t, entitlements.Limits{
		MaxChecksPerMinute: entitlements.Int(0),
	}))

	check := env.createInternalCheck("plumbing", []string{testRegion})

	token := env.mintToken()
	conn, _, _ := env.enroll(token, "dc1-agent")

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10,
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)
	r.Len(resp.Jobs, 1, "an internal check must be dispatched even with the bucket drained")

	skipped, err := env.dbSvc.GetMonthlyUsage(
		t.Context(), env.org.UID, models.UsageCounterKindCheckRateLimited,
		time.Now().UTC().Format("2006-01-02"),
	)
	r.NoError(err)
	r.Zero(skipped, "an exempt check must not tick the org's skipped-today counter")

	// The job was really leased out, not silently dropped from the batch.
	after := env.jobFor(check.UID)
	r.NotNil(after.LeaseWorkerUID)
}

// TestAgentClaimStillMetersNormalChecksAlongsideInternalOnes is the positive
// control, and it runs BOTH kinds through one claim: same org, same drained
// bucket, same batch. The internal job goes out, the normal one is deferred and
// counted — so the exemption is keyed on the check, not on some global escape.
func TestAgentClaimStillMetersNormalChecksAlongsideInternalOnes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newEnvWith(t, entitlementsWithLimits(t, entitlements.Limits{
		MaxChecksPerMinute: entitlements.Int(0),
	}))

	internalCheck := env.createInternalCheck("plumbing", []string{testRegion})
	normalCheck := env.createCheck("customer", []string{testRegion})

	token := env.mintToken()
	conn, _, _ := env.enroll(token, "dc1-agent")

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10,
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)
	r.Len(resp.Jobs, 1, "only the internal job may be dispatched")
	r.Equal(internalCheck.UID, resp.Jobs[0].CheckUID)

	skipped, err := env.dbSvc.GetMonthlyUsage(
		t.Context(), env.org.UID, models.UsageCounterKindCheckRateLimited,
		time.Now().UTC().Format("2006-01-02"),
	)
	r.NoError(err)
	r.Equal(1, skipped, "exactly the normal check's skip is counted")

	deferredJob := env.jobFor(normalCheck.UID)
	r.Nil(deferredJob.LeaseWorkerUID, "the normal check's lease was released by the deferral")
}
