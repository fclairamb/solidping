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

// TestCLICoverage_StatusPageLifecycle exercises the status-page CRUD
// generated-client operations (create/list/get/update/delete) end to end
// against the real statuspages handler.
func TestCLICoverage_StatusPageLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	// A typed enum now: the OpenAPI spec constrains visibility to
	// public|private|password, so oapi-codegen emits a named string type.
	visibility := client.CreateStatusPageRequestVisibilityPublic
	createResp, err := apiClient.CreateStatusPageWithResponse(ctx, TestOrgSlug, client.CreateStatusPageJSONRequestBody{
		Name:       "CLI Coverage Page",
		Slug:       ptrOf("cli-cov-page"),
		Visibility: &visibility,
	})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal("cli-cov-page", createResp.JSON201.Slug)
	pageUID := createResp.JSON201.Uid

	// List — the new page must be present.
	listResp, err := apiClient.ListStatusPagesWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	found := false
	for i := range *listResp.JSON200.Data {
		if (*listResp.JSON200.Data)[i].Uid == pageUID {
			found = true
		}
	}
	r.True(found, "created status page must appear in the list")

	// Get by UID.
	getResp, err := apiClient.GetStatusPageWithResponse(ctx, TestOrgSlug, pageUID.String(), &client.GetStatusPageParams{})
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal(pageUID, getResp.JSON200.Uid)

	// Update name + slug + disable.
	newName := "CLI Coverage Page Renamed"
	disabled := false
	updateResp, err := apiClient.UpdateStatusPageWithResponse(ctx, TestOrgSlug, pageUID.String(),
		client.UpdateStatusPageJSONRequestBody{Name: &newName, Enabled: &disabled})
	r.NoError(err)
	r.Equal(200, updateResp.StatusCode())
	r.NotNil(updateResp.JSON200)
	r.Equal(newName, updateResp.JSON200.Name)
	r.False(updateResp.JSON200.Enabled)

	// Delete; a subsequent GET must 404.
	deleteResp, err := apiClient.DeleteStatusPageWithResponse(ctx, TestOrgSlug, pageUID.String())
	r.NoError(err)
	r.Equal(204, deleteResp.StatusCode())

	goneResp, err := apiClient.GetStatusPageWithResponse(ctx, TestOrgSlug, pageUID.String(), &client.GetStatusPageParams{})
	r.NoError(err)
	r.Equal(404, goneResp.StatusCode())
	r.Nil(goneResp.JSON200)
}

