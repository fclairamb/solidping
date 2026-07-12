package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestCLICoverage_MaintenanceWindowLifecycle exercises the maintenance-window
// CRUD and check-assignment generated-client operations end to end against the
// real handler.
func TestCLICoverage_MaintenanceWindowLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	checkUID := "40000000-0000-0000-0000-0000000000d0"
	cliCovSeedCheck(ctx, t, ts, checkUID, "cli-mw-check", "http",
		models.JSONMap{"url": "https://example.com"})

	apiClient := cliCovAuthedClient(ctx, t, ts)

	start := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	end := start.Add(2 * time.Hour)
	recurrence := "none"

	// Create.
	createResp, err := apiClient.CreateMaintenanceWindowWithResponse(ctx, TestOrgSlug,
		client.CreateMaintenanceWindowJSONRequestBody{
			Title:      "CLI coverage window",
			StartAt:    start,
			EndAt:      end,
			Recurrence: &recurrence,
		})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal("CLI coverage window", createResp.JSON201.Title)
	r.Equal("none", createResp.JSON201.Recurrence)
	windowUID := createResp.JSON201.Uid

	// List — the new window must be present.
	listResp, err := apiClient.ListMaintenanceWindowsWithResponse(ctx, TestOrgSlug,
		&client.ListMaintenanceWindowsParams{})
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	found := false
	for i := range *listResp.JSON200.Data {
		if (*listResp.JSON200.Data)[i].Uid == windowUID {
			found = true
		}
	}
	r.True(found, "created window must appear in the list")

	// Get.
	getResp, err := apiClient.GetMaintenanceWindowWithResponse(ctx, TestOrgSlug, windowUID)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal(windowUID, getResp.JSON200.Uid)

	// Update the title.
	newTitle := "CLI coverage window renamed"
	updateResp, err := apiClient.UpdateMaintenanceWindowWithResponse(ctx, TestOrgSlug, windowUID,
		client.UpdateMaintenanceWindowJSONRequestBody{Title: &newTitle})
	r.NoError(err)
	r.Equal(200, updateResp.StatusCode())
	r.NotNil(updateResp.JSON200)
	r.Equal(newTitle, updateResp.JSON200.Title)

	// Assign a check to the window.
	checkUUID := uuid.MustParse(checkUID)
	setResp, err := apiClient.SetMaintenanceWindowChecksWithResponse(ctx, TestOrgSlug, windowUID,
		client.SetMaintenanceWindowChecksJSONRequestBody{
			CheckUids: &[]openapi_types.UUID{checkUUID},
		})
	r.NoError(err)
	r.Equal(204, setResp.StatusCode())

	// List the covered checks.
	checksResp, err := apiClient.ListMaintenanceWindowChecksWithResponse(ctx, TestOrgSlug, windowUID)
	r.NoError(err)
	r.Equal(200, checksResp.StatusCode())
	r.NotNil(checksResp.JSON200)
	r.NotNil(checksResp.JSON200.Data)
	r.Len(*checksResp.JSON200.Data, 1)
	r.NotNil((*checksResp.JSON200.Data)[0].CheckUid)
	r.Equal(checkUUID, *(*checksResp.JSON200.Data)[0].CheckUid)

	// Delete; a subsequent GET must 404.
	deleteResp, err := apiClient.DeleteMaintenanceWindowWithResponse(ctx, TestOrgSlug, windowUID)
	r.NoError(err)
	r.Equal(204, deleteResp.StatusCode())

	goneResp, err := apiClient.GetMaintenanceWindowWithResponse(ctx, TestOrgSlug, windowUID)
	r.NoError(err)
	r.Equal(404, goneResp.StatusCode())
	r.Nil(goneResp.JSON200)
}
