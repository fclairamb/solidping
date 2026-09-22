package main

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/invopop/jsonschema"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
)

// collectNotes assembles the `x-solidping-notes` array: what the config itself
// declares it enforces beyond the struct shape, plus every place the reflected
// `required` list and the Go validator disagree.
//
// Recording the disagreement rather than "fixing" the schema is deliberate. The
// schema's `required` comes from the struct's `omitempty` tags, which is what a
// reflection-generated schema can honestly claim; the validator is the
// authority. Hand-editing the generated file to match would break the one
// property that makes these files trustworthy — that they are derived, not
// maintained.
func collectNotes(
	checkType checkerdef.CheckType,
	cfg checkerdef.Config,
	schema *jsonschema.Schema,
) []string {
	notes := make([]string, 0, 4)

	if noter, ok := cfg.(checkerdef.SchemaNoter); ok {
		notes = append(notes, noter.SchemaNotes()...)
	}

	gaps, warning := requiredGaps(checkType, schema, exclusiveKeys(cfg))
	if warning != "" {
		notes = append(notes, warning)
	}

	return append(notes, gaps...)
}

// exclusiveKeys returns the keys already covered by a `oneOf` exclusive group.
// Their required-ness is encoded there, so the required-parity walk must not
// also report them as a gap.
func exclusiveKeys(cfg checkerdef.Config) map[string]bool {
	out := map[string]bool{}

	grouper, ok := cfg.(checkerdef.SchemaExclusiveGrouper)
	if !ok {
		return out
	}

	for _, group := range grouper.SchemaExclusiveGroups() {
		for _, key := range group {
			out[key] = true
		}
	}

	return out
}

// requiredGaps compares the schema's reflected `required` list against what the
// Go validator actually enforces, and returns one note per disagreement.
//
// The probe needs a config the validator accepts, and the checkers already ship
// one: their sample configs. Starting from a valid sample and removing one key
// at a time answers "does Validate() reject a config without this key?" exactly,
// with no guessed placeholder values. A key absent from a valid sample is, by
// that same token, not required.
func requiredGaps(
	checkType checkerdef.CheckType,
	schema *jsonschema.Schema,
	skip map[string]bool,
) ([]string, string) {
	sample, warning := baseline(checkType, schema)
	if sample == nil {
		return nil, warning
	}

	reflected := map[string]bool{}
	for _, key := range schema.Required {
		reflected[key] = true
	}

	notes := make([]string, 0, 2)

	for _, key := range schemaKeys(schema) {
		if skip[key] {
			continue
		}

		enforced := validatorRequires(checkType, sample, key)

		switch {
		case enforced && !reflected[key]:
			notes = append(notes, fmt.Sprintf(
				"`%s` is not in `required` (its Go field carries `omitempty`), but `Validate()` "+
					"rejects a config without it. The Go validator is authoritative.", key))
		case !enforced && reflected[key]:
			notes = append(notes, fmt.Sprintf(
				"`%s` is listed in `required` (its Go field has no `omitempty`), but `Validate()` "+
					"accepts a config without it. The Go validator is authoritative.", key))
		}
	}

	return notes, warning
}

// baseline returns a config the validator accepts, to remove keys from. It tries
// the checker's own sample configs first — real data, no guessing — and falls
// back to building one from the schema's declared property types for the types
// that ship no sample (or ship a template with a placeholder the validator
// rejects, like freebox_line's empty `connectionUid`).
//
// When neither works it returns nil plus the note that says so. An unprobeable
// type is surfaced rather than silently treated as gap-free: a schema that
// quietly stopped being checked is worse than one that admits it.
func baseline(checkType checkerdef.CheckType, schema *jsonschema.Schema) (map[string]any, string) {
	opts := &checkerdef.ListSampleOptions{Type: checkerdef.Default, BaseURL: "https://example.com"}

	for _, spec := range registry.GetAllSampleConfigs(opts)[checkType] {
		if configregistry.ValidateSpec(checkType, &spec) == nil { //nolint:gosec,exportloopref // Go 1.22+ loop var
			return spec.Config, ""
		}
	}

	if cfg := constructBaseline(checkType, schema); cfg != nil {
		return cfg, ""
	}

	return nil, "Required-field parity against `Validate()` was not verified for this type: the " +
		"generator could not obtain a config `Validate()` accepts, so the `required` list below is " +
		"the reflected one (fields without `omitempty`) and has not been cross-checked."
}

