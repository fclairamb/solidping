package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/client"
)

func TestPUTCheckUpsert(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	r.NoError(err)

	slug := "test-google"
	url := "https://google.com"
	checkType := client.UpsertCheckRequestType("http")
	checkName := "Google Check"
	description := "Monitors Google"

	config := map[string]any{
		"url": url,
	}

	// First PUT - should create the check (201 Created)
	createResult, err := apiClient.UpsertCheckWithResponse(ctx, TestOrgSlug, slug, client.UpsertCheckJSONRequestBody{
		Name:        &checkName,
		Description: &description,
		Type:        &checkType,
		Config:      config,
	})
	r.NoError(err)
	r.Equal(201, createResult.HTTPResponse.StatusCode, "expected status 201 for create")
	r.NotNil(createResult.JSON201, "expected JSON201 response for create")

	createResp := createResult.JSON201
	r.NotNil(createResp.Slug)
	r.Equal(slug, *createResp.Slug)
	r.NotNil(createResp.Type)
	r.Equal(string(checkType), string(*createResp.Type))
	r.NotNil(createResp.Description)
	r.Equal(description, *createResp.Description)

	checkUID := createResp.Uid.String()

	// Second PUT with same slug - should update the check (200 OK)
	updatedName := "Updated Google Check"
	updatedDescription := "Updated description for Google"
	enabled := false

	updateResult, err := apiClient.UpsertCheckWithResponse(ctx, TestOrgSlug, slug, client.UpsertCheckJSONRequestBody{
		Name:        &updatedName,
		Description: &updatedDescription,
		Type:        &checkType,
		Config:      config,
		Enabled:     &enabled,
	})
	r.NoError(err)
	r.Equal(200, updateResult.HTTPResponse.StatusCode, "expected status 200 for update")
	r.NotNil(updateResult.JSON200, "expected JSON200 response for update")

	updateResp := updateResult.JSON200
	r.Equal(checkUID, updateResp.Uid.String(), "should have same UID")
	r.NotNil(updateResp.Name)
	r.Equal(updatedName, *updateResp.Name)
	r.NotNil(updateResp.Description)
	r.Equal(updatedDescription, *updateResp.Description)
	r.NotNil(updateResp.Enabled)
	r.Equal(enabled, *updateResp.Enabled)

	// Verify via GET
	getResult, err := apiClient.GetCheckWithResponse(ctx, TestOrgSlug, slug)
	r.NoError(err)
	r.NotNil(getResult.JSON200)

	getResp := getResult.JSON200
	r.NotNil(getResp.Name)
	r.Equal(updatedName, *getResp.Name)
	r.NotNil(getResp.Description)
	r.Equal(updatedDescription, *getResp.Description)
}

func TestPUTCheckWithLabels(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	r.NoError(err)

	slug := "test-github-api"
	checkType := client.UpsertCheckRequestType("http")
	checkName := "GitHub API Check"
	description := "Monitors GitHub API"

	config := map[string]any{
		"url": "https://api.github.com",
	}

	labels := map[string]string{
		"env":     "prod",
		"service": "github",
		"team":    "platform",
	}

	// Create check with labels
	createResult, err := apiClient.UpsertCheckWithResponse(ctx, TestOrgSlug, slug, client.UpsertCheckJSONRequestBody{
		Name:        &checkName,
		Description: &description,
		Type:        &checkType,
		Config:      config,
		Labels:      &labels,
	})
	r.NoError(err)
	r.NotNil(createResult.JSON201)

	createResp := createResult.JSON201
	r.NotNil(createResp.Labels)
	r.Len(*createResp.Labels, 3)

	respLabels := *createResp.Labels
	r.Equal("prod", respLabels["env"])
	r.Equal("github", respLabels["service"])
	r.Equal("platform", respLabels["team"])

	// Update check with different labels
	updatedLabels := map[string]string{
		"env":  "staging", // changed
		"team": "platform",
		"app":  "monitor", // added
	}

	updateResult, err := apiClient.UpsertCheckWithResponse(ctx, TestOrgSlug, slug, client.UpsertCheckJSONRequestBody{
		Name:        &checkName,
		Description: &description,
		Type:        &checkType,
		Config:      config,
		Labels:      &updatedLabels,
	})
	r.NoError(err)
	r.NotNil(updateResult.JSON200)

	updateResp := updateResult.JSON200
	r.NotNil(updateResp.Labels)

	updatedRespLabels := *updateResp.Labels
	r.Len(updatedRespLabels, 3)
	r.Equal("staging", updatedRespLabels["env"])
	r.Equal("monitor", updatedRespLabels["app"])
	r.NotContains(updatedRespLabels, "service", "service label should be removed")
}

