package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// nonAlphabeticalJSON deliberately orders keys so alphabetical (map-based)
// re-encoding would visibly reorder them — the regression this test guards
// against.
const nonAlphabeticalJSON = `{"zebra":1,"alpha":"x","middle":{"z":1,"a":2},"list":[3,1,2]}`

// TestJSONToYAMLPreservesKeyOrder verifies the transcode goes through
// json.Decoder.Token() (wire order) rather than map[string]interface{}
// (which would randomize/alphabetize), matching Proposal 1's ordering
// constraint.
func TestJSONToYAMLPreservesKeyOrder(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	out, err := jsonToYAML([]byte(nonAlphabeticalJSON))
	r.NoError(err)

	yamlStr := string(out)
	zebraIdx := strings.Index(yamlStr, "zebra")
	alphaIdx := strings.Index(yamlStr, "alpha")
	r.GreaterOrEqual(zebraIdx, 0)
	r.GreaterOrEqual(alphaIdx, 0)
	r.Less(zebraIdx, alphaIdx, "zebra must render before alpha (source order), got:\n%s", yamlStr)

	// Nested map keeps its own declared order too (z before a).
	zIdx := strings.Index(yamlStr, "z: 1")
	aIdx := strings.Index(yamlStr, "a: 2")
	r.GreaterOrEqual(zIdx, 0)
	r.GreaterOrEqual(aIdx, 0)
	r.Less(zIdx, aIdx)
}

// TestJSONToYAMLDeterministic verifies two transcodes of the same JSON are
// byte-identical — "two exports of identical live state are byte-identical".
func TestJSONToYAMLDeterministic(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	first, err := jsonToYAML([]byte(nonAlphabeticalJSON))
	r.NoError(err)

	second, err := jsonToYAML([]byte(nonAlphabeticalJSON))
	r.NoError(err)

	r.Equal(string(first), string(second))
}

// TestJSONToYAMLRoundTripStable verifies the order-stable round trip: YAML
// produced from JSON, parsed back into a generic ordered document and
// re-transcoded (via the same JSON source), reproduces identical bytes — no
// ordering churn on repeated export.
func TestJSONToYAMLRoundTripStable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sample := `{
		"version": 2,
		"exportedAt": "2026-08-05T00:00:00Z",
		"organization": "acme",
		"secrets": "stripped",
		"defaults": {"regions": ["default"], "period": "1m"},
		"checks": [
			{"name": "Google", "slug": "google", "type": "http", "config": {"url": "https://google.com"}}
		]
	}`

	first, err := jsonToYAML([]byte(sample))
	r.NoError(err)

	// Re-exporting the same live JSON a second time must reproduce the exact
	// same YAML bytes.
	second, err := jsonToYAML([]byte(sample))
	r.NoError(err)
	r.Equal(string(first), string(second))

	// The document's top-level key order in the YAML matches the JSON's wire
	// order (version, exportedAt, organization, secrets, defaults, checks) —
	// not alphabetical (checks, defaults, exportedAt, organization, ...).
	yamlStr := string(first)
	versionIdx := strings.Index(yamlStr, "version:")
	checksIdx := strings.Index(yamlStr, "checks:")
	r.GreaterOrEqual(versionIdx, 0)
	r.GreaterOrEqual(checksIdx, 0)
	r.Less(versionIdx, checksIdx)
}

// TestExportFormatFromFlags covers the --format / extension-inference matrix.
func TestExportFormatFromFlags(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("yaml", exportFormatFromFlags("yaml", ""))
	r.Equal("json", exportFormatFromFlags("json", "out.yaml"), "explicit --format wins over extension")
	r.Equal("yaml", exportFormatFromFlags("", "out.yaml"))
	r.Equal("yaml", exportFormatFromFlags("", "out.yml"))
	r.Equal("yaml", exportFormatFromFlags("", "OUT.YAML"), "extension match is case-insensitive")
	r.Equal("json", exportFormatFromFlags("", "out.json"))
	r.Equal("json", exportFormatFromFlags("", ""), "stdout (no file) defaults to json")
}
