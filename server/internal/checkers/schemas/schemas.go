// Package schemas serves the generated JSON Schema of every check type's
// `config` object.
//
// # These schemas are a description, not a validator
//
// They exist so an editor can complete a config-as-code manifest and a third
// party can generate or lint one without reading Go source. They are NOT the
// validation authority and must never be wired into a CI gate in place of
// `sp checks validate`: SolidPing validates a config with the Go `Validate()` of
// its checker, which enforces formats, bounds and cross-field rules that
// reflection over a struct cannot express. A config that satisfies the schema
// may still be rejected by the server. Every generated file repeats this in its
// `description` and in `x-solidping-validation`, and the gaps a given schema
// knowingly does not encode are listed in its `x-solidping-notes`.
//
// # Generation
//
// The files are produced by server/gen/checkerschema, which reflects over the
// same `XConfig` structs the validators run on, enumerated through
// internal/checkers/configregistry — so adding a check type to the registry is
// what adds its schema. They are committed, and CI fails when they are stale:
//
//	go generate ./internal/checkers/schemas/...
//
// Never hand-edit a file in this directory. To change a schema, change the Go
// config struct (a json tag, an `omitempty`, a `jsonschema:"..."` tag) or the
// config's SchemaNotes/SchemaExclusiveGroups, then regenerate.
package schemas

//go:generate go run github.com/fclairamb/solidping/server/gen/checkerschema -out .

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// files holds every generated schema. Embedding keeps them in the binary, so the
// API can serve them with no deployment step and no disk layout to get wrong.
//
//go:embed *.json
var files embed.FS

// ErrNotFound is returned for a check type this build publishes no schema for.
// It is a distinct error so a handler can answer 404 rather than 500.
var ErrNotFound = errors.New("no config schema for this check type")

// MediaType is the content type a JSON Schema document is served with. It is
// the registered type for JSON Schema; a plain `application/json` would also
// parse, but tooling sniffs this one.
const MediaType = "application/schema+json"

// Get returns the raw JSON Schema document for a check type.
//
// The returned bytes are the embedded file itself — callers must not modify
// them. checkType is matched exactly against the registry's type names, and
// anything that is not a bare type name (a path separator, a dot) is rejected
// rather than resolved, so the lookup cannot be walked out of the embedded FS.
func Get(checkType string) ([]byte, error) {
	if checkType == "" || strings.ContainsAny(checkType, "/\\.") {
		return nil, fmt.Errorf("%q: %w", checkType, ErrNotFound)
	}

	raw, err := files.ReadFile(checkType + ".json")
	if err != nil {
		return nil, fmt.Errorf("%q: %w", checkType, ErrNotFound)
	}

	return raw, nil
}

// Types returns every check type that has a published schema, sorted.
func Types() []string {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		// Reading the root of an embed.FS cannot fail; a panic here would be a
		// build-time corruption, and an empty list would hide it.
		return nil
	}

	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, strings.TrimSuffix(path.Base(entry.Name()), ".json"))
	}

	sort.Strings(out)

	return out
}

// FS exposes the embedded schemas for callers that need the files themselves —
// the release-artifact step and tests that walk the whole set.
func FS() fs.FS {
	return files
}