// maxProbeSteps bounds the constructed-baseline search. Each step fixes at most
// one parameter, so a config with a dozen interlocking rules still terminates.
const maxProbeSteps = 64

// constructBaseline builds a config the validator accepts by letting the
// validator drive: run it, read the parameter its ConfigError names, fill that
// parameter with the next candidate value for its declared type, repeat. It
// returns nil when it runs out of candidates or steps.
//
// The candidates are deliberately dull and deterministic — the point is to reach
// *a* valid config, not a realistic one.
func constructBaseline(checkType checkerdef.CheckType, schema *jsonschema.Schema) map[string]any {
	cfg := map[string]any{}
	tried := map[string]int{}

	for range maxProbeSteps {
		spec := checkerdef.CheckSpec{Name: "schema probe", Slug: "schema-probe", Config: maps.Clone(cfg)}

		err := configregistry.ValidateSpec(checkType, &spec)
		if err == nil {
			return cfg
		}

		var cerr *checkerdef.ConfigError
		if !errors.As(err, &cerr) || cerr.Parameter == "" {
			return nil
		}

		value, ok := candidate(schema, cerr.Parameter, tried[cerr.Parameter])
		if !ok {
			return nil
		}

		tried[cerr.Parameter]++
		cfg[cerr.Parameter] = value
	}

	return nil
}

// candidate returns the nth try for a config key, based on the type the schema
// declares for it. The string ladder covers the formats the validators ask for
// (hostname, URL, email, path, absolute name) in decreasing order of likelihood.
func candidate(schema *jsonschema.Schema, key string, attempt int) (any, bool) {
	prop := property(schema, key)
	if prop == nil {
		return nil, false
	}

	if len(prop.Enum) > 0 {
		if attempt >= len(prop.Enum) {
			return nil, false
		}

		return prop.Enum[attempt], true
	}

	var ladder []any

	switch prop.Type {
	case "string":
		ladder = []any{"example.com", "https://example.com", "probe@example.com", "/", "probe"}
	case "integer", "number":
		ladder = []any{float64(1)}
	case "boolean":
		ladder = []any{true}
	case "array":
		ladder = []any{[]any{"example.com"}}
	case "object":
		ladder = []any{map[string]any{}}
	default:
		return nil, false
	}

	if attempt >= len(ladder) {
		return nil, false
	}

	return ladder[attempt], true
}

// property returns the schema for one top-level config key, or nil.
func property(schema *jsonschema.Schema, key string) *jsonschema.Schema {
	if schema.Properties == nil {
		return nil
	}

	prop, ok := schema.Properties.Get(key)
	if !ok {
		return nil
	}

	return prop
}

// validatorRequires reports whether removing key from an otherwise valid config
// makes Validate() fail. It asks about the key's presence, not about which
// parameter the error happens to name, so a cross-field message ("password or
// private_key is required") still counts.
func validatorRequires(checkType checkerdef.CheckType, sample map[string]any, key string) bool {
	if _, present := sample[key]; !present {
		// The sample validates without it, so the validator does not need it.
		return false
	}

	reduced := maps.Clone(sample)
	delete(reduced, key)

	spec := checkerdef.CheckSpec{Name: "schema probe", Slug: "schema-probe", Config: reduced}

	return configregistry.ValidateSpec(checkType, &spec) != nil
}

// schemaKeys returns the schema's property names in a stable order.
func schemaKeys(schema *jsonschema.Schema) []string {
	if schema.Properties == nil {
		return nil
	}

	keys := slices.Collect(schema.Properties.KeysFromOldest())
	sort.Strings(keys)

	return keys
}
