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

// TestMaintenanceWindowSetChecks_ValidatesUIDs checks that foreign-org and unknown
// uids are rejected with a validation error and leave the associations unchanged.
func TestMaintenanceWindowSetChecks_ValidatesUIDs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	validUID := "40000000-0000-0000-0000-0000000000e0"
	otherValidUID := "40000000-0000-0000-0000-0000000000e1"
	foreignUID := "40000000-0000-0000-0000-0000000000e2"
	cliCovSeedCheck(ctx, t, ts, validUID, "mw-valid", "http", models.JSONMap{"url": "https://example.com"})
	cliCovSeedCheck(ctx, t, ts, otherValidUID, "mw-valid-2", "http", models.JSONMap{"url": "https://example.com"})

	name := "foreign"
	now := time.Now()
	r.NoError(ts.Server.DBService().CreateCheck(ctx, &models.Check{
		UID: foreignUID, OrganizationUID: "10000000-0000-0000-0000-000000000010",
		Name: &name, Slug: &name, Type: "http", Config: models.JSONMap{"url": "https://example.com"},
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}))

	apiClient := cliCovAuthedClient(ctx, t, ts)
	start := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	createResp, err := apiClient.CreateMaintenanceWindowWithResponse(ctx, TestOrgSlug,
		client.CreateMaintenanceWindowJSONRequestBody{Title: "w", StartAt: start, EndAt: start.Add(time.Hour)})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	windowUID := createResp.JSON201.Uid

	set := func(uids ...string) int {
		parsed := make([]openapi_types.UUID, 0, len(uids))
		for _, u := range uids {
			parsed = append(parsed, uuid.MustParse(u))
		}
		resp, setErr := apiClient.SetMaintenanceWindowChecksWithResponse(ctx, TestOrgSlug, windowUID,
			client.SetMaintenanceWindowChecksJSONRequestBody{CheckUids: &parsed})
		r.NoError(setErr)

		return resp.StatusCode()
	}
	listed := func() []string {
		resp, listErr := apiClient.ListMaintenanceWindowChecksWithResponse(ctx, TestOrgSlug, windowUID)
		r.NoError(listErr)
		out := make([]string, 0, len(*resp.JSON200.Data))
		for _, c := range *resp.JSON200.Data {
			out = append(out, c.CheckUid.String())
		}

		return out
	}

	// Valid set with a duplicate: deduplicated.
	r.Equal(204, set(validUID, validUID))
	r.Equal([]string{validUID}, listed())

	// Foreign-org and unknown uids: 422, associations unchanged.
	r.Equal(422, set(otherValidUID, foreignUID))
	r.Equal(422, set("40000000-0000-0000-0000-0000000000ff"))
	r.Equal([]string{validUID}, listed())

	// A valid set replaces the previous one.
	r.Equal(204, set(otherValidUID))
	r.Equal([]string{otherValidUID}, listed())
}
