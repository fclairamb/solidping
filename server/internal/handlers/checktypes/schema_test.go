package checktypes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// schemaRouter mounts the two schema routes on a real chi router, so the tests
// exercise path-parameter extraction the same way the server does rather than
// stuffing a route context by hand.
func schemaRouter(t *testing.T) http.Handler {
	t.Helper()

	handler := NewHandler(nil, &config.Config{})
	router := chi.NewRouter()

	// A handler error is surfaced as a 500 rather than asserted inside the
	// handler goroutine: an assertion there would race the test's own failure
	// reporting, and every test below already asserts on the status.
	wrap := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return func(writer http.ResponseWriter, req *http.Request) {
			if err := fn(writer, req); err != nil {
				http.Error(writer, err.Error(), http.StatusInternalServerError)
			}
		}
	}

	router.Get("/api/v1/checks/schema", wrap(handler.ListConfigSchemas))
	router.Get("/api/v1/checks/schema/{type}", wrap(handler.GetConfigSchema))

	return router
}

func get(t *testing.T, router http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))

	return rec
}

// TestGetConfigSchemaServesTheDocument pins the content type, the cacheability
// and the fact that the body is the generated schema itself — a consumer points
// an editor at this URL, so a JSON envelope around it would be useless.
func TestGetConfigSchemaServesTheDocument(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec := get(t, schemaRouter(t), "/api/v1/checks/schema/kubernetes")

	r.Equal(http.StatusOK, rec.Code)
	r.Equal("application/schema+json", rec.Header().Get("Content-Type"))
	r.Contains(rec.Header().Get("Cache-Control"), "max-age=")

	var doc map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &doc))

	r.Equal("https://json-schema.org/draft/2020-12/schema", doc["$schema"])
	r.Equal("kubernetes", doc["x-solidping-check-type"])
	r.Contains(doc["description"], "DESCRIPTIVE ONLY",
		"the served document must say it is not the validator")
}

// TestGetConfigSchemaUnknownTypeIs404 pins the error shape: a type this build
// does not implement is a not-found, not a 500 and not an empty 200.
func TestGetConfigSchemaUnknownTypeIs404(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router := schemaRouter(t)

	for _, bad := range []string{"nope", "kubernetes.json", "..%2Fschemas.go"} {
		rec := get(t, router, "/api/v1/checks/schema/"+bad)

		r.Equalf(http.StatusNotFound, rec.Code, "GET .../%s", bad)

		var body struct {
			Code string `json:"code"`
		}

		r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
		r.Equal("NOT_FOUND", body.Code)
	}
}

// TestListConfigSchemasIsACatalog pins the discovery endpoint: wrapped in
// `data`, one entry per published type, each carrying a fetchable path, plus the
// reminder that keeps a reader from mistaking these for the validator.
func TestListConfigSchemasIsACatalog(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec := get(t, schemaRouter(t), "/api/v1/checks/schema")
	r.Equal(http.StatusOK, rec.Code)

	var body ListConfigSchemasResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))

	r.NotEmpty(body.Data)
	r.Contains(body.Note, "not the validator")

	seen := map[string]bool{}
	router := schemaRouter(t)

	for _, entry := range body.Data {
		r.NotEmpty(entry.CheckType)
		r.Equal("/api/v1/checks/schema/"+entry.CheckType, entry.Ref)
		r.Falsef(seen[entry.CheckType], "%q listed twice", entry.CheckType)
		seen[entry.CheckType] = true

		// Every advertised ref must actually resolve — a catalog of 404s is worse
		// than no catalog.
		r.Equalf(http.StatusOK, get(t, router, entry.Ref).Code, "ref %s must resolve", entry.Ref)
	}

	r.True(seen["http"], "the catalog must include the http type")
	r.True(seen["kubernetes"], "the catalog must include the kubernetes type")
}