func TestListChecksWithLabelFilter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	r.NoError(err)

	// Create multiple checks with different labels
	checks := []struct {
		slug   string
		name   string
		labels map[string]string
	}{
		{
			slug: "api-prod-1",
			name: "API Prod 1",
			labels: map[string]string{
				"env":  "prod",
				"team": "api",
			},
		},
		{
			slug: "api-prod-2",
			name: "API Prod 2",
			labels: map[string]string{
				"env":  "prod",
				"team": "api",
			},
		},
		{
			slug: "web-prod",
			name: "Web Prod",
			labels: map[string]string{
				"env":  "prod",
				"team": "web",
			},
		},
		{
			slug: "api-staging",
			name: "API Staging",
			labels: map[string]string{
				"env":  "staging",
				"team": "api",
			},
		},
	}

	for _, tc := range checks {
		config := map[string]any{
			"url": "https://example.com",
		}
		checkType := client.UpsertCheckRequestType("http")
		_, upsertErr := apiClient.UpsertCheckWithResponse(ctx, TestOrgSlug, tc.slug, client.UpsertCheckJSONRequestBody{
			Name:   &tc.name,
			Type:   &checkType,
			Config: config,
			Labels: &tc.labels,
		})
		r.NoError(upsertErr, "failed to create check %s", tc.slug)
	}

	// Test 1: Filter by env=prod (should get 3 checks)
	prodLabels := "env:prod"
	prodResult, err := apiClient.ListChecksWithResponse(ctx, TestOrgSlug, &client.ListChecksParams{
		Labels: &prodLabels,
	})
	r.NoError(err)
	r.NotNil(prodResult.JSON200)
	r.NotNil(prodResult.JSON200.Data)
	r.Len(*prodResult.JSON200.Data, 3, "expected 3 checks with env=prod")

	// Test 2: Filter by env=prod AND team=api (should get 2 checks)
	apiProdLabels := "env:prod,team:api"
	apiProdResult, err := apiClient.ListChecksWithResponse(ctx, TestOrgSlug, &client.ListChecksParams{
		Labels: &apiProdLabels,
	})
	r.NoError(err)
	r.NotNil(apiProdResult.JSON200)
	r.NotNil(apiProdResult.JSON200.Data)
	r.Len(*apiProdResult.JSON200.Data, 2, "expected 2 checks with env=prod AND team=api")

	// Test 3: Filter by team=web (should get 1 check)
	webLabels := "team:web"
	webResult, err := apiClient.ListChecksWithResponse(ctx, TestOrgSlug, &client.ListChecksParams{
		Labels: &webLabels,
	})
	r.NoError(err)
	r.NotNil(webResult.JSON200)
	r.NotNil(webResult.JSON200.Data)
	r.Len(*webResult.JSON200.Data, 1, "expected 1 check with team=web")

	// Test 4: Filter by non-existent label (should get 0 checks)
	devLabels := "env:dev"
	noResult, err := apiClient.ListChecksWithResponse(ctx, TestOrgSlug, &client.ListChecksParams{
		Labels: &devLabels,
	})
	r.NoError(err)
	r.NotNil(noResult.JSON200)
	r.NotNil(noResult.JSON200.Data)
	r.Empty(*noResult.JSON200.Data, "expected 0 checks with env=dev")
}

func TestCreateCheckWithDescription(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	r.NoError(err)

	slug := "test-with-desc"
	checkType := client.CreateCheckRequestType("http")
	checkName := "Test with Description"
	description := "This is a detailed description of the check"

	config := map[string]any{
		"url": "https://example.com",
	}

	// Create check with description using POST
	createResult, err := apiClient.CreateCheckWithResponse(ctx, TestOrgSlug, client.CreateCheckJSONRequestBody{
		Slug:        &slug,
		Name:        &checkName,
		Description: &description,
		Type:        &checkType,
		Config:      config,
	})
	r.NoError(err)
	r.NotNil(createResult.JSON201)

	createResp := createResult.JSON201
	r.NotNil(createResp.Description)
	r.Equal(description, *createResp.Description)

	// Verify via GET
	getResult, err := apiClient.GetCheckWithResponse(ctx, TestOrgSlug, slug)
	r.NoError(err)
	r.NotNil(getResult.JSON200)

	getResp := getResult.JSON200
	r.NotNil(getResp.Description)
	r.Equal(description, *getResp.Description)
}