// TestCLICoverage_StatusPageSectionsResourcesSubscribers exercises the section,
// resource and subscriber generated-client operations against the real handlers.
func TestCLICoverage_StatusPageSectionsResourcesSubscribers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	// A status page to hang sections/resources/subscribers off.
	pageResp, err := apiClient.CreateStatusPageWithResponse(ctx, TestOrgSlug, client.CreateStatusPageJSONRequestBody{
		Name: "CLI Cov SR Page",
		Slug: ptrOf("cli-cov-sr-page"),
	})
	r.NoError(err)
	r.Equal(201, pageResp.StatusCode())
	r.NotNil(pageResp.JSON201)
	pageUID := pageResp.JSON201.Uid

	// --- Sections ---
	sectionResp, err := apiClient.CreateStatusPageSectionWithResponse(ctx, TestOrgSlug, pageUID.String(),
		client.CreateStatusPageSectionJSONRequestBody{Name: "Core Systems", Slug: ptrOf("core-systems")})
	r.NoError(err)
	r.Equal(201, sectionResp.StatusCode())
	r.NotNil(sectionResp.JSON201)
	sectionUID := sectionResp.JSON201.Uid

	listSectionsResp, err := apiClient.ListStatusPageSectionsWithResponse(ctx, TestOrgSlug, pageUID.String())
	r.NoError(err)
	r.Equal(200, listSectionsResp.StatusCode())
	r.NotNil(listSectionsResp.JSON200)
	r.NotNil(listSectionsResp.JSON200.Data)
	r.Len(*listSectionsResp.JSON200.Data, 1)

	getSectionResp, err := apiClient.GetStatusPageSectionWithResponse(
		ctx, TestOrgSlug, pageUID.String(), sectionUID.String())
	r.NoError(err)
	r.Equal(200, getSectionResp.StatusCode())
	r.NotNil(getSectionResp.JSON200)
	r.Equal(sectionUID, getSectionResp.JSON200.Uid)

	renamedSection := "Core"
	updateSectionResp, err := apiClient.UpdateStatusPageSectionWithResponse(ctx, TestOrgSlug, pageUID.String(),
		sectionUID.String(), client.UpdateStatusPageSectionJSONRequestBody{Name: &renamedSection})
	r.NoError(err)
	r.Equal(200, updateSectionResp.StatusCode())
	r.NotNil(updateSectionResp.JSON200)
	r.Equal(renamedSection, updateSectionResp.JSON200.Name)

	// Reordering a single section is a no-op that must still succeed.
	reorderSectionsResp, err := apiClient.ReorderStatusPageSectionsWithResponse(ctx, TestOrgSlug, pageUID.String(),
		client.ReorderStatusPageSectionsJSONRequestBody{Uids: []openapi_types.UUID{sectionUID}})
	r.NoError(err)
	r.Equal(204, reorderSectionsResp.StatusCode())

	// --- Resources ---
	checkUID := "40000000-0000-0000-0000-0000000000a0"
	checkSlug := "cli-cov-sr-check"
	cliCovSeedCheck(ctx, t, ts, checkUID, checkSlug, "http", models.JSONMap{"url": "https://example.com"})

	resourceResp, err := apiClient.CreateStatusPageResourceWithResponse(ctx, TestOrgSlug, pageUID.String(),
		sectionUID.String(), client.CreateStatusPageResourceJSONRequestBody{CheckUid: &checkSlug})
	r.NoError(err)
	r.Equal(201, resourceResp.StatusCode())
	r.NotNil(resourceResp.JSON201)
	resourceUID := resourceResp.JSON201.Uid
	r.NotNil(resourceResp.JSON201.CheckUid)
	r.Equal(checkUID, resourceResp.JSON201.CheckUid.String())

	listResourcesResp, err := apiClient.ListStatusPageResourcesWithResponse(
		ctx, TestOrgSlug, pageUID.String(), sectionUID.String())
	r.NoError(err)
	r.Equal(200, listResourcesResp.StatusCode())
	r.NotNil(listResourcesResp.JSON200)
	r.NotNil(listResourcesResp.JSON200.Data)
	r.Len(*listResourcesResp.JSON200.Data, 1)

	publicName := "API"
	updateResourceResp, err := apiClient.UpdateStatusPageResourceWithResponse(ctx, TestOrgSlug, pageUID.String(),
		sectionUID.String(), resourceUID, client.UpdateStatusPageResourceJSONRequestBody{PublicName: &publicName})
	r.NoError(err)
	r.Equal(200, updateResourceResp.StatusCode())
	r.NotNil(updateResourceResp.JSON200)
	r.NotNil(updateResourceResp.JSON200.PublicName)
	r.Equal(publicName, *updateResourceResp.JSON200.PublicName)

	reorderResourcesResp, err := apiClient.ReorderStatusPageResourcesWithResponse(ctx, TestOrgSlug, pageUID.String(),
		sectionUID.String(), client.ReorderStatusPageResourcesJSONRequestBody{Uids: []openapi_types.UUID{resourceUID}})
	r.NoError(err)
	r.Equal(204, reorderResourcesResp.StatusCode())

	// --- Subscribers ---
	now := time.Now()
	sub := models.NewStatusPageSubscriber(
		cliCovOrgUID, pageUID.String(), "watcher@example.com", models.SubscriberScopePage)
	sub.ConfirmToken = uuid.NewString()
	sub.UnsubscribeToken = uuid.NewString()
	sub.ConfirmedAt = &now
	r.NoError(ts.Server.DBService().CreateSubscriber(ctx, sub))

	listSubsResp, err := apiClient.ListStatusPageSubscribersWithResponse(ctx, TestOrgSlug, pageUID.String())
	r.NoError(err)
	r.Equal(200, listSubsResp.StatusCode())
	r.NotNil(listSubsResp.JSON200)
	r.NotNil(listSubsResp.JSON200.Data)
	r.Len(*listSubsResp.JSON200.Data, 1)
	subscriberUID := (*listSubsResp.JSON200.Data)[0].Uid

	removeSubResp, err := apiClient.RemoveStatusPageSubscriberWithResponse(
		ctx, TestOrgSlug, pageUID.String(), subscriberUID)
	r.NoError(err)
	r.Equal(204, removeSubResp.StatusCode())

	goneSubsResp, err := apiClient.ListStatusPageSubscribersWithResponse(ctx, TestOrgSlug, pageUID.String())
	r.NoError(err)
	r.Equal(200, goneSubsResp.StatusCode())
	r.NotNil(goneSubsResp.JSON200)
	if goneSubsResp.JSON200.Data != nil {
		r.Empty(*goneSubsResp.JSON200.Data)
	}

	// --- Teardown of nested resources ---
	delResourceResp, err := apiClient.DeleteStatusPageResourceWithResponse(ctx, TestOrgSlug, pageUID.String(),
		sectionUID.String(), resourceUID)
	r.NoError(err)
	r.Equal(204, delResourceResp.StatusCode())

	delSectionResp, err := apiClient.DeleteStatusPageSectionWithResponse(
		ctx, TestOrgSlug, pageUID.String(), sectionUID.String())
	r.NoError(err)
	r.Equal(204, delSectionResp.StatusCode())
}

