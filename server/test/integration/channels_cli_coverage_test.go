package integration

import (
	"net/http"
	"net/http/httptest"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestCLICoverage_ChannelLifecycle exercises the channel CRUD generated-client
// operations (create/list/get/update/rotate-secret/test/delete) end to end
// against the real integrations handler. A local webhook receiver makes the
// test-channel delivery deterministic.
func TestCLICoverage_ChannelLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	// Create a webhook channel pointed at the local receiver.
	createResp, err := apiClient.CreateChannelWithResponse(ctx, TestOrgSlug, client.CreateChannelJSONRequestBody{
		Type:     "webhook",
		Name:     "cli-cov-webhook",
		Settings: &map[string]interface{}{"url": receiver.URL},
	})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal("webhook", createResp.JSON201.Type)
	r.Equal("cli-cov-webhook", createResp.JSON201.Name)
	channelUID := createResp.JSON201.Uid

	// List channels — the new one must be present.
	listResp, err := apiClient.ListChannelsWithResponse(ctx, TestOrgSlug, &client.ListChannelsParams{})
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	found := false
	for i := range *listResp.JSON200.Data {
		if (*listResp.JSON200.Data)[i].Uid == channelUID {
			found = true
		}
	}
	r.True(found, "created channel must appear in the list")

	// Get the channel by UID.
	getResp, err := apiClient.GetChannelWithResponse(ctx, TestOrgSlug, channelUID)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal(channelUID, getResp.JSON200.Uid)

	// Test delivery — the receiver returns 200 so this must succeed.
	testResp, err := apiClient.TestChannelWithResponse(ctx, TestOrgSlug, channelUID)
	r.NoError(err)
	r.Equal(200, testResp.StatusCode())
	r.NotNil(testResp.JSON200)
	r.True(testResp.JSON200.Success)
	r.Equal(200, testResp.JSON200.StatusCode)

	// Rotate the signing secret.
	rotateResp, err := apiClient.RotateChannelSecretWithResponse(ctx, TestOrgSlug, channelUID)
	r.NoError(err)
	r.Equal(200, rotateResp.StatusCode())
	r.NotNil(rotateResp.JSON200)
	r.Equal(channelUID, rotateResp.JSON200.Uid)

	// Update name + disable.
	newName := "cli-cov-webhook-renamed"
	disabled := false
	updateResp, err := apiClient.UpdateChannelWithResponse(ctx, TestOrgSlug, channelUID,
		client.UpdateChannelJSONRequestBody{Name: &newName, Enabled: &disabled})
	r.NoError(err)
	r.Equal(200, updateResp.StatusCode())
	r.NotNil(updateResp.JSON200)
	r.Equal(newName, updateResp.JSON200.Name)
	r.False(updateResp.JSON200.Enabled)

	// Delete the channel; a subsequent GET must 404.
	deleteResp, err := apiClient.DeleteChannelWithResponse(ctx, TestOrgSlug, channelUID)
	r.NoError(err)
	r.Equal(204, deleteResp.StatusCode())

	goneResp, err := apiClient.GetChannelWithResponse(ctx, TestOrgSlug, channelUID)
	r.NoError(err)
	r.Equal(404, goneResp.StatusCode())
	r.Nil(goneResp.JSON200)
}

// TestCLICoverage_CheckChannelBindings exercises the per-check channel binding
// generated-client operations (list/set/add/remove) against the real
// checkchannels handler.
func TestCLICoverage_CheckChannelBindings(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	checkUID := "40000000-0000-0000-0000-0000000000c0"
	cliCovSeedCheck(ctx, t, ts, checkUID, "cli-chanbind-check", "http",
		models.JSONMap{"url": "https://example.com"})

	apiClient := cliCovAuthedClient(ctx, t, ts)

	// A notifiable channel to bind.
	createResp, err := apiClient.CreateChannelWithResponse(ctx, TestOrgSlug, client.CreateChannelJSONRequestBody{
		Type:     "webhook",
		Name:     "cli-chanbind-webhook",
		Settings: &map[string]interface{}{"url": "https://example.com/hook"},
	})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	channelUID := createResp.JSON201.Uid

	// Initially no channels bound.
	emptyResp, err := apiClient.ListCheckChannelsWithResponse(ctx, TestOrgSlug, "cli-chanbind-check")
	r.NoError(err)
	r.Equal(200, emptyResp.StatusCode())
	r.NotNil(emptyResp.JSON200)
	if emptyResp.JSON200.Data != nil {
		r.Empty(*emptyResp.JSON200.Data)
	}

	// Add a single binding.
	addResp, err := apiClient.AddCheckChannelWithResponse(ctx, TestOrgSlug, "cli-chanbind-check", channelUID)
	r.NoError(err)
	r.Equal(201, addResp.StatusCode())

	boundResp, err := apiClient.ListCheckChannelsWithResponse(ctx, TestOrgSlug, "cli-chanbind-check")
	r.NoError(err)
	r.Equal(200, boundResp.StatusCode())
	r.NotNil(boundResp.JSON200)
	r.NotNil(boundResp.JSON200.Data)
	r.Len(*boundResp.JSON200.Data, 1)
	r.Equal(channelUID, (*boundResp.JSON200.Data)[0].Uid)

	// Replace the full set (idempotent here — same single channel).
	setResp, err := apiClient.SetCheckChannelsWithResponse(ctx, TestOrgSlug, "cli-chanbind-check",
		client.SetCheckChannelsJSONRequestBody{ConnectionUids: []openapi_types.UUID{channelUID}})
	r.NoError(err)
	r.Equal(204, setResp.StatusCode())

	stillBound, err := apiClient.ListCheckChannelsWithResponse(ctx, TestOrgSlug, "cli-chanbind-check")
	r.NoError(err)
	r.Equal(200, stillBound.StatusCode())
	r.NotNil(stillBound.JSON200)
	r.NotNil(stillBound.JSON200.Data)
	r.Len(*stillBound.JSON200.Data, 1)

	// Remove the binding.
	removeResp, err := apiClient.RemoveCheckChannelWithResponse(ctx, TestOrgSlug, "cli-chanbind-check", channelUID)
	r.NoError(err)
	r.Equal(204, removeResp.StatusCode())

	goneResp, err := apiClient.ListCheckChannelsWithResponse(ctx, TestOrgSlug, "cli-chanbind-check")
	r.NoError(err)
	r.Equal(200, goneResp.StatusCode())
	r.NotNil(goneResp.JSON200)
	if goneResp.JSON200.Data != nil {
		r.Empty(*goneResp.JSON200.Data)
	}
}
