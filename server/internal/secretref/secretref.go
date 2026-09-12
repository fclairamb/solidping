// Package secretref owns the `${env:NAME}` / `${param:KEY}` reference grammar
// used by config-as-code documents and by check configs at rest.
//
// A reference is STORED, never resolved-and-stored: the value a reference
// points at is materialized at execution time and must never be written to a
// check's public `config`, nor returned by any read endpoint. Keeping the
// grammar in one leaf package is what lets the write path (import/apply), the
// dispatch path (the API resolving `param:` into the sealed payload) and the
// executing path (a worker or a deported agent resolving `env:` from its own
// environment) agree on what a reference is without importing each other.
package secretref

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// The two reference schemes.
const (
	// SchemeEnv reads the executing process's environment. Self-hosted form:
	// operator-managed, needs a restart to change, and on a deported agent it
	// resolves against THAT agent's environment (per-region secrets).
	SchemeEnv = "env"
	// SchemeParam reads the org-scoped `parameters` table, falling back to the
	// system-wide one. SaaS form: API-managed, resolved on the API and shipped
	// inside the sealed job payload.
	SchemeParam = "param"
)

// Pattern matches a single `${env:NAME}` / `${param:KEY}` reference. Group 1 is
// the scheme, group 2 the name (everything up to the closing brace).
//
//nolint:gochecknoglobals // the grammar itself, compiled once
var Pattern = regexp.MustCompile(`\$\{(env|param):([^}]+)\}`)

var (
	// ErrUnresolved is the sentinel every resolution failure wraps. It is what
	// turns an apply/import into a 400 and an execution into an error result.
	ErrUnresolved = errors.New("unresolved secret reference")

	// ErrSkip is returned BY a ResolveFunc to mean "not mine — leave this
	// reference exactly as it is". It is not a failure: it is how the API
	// resolves `param:` while handing `env:` on to whichever process ends up
	// executing the check.
	ErrSkip = errors.New("secret reference left unresolved on purpose")
)

// ResolveFunc resolves one reference to its plaintext value. Returning ErrSkip
// leaves the reference in place; any other error aborts the walk.
type ResolveFunc func(ctx context.Context, scheme, name string) (string, error)

// Contains reports whether a string carries at least one reference.
func Contains(value string) bool {
	return Pattern.MatchString(value)
}

// ContainsInConfig reports whether any string anywhere in a config map (nested
// maps and slices included) carries a reference.
func ContainsInConfig(config map[string]any) bool {
	found := false

	VisitStrings(config, func(_ string, value string) {
		if Contains(value) {
			found = true
		}
	})

	return found
}

// VisitStrings walks every string in a config map — nested maps and slices
// included — calling visit with the top-level key it lives under and the
// string itself. Used for auditing rather than rewriting.
func VisitStrings(config map[string]any, visit func(key, value string)) {
	for key, value := range config {
		visitValue(key, value, visit)
	}
}

func visitValue(key string, value any, visit func(key, value string)) {
	switch typed := value.(type) {
	case string:
		visit(key, typed)
	case map[string]any:
		for _, nested := range typed {
			visitValue(key, nested, visit)
		}
	case []any:
		for _, nested := range typed {
			visitValue(key, nested, visit)
		}
	}
}

// ResolveString replaces every reference in a single string. It reports whether
// anything was actually replaced, so a caller can warn about a deployment where
// resolution has a side effect worth mentioning. References the resolver skips
// come back verbatim.
func ResolveString(ctx context.Context, input string, resolve ResolveFunc) (string, bool, error) {
	if !Contains(input) {
		return input, false, nil
	}

	var resolveErr error

	replaced := false

	out := Pattern.ReplaceAllStringFunc(input, func(match string) string {
		if resolveErr != nil {
			return match
		}

		groups := Pattern.FindStringSubmatch(match)
		scheme, name := groups[1], groups[2]

		value, err := resolve(ctx, scheme, name)

		switch {
		case errors.Is(err, ErrSkip):
			return match
		case err != nil:
			resolveErr = err

			return match
		}

		replaced = true

		return value
	})

	if resolveErr != nil {
		return "", false, resolveErr
	}

	return out, replaced, nil
}

// ResolveConfig returns a COPY of config with every reference resolved. The
// input is never mutated: the caller usually holds a cached model whose config
// map is shared, and materializing an effective config must not write the
// resolved value back into anything that could be persisted or served.
//
// Nested maps and slices are walked. The bool reports whether any reference was
// actually replaced.
func ResolveConfig(
	ctx context.Context, config map[string]any, resolve ResolveFunc,
) (map[string]any, bool, error) {
	out := make(map[string]any, len(config))
	replacedAny := false

	for key, value := range config {
		resolved, replaced, err := resolveValue(ctx, value, resolve)
		if err != nil {
			// The sentinel's own wording leads, so the message a check result
			// shows starts with "unresolved secret reference: param:…" and the
			// config key that carried it follows as context.
			return nil, false, fmt.Errorf("%w (config %q)", err, key)
		}

		if replaced {
			replacedAny = true
		}

		out[key] = resolved
	}

	return out, replacedAny, nil
}

func resolveValue(ctx context.Context, value any, resolve ResolveFunc) (any, bool, error) {
	switch typed := value.(type) {
	case string:
		return ResolveString(ctx, typed, resolve)
	case map[string]any:
		return ResolveConfig(ctx, typed, resolve)
	case []any:
		out := make([]any, len(typed))
		replacedAny := false

		for i, item := range typed {
			resolved, replaced, err := resolveValue(ctx, item, resolve)
			if err != nil {
				return nil, false, err
			}

			if replaced {
				replacedAny = true
			}

			out[i] = resolved
		}

		return out, replacedAny, nil
	default:
		return value, false, nil
	}
}

// Unresolvedf builds an ErrUnresolved-wrapping error naming the reference, in
// the one spelling every surface shows: `unresolved secret reference: param:x`.
func Unresolvedf(scheme, name string) error {
	return fmt.Errorf("%w: %s:%s", ErrUnresolved, scheme, name)
}
