package app

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// openAPISchemas is the minimal shape needed to walk components.schemas from
// the embedded openapi.yaml without pulling in a full OpenAPI parser.
type openAPISchemas struct {
	Components struct {
		Schemas map[string]struct {
			Properties map[string]struct {
				Ref string `yaml:"$ref"`
			} `yaml:"properties"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

// TestOpenAPI_ResultsPaginationHasNoTotal pins the API break from spec
// 2026-08-18-04: the results endpoint's pagination schema must not declare
// `total` (the DB layer never computes it — an unbounded COUNT(*) on
// `results`, the largest table in the system, is too expensive on every page
// load), while incidents — which returns a genuine total from a bounded
// query — must keep it. A regression here means either the schema drifted
// back to promising a field nobody fills, or the split accidentally struck
// `total` from incidents too.
func TestOpenAPI_ResultsPaginationHasNoTotal(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	raw, err := openAPIFiles.ReadFile("openapi/openapi.yaml")
	r.NoError(err)

	var doc openAPISchemas
	r.NoError(yaml.Unmarshal(raw, &doc))

	resultsResp, ok := doc.Components.Schemas["OrgResultListResponse"]
	r.True(ok, "OrgResultListResponse schema must exist")
	resultsPaginationRef := resultsResp.Properties["pagination"].Ref
	r.NotEmpty(resultsPaginationRef, "OrgResultListResponse.pagination must be a $ref")

	incidentResp, ok := doc.Components.Schemas["IncidentListResponse"]
	r.True(ok, "IncidentListResponse schema must exist")
	incidentPaginationRef := incidentResp.Properties["pagination"].Ref
	r.NotEmpty(incidentPaginationRef, "IncidentListResponse.pagination must be a $ref")

	// The two endpoints must NOT share a schema: results has no total,
	// incidents does, so collapsing them back into one schema would either
	// resurrect the dead `total` on results or strip the real one from
	// incidents.
	r.NotEqual(incidentPaginationRef, resultsPaginationRef,
		"results and incidents must use distinct pagination schemas")

	resultsPaginationName := schemaNameFromRef(resultsPaginationRef)
	resultsPaginationSchema, ok := doc.Components.Schemas[resultsPaginationName]
	r.True(ok, "results pagination schema %q must exist", resultsPaginationName)
	_, hasTotal := resultsPaginationSchema.Properties["total"]
	r.False(hasTotal, "results pagination schema must not declare total")
	_, hasCursor := resultsPaginationSchema.Properties["cursor"]
	r.True(hasCursor, "results pagination schema must keep cursor")
	_, hasSize := resultsPaginationSchema.Properties["size"]
	r.True(hasSize, "results pagination schema must keep size")

	incidentPaginationName := schemaNameFromRef(incidentPaginationRef)
	incidentPaginationSchema, ok := doc.Components.Schemas[incidentPaginationName]
	r.True(ok, "incident pagination schema %q must exist", incidentPaginationName)
	_, incidentHasTotal := incidentPaginationSchema.Properties["total"]
	r.True(incidentHasTotal, "incidents pagination schema must keep total — it returns a genuine count")
}

func schemaNameFromRef(ref string) string {
	const prefix = "#/components/schemas/"
	if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
		return ref[len(prefix):]
	}

	return ref
}
