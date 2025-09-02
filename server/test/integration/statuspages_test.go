package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// --- Response types for status page tests ---

type statusPageResponse struct {
	UID         string            `json:"uid"`
	Name        string            `json:"name"`
	Slug        string            `json:"slug"`
	Description *string           `json:"description,omitempty"`
	Visibility  string            `json:"visibility"`
	IsDefault   bool              `json:"isDefault"`
	Enabled     bool              `json:"enabled"`
	Sections    []sectionResponse `json:"sections,omitempty"`
	CreatedAt   *time.Time        `json:"createdAt,omitempty"`
}

type sectionResponse struct {
	UID       string             `json:"uid"`
	Name      string             `json:"name"`
	Slug      string             `json:"slug"`
	Position  int                `json:"position"`
	Resources []resourceResponse `json:"resources,omitempty"`
	CreatedAt *time.Time         `json:"createdAt,omitempty"`
}

type resourceResponse struct {
	UID         string             `json:"uid"`
	CheckUID    string             `json:"checkUid"`
	PublicName  *string            `json:"publicName,omitempty"`
	Explanation *string            `json:"explanation,omitempty"`
	Position    int                `json:"position"`
	Check       *resourceCheckInfo `json:"check,omitempty"`
	CreatedAt   *time.Time         `json:"createdAt,omitempty"`
}

type resourceCheckInfo struct {
	Name   *string `json:"name,omitempty"`
	Type   string  `json:"type"`
	Status string  `json:"status"`
}

type listStatusPagesResponse struct {
	Data []statusPageResponse `json:"data"`
}

type listSectionsResponse struct {
	Data []sectionResponse `json:"data"`
}

type listResourcesResponse struct {
	Data []resourceResponse `json:"data"`
}

// --- Test helper ---

type statusPageTestHelper struct {
	testServer *TestServer
	token      string
}

func newStatusPageTestHelper(t *testing.T) *statusPageTestHelper {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	testServer := NewTestServer(t)

	apiClient := testServer.NewClient()
	loginResp, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	r.NoError(err)
	r.NotNil(loginResp.AccessToken)

	return &statusPageTestHelper{
		testServer: testServer,
		token:      *loginResp.AccessToken,
	}
}

func (h *statusPageTestHelper) doRequest(
	t *testing.T, method, path string, body any,
) (*http.Response, error) {
	t.Helper()

	ctx := t.Context()

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}

		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, h.testServer.HTTPServer.URL+path, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}

	client := &http.Client{}

	return client.Do(req)
}

// doJSON makes an authenticated request and decodes the JSON response body.
func doJSON[T any](
	t *testing.T, h *statusPageTestHelper, method, path string, body any,
) (T, int) {
	t.Helper()

	resp, err := h.doRequest(t, method, path, body)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	status := resp.StatusCode

	var result T
	if status != http.StatusNoContent {
		err = json.NewDecoder(resp.Body).Decode(&result)
		require.NoError(t, err)
	}

	return result, status
}

// doGetPublic makes an unauthenticated GET request and decodes the JSON response.
func doGetPublic[T any](
	t *testing.T, h *statusPageTestHelper, path string,
) (T, int) {
	t.Helper()

	ctx := t.Context()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.testServer.HTTPServer.URL+path, nil)
	require.NoError(t, err)

	client := &http.Client{}

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	status := resp.StatusCode

	var result T
	if status == http.StatusOK {
		err = json.NewDecoder(resp.Body).Decode(&result)
		require.NoError(t, err)
	}

	return result, status
}

// assertRequestStatus makes a request and asserts the status code, discarding the body.
func (h *statusPageTestHelper) assertRequestStatus(
	t *testing.T, method, path string, body any, expectedStatus int,
) {
	t.Helper()

	resp, err := h.doRequest(t, method, path, body)
	require.NoError(t, err)
	require.Equal(t, expectedStatus, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}

// assertGetPublicStatus makes an unauthenticated GET and asserts the status code.
func (h *statusPageTestHelper) assertGetPublicStatus(
	t *testing.T, path string, expectedStatus int,
) {
	t.Helper()

	ctx := t.Context()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.testServer.HTTPServer.URL+path, nil)
	require.NoError(t, err)

	client := &http.Client{}

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, expectedStatus, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}

// --- Tests ---

func TestStatusPageFullLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newStatusPageTestHelper(t)

	basePath := "/api/v1/orgs/" + TestOrgSlug + "/status-pages"

	// Step 1: Create a check to use as a resource
	checkUID := createTestCheck(t, h)

	// Step 2: Create a status page
	page, status := doJSON[statusPageResponse](t, h, "POST", basePath, map[string]any{
		"name":        "Production Status",
		"slug":        "production",
		"description": "Production environment status page",
		"visibility":  "public",
		"isDefault":   true,
	})
	r.Equal(http.StatusCreated, status)
	r.NotEmpty(page.UID)
	r.Equal("Production Status", page.Name)
	r.Equal("production", page.Slug)
	r.NotNil(page.Description)
	r.Equal("Production environment status page", *page.Description)
	r.Equal("public", page.Visibility)
	r.True(page.IsDefault)
	r.True(page.Enabled)

	pageUID := page.UID
	pagePath := basePath + "/" + pageUID

	// Step 3: List status pages — should have one
	listResp, status := doJSON[listStatusPagesResponse](t, h, "GET", basePath, nil)
	r.Equal(http.StatusOK, status)
	r.Len(listResp.Data, 1)
	r.Equal(pageUID, listResp.Data[0].UID)

	// Step 4: Get status page by UID
	getPage, status := doJSON[statusPageResponse](t, h, "GET", pagePath, nil)
	r.Equal(http.StatusOK, status)
	r.Equal(pageUID, getPage.UID)
	r.Equal("Production Status", getPage.Name)

	// Step 5: Get status page by slug
	getPageBySlug, status := doJSON[statusPageResponse](t, h, "GET", basePath+"/production", nil)
	r.Equal(http.StatusOK, status)
	r.Equal(pageUID, getPageBySlug.UID)

	// Step 6: Create a section
	section, status := doJSON[sectionResponse](t, h, "POST", pagePath+"/sections", map[string]any{
		"name":     "API Services",
		"slug":     "api-services",
		"position": 0,
	})
	r.Equal(http.StatusCreated, status)
	r.NotEmpty(section.UID)
	r.Equal("API Services", section.Name)
	r.Equal("api-services", section.Slug)
	r.Equal(0, section.Position)

	sectionUID := section.UID
	sectionPath := pagePath + "/sections/" + sectionUID

	// Step 7: Create a second section
	section2, status := doJSON[sectionResponse](t, h, "POST", pagePath+"/sections", map[string]any{
		"name":     "Web Services",
		"slug":     "web-services",
		"position": 1,
	})
	r.Equal(http.StatusCreated, status)
	section2UID := section2.UID

	// Step 8: List sections
	sectionsResp, status := doJSON[listSectionsResponse](t, h, "GET", pagePath+"/sections", nil)
	r.Equal(http.StatusOK, status)
	r.Len(sectionsResp.Data, 2)

	// Step 9: Add a check as a resource to the first section
	publicName := "Main API"
	explanation := "Our main API endpoint"
	resource, status := doJSON[resourceResponse](t, h, "POST", sectionPath+"/resources", map[string]any{
		"checkUid":    checkUID,
		"publicName":  publicName,
		"explanation": explanation,
		"position":    0,
	})
	r.Equal(http.StatusCreated, status)
	r.NotEmpty(resource.UID)
	r.Equal(checkUID, resource.CheckUID)
	r.NotNil(resource.PublicName)
	r.Equal(publicName, *resource.PublicName)
	r.NotNil(resource.Explanation)
	r.Equal(explanation, *resource.Explanation)

	resourceUID := resource.UID

	// Step 10: List resources in the section
	resourcesResp, status := doJSON[listResourcesResponse](t, h, "GET", sectionPath+"/resources", nil)
	r.Equal(http.StatusOK, status)
	r.Len(resourcesResp.Data, 1)
	r.Equal(resourceUID, resourcesResp.Data[0].UID)

	// Step 11: Get status page with sections (using ?with=sections)
	pageWithSections, status := doJSON[statusPageResponse](t, h, "GET", pagePath+"?with=sections", nil)
	r.Equal(http.StatusOK, status)
	r.Equal(pageUID, pageWithSections.UID)
	r.Len(pageWithSections.Sections, 2)

	// Find the API Services section and check its resources
	var apiSection *sectionResponse
	for i := range pageWithSections.Sections {
		if pageWithSections.Sections[i].Slug == "api-services" {
			apiSection = &pageWithSections.Sections[i]

			break
		}
	}
	r.NotNil(apiSection, "should find api-services section")
	r.Len(apiSection.Resources, 1)
	r.Equal(checkUID, apiSection.Resources[0].CheckUID)

	// Step 12: View public status page (unauthenticated)
	publicPage, status := doGetPublic[statusPageResponse](t, h,
		"/api/v1/status-pages/"+TestOrgSlug+"/production")
	r.Equal(http.StatusOK, status)
	r.Equal("Production Status", publicPage.Name)
	r.Len(publicPage.Sections, 2)

	// Check that live check data is present in the resource
	var publicAPISection *sectionResponse
	for i := range publicPage.Sections {
		if publicPage.Sections[i].Slug == "api-services" {
			publicAPISection = &publicPage.Sections[i]

			break
		}
	}
	r.NotNil(publicAPISection)
	r.Len(publicAPISection.Resources, 1)
	r.NotNil(publicAPISection.Resources[0].Check, "resource should include live check data")
	r.Equal("http", publicAPISection.Resources[0].Check.Type)

	// Step 13: View default status page (unauthenticated)
	defaultPage, status := doGetPublic[statusPageResponse](t, h,
		"/api/v1/status-pages/"+TestOrgSlug)
	r.Equal(http.StatusOK, status)
	r.Equal("Production Status", defaultPage.Name)

	// Step 14: Update the status page
	updatedPage, status := doJSON[statusPageResponse](t, h, "PATCH", pagePath, map[string]any{
		"name":        "Production Status Updated",
		"description": "Updated description",
	})
	r.Equal(http.StatusOK, status)
	r.Equal("Production Status Updated", updatedPage.Name)

	// Step 15: Update the section
	updatedSection, status := doJSON[sectionResponse](t, h, "PATCH", sectionPath, map[string]any{
		"name":     "Core API Services",
		"position": 5,
	})
	r.Equal(http.StatusOK, status)
	r.Equal("Core API Services", updatedSection.Name)
	r.Equal(5, updatedSection.Position)

	// Step 16: Delete the resource
	h.assertRequestStatus(t, "DELETE", sectionPath+"/resources/"+resourceUID, nil, http.StatusNoContent)

	// Verify resource is gone
	emptyResources, status := doJSON[listResourcesResponse](t, h, "GET", sectionPath+"/resources", nil)
	r.Equal(http.StatusOK, status)
	r.Empty(emptyResources.Data)

	// Step 17: Delete the second section
	h.assertRequestStatus(t, "DELETE", pagePath+"/sections/"+section2UID, nil, http.StatusNoContent)

	// Step 18: Delete the status page
	h.assertRequestStatus(t, "DELETE", pagePath, nil, http.StatusNoContent)

	// Verify page is gone
	h.assertRequestStatus(t, "GET", pagePath, nil, http.StatusNotFound)

	// Verify public endpoint returns 404
	h.assertGetPublicStatus(t, "/api/v1/status-pages/"+TestOrgSlug+"/production", http.StatusNotFound)
}

