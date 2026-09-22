package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/invopop/jsonschema"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

const (
	// schemaIDBase is the stable, version-free identity of a published schema.
	// It matches the release-asset and API paths, so a `$id` seen in an editor
	// is also a URL.
	schemaIDBase = "https://solidping.io/schemas/checks/"

	// secretRefFormat marks a config key whose value is stored encrypted. It is
	// a SolidPing-specific `format`, so a generic validator ignores it while our
	// own tooling can nudge an author towards a reference.
	secretRefFormat = "solidping-secret-ref"

	// secretRefNote is appended to every secret field's description. It names
	// the two reference forms validateNoInlinedCredentials accepts.
	secretRefNote = "Stored encrypted. Prefer a `${env:VAR}` or `${param:key}` reference over an " +
		"inlined credential in a config-as-code manifest."

	// durationNote describes the wire shape of a Go time.Duration in a config
	// map: FromMap parses it with time.ParseDuration, so it is a STRING on the
	// wire even though it is an integer in Go.
	durationNote = "A Go duration string such as \"10s\", \"500ms\" or \"1m30s\"."

	// validationAuthorityNote is the whole point of `x-solidping-validation`,
	// repeated in each schema's description: nobody should wire a CI gate onto
	// this JSON instead of `sp checks validate`.
	validationAuthorityNote = "DESCRIPTIVE ONLY. This schema exists for editor completion and third-party " +
		"tooling; it is NOT the validator. SolidPing validates a check config with the Go " +
		"`Validate()` of its checker (run it yourself with `sp checks validate`), which enforces " +
		"rules this schema does not and cannot express. A config that satisfies this schema may " +
		"still be rejected."
)

// errUnknownCheckType guards the impossible case: knownTypes() filters on
// IsKnownType, so a type reaching buildSchema without a config means the two
// disagree.
var errUnknownCheckType = errors.New("configregistry has no config for check type")

// buildSchema returns the formatted JSON Schema document for one check type.
func buildSchema(checkType checkerdef.CheckType) ([]byte, error) {
	cfg, ok := configregistry.ParseConfig(checkType)
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownCheckType, checkType)
	}

	schema := reflectConfig(cfg)
	schema.Version = "https://json-schema.org/draft/2020-12/schema"
	schema.ID = jsonschema.ID(schemaIDBase + string(checkType) + ".json")
	schema.Title = string(checkType) + " check config"

	secrets := sortedSecretFields(cfg)
	annotateSecrets(schema, secrets)
	applyExclusiveGroups(schema, cfg)

	notes := reconcileRequired(checkType, cfg, schema)

	schema.Description = describe(checkType, notes)
	schema.Extras = map[string]any{
		"x-solidping-check-type":    string(checkType),
		"x-solidping-secret-fields": secrets,
		"x-solidping-validation": map[string]any{
			"authority": "go",
			"command":   "sp checks validate <manifest>",
			"note":      validationAuthorityNote,
		},
		"x-solidping-notes": notes,
	}

	return marshal(schema)
}

// reflectConfig turns the config struct into a schema. ExpandedStruct puts the
// config object at the root (so `properties` are where a consumer expects them)
// while nested types still land in `$defs` behind a `$ref`: `checkjs` nests a
// sub-check inside a sub-check, and inlining everything makes the reflector
// recurse until the stack gives out.
//
// AllowAdditionalProperties is deliberately on. A stored config legitimately
// carries keys the type's own struct does not model — `tunnelCheckUid`,
// `ipVersion` and the shared plumbing the handlers add — so
// `additionalProperties: false` would make the schema reject configs the server
// accepts, which is the exact failure mode this spec's "never a validator" rule
// is guarding against.
func reflectConfig(cfg checkerdef.Config) *jsonschema.Schema {
	reflector := &jsonschema.Reflector{
		Anonymous:                 true,
		ExpandedStruct:            true,
		AllowAdditionalProperties: true,
		Mapper:                    mapGoType,
	}

	return reflector.Reflect(cfg)
}

