package schemas_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	jsv "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkers/schemas"
)

// compile loads a published schema into a real JSON Schema validator, so the
// parity assertions below are made by a third-party implementation of the spec
// rather than by our reading of it.
func compile(t *testing.T, checkType string) *jsv.Schema {
	t.Helper()

	raw, err := schemas.Get(checkType)
	require.NoError(t, err)

	doc, err := jsv.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)

	url := "https://solidping.io/schemas/checks/" + checkType + ".json"

	compiler := jsv.NewCompiler()
	require.NoError(t, compiler.AddResource(url, doc))

	schema, err := compiler.Compile(url)
	require.NoError(t, err, "every published schema must compile as draft 2020-12")

	return schema
}

// goRejects reports whether the Go validator — the authority — refuses a config.
func goRejects(checkType string, config map[string]any) bool {
	spec := &checkerdef.CheckSpec{Name: "parity", Slug: "parity", Config: config}

	return configregistry.ValidateSpec(checkerdef.CheckType(checkType), spec) != nil
}

// schemaRejects reports whether the published schema refuses a config.
func schemaRejects(t *testing.T, schema *jsv.Schema, config map[string]any) bool {
	t.Helper()

	// Round-trip through JSON so the validator sees the same value shapes a real
	// request would (float64 for numbers, no Go-native types).
	raw, err := json.Marshal(config)
	require.NoError(t, err)

	value, err := jsv.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)

	return schema.Validate(value) != nil
}

// notesOf returns a check type's x-solidping-notes, joined — the place a gap
// between the schema and the validator must be recorded.
func notesOf(t *testing.T, checkType string) string {
	t.Helper()

	raw, err := schemas.Get(checkType)
	require.NoError(t, err)

	var doc struct {
		Notes []string `json:"x-solidping-notes"`
	}

	require.NoError(t, json.Unmarshal(raw, &doc))

	return strings.Join(doc.Notes, "\n")
}

// TestKubernetesSchemaParity spot-checks the type with the trickiest reflected
// constraint: `kind` is a closed set, and the schema's `enum` is the only place
// that can be stated to an editor.
//
// The assertions run in both directions, because either one alone is worthless:
// a schema that rejects everything would pass the "also rejects" half, and a
// schema that accepts everything would pass nothing but still look tidy.
func TestKubernetesSchemaParity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	schema := compile(t, "kubernetes")

	valid := map[string]any{
		"clusterUid": "cluster-1",
		"namespace":  "default",
		"kind":       "Deployment",
		"name":       "api",
	}

	// Positive control.
	r.False(goRejects("kubernetes", valid), "the baseline config must be valid in Go")
	r.False(schemaRejects(t, schema, valid), "the baseline config must satisfy the schema")

	cases := []struct {
		name  string
		patch func(map[string]any)
	}{
		{"kind outside the enum", func(c map[string]any) { c["kind"] = "StatefulSet" }},
		{"kind empty", func(c map[string]any) { c["kind"] = "" }},
		{"clusterUid missing", func(c map[string]any) { delete(c, "clusterUid") }},
		{"namespace missing", func(c map[string]any) { delete(c, "namespace") }},
		{"name missing", func(c map[string]any) { delete(c, "name") }},
		{"timeout not a duration string", func(c map[string]any) { c["timeout"] = 30 }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			config := cloneConfig(valid)
			testCase.patch(config)

			r.True(goRejects("kubernetes", config), "the Go validator must reject this")
			r.True(schemaRejects(t, schema, config),
				"the schema claims to encode this constraint, so it must reject this too — "+
					"if it cannot, the gap belongs in x-solidping-notes")
		})
	}
}

