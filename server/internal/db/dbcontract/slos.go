package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

//nolint:funlen // linear contract scenario
func runSLOs(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	slo := models.NewSLO(fix.org.UID, "API availability", "api-availability", 99.9)
	slo.CheckUID = &fix.check.UID
	must.NoError(svc.CreateSLO(ctx, slo))

	got, err := svc.GetSLO(ctx, fix.org.UID, slo.UID)
	must.NoError(err)
	must.InDelta(99.9, got.TargetPct, 0.0001)

	bySlug, err := svc.GetSLOBySlug(ctx, fix.org.UID, "api-availability")
	must.NoError(err)
	must.Equal(slo.UID, bySlug.UID)

	all, err := svc.ListSLOs(ctx, fix.org.UID, models.ListSLOsFilter{})
	must.NoError(err)
	must.Len(all, 1)

	forCheck, err := svc.ListSLOs(ctx, fix.org.UID, models.ListSLOsFilter{
		CheckUID: fix.check.UID, EnabledOnly: true, Limit: 5,
	})
	must.NoError(err)
	must.Len(forCheck, 1)

	forChecks, err := svc.ListSLOsForChecks(ctx, fix.org.UID, []string{fix.check.UID})
	must.NoError(err)
	must.Len(forChecks, 1)

	target := 99.5
	must.NoError(svc.UpdateSLO(ctx, slo.UID, models.SLOUpdate{TargetPct: &target}))
	got, err = svc.GetSLO(ctx, fix.org.UID, slo.UID)
	must.NoError(err)
	must.InDelta(target, got.TargetPct, 0.0001)

	// Burn-rate alert policies.
	policy := models.NewSLOAlertPolicy(fix.org.UID, slo.UID, &models.SLOAlertPolicyDefault{
		Kind: "fast", LongWindowSeconds: 3600, ShortWindowSeconds: 300, Threshold: 14.4, Severity: "critical",
	})
	must.NoError(svc.CreateSLOAlertPolicy(ctx, policy))

	gotPolicy, err := svc.GetSLOAlertPolicy(ctx, fix.org.UID, policy.UID)
	must.NoError(err)
	must.False(gotPolicy.Enabled, "policies start disabled")

	policies, err := svc.ListSLOAlertPolicies(ctx, slo.UID)
	must.NoError(err)
	must.Len(policies, 1)

	enabled, err := svc.ListEnabledSLOAlertPolicies(ctx, 10)
	must.NoError(err)
	must.Empty(enabled)

	on := true
	threshold := 10.0
	update := &models.SLOAlertPolicyUpdate{Enabled: &on, Threshold: &threshold}
	must.NoError(svc.UpdateSLOAlertPolicy(ctx, policy.UID, update))

	enabled, err = svc.ListEnabledSLOAlertPolicies(ctx, 10)
	must.NoError(err)
	must.Len(enabled, 1)
	must.InDelta(threshold, enabled[0].Threshold, 0.0001)

	// No burn incident is open yet.
	burn, err := svc.ListActiveBurnIncidentsForSLOs(ctx, fix.org.UID, []string{slo.UID})
	must.NoError(err)
	must.Empty(burn)
	_, err = svc.FindActiveBurnIncident(ctx, slo.UID, policy.UID)
	must.Error(err)

	must.NoError(svc.DeleteSLO(ctx, fix.org.UID, slo.UID))
	_, err = svc.GetSLO(ctx, fix.org.UID, slo.UID)
	must.Error(err)
}
