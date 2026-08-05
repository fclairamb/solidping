package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

// sampleExportJSON is a small export document whose key order deliberately
// doesn't match alphabetical order (version, exportedAt, organization,
// secrets, defaults, checks — not the alphabetical checks, defaults,
// exportedAt, organization, secrets, version), so an ordering regression
// would be visible.
const sampleExportJSON = `{
	"version": 2,
	"exportedAt": "2026-08-05T00:00:00Z",
	"organization": "acme",
	"secrets": "stripped",
	"defaults": {"regions": ["default"], "period": "1m"},
	"checks": [
		{"name": "Google", "slug": "google", "type": "http", "config": {"url": "https://google.com"}}
	]
}`

// TestJSONToYAMLKeyOrderMatchesWireOrder verifies the document's top-level
// key order in the produced YAML matches the JSON's wire order, not
// alphabetical order.
func TestJSONToYAMLKeyOrderMatchesWireOrder(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	out, err := jsonToYAML([]byte(sampleExportJSON))
	r.NoError(err)

	yamlStr := string(out)
	versionIdx := strings.Index(yamlStr, "version:")
	checksIdx := strings.Index(yamlStr, "checks:")
	r.GreaterOrEqual(versionIdx, 0)
	r.GreaterOrEqual(checksIdx, 0)
	r.Less(versionIdx, checksIdx)
}

// TestJSONToYAMLRoundTripStable verifies the real "export -> parse ->
// re-export identical" round trip Proposal 1 requires: the YAML jsonToYAML
// produces is parsed back by an actual YAML parser (into an order-preserving
// yaml.Node, not a map) and re-rendered with the same encoder settings, and
// that reproduces byte-identical output. This is what "no ordering churn on
// repeated export" actually rests on — TestJSONToYAMLDeterministic only
// proves the transcode step itself is deterministic; it doesn't parse
// anything back.
func TestJSONToYAMLRoundTripStable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	exported, err := jsonToYAML([]byte(sampleExportJSON))
	r.NoError(err)

	var node yaml.Node
	r.NoError(yaml.Unmarshal(exported, &node), "the produced YAML must itself be valid YAML")

	reExported := reEncodeYAMLNode(t, &node)

	r.Equal(string(exported), reExported,
		"parsing the exported YAML back and re-rendering it must reproduce identical bytes")
}

// reEncodeYAMLNode re-renders a parsed yaml.Node with the same indent
// jsonToYAML uses, so re-exporting an already-round-tripped document doesn't
// introduce ordering or formatting churn.
func reEncodeYAMLNode(t *testing.T, node *yaml.Node) string {
	t.Helper()

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(yamlIndent)
	require.NoError(t, enc.Encode(node))
	require.NoError(t, enc.Close())

	return buf.String()
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
