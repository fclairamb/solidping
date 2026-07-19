package escalationpolicies_test

// Tests for the list-usage counts (spec 2026-07-19-01): step counts for the
// "0 steps — silent" badge, per-policy check/group usage for the delete-guard
// confirmation, and the org-default blast-radius count.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/escalationpolicies"
)

func newCountsTestEnv(t *testing.T) (*escalationpolicies.Service, db.Service, *models.Organization) {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(t.Context()))

	org := models.NewOrganization("counts-test", "")
	require.NoError(t, dbSvc.CreateOrganization(t.Context(), org))

	return escalationpolicies.NewService(dbSvc), dbSvc, org
}

func addCheck(t *testing.T, dbSvc db.Service, orgUID, slug string, groupUID, policyUID *string) {
	t.Helper()

	check := models.NewCheck(orgUID, slug, "http")
	check.Enabled = false // skip check_job creation in the in-memory test DB
	check.CheckGroupUID = groupUID
	check.EscalationPolicyUID = policyUID
	require.NoError(t, dbSvc.CreateCheck(t.Context(), check))
}

func addGroup(t *testing.T, dbSvc db.Service, orgUID, slug string, policyUID *string) string {
	t.Helper()

	group := models.NewCheckGroup(orgUID, slug, slug)
	group.EscalationPolicyUID = policyUID
	require.NoError(t, dbSvc.CreateCheckGroup(t.Context(), group))

	return group.UID
}

func findItem(items []*escalationpolicies.PolicyListItem, uid string) *escalationpolicies.PolicyListItem {
	for _, it := range items {
		if it.Policy.UID == uid {
			return it
		}
	}

	return nil
}

func TestListPoliciesWithCounts(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newCountsTestEnv(t)
	ctx := context.Background()

	// paging: 1 step. silent: 0 steps.
	paging, err := svc.CreatePolicy(ctx, &escalationpolicies.CreatePolicyInput{
		OrganizationUID: org.UID,
		Name:            "paging",
		Steps: []escalationpolicies.StepInput{
			{DelaySeconds: 0, Targets: []escalationpolicies.TargetInput{{Type: models.EscalationTargetAllAdmins}}},
		},
	})
	require.NoError(t, err)

	silent, err := svc.CreatePolicy(ctx, &escalationpolicies.CreatePolicyInput{
		OrganizationUID: org.UID,
		Name:            "silent",
		Steps:           nil, // zero-step / silent
	})
	require.NoError(t, err)

	// 2 checks + 1 group directly reference paging; the silent policy is used by
	// 1 check only.
	group := addGroup(t, dbSvc, org.UID, "grp", &paging.UID)
	addCheck(t, dbSvc, org.UID, "chk-one", nil, &paging.UID)
	addCheck(t, dbSvc, org.UID, "chk-two", &group, &paging.UID)
	addCheck(t, dbSvc, org.UID, "chk-three", nil, &silent.UID)

	items, err := svc.ListPoliciesWithCounts(ctx, org.UID)
	require.NoError(t, err)
	require.Len(t, items, 2)

	pagingItem := findItem(items, paging.UID)
	require.NotNil(t, pagingItem)
	require.Equal(t, 1, pagingItem.StepCount)
	require.Equal(t, 2, pagingItem.UsageCheckCount)
	require.Equal(t, 1, pagingItem.UsageGroupCount)

	silentItem := findItem(items, silent.UID)
	require.NotNil(t, silentItem)
	require.Equal(t, 0, silentItem.StepCount, "zero-step policy must report 0 steps (silent badge)")
	require.Equal(t, 1, silentItem.UsageCheckCount)
	require.Equal(t, 0, silentItem.UsageGroupCount)
}

func TestCountChecksInheritingOrgDefault(t *testing.T) {
	t.Parallel()

	_, dbSvc, org := newCountsTestEnv(t)
	ctx := context.Background()

	policy := models.NewEscalationPolicy(org.UID, "p")
	require.NoError(t, dbSvc.CreateEscalationPolicy(ctx, policy))

	groupWithPolicy := addGroup(t, dbSvc, org.UID, "grp-with", &policy.UID)
	groupNoPolicy := addGroup(t, dbSvc, org.UID, "grp-none", nil)

	// Inheriting (counted): no direct policy AND (no group OR group has none).
	addCheck(t, dbSvc, org.UID, "bare-check", nil, nil)                // no group, no policy → inherits
	addCheck(t, dbSvc, org.UID, "grp-none-check", &groupNoPolicy, nil) // group w/o policy → inherits
	// Not inheriting (excluded).
	addCheck(t, dbSvc, org.UID, "direct-check", nil, &policy.UID)        // direct policy
	addCheck(t, dbSvc, org.UID, "grp-with-check", &groupWithPolicy, nil) // group provides policy

	count, err := dbSvc.CountChecksInheritingOrgDefault(ctx, org.UID)
	require.NoError(t, err)
	require.Equal(t, 2, count, "only checks resolving to no policy of their own are in the blast radius")
}
