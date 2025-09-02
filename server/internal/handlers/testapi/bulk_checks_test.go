package testapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func setupBulkTestHandler(t *testing.T) *Handler {
	t.Helper()

	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	t.Cleanup(func() { _ = svc.Close() })

	require.NoError(t, svc.Initialize(ctx))

	// Create test organization
	org := models.NewOrganization("test", "Test Org")
	require.NoError(t, svc.CreateOrganization(ctx, org))

	return NewHandler(nil, svc, nil)
}

func TestBulkCreateChecks(t *testing.T) {
	t.Parallel()

	t.Run("creates multiple HTTP checks", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=http&slug=http-{nb}"+
				"&url=http://localhost:4000/api/v1/fake?nb={nb}&period=10s&count=5&org=test",
			nil)
		w := httptest.NewRecorder()

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)
		router.ServeHTTP(w, req)

		r.Equal(http.StatusOK, w.Code)

		var resp BulkCreateChecksResponse
		r.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
		r.Equal(5, resp.Created)
		r.Equal(0, resp.Failed)
		r.Empty(resp.Errors)
		r.Equal("http-0", resp.FirstSlug)
		r.Equal("http-4", resp.LastSlug)
	})

	t.Run("replaces {nb} in slug, name, and url", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=http&slug=check-{nb}&name=Check+{nb}&url=http://example.com/{nb}&count=3&org=test",
			nil)
		w := httptest.NewRecorder()

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)
		router.ServeHTTP(w, req)

		r.Equal(http.StatusOK, w.Code)

		var resp BulkCreateChecksResponse
		r.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
		r.Equal(3, resp.Created)
		r.Equal("check-0", resp.FirstSlug)
		r.Equal("check-2", resp.LastSlug)

		// Verify check was created with correct config
		ctx := req.Context()
		org, err := handler.dbService.GetOrganizationBySlug(ctx, "test")
		r.NoError(err)

		check, err := handler.dbService.GetCheckByUidOrSlug(ctx, org.UID, "check-1")
		r.NoError(err)
		r.Equal("Check 1", *check.Name)
		r.Equal("http://example.com/1", check.Config["url"])
	})

	t.Run("missing type returns error", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?slug=http-{nb}&count=5&org=test",
			nil)
		w := httptest.NewRecorder()

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)
		router.ServeHTTP(w, req)

		r.Equal(http.StatusBadRequest, w.Code)
		r.Contains(w.Body.String(), "type")
	})

	t.Run("missing slug returns error", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=http&count=5&org=test",
			nil)
		w := httptest.NewRecorder()

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)
		router.ServeHTTP(w, req)

		r.Equal(http.StatusBadRequest, w.Code)
		r.Contains(w.Body.String(), "slug")
	})

	t.Run("slug without {nb} returns error", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=http&slug=http-fixed&count=5&org=test",
			nil)
		w := httptest.NewRecorder()

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)
		router.ServeHTTP(w, req)

		r.Equal(http.StatusBadRequest, w.Code)
		r.Contains(w.Body.String(), "{nb}")
	})

	t.Run("invalid check type returns error", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=invalid&slug=x-{nb}&count=5&org=test",
			nil)
		w := httptest.NewRecorder()

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)
		router.ServeHTTP(w, req)

		r.Equal(http.StatusBadRequest, w.Code)
		r.Contains(w.Body.String(), "Unknown check type")
	})

	t.Run("count exceeding max returns error", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=http&slug=http-{nb}&count=99999&org=test",
			nil)
		w := httptest.NewRecorder()

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)
		router.ServeHTTP(w, req)

		r.Equal(http.StatusBadRequest, w.Code)
		r.Contains(w.Body.String(), "count")
	})

	t.Run("duplicate slugs report failures", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)

		// Create first batch
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=http&slug=dup-{nb}&url=http://example.com/{nb}&count=3&org=test",
			nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		r.Equal(http.StatusOK, w.Code)

		// Create same slugs again
		req = httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=http&slug=dup-{nb}&url=http://example.com/{nb}&count=3&org=test",
			nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		r.Equal(http.StatusOK, w.Code)

		var resp BulkCreateChecksResponse
		r.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
		r.Equal(0, resp.Created)
		r.Equal(3, resp.Failed)
		r.NotEmpty(resp.Errors)
	})

	t.Run("defaults to test org", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=http&slug=def-{nb}&url=http://example.com/{nb}&count=2",
			nil)
		w := httptest.NewRecorder()

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)
		router.ServeHTTP(w, req)

		r.Equal(http.StatusOK, w.Code)

		var resp BulkCreateChecksResponse
		r.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
		r.Equal(2, resp.Created)
	})
}

func TestBulkDeleteChecks(t *testing.T) {
	t.Parallel()

	t.Run("deletes previously created checks", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		router := bunrouter.New()
		router.POST("/api/v1/test/checks/bulk", handler.BulkCreateChecks)
		router.DELETE("/api/v1/test/checks/bulk", handler.BulkDeleteChecks)

		// Create checks
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/test/checks/bulk?type=http&slug=del-{nb}&url=http://example.com/{nb}&count=5&org=test",
			nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		r.Equal(http.StatusOK, w.Code)

		// Delete them
		req = httptest.NewRequestWithContext(t.Context(), http.MethodDelete,
			"/api/v1/test/checks/bulk?slug=del-{nb}&count=5&org=test",
			nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		r.Equal(http.StatusOK, w.Code)

		var resp BulkDeleteChecksResponse
		r.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
		r.Equal(5, resp.Deleted)
	})

	t.Run("returns zero for non-existent checks", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete,
			"/api/v1/test/checks/bulk?slug=nope-{nb}&count=3&org=test",
			nil)
		w := httptest.NewRecorder()

		router := bunrouter.New()
		router.DELETE("/api/v1/test/checks/bulk", handler.BulkDeleteChecks)
		router.ServeHTTP(w, req)

		r.Equal(http.StatusOK, w.Code)

		var resp BulkDeleteChecksResponse
		r.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
		r.Equal(0, resp.Deleted)
	})

	t.Run("missing parameters return errors", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		handler := setupBulkTestHandler(t)

		router := bunrouter.New()
		router.DELETE("/api/v1/test/checks/bulk", handler.BulkDeleteChecks)

		// Missing slug
		req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete,
			"/api/v1/test/checks/bulk?count=5&org=test",
			nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		r.Equal(http.StatusBadRequest, w.Code)

		// Missing count
		req = httptest.NewRequestWithContext(t.Context(), http.MethodDelete,
			"/api/v1/test/checks/bulk?slug=x-{nb}&org=test",
			nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		r.Equal(http.StatusBadRequest, w.Code)
	})
}
