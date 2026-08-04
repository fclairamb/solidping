package checks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// newImportRouter wires POST /checks/import over a fresh in-memory DB.
func newImportRouter(t *testing.T) (*httpx.Router, string) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("import-fmt", "Import Format Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	router.NewGroup("/api/v1/orgs/:org/checks").POST("/import", handler.ImportChecks)

	return router, org.Slug
}

// postImport issues one import request with the given body and content type.
func postImport(t *testing.T, router *httpx.Router, org, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/orgs/"+org+"/checks/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestImportChecksAcceptsYAMLAndJSON covers the import endpoint's document
// format handling. The dashboard's import flow advertises "JSON/YAML", and
// /apply has always accepted both — import now matches, rather than the UI
// overclaiming what the endpoint takes.
func TestImportChecksAcceptsYAMLAndJSON(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router, org := newImportRouter(t)

	yamlDoc := []byte(`version: 1
organization: import-fmt
checks:
  - name: Yaml Check
    slug: yaml-check
    type: http
    enabled: true
    config:
      url: https://example.com/yaml
`)

	rec := postImport(t, router, org, "application/yaml", yamlDoc)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var result checks.ImportResult
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &result))
	r.Empty(result.Errors)
	r.Equal(1, result.Created)

	// JSON keeps working exactly as before — including with no Content-Type at
	// all, which the body sniffer resolves from the leading brace.
	jsonDoc := []byte(`{"version":1,"organization":"import-fmt","checks":[
		{"name":"Json Check","slug":"json-check","type":"http","enabled":true,
		 "config":{"url":"https://example.com/json"}}]}`)

	rec = postImport(t, router, org, "application/json", jsonDoc)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &result))
	r.Equal(1, result.Created)

	rec = postImport(t, router, org, "", jsonDoc)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &result))
	r.Equal(1, result.Updated, "re-importing the same slug updates it")
}

// TestImportChecksRejectsGarbage is the negative control: broadening the parser
// must not turn malformed input into a silent success.
func TestImportChecksRejectsGarbage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router, org := newImportRouter(t)

	rec := postImport(t, router, org, "application/json", []byte(`{"version": `))
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	rec = postImport(t, router, org, "application/yaml", []byte("\tnot: [valid: yaml"))
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}
