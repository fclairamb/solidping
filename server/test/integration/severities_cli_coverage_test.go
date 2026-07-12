package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestCLICoverage_SeverityLifecycle exercises the severity CRUD generated-client
// operations end to end against the real handler.
func TestCLICoverage_SeverityLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	slug := "cli-cov-sev"
	description := "CLI coverage severity"

	// Create a non-default severity so it can be deleted later.
	createResp, err := apiClient.CreateSeverityWithResponse(ctx, TestOrgSlug,
		client.CreateSeverityJSONRequestBody{
			Slug:        &slug,
			Name:        "CLI coverage severity",
			Description: &description,
			Channels:    &[]string{"slack", "email"},
		})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal(slug, createResp.JSON201.Slug)
	r.Equal("CLI coverage severity", createResp.JSON201.Name)
	r.ElementsMatch([]string{"slack", "email"}, createResp.JSON201.Channels)
	r.False(createResp.JSON201.IsDefault)
	severityUID := createResp.JSON201.Uid

	// List — the new severity must be present.
	listResp, err := apiClient.ListSeveritiesWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	found := false
	for i := range *listResp.JSON200.Data {
		if (*listResp.JSON200.Data)[i].Uid == severityUID {
			found = true
		}
	}
	r.True(found, "created severity must appear in the list")

	// Get by slug.
	getResp, err := apiClient.GetSeverityWithResponse(ctx, TestOrgSlug, slug)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal(severityUID, getResp.JSON200.Uid)

	// Update the name and channels.
	newName := "CLI coverage severity renamed"
	updateResp, err := apiClient.UpdateSeverityWithResponse(ctx, TestOrgSlug, severityUID.String(),
		client.UpdateSeverityJSONRequestBody{
			Name:     &newName,
			Channels: &[]string{"webhook"},
		})
	r.NoError(err)
	r.Equal(200, updateResp.StatusCode())
	r.NotNil(updateResp.JSON200)
	r.Equal(newName, updateResp.JSON200.Name)
	r.ElementsMatch([]string{"webhook"}, updateResp.JSON200.Channels)

	// Delete; a subsequent GET must 404.
	deleteResp, err := apiClient.DeleteSeverityWithResponse(ctx, TestOrgSlug, severityUID.String())
	r.NoError(err)
	r.Equal(204, deleteResp.StatusCode())

	goneResp, err := apiClient.GetSeverityWithResponse(ctx, TestOrgSlug, severityUID.String())
	r.NoError(err)
	r.Equal(404, goneResp.StatusCode())
	r.Nil(goneResp.JSON200)
}
