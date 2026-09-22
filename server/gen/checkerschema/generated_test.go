package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// schemaDir is where the committed schemas live, relative to this package.
const schemaDir = "../../internal/checkers/schemas"

// TestCommittedSchemasAreCurrent fails when a committed schema differs from what
// the generator produces right now — a config struct changed and nobody ran
// `go generate`. It runs in-process rather than shelling out, so it is part of
// the ordinary `go test ./...` and not only of the CI diff check.
func TestCommittedSchemasAreCurrent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	types := knownTypes()
	r.NotEmpty(types)

	for _, checkType := range types {
		want, err := buildSchema(checkType)
		r.NoErrorf(err, "build schema for %q", checkType)

		path := filepath.Join(schemaDir, string(checkType)+".json")

		got, err := os.ReadFile(path) //nolint:gosec // fixed test path
		r.NoErrorf(err, "%s is missing — run `go generate ./internal/checkers/schemas/...`", path)

		r.Equalf(string(want), string(got),
			"%s is stale — run `go generate ./internal/checkers/schemas/...` and commit the result", path)
	}
}

// TestGenerationIsIdempotent pins that two runs produce byte-identical output.
// The reflector walks Go maps in places, and a schema whose key order or note
// order wobbled would make every unrelated PR carry a schema diff, which is how
// a generated file stops being trusted.
func TestGenerationIsIdempotent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, checkType := range knownTypes() {
		first, err := buildSchema(checkType)
		r.NoError(err)

		for range 3 {
			again, err := buildSchema(checkType)
			r.NoError(err)
			r.Equalf(string(first), string(again), "generation for %q is not deterministic", checkType)
		}
	}
}

// TestNoUnprobedTypeGoesUnreported is the honesty check on the required-parity
// walk. A type whose validator could not be probed must say so in its notes;
// otherwise an empty `x-solidping-notes` would read as "checked, no gaps" for a
// type that was never checked at all.
func TestNoUnprobedTypeGoesUnreported(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// freebox_line is the known unprobeable type: its `connectionUid` must name a
	// real Freebox integration, which no offline config can supply. If this list
	// grows, the new entry needs the same justification.
	unprobeable := map[string]bool{"freebox_line": true}

	for _, checkType := range knownTypes() {
		doc, err := buildSchema(checkType)
		r.NoError(err)

		says := strings.Contains(string(doc), "could not obtain a config `Validate()` accepts")
		r.Equalf(unprobeable[string(checkType)], says,
			"check type %q: unprobeable=%v but the schema says %v — either the probe regressed "+
				"or the type became probeable and this list is stale",
			checkType, unprobeable[string(checkType)], says)
	}
}
