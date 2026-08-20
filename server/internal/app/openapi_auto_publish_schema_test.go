package app

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// openAPIPropertyNames is the minimal shape needed to walk
// components.schemas.*.properties from the embedded openapi.yaml without
// pulling in a full OpenAPI parser. It mirrors openAPISchemas in
// openapi_results_pagination_test.go, but keeps only the property KEY set —
// which is all this test asserts on.
type openAPIPropertyNames struct {
	Components struct {
		Schemas map[string]struct {
			Properties map[string]yaml.Node `yaml:"properties"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

// TestOpenAPI_AutoPublishSettingsAreOnRequestSchemas pins the auto-publish
// settings onto the REQUEST schemas, not just the response ones (spec
// 2026-08-19-08).
//
// This is not a tidiness test. `server/pkg/client` is GENERATED from this
// file, and `pkg/cli` is built on that client — so a field the handler accepts
// but the request schema omits is a field no generated client can ever send.
// The feature shipped once in exactly that state: `StatusPage` documented all
// three settings, the Go handler decoded them, and the generated
// CreateStatusPageRequest/UpdateStatusPageRequest carried none of them, which
// left the CLI unable to configure the feature at all.
//
// Response-side coverage is deliberately asserted too: it is what makes a
// failure here readable as "the request half drifted" rather than "the whole
// feature vanished".
func TestOpenAPI_AutoPublishSettingsAreOnRequestSchemas(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	raw, err := openAPIFiles.ReadFile("openapi/openapi.yaml")
	r.NoError(err)

	var doc openAPIPropertyNames
	r.NoError(yaml.Unmarshal(raw, &doc))

	pageSettings := []string{"autoPublish", "autoPublishDelaySeconds", "autoResolve"}

	expectations := map[string][]string{
		// Page settings: request AND response.
		"CreateStatusPageRequest": pageSettings,
		"UpdateStatusPageRequest": pageSettings,
		"StatusPage":              pageSettings,
		// The per-resource override, same rule.
		"CreateStatusPageResourceRequest": {"autoPublish"},
		"UpdateStatusPageResourceRequest": {"autoPublish"},
		"StatusPageResource":              {"autoPublish"},
	}

	for schemaName, fields := range expectations {
		schema, ok := doc.Components.Schemas[schemaName]
		r.True(ok, "%s schema must exist", schemaName)

		for _, field := range fields {
			_, present := schema.Properties[field]
			r.True(present,
				"%s must declare %q — pkg/client is generated from this file, so a field "+
					"missing here is a field no generated client can send",
				schemaName, field)
		}
	}
}