func TestPatchCheckDescription(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	r.NoError(err)

	slug := "test-patch-desc"
	checkType := client.CreateCheckRequestType("http")
	checkName := "Test Patch Description"

	config := map[string]any{
		"url": "https://example.com",
	}

	// Create check without description
	_, err = apiClient.CreateCheckWithResponse(ctx, TestOrgSlug, client.CreateCheckJSONRequestBody{
		Slug:   &slug,
		Name:   &checkName,
		Type:   &checkType,
		Config: config,
	})
	r.NoError(err)

	// Update check to add description using PATCH
	newDescription := "Added description via PATCH"
	patchResult, err := apiClient.UpdateCheckWithResponse(ctx, TestOrgSlug, slug, client.UpdateCheckJSONRequestBody{
		Description: &newDescription,
	})
	r.NoError(err)
	r.NotNil(patchResult.JSON200)

	patchResp := patchResult.JSON200
	r.NotNil(patchResp.Description)
	r.Equal(newDescription, *patchResp.Description)

	// Update description again
	updatedDescription := "Updated description via PATCH"
	patchResult2, err := apiClient.UpdateCheckWithResponse(ctx, TestOrgSlug, slug, client.UpdateCheckJSONRequestBody{
		Description: &updatedDescription,
	})
	r.NoError(err)
	r.NotNil(patchResult2.JSON200)

	patchResp2 := patchResult2.JSON200
	r.NotNil(patchResp2.Description)
	r.Equal(updatedDescription, *patchResp2.Description)
}

func TestCreateCheckAutoSlugLength(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	r.NoError(err)

	// Create check with a long URL and no explicit slug — slug should be auto-generated
	config := map[string]any{
		"url": "https://solidping.k8xp.com/api/v1/fake",
	}

	createResult, err := apiClient.CreateCheckWithResponse(ctx, TestOrgSlug, client.CreateCheckJSONRequestBody{
		Config: config,
	})
	r.NoError(err)
	r.NotNil(createResult.JSON201, "expected 201 Created")

	createResp := createResult.JSON201
	r.NotNil(createResp.Slug, "slug should be set")

	slug := *createResp.Slug
	r.LessOrEqual(len(slug), 20, "auto-generated slug %q exceeds 20 chars", slug)
	r.GreaterOrEqual(len(slug), 3, "auto-generated slug %q is too short", slug)

	// Update the check: change the period, sending back the same auto-generated slug
	newPeriod := "00:05:00"
	updateResult, err := apiClient.UpdateCheckWithResponse(ctx, TestOrgSlug, slug, client.UpdateCheckJSONRequestBody{
		Slug:   &slug,
		Period: &newPeriod,
	})
	r.NoError(err)
	r.NotNil(updateResult.JSON200, "expected 200 OK")

	updateResp := updateResult.JSON200
	r.NotNil(updateResp.Slug)
	r.Equal(slug, *updateResp.Slug, "slug should remain unchanged")
	r.NotNil(updateResp.Period)
	r.Equal(newPeriod, *updateResp.Period)
}

func TestPUTCheckValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()
	apiClient := testServer.NewClient()

	// Login
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	r.NoError(err)

	testName := "Test"
	invalidType := client.UpsertCheckRequestType("invalid-type")

	tests := []struct {
		name            string
		slug            string
		request         client.UpsertCheckJSONRequestBody
		expectErrorCode int
	}{
		{
			name: "missing type with unrecognized scheme",
			slug: "test-no-type",
			request: client.UpsertCheckJSONRequestBody{
				Name:   &testName,
				Config: map[string]any{"url": "ftp://example.com"},
			},
			expectErrorCode: 422,
		},
		{
			name: "invalid check type",
			slug: "test-invalid-type",
			request: client.UpsertCheckJSONRequestBody{
				Name:   &testName,
				Type:   &invalidType,
				Config: map[string]any{"url": "https://example.com"},
			},
			expectErrorCode: 422,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			result, err := apiClient.UpsertCheckWithResponse(ctx, TestOrgSlug, tc.slug, tc.request)
			if err == nil {
				r.NotNil(result)
				r.NotNil(result.HTTPResponse)
				r.GreaterOrEqual(result.HTTPResponse.StatusCode, 400, "expected error status code")
				r.Equal(tc.expectErrorCode, result.HTTPResponse.StatusCode)
			}
		})
	}
}
