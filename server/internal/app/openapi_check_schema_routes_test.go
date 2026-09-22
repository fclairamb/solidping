package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/schemas"
)

// TestOpenAPI_CheckConfigSchemaRoutesDocumented pins the two read-only schema
// routes onto the published API surface (spec 2026-09-22-02).
//
// `server/pkg/client` is GENERATED from openapi.yaml and `web/docs/docs/api/*` is
// rendered from it, so a route the server serves but the spec omits is a route no
// generated client can call and no reader of the API reference knows exists. For
// these two that would defeat the point: the whole deliverable is a
// machine-readable description that third-party tooling can find.
func TestOpenAPI_CheckConfigSchemaRoutesDocumented(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec := loadOpenAPIPaths(t)

	catalog, ok := spec.Paths["/api/v1/checks/schema"]
	r.True(ok, "the schema catalog path must be documented")
	r.Equal("listCheckConfigSchemas", catalog["get"].OperationID)
	r.Contains(catalog["get"].Responses, "200")

	document, ok := spec.Paths["/api/v1/checks/schema/{type}"]
	r.True(ok, "the per-type schema path must be documented")
	r.Equal("getCheckConfigSchema", document["get"].OperationID)
	r.Contains(document["get"].Responses, "200")
	r.Contains(document["get"].Responses, "404")

	_, ok = spec.Components.Schemas["CheckConfigSchemaListResponse"]
	r.True(ok, "the catalog response schema must be declared in components")
}

// TestOpenAPI_CheckConfigSchemaRoutesStateTheyAreNotAValidator is the docs half
// of the spec's "never a validator" rule. The route description is where someone
// deciding what to wire into CI will read it, so an accurate description here is
// as load-bearing as the note inside each schema file.
func TestOpenAPI_CheckConfigSchemaRoutesStateTheyAreNotAValidator(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw, err := openAPIFiles.ReadFile("openapi/openapi.yaml")
	r.NoError(err)

	text := string(raw)

	r.Contains(text, "These schemas are a description, not a validator",
		"the catalog route must say so in its description")
	r.Contains(text, "sp checks validate",
		"the description must point at the real validator")
}

// TestPublishedSchemasCoverEveryCheckType guards the API surface rather than the
// generator: the catalog handler serves whatever is embedded, so an empty or
// half-populated embed would produce a route that returns 200 and says nothing.
func TestPublishedSchemasCoverEveryCheckType(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	published := schemas.Types()
	r.Greater(len(published), 30, "every check type must publish a schema")
	r.Contains(published, "http")
	r.Contains(published, "kubernetes")
	r.Contains(published, "sftp")
}
