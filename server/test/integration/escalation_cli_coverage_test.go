package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestCLICoverage_EscalationPolicyLifecycle exercises the escalation-policy CRUD
// generated-client operations end to end against the real handlers, including
// nested steps and targets.
func TestCLICoverage_EscalationPolicyLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	// Create a policy with a single step targeting all admins.
	createResp, err := apiClient.CreateEscalationPolicyWithResponse(ctx, TestOrgSlug,
		client.CreateEscalationPolicyJSONRequestBody{
			Name: "CLI coverage policy",
			Steps: &[]client.EscalationStepInput{
				{
					DelaySeconds: 0,
					Targets:      &[]client.EscalationTargetInput{{Type: "all_admins"}},
				},
			},
		})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal("CLI coverage policy", createResp.JSON201.Name)
	r.NotNil(createResp.JSON201.Steps)
	r.Len(*createResp.JSON201.Steps, 1)
	policyUID := createResp.JSON201.Uid

	// List — the new policy must be present.
	listResp, err := apiClient.ListEscalationPoliciesWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	found := false
	for i := range *listResp.JSON200.Data {
		if (*listResp.JSON200.Data)[i].Uid == policyUID {
			found = true
		}
	}
	r.True(found, "created policy must appear in the list")

	// Get.
	getResp, err := apiClient.GetEscalationPolicyWithResponse(ctx, TestOrgSlug, policyUID)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal(policyUID, getResp.JSON200.Uid)

	// Update: rename, set repeat, and replace the steps with a user target.
	newName := "CLI coverage policy renamed"
	repeatMax := 2
	repeatAfter := 300
	targetUID := cliCovTestUserUID
	updateResp, err := apiClient.UpdateEscalationPolicyWithResponse(ctx, TestOrgSlug, policyUID,
		client.UpdateEscalationPolicyJSONRequestBody{
			Name:               &newName,
			RepeatMax:          &repeatMax,
			RepeatAfterSeconds: &repeatAfter,
			Steps: &[]client.EscalationStepInput{
				{
					DelaySeconds: 60,
					Targets: &[]client.EscalationTargetInput{
						{Type: "user", TargetUid: &targetUID},
					},
				},
			},
		})
	r.NoError(err)
	r.Equal(200, updateResp.StatusCode())
	r.NotNil(updateResp.JSON200)
	r.Equal(newName, updateResp.JSON200.Name)
	r.Equal(2, updateResp.JSON200.RepeatMax)
	r.NotNil(updateResp.JSON200.RepeatAfterSeconds)
	r.Equal(300, *updateResp.JSON200.RepeatAfterSeconds)
	r.NotNil(updateResp.JSON200.Steps)
	r.Len(*updateResp.JSON200.Steps, 1)
	step := (*updateResp.JSON200.Steps)[0]
	r.Equal(60, step.DelaySeconds)
	r.NotNil(step.Targets)
	r.Len(*step.Targets, 1)
	target := (*step.Targets)[0]
	r.Equal("user", target.Type)
	r.NotNil(target.TargetUid)
	r.Equal(cliCovTestUserUID, *target.TargetUid)

	// Delete; a subsequent GET must 404.
	deleteResp, err := apiClient.DeleteEscalationPolicyWithResponse(ctx, TestOrgSlug, policyUID)
	r.NoError(err)
	r.Equal(204, deleteResp.StatusCode())

	goneResp, err := apiClient.GetEscalationPolicyWithResponse(ctx, TestOrgSlug, policyUID)
	r.NoError(err)
	r.Equal(404, goneResp.StatusCode())
	r.Nil(goneResp.JSON200)
}
