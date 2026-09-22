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

// reconcileRequired rewrites the schema's `required` list to what the Go
// validator actually enforces, and returns the `x-solidping-notes` array: what
// the config declares it enforces beyond the struct shape, plus one note per
// place the struct's `omitempty` tags and the validator disagreed.
//
// `omitempty` is the only thing reflection can go on, and it is wrong in both
// directions here: ssl's `port` has no `omitempty` yet `Validate()` accepts a
// config without it (so a reflected schema would reject a config the server
// takes — the harmful direction), while tcp's `host` carries `omitempty` and is
// nonetheless mandatory. The struct's `Validate()` wins, and the correction is
// recorded rather than silently applied: a reader needs to know the list was
// derived from behaviour, not from tags. What is never allowed is hand-editing
// the generated file, which would break the one property that makes these files
// trustworthy — that they are derived, not maintained.
func reconcileRequired(
	checkType checkerdef.CheckType,
	cfg checkerdef.Config,
	schema *jsonschema.Schema,
) []string {
	notes := make([]string, 0, 4)

	if noter, ok := cfg.(checkerdef.SchemaNoter); ok {
		notes = append(notes, noter.SchemaNotes()...)
	}

	required, gaps, warning := requiredFromValidator(checkType, schema, exclusiveKeys(cfg))
	if warning != "" {
		notes = append(notes, warning)
	} else {
		schema.Required = required
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

// requiredFromValidator returns the `required` list the Go validator actually
// enforces, plus one note per key where that differs from the struct's
// `omitempty` tags.
//
// The probe needs a config the validator accepts, and the checkers already ship
// one: their sample configs. Starting from a valid config and removing one key
// at a time answers "does Validate() reject a config without this key?" exactly,
// with no guessed placeholder values. A key absent from a valid config is, by
// that same token, not required.
//
// Keys in an exclusive group are left alone: their required-ness is already
// encoded in the group's `oneOf`, and removing one of them from a valid config
// naturally fails, which would wrongly mark it unconditionally required.
//
// The probe runs over EVERY valid config the type offers, not one, and only a key
// that is indispensable to all of them lands in `required`. prometheus is why: its
// `metric` is mandatory in `metric` mode and meaningless in `promql` mode, and a
// single baseline would have published whichever of the two it happened to pick.
func requiredFromValidator(
	checkType checkerdef.CheckType,
	schema *jsonschema.Schema,
	skip map[string]bool,
) ([]string, []string, string) {
	bases, warning := baselines(checkType, schema)
	if len(bases) == 0 {
		return nil, nil, warning
	}

	reflected := map[string]bool{}
	for _, key := range schema.Required {
		reflected[key] = true
	}

	notes := make([]string, 0, 2)
	required := make([]string, 0, len(schema.Required))

	for _, key := range schemaKeys(schema) {
		indispensable, sometimes := requiredAcross(checkType, bases, key)

		enforced := indispensable
		if skip[key] {
			enforced = reflected[key]
		}

		if enforced {
			required = append(required, key)
		}

		switch {
		case skip[key]:
		case enforced && !reflected[key]:
			notes = append(notes, fmt.Sprintf(
				"`%s` carries `omitempty` in Go, but `Validate()` rejects a config without it, so it "+
					"IS listed in `required`. The Go validator is authoritative.", key))
		case !enforced && reflected[key]:
			notes = append(notes, fmt.Sprintf(
				"`%s` has no `omitempty` in Go, but `Validate()` accepts a config without it, so it is "+
					"NOT listed in `required`. The Go validator is authoritative.", key))
		case sometimes && !enforced:
			notes = append(notes, fmt.Sprintf(
				"`%s` is required only in some configurations — `Validate()` decides from other keys — "+
					"so it is not in `required`. The Go validator is authoritative.", key))
		}
	}

	return required, notes, warning
}

// requiredAcross reports whether a key is indispensable to every valid config
// (so it belongs in `required`), and whether it is indispensable to at least one
// (so it is conditionally required and worth a note).
func requiredAcross(checkType checkerdef.CheckType, bases []map[string]any, key string) (bool, bool) {
	needed := 0

	for _, base := range bases {
		if validatorRequires(checkType, base, key) {
			needed++
		}
	}

	return needed == len(bases), needed > 0
}

// baselines returns every config the validator accepts, to remove keys from. It
// uses the checker's own sample configs — real data, no guessing — and falls back
// to building one from the schema's declared property types for the types that
// ship no sample (or ship a template with a placeholder the validator rejects,
// like freebox_line's empty `connectionUid`).
//
// When neither works it returns nothing plus the note that says so. An unprobeable
// type is surfaced rather than silently treated as gap-free: a schema that
// quietly stopped being checked is worse than one that admits it.
func baselines(checkType checkerdef.CheckType, schema *jsonschema.Schema) ([]map[string]any, string) {
	opts := &checkerdef.ListSampleOptions{Type: checkerdef.Default, BaseURL: "https://example.com"}
	specs := registry.GetAllSampleConfigs(opts)[checkType]

	out := make([]map[string]any, 0, len(specs))

	for idx := range specs {
		spec := specs[idx]
		if configregistry.ValidateSpec(checkType, &spec) == nil {
			out = append(out, spec.Config)
		}
	}

	if len(out) > 0 {
		return out, ""
	}

	if cfg := constructBaseline(checkType, schema); cfg != nil {
		return []map[string]any{cfg}, ""
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
