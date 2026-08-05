package checks

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseManifestJSONAndYAMLAreEquivalent verifies that a JSON body and the
// equivalent YAML body parse into the same ExportDocument — the apply endpoint
// accepts both (JSON is what export emits; YAML is the hand-authored surface).
func TestParseManifestJSONAndYAMLAreEquivalent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jsonBody := []byte(`{
  "version": 1,
  "organization": "team-a",
  "checks": [
    {"name": "Google", "slug": "google", "type": "http", "config": {"url": "https://google.com"}, "enabled": true}
  ]
}`)

	yamlBody := []byte(`
version: 1
organization: team-a
checks:
  - name: Google
    slug: google
    type: http
    config:
      url: https://google.com
    enabled: true
`)

	fromJSON, err := ParseManifest(jsonBody, "application/json")
	r.NoError(err)

	fromYAML, err := ParseManifest(yamlBody, "application/yaml")
	r.NoError(err)

	r.Equal(fromJSON, fromYAML, "JSON and YAML manifests must parse to the same document")
	r.Equal(1, fromYAML.Version)
	r.Equal("team-a", fromYAML.Organization)
	r.Len(fromYAML.Checks, 1)
	r.Equal("google", fromYAML.Checks[0].Slug)
	r.Equal("https://google.com", fromYAML.Checks[0].Config["url"])
}

// TestParseManifestContentTypeSniffing verifies the parser picks JSON vs YAML
// from the first non-space byte even when the Content-Type is missing.
func TestParseManifestContentTypeSniffing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Leading whitespace before the JSON object, no content type.
	jsonBody := []byte("   \n  {\"version\":1,\"checks\":[{\"slug\":\"a\",\"type\":\"http\",\"config\":{}}]}")
	doc, err := ParseManifest(jsonBody, "")
	r.NoError(err)
	r.Equal(1, doc.Version)
	r.Equal("a", doc.Checks[0].Slug)
}

// TestParseManifestInvalid rejects malformed bodies.
func TestParseManifestInvalid(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, err := ParseManifest([]byte("{not json"), "application/json")
	r.Error(err)

	_, err = ParseManifest([]byte(":\n  - : :"), "application/yaml")
	r.Error(err)
}

// TestManifestName resolves the managed-label value: the document Organization
// field names the manifest, falling back to the org slug.
func TestManifestName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("team-a", manifestName(&ExportDocument{Organization: "team-a"}, "default"))
	r.Equal("default", manifestName(&ExportDocument{}, "default"))
}

// TestSecretRefPattern documents the exact reference grammar the resolver
// understands: ${env:NAME} and ${param:KEY}.
func TestSecretRefPattern(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(secretRefPattern.MatchString("${env:TOKEN}"))
	r.True(secretRefPattern.MatchString("Bearer ${param:api_key}"))
	r.False(secretRefPattern.MatchString("plain value"))
	r.False(secretRefPattern.MatchString("${unknown:x}"))

	m := secretRefPattern.FindStringSubmatch("${param:my.key}")
	r.Equal("param", m[1])
	r.Equal("my.key", m[2])
}