// TestSFTPSchemaParity spot-checks the cross-field case: sftp requires exactly
// one of `password` / `private_key`, which reflection cannot see and the config
// declares through SchemaExclusiveGroups.
func TestSFTPSchemaParity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	schema := compile(t, "sftp")

	valid := map[string]any{
		"host":     "sftp.example.com",
		"username": "probe",
		"password": "s3cret",
	}

	r.False(goRejects("sftp", valid), "the baseline config must be valid in Go")
	r.False(schemaRejects(t, schema, valid), "the baseline config must satisfy the schema")

	keyOnly := cloneConfig(valid)
	delete(keyOnly, "password")
	keyOnly["private_key"] = "-----BEGIN OPENSSH PRIVATE KEY-----\nQUJD\n-----END OPENSSH PRIVATE KEY-----\n"
	r.False(goRejects("sftp", keyOnly), "private_key alone must be valid in Go")
	r.False(schemaRejects(t, schema, keyOnly), "private_key alone must satisfy the oneOf")

	cases := []struct {
		name  string
		patch func(map[string]any)
	}{
		{"neither credential", func(c map[string]any) { delete(c, "password") }},
		{"both credentials", func(c map[string]any) { c["private_key"] = "-----BEGIN X-----\nQQ==\n-----END X-----\n" }},
		{"host missing", func(c map[string]any) { delete(c, "host") }},
		{"username missing", func(c map[string]any) { delete(c, "username") }},
		{"port not a number", func(c map[string]any) { c["port"] = "22" }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			config := cloneConfig(valid)
			testCase.patch(config)

			r.True(goRejects("sftp", config), "the Go validator must reject this")
			r.True(schemaRejects(t, schema, config),
				"the schema claims to encode this constraint, so it must reject this too")
		})
	}
}

// TestSFTPDocumentedGapsAreReallyGaps is the other half of the parity contract:
// where the schema CANNOT express a rule, the note must exist and the gap must
// be real. A note about a constraint the schema does in fact enforce would be
// just as misleading as a missing one.
func TestSFTPDocumentedGapsAreReallyGaps(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	schema := compile(t, "sftp")
	notes := notesOf(t, "sftp")

	r.Contains(notes, "PEM", "the PEM-format gap must be documented")
	r.Contains(notes, "host_key_fingerprint", "the fingerprint-format gap must be documented")

	gaps := []map[string]any{
		// A private_key that is not PEM: the Go validator refuses it, the schema
		// only knows it is a string.
		{"host": "sftp.example.com", "username": "probe", "private_key": "not-a-pem-key"},
		// A fingerprint without the SHA256: prefix: same shape of gap.
		{
			"host": "sftp.example.com", "username": "probe", "password": "s3cret",
			"host_key_fingerprint": "MD5:00:11",
		},
	}

	for _, config := range gaps {
		r.Truef(goRejects("sftp", config), "the Go validator must reject %v", config)
		r.Falsef(schemaRejects(t, schema, config),
			"the schema is documented as NOT encoding this rule, so it must accept %v — "+
				"if the schema now catches it, drop the note", config)
	}
}

// TestSIPRegisterModeGapIsDocumented pins the second cross-field rule the spec
// calls out: register mode needs credentials, and nothing in the schema says so.
func TestSIPRegisterModeGapIsDocumented(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	schema := compile(t, "sip")
	notes := notesOf(t, "sip")

	r.Contains(notes, "register", "the register-mode credential rule must be documented")

	config := map[string]any{"host": "sip.example.com", "mode": "register"}

	r.True(goRejects("sip", config), "register mode without credentials must be rejected in Go")
	r.False(schemaRejects(t, schema, config),
		"the schema does not encode the register-mode rule, which is why it is a note")
}

// TestEverySchemaAcceptsWhatTheValidatorAccepts is the broad direction of the
// parity guarantee, across all types: a schema must never reject a config the
// server itself accepts. That is the failure that would break an editor for a
// perfectly good manifest, and it is what makes `additionalProperties: true`
// load-bearing rather than lazy.
func TestEverySchemaAcceptsWhatTheValidatorAccepts(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	accepted := 0

	for _, checkType := range schemas.Types() {
		schema := compile(t, checkType)

		for _, spec := range sampleSpecs(t, checkType) {
			if goRejects(checkType, spec) {
				continue
			}

			accepted++

			r.Falsef(schemaRejects(t, schema, spec),
				"%q: the schema rejects a config the Go validator accepts: %v", checkType, spec)
		}
	}

	r.Positive(accepted, "at least one sample config must be valid, or this proves nothing")
}

// sampleSpecs returns the check type's shipped sample configs — real data to
// assert against, rather than configs a test author invented to pass.
func sampleSpecs(t *testing.T, checkType string) []map[string]any {
	t.Helper()

	opts := &checkerdef.ListSampleOptions{Type: checkerdef.Default, BaseURL: "https://example.com"}
	specs := registry.GetAllSampleConfigs(opts)[checkerdef.CheckType(checkType)]

	out := make([]map[string]any, 0, len(specs))
	for idx := range specs {
		out = append(out, specs[idx].Config)
	}

	return out
}

func cloneConfig(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}

	return out
}
