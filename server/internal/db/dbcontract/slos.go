package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
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

	runSLOAlertPoliciesOrgDeleted(t, fix)
	runSLOAlertPoliciesOrgDeletedLegacy(t, fix)
}

// createEnabledSLOPolicy creates an org with a check, an enabled SLO and an
// enabled burn-rate policy, and returns the org and the policy.
func createEnabledSLOPolicy(t *testing.T, svc db.Service, slug string) (*models.Organization, *models.SLOAlertPolicy) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)

	org := models.NewOrganization(slug, slug)
	must.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, slug+"-check", "http")
	must.NoError(svc.CreateCheck(ctx, check))

	slo := models.NewSLO(org.UID, "Availability", slug+"-slo", 99.9)
	slo.CheckUID = &check.UID
	must.NoError(svc.CreateSLO(ctx, slo))

	policy := models.NewSLOAlertPolicy(org.UID, slo.UID, &models.SLOAlertPolicyDefault{
		Kind: "fast", LongWindowSeconds: 3600, ShortWindowSeconds: 300, Threshold: 14.4, Severity: "critical",
	})
	must.NoError(svc.CreateSLOAlertPolicy(ctx, policy))

	on := true
	must.NoError(svc.UpdateSLOAlertPolicy(ctx, policy.UID, &models.SLOAlertPolicyUpdate{Enabled: &on}))

	return org, policy
}

func enabledPolicyUIDs(t *testing.T, svc db.Service) []string {
	t.Helper()

	policies, err := svc.ListEnabledSLOAlertPolicies(t.Context(), 0)
	require.NoError(t, err)

	uids := make([]string, 0, len(policies))
	for _, p := range policies {
		uids = append(uids, p.UID)
	}

	return uids
}

// Deleting an organization takes its burn-rate policies out of the
// instance-wide evaluation queue, and disables them.
func runSLOAlertPoliciesOrgDeleted(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	_, live := createEnabledSLOPolicy(t, svc, "slo-live-org")
	gone, orphan := createEnabledSLOPolicy(t, svc, "slo-gone-org")

	uids := enabledPolicyUIDs(t, svc)
	must.Contains(uids, live.UID)
	must.Contains(uids, orphan.UID, "both orgs' policies are queued before the delete")

	must.NoError(svc.DeleteOrganization(ctx, gone.UID))

	uids = enabledPolicyUIDs(t, svc)
	must.Contains(uids, live.UID, "a live org's policy stays queued")
	must.NotContains(uids, orphan.UID, "a deleted org's policy is not queued")

	got, err := svc.GetSLOAlertPolicy(ctx, gone.UID, orphan.UID)
	must.NoError(err)
	must.False(got.Enabled, "deleting the org disables its policies")
}

// An org deleted before policies were disabled on delete (deleted_at set,
// policy still enabled) is still kept out of the queue.
func runSLOAlertPoliciesOrgDeletedLegacy(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	gone, orphan := createEnabledSLOPolicy(t, svc, "slo-legacy-gone-org")
	must.Contains(enabledPolicyUIDs(t, svc), orphan.UID)

	_, err := svc.DB().NewUpdate().
		Model((*models.Organization)(nil)).
		Where("uid = ?", gone.UID).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)
	must.NoError(err)

	got, err := svc.GetSLOAlertPolicy(ctx, gone.UID, orphan.UID)
	must.NoError(err)
	must.True(got.Enabled, "the legacy deletion left the policy enabled")

	must.NotContains(enabledPolicyUIDs(t, svc), orphan.UID, "a deleted org's enabled policy is not queued")
}