// TestCLICoverage_StatusUpdateLifecycle exercises the status-update CRUD
// generated-client operations (create/list/get/update/delete) against the real
// statusupdates handler.
func TestCLICoverage_StatusUpdateLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	pageResp, err := apiClient.CreateStatusPageWithResponse(ctx, TestOrgSlug, client.CreateStatusPageJSONRequestBody{
		Name: "CLI Cov Update Page",
		Slug: ptrOf("cli-cov-upd-page"),
	})
	r.NoError(err)
	r.Equal(201, pageResp.StatusCode())
	r.NotNil(pageResp.JSON201)
	pageUID := pageResp.JSON201.Uid

	createResp, err := apiClient.CreateStatusUpdateWithResponse(ctx, TestOrgSlug, client.CreateStatusUpdateJSONRequestBody{
		StatusPageUid: pageUID,
		Title:         "Investigating latency",
		BodyMarkdown:  "We are looking into elevated latency.",
		Kind:          "investigating",
	})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal("investigating", createResp.JSON201.Kind)
	updateUID := createResp.JSON201.Uid

	// List filtered by status page.
	statusPageFilter := pageUID.String()
	listResp, err := apiClient.ListStatusUpdatesWithResponse(ctx, TestOrgSlug,
		&client.ListStatusUpdatesParams{StatusPage: &statusPageFilter})
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	r.Len(*listResp.JSON200.Data, 1)
	r.Equal(updateUID, (*listResp.JSON200.Data)[0].Uid)

	// Get by UID.
	getResp, err := apiClient.GetStatusUpdateWithResponse(ctx, TestOrgSlug, updateUID)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal(updateUID, getResp.JSON200.Uid)

	// Patch title + kind.
	newTitle := "Latency resolved"
	newKind := "resolved"
	patchResp, err := apiClient.UpdateStatusUpdateWithResponse(ctx, TestOrgSlug, updateUID,
		client.UpdateStatusUpdateJSONRequestBody{Title: &newTitle, Kind: &newKind})
	r.NoError(err)
	r.Equal(200, patchResp.StatusCode())
	r.NotNil(patchResp.JSON200)
	r.Equal(newTitle, patchResp.JSON200.Title)
	r.Equal("resolved", patchResp.JSON200.Kind)

	// Delete; a subsequent GET must 404.
	delResp, err := apiClient.DeleteStatusUpdateWithResponse(ctx, TestOrgSlug, updateUID)
	r.NoError(err)
	r.Equal(204, delResp.StatusCode())

	goneResp, err := apiClient.GetStatusUpdateWithResponse(ctx, TestOrgSlug, updateUID)
	r.NoError(err)
	r.Equal(404, goneResp.StatusCode())
	r.Nil(goneResp.JSON200)
}
