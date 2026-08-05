package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizeForDiffStripsExportedAt verifies exportedAt never registers as
// drift: two documents differing only in exportedAt normalize identically.
func TestNormalizeForDiffStripsExportedAt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	docA := []byte(`{"version":2,"exportedAt":"2026-08-01T00:00:00Z","organization":"acme","checks":[]}`)
	docB := []byte(`{"version":2,"exportedAt":"2026-08-05T12:34:56Z","organization":"acme","checks":[]}`)

	linesA, err := normalizeForDiff(docA)
	r.NoError(err)
	linesB, err := normalizeForDiff(docB)
	r.NoError(err)

	r.True(equalLines(linesA, linesB), "documents differing only in exportedAt must normalize identically")
}

// TestNormalizeForDiffDetectsRealDrift verifies a genuine content difference
// still registers.
func TestNormalizeForDiffDetectsRealDrift(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	docA := []byte(`{"version":2,"organization":"acme","checks":[{"slug":"a"}]}`)
	docB := []byte(`{"version":2,"organization":"acme","checks":[{"slug":"b"}]}`)

	linesA, err := normalizeForDiff(docA)
	r.NoError(err)
	linesB, err := normalizeForDiff(docB)
	r.NoError(err)

	r.False(equalLines(linesA, linesB))
}

// TestNormalizeForDiffJSONAndYAMLEquivalent verifies a YAML file and the
// equivalent JSON normalize to the same lines, so diffing a hand-authored
// YAML manifest against the (JSON) live export works.
func TestNormalizeForDiffJSONAndYAMLEquivalent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jsonDoc := []byte(`{"version":2,"organization":"acme","checks":[{"slug":"google","type":"http"}]}`)
	yamlDoc := []byte("version: 2\norganization: acme\nchecks:\n  - slug: google\n    type: http\n")

	jsonLines, err := normalizeForDiff(jsonDoc)
	r.NoError(err)
	yamlLines, err := normalizeForDiff(yamlDoc)
	r.NoError(err)

	r.True(equalLines(jsonLines, yamlLines))
}

// TestDetectContentType covers the JSON/YAML extension inference reused by
// both `sp apply` and `sp checks import`.
func TestDetectContentType(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("application/json", detectContentType("manifest.json"))
	r.Equal("application/json", detectContentType("MANIFEST.JSON"))
	r.Equal("application/yaml", detectContentType("manifest.yaml"))
	r.Equal("application/yaml", detectContentType("manifest.yml"))
	r.Equal("application/yaml", detectContentType("manifest"))
}