// mapGoType overrides the reflection of Go types whose JSON shape the reflector
// would get wrong. time.Duration is an int64 in Go and a string on the wire —
// every config's FromMap runs time.ParseDuration over it — so a reflected
// `"type": "integer"` would tell an editor to emit exactly the value the server
// rejects.
func mapGoType(typ reflect.Type) *jsonschema.Schema {
	if typ == reflect.TypeOf(time.Duration(0)) {
		return &jsonschema.Schema{
			Type:        "string",
			Description: durationNote,
			Extras:      map[string]any{"x-solidping-go-type": "time.Duration"},
		}
	}

	return nil
}

// sortedSecretFields returns the config's declared secret keys, sorted, never
// nil — `[]` says "no secrets" where a missing key would say "unknown".
func sortedSecretFields(cfg checkerdef.Config) []string {
	fields := credentials.SecretFieldsFor(cfg)
	out := make([]string, len(fields))
	copy(out, fields)
	sort.Strings(out)

	return out
}

// annotateSecrets stamps every secret-bearing property with the SolidPing
// `format` and the reference hint.
func annotateSecrets(schema *jsonschema.Schema, secrets []string) {
	if schema.Properties == nil {
		return
	}

	for _, key := range secrets {
		prop, ok := schema.Properties.Get(key)
		if !ok || prop == nil {
			continue
		}

		prop.Format = secretRefFormat
		prop.Description = joinSentences(prop.Description, secretRefNote)
	}
}

// applyExclusiveGroups encodes an "exactly one of" rule as a `oneOf` over
// single-key `required` branches: zero present fails every branch, two present
// match two branches, and `oneOf` demands exactly one. It is the only cross-field
// shape with a clean encoding; the rest live in the notes.
func applyExclusiveGroups(schema *jsonschema.Schema, cfg checkerdef.Config) {
	grouper, ok := cfg.(checkerdef.SchemaExclusiveGrouper)
	if !ok {
		return
	}

	for _, group := range grouper.SchemaExclusiveGroups() {
		if len(group) < 2 {
			continue
		}

		branches := make([]*jsonschema.Schema, 0, len(group))
		for _, key := range group {
			branches = append(branches, &jsonschema.Schema{Required: []string{key}})
		}

		// Each group goes into its own `allOf` entry so several groups compose;
		// putting them straight into the root `oneOf` would make them alternatives
		// to each other.
		schema.AllOf = append(schema.AllOf, &jsonschema.Schema{OneOf: branches})
	}
}

// describe builds the schema's `description`: what the type does, the
// never-a-validator disclaimer, then every note, so the disclaimer and the gaps
// are visible in an editor tooltip and not only to something that reads
// `x-solidping-notes`.
func describe(checkType checkerdef.CheckType, notes []string) string {
	parts := make([]string, 0, len(notes)+2)

	if meta := checkerdef.GetCheckTypeMeta(checkType); meta != nil && meta.Description != "" {
		parts = append(parts, meta.Description+".")
	}

	parts = append(parts, validationAuthorityNote)
	parts = append(parts, notes...)

	return strings.Join(parts, "\n\n")
}

// joinSentences appends an addition to an existing description without gluing
// two sentences together.
func joinSentences(existing, addition string) string {
	if existing == "" {
		return addition
	}

	return strings.TrimRight(existing, " ") + " " + addition
}

// marshal renders the schema as indented JSON with a trailing newline, so a
// committed file is reviewable and `git diff` stays quiet across runs.
func marshal(schema *jsonschema.Schema) ([]byte, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}

	// json.Marshal HTML-escapes `<`, `>` and `&`, and the descriptions carry them
	// (`sp checks validate <manifest>`). Schema has a custom MarshalJSON, so an
	// Encoder with SetEscapeHTML(false) would not reach the nested values —
	// undoing the three escapes afterwards does, and none of them can occur in a
	// source string for any other reason. Property order (struct order) survives,
	// which a round-trip through map[string]any would not.
	for escaped, plain := range map[string]string{
		"\\u003c": "<",
		"\\u003e": ">",
		"\\u0026": "&",
	} {
		raw = bytes.ReplaceAll(raw, []byte(escaped), []byte(plain))
	}

	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, fmt.Errorf("indent schema: %w", err)
	}

	buf.WriteByte('\n')

	return buf.Bytes(), nil
}
