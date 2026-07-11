package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestCLICoverage_LabelsList exercises the label-suggestion list op.
func TestCLICoverage_LabelsList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	resp, err := apiClient.ListLabelsWithResponse(ctx, TestOrgSlug, &client.ListLabelsParams{})
	r.NoError(err)
	r.Equal(200, resp.StatusCode())
	r.NotNil(resp.JSON200)
	r.NotNil(resp.JSON200.Data)
}

// TestCLICoverage_RegionsList exercises the org region list op.
func TestCLICoverage_RegionsList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	resp, err := apiClient.ListRegionsWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, resp.StatusCode())
	r.NotNil(resp.JSON200)
	r.NotNil(resp.JSON200.Data)
}

// TestCLICoverage_CheckTypesList exercises the check-type catalog list op.
func TestCLICoverage_CheckTypesList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	resp, err := apiClient.ListCheckTypesWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, resp.StatusCode())
	r.NotNil(resp.JSON200)
	r.NotNil(resp.JSON200.Data)
	r.NotEmpty(*resp.JSON200.Data)

	foundHTTP := false
	for i := range *resp.JSON200.Data {
		if (*resp.JSON200.Data)[i].Type == "http" {
			foundHTTP = true
		}
	}
	r.True(foundHTTP, "the http check type must be listed")
}

// TestCLICoverage_CheckTypeSamples exercises the sample-config list op.
func TestCLICoverage_CheckTypeSamples(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	resp, err := apiClient.ListCheckTypeSamplesWithResponse(ctx, &client.ListCheckTypeSamplesParams{})
	r.NoError(err)
	r.Equal(200, resp.StatusCode())
	r.NotNil(resp.JSON200)
	r.NotNil(resp.JSON200.Data)

	// Every group must carry its check-type identity.
	for i := range *resp.JSON200.Data {
		r.NotEmpty((*resp.JSON200.Data)[i].CheckType)
	}
}
