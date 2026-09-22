package schemas_test

import (
	"encoding/json"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/checkers/schemas"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// TestSchemaPerRegistryTypeBothDirections is the invariant the whole generator
// rests on: exactly one schema file per check type the registry knows, and no
// schema for anything else.
//
// Both directions matter. A missing schema means an editor silently has nothing
// to say about a real check type — the failure mode "generated from the registry"
// exists to prevent. A leftover schema means tooling keeps describing a type the
// server rejects, which is worse than silence.
func TestSchemaPerRegistryTypeBothDirections(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	registryTypes := make([]string, 0, 64)

	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		if configregistry.IsKnownType(checkType) {
			registryTypes = append(registryTypes, string(checkType))
		}
	}

	r.NotEmpty(registryTypes, "the registry must know at least one check type")
	sort.Strings(registryTypes)

	published := schemas.Types()

	r.Equal(registryTypes, published,
		"every registry check type needs a schema and nothing else may have one — "+
			"run `go generate ./internal/checkers/schemas/...` and commit the result")

	for _, checkType := range registryTypes {
		raw, err := schemas.Get(checkType)
		r.NoErrorf(err, "schemas.Get(%q)", checkType)
		r.NotEmptyf(raw, "schema for %q is empty", checkType)
	}
}

// TestGetRejectsUnknownAndTraversal pins that Get answers ErrNotFound (so the
// handler can 404) rather than reaching outside the embedded FS for a crafted
// "type".
func TestGetRejectsUnknownAndTraversal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, bad := range []string{"", "nope", "../schemas.go", "http/../../secret", "http.json", "a/b"} {
		_, err := schemas.Get(bad)
		r.ErrorIsf(err, schemas.ErrNotFound, "Get(%q) must be a not-found", bad)
	}
}

// TestEverySchemaIsAWellFormedDocument checks the shape a consumer relies on:
// draft 2020-12, an object schema, a stable `$id`, the check type echoed back,
// and the never-a-validator disclaimer present in both the description and the
// `x-solidping-validation` block.
func TestEverySchemaIsAWellFormedDocument(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, checkType := range schemas.Types() {
		raw, err := schemas.Get(checkType)
		r.NoError(err)

		var doc map[string]any
		r.NoErrorf(json.Unmarshal(raw, &doc), "schema for %q must be valid JSON", checkType)

		r.Equalf("https://json-schema.org/draft/2020-12/schema", doc["$schema"],
			"%q must declare draft 2020-12", checkType)
		r.Equalf("object", doc["type"], "%q must describe an object", checkType)
		r.Equalf("https://solidping.io/schemas/checks/"+checkType+".json", doc["$id"],
			"%q must carry its stable $id", checkType)
		r.Equalf(checkType, doc["x-solidping-check-type"], "%q must echo its check type", checkType)

		description, ok := doc["description"].(string)
		r.Truef(ok, "%q must carry a description", checkType)
		r.Containsf(description, "DESCRIPTIVE ONLY",
			"%q must say in its own description that it is not the validator", checkType)

		validation, ok := doc["x-solidping-validation"].(map[string]any)
		r.Truef(ok, "%q must carry x-solidping-validation", checkType)
		r.Equalf("go", validation["authority"], "%q must name Go as the validation authority", checkType)

		_, ok = doc["x-solidping-notes"].([]any)
		r.Truef(ok, "%q must carry an x-solidping-notes array (possibly empty)", checkType)
	}
}

// TestSecretFieldsAreMarked pins the secret-reference contract: every key a
// config declares in SecretFields() carries the SolidPing format and the
// `${env:}`/`${param:}` hint, so tooling can nudge an author away from an
// inlined credential the way validateNoInlinedCredentials does.
func TestSecretFieldsAreMarked(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checked := 0

	for _, checkType := range schemas.Types() {
		cfg, ok := configregistry.ParseConfig(checkerdef.CheckType(checkType))
		r.Truef(ok, "no config for %q", checkType)

		secrets := credentials.SecretFieldsFor(cfg)
		if len(secrets) == 0 {
			continue
		}

		raw, err := schemas.Get(checkType)
		r.NoError(err)

		var doc struct {
			Properties map[string]struct {
				Format      string `json:"format"`
				Description string `json:"description"`
			} `json:"properties"`
			SecretFields []string `json:"x-solidping-secret-fields"`
		}

		r.NoError(json.Unmarshal(raw, &doc))

		want := append([]string(nil), secrets...)
		sort.Strings(want)
		r.Equalf(want, doc.SecretFields, "%q must list its secret fields", checkType)

		for _, key := range secrets {
			prop, present := doc.Properties[key]
			r.Truef(present, "%q: secret key %q has no property in the schema", checkType, key)
			r.Equalf("solidping-secret-ref", prop.Format,
				"%q: secret key %q must carry the secret-ref format", checkType, key)
			r.Containsf(prop.Description, "${env:", "%q: secret key %q must point at a reference", checkType, key)

			checked++
		}
	}

	// Positive control: a green run above cannot mean "no secret fields were
	// found because SecretFields() stopped being consulted".
	r.Positive(checked, "at least one check type must declare a secret field")
}

// TestDurationFieldsAreStrings pins the one place naive reflection would lie
// outright: time.Duration is an int64 in Go and a string on the wire, because
// every FromMap runs time.ParseDuration over it. A schema saying "integer" would
// tell an editor to emit exactly the value the server rejects.
func TestDurationFieldsAreStrings(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	found := 0

	r.NoError(fs.WalkDir(schemas.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return err
		}

		raw, err := fs.ReadFile(schemas.FS(), name)
		r.NoError(err)

		var doc struct {
			Properties map[string]map[string]any `json:"properties"`
		}

		r.NoError(json.Unmarshal(raw, &doc))

		for key, prop := range doc.Properties {
			if prop["x-solidping-go-type"] != "time.Duration" {
				continue
			}

			found++

			r.Equalf("string", prop["type"],
				"%s: %q is a time.Duration and must be described as a string", path.Base(name), key)
		}

		return nil
	}))

	r.Positive(found, "at least one config must have a time.Duration field")
}
