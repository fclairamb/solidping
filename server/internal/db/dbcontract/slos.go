package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runSLOs(t *testing.T, f *fixture) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := f.svc

	slo := models.NewSLO(f.org.UID, "API availability", "api-availability", 99.9)
	slo.CheckUID = &f.check.UID
	r.NoError(svc.CreateSLO(ctx, slo))

	got, err := svc.GetSLO(ctx, f.org.UID, slo.UID)
	r.NoError(err)
	r.InDelta(99.9, got.TargetPct, 0.0001)

	bySlug, err := svc.GetSLOBySlug(ctx, f.org.UID, "api-availability")
	r.NoError(err)
	r.Equal(slo.UID, bySlug.UID)

	all, err := svc.ListSLOs(ctx, f.org.UID, models.ListSLOsFilter{})
	r.NoError(err)
	r.Len(all, 1)

	forCheck, err := svc.ListSLOs(ctx, f.org.UID, models.ListSLOsFilter{CheckUID: f.check.UID, EnabledOnly: true, Limit: 5})
	r.NoError(err)
	r.Len(forCheck, 1)

	forChecks, err := svc.ListSLOsForChecks(ctx, f.org.UID, []string{f.check.UID})
	r.NoError(err)
	r.Len(forChecks, 1)

	target := 99.5
	r.NoError(svc.UpdateSLO(ctx, slo.UID, models.SLOUpdate{TargetPct: &target}))
	got, err = svc.GetSLO(ctx, f.org.UID, slo.UID)
	r.NoError(err)
	r.InDelta(target, got.TargetPct, 0.0001)

	// Burn-rate alert policies.
	policy := models.NewSLOAlertPolicy(f.org.UID, slo.UID, &models.SLOAlertPolicyDefault{
		Kind: "fast", LongWindowSeconds: 3600, ShortWindowSeconds: 300, Threshold: 14.4, Severity: "critical",
	})
	r.NoError(svc.CreateSLOAlertPolicy(ctx, policy))

	gotPolicy, err := svc.GetSLOAlertPolicy(ctx, f.org.UID, policy.UID)
	r.NoError(err)
	r.False(gotPolicy.Enabled, "policies start disabled")

	policies, err := svc.ListSLOAlertPolicies(ctx, slo.UID)
	r.NoError(err)
	r.Len(policies, 1)

	enabled, err := svc.ListEnabledSLOAlertPolicies(ctx, 10)
	r.NoError(err)
	r.Empty(enabled)

	on := true
	threshold := 10.0
	r.NoError(svc.UpdateSLOAlertPolicy(ctx, policy.UID, &models.SLOAlertPolicyUpdate{Enabled: &on, Threshold: &threshold}))

	enabled, err = svc.ListEnabledSLOAlertPolicies(ctx, 10)
	r.NoError(err)
	r.Len(enabled, 1)
	r.InDelta(threshold, enabled[0].Threshold, 0.0001)

	// No burn incident is open yet.
	burn, err := svc.ListActiveBurnIncidentsForSLOs(ctx, f.org.UID, []string{slo.UID})
	r.NoError(err)
	r.Empty(burn)
	_, err = svc.FindActiveBurnIncident(ctx, slo.UID, policy.UID)
	r.Error(err)

	r.NoError(svc.DeleteSLO(ctx, f.org.UID, slo.UID))
	_, err = svc.GetSLO(ctx, f.org.UID, slo.UID)
	r.Error(err)
}
