package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestCLICoverage_CheckGroupLifecycle exercises the check-group CRUD
// generated-client operations end to end against the real handler.
func TestCLICoverage_CheckGroupLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	slug := "cli-cov-group"
	sortOrder := 5

	// Create.
	createResp, err := apiClient.CreateCheckGroupWithResponse(ctx, TestOrgSlug,
		client.CreateCheckGroupJSONRequestBody{
			Name:      "CLI coverage group",
			Slug:      &slug,
			SortOrder: &sortOrder,
		})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal("CLI coverage group", createResp.JSON201.Name)
	r.Equal(slug, createResp.JSON201.Slug)
	r.Equal(5, createResp.JSON201.SortOrder)
	groupUID := createResp.JSON201.Uid

	// List — the new group must be present.
	listResp, err := apiClient.ListCheckGroupsWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	found := false
	for i := range *listResp.JSON200.Data {
		if (*listResp.JSON200.Data)[i].Uid == groupUID {
			found = true
		}
	}
	r.True(found, "created group must appear in the list")

	// Get by slug.
	getResp, err := apiClient.GetCheckGroupWithResponse(ctx, TestOrgSlug, slug)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal(groupUID, getResp.JSON200.Uid)

	// Update the name.
	newName := "CLI coverage group renamed"
	updateResp, err := apiClient.UpdateCheckGroupWithResponse(ctx, TestOrgSlug, groupUID.String(),
		client.UpdateCheckGroupJSONRequestBody{Name: &newName})
	r.NoError(err)
	r.Equal(200, updateResp.StatusCode())
	r.NotNil(updateResp.JSON200)
	r.Equal(newName, updateResp.JSON200.Name)

	// Delete; a subsequent GET must 404.
	deleteResp, err := apiClient.DeleteCheckGroupWithResponse(ctx, TestOrgSlug, groupUID.String())
	r.NoError(err)
	r.Equal(204, deleteResp.StatusCode())

	goneResp, err := apiClient.GetCheckGroupWithResponse(ctx, TestOrgSlug, groupUID.String())
	r.NoError(err)
	r.Equal(404, goneResp.StatusCode())
	r.Nil(goneResp.JSON200)
}