func TestStatusPagePrivateNotPubliclyAccessible(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newStatusPageTestHelper(t)

	// Create a private status page
	_, status := doJSON[statusPageResponse](t, h, "POST",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages", map[string]any{
			"name":       "Internal Status",
			"slug":       "internal",
			"visibility": "private",
		})
	r.Equal(http.StatusCreated, status)

	// Try to access via public endpoint — should 404
	h.assertGetPublicStatus(t, "/api/v1/status-pages/"+TestOrgSlug+"/internal", http.StatusNotFound)
}

func TestStatusPageSlugValidation(t *testing.T) {
	t.Parallel()

	h := newStatusPageTestHelper(t)

	tests := []struct {
		name string
		slug string
	}{
		{name: "too short", slug: "ab"},
		{name: "starts with digit", slug: "1abc"},
		{name: "contains uppercase", slug: "Abc"},
		{name: "contains spaces", slug: "abc def"},
		{name: "is a UUID", slug: "550e8400-e29b-41d4-a716-446655440000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h.assertRequestStatus(t, "POST",
				"/api/v1/orgs/"+TestOrgSlug+"/status-pages",
				map[string]any{"name": "Test Page", "slug": tc.slug},
				http.StatusUnprocessableEntity)
		})
	}
}

func TestStatusPageDuplicateSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newStatusPageTestHelper(t)

	// Create first page
	_, status := doJSON[statusPageResponse](t, h, "POST",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages", map[string]any{
			"name": "First Page",
			"slug": "duplicate-test",
		})
	r.Equal(http.StatusCreated, status)

	// Try to create second page with same slug
	h.assertRequestStatus(t, "POST",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages",
		map[string]any{"name": "Second Page", "slug": "duplicate-test"},
		http.StatusUnprocessableEntity)
}

func TestStatusPageUnauthorized(t *testing.T) {
	t.Parallel()

	h := newStatusPageTestHelper(t)

	// Try to access without auth
	h.assertGetPublicStatus(t, "/api/v1/orgs/"+TestOrgSlug+"/status-pages", http.StatusUnauthorized)
}

// --- Helper ---

const testStatusPageCheckUID = "40000000-0000-0000-0000-000000000001"

func createTestCheck(t *testing.T, h *statusPageTestHelper) string {
	t.Helper()

	ctx := t.Context()
	dbService := h.testServer.Server.DBService()

	checkName := "Status Page Test HTTP Check"
	checkSlug := "sp-test-http-check"
	check := &models.Check{
		UID:             testStatusPageCheckUID,
		OrganizationUID: "10000000-0000-0000-0000-000000000001", // matches test org
		Name:            &checkName,
		Slug:            &checkSlug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://example.com"},
		Enabled:         true,
		Status:          models.CheckStatusUp,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if err := dbService.CreateCheck(ctx, check); err != nil {
		t.Fatalf("failed to create test check: %v", err)
	}

	return testStatusPageCheckUID
}
