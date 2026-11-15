package secretref

import (
	"context"
	"slices"
	"strings"
)

// CollectingResolver wraps resolve and records, in *values, every string an
// `${env:…}` / `${param:…}` reference resolved to (spec 2026-11-15-01). `${var:…}`
// values are generated, not secret, and are not recorded; nor are empty
// strings, nor skipped references. The walk is sequential, so no locking.
//
// The recorded values are secrets: they go on the checker's context
// (checkerdef.WithResolvedReferenceValues) and nowhere else, never a log line.
func CollectingResolver(resolve ResolveFunc, values *[]string) ResolveFunc {
	return func(ctx context.Context, scheme, name string) (string, error) {
		value, err := resolve(ctx, scheme, name)
		if err == nil && IsSecretScheme(scheme) {
			*values = appendValue(*values, value)
		}

		return value, err
	}
}

// ReferenceValues returns the values the `${env:…}` / `${param:…}` references
// of template resolved to in resolved, a copy of template with its references
// replaced (spec 2026-11-15-01). It is how the worker learns the `${param:…}`
// values, which were resolved server-side at the claim boundary and reach it
// as an overlay of already-resolved strings.
//
// Both maps are walked in parallel, nested maps and slices included. In each
// string of template holding a secret reference, the literal text around the
// references is matched against the resolved string and the text standing in
// for the references is reported. Adjacent references come back as one value
// (their concatenation); a run made only of `${var:…}` references is not
// reported. A string whose literal text does not match is skipped.
func ReferenceValues(template, resolved map[string]any) []string {
	var values []string

	for key, value := range template {
		values = pairValues(value, resolved[key], values)
	}

	return values
}

func pairValues(template, resolved any, values []string) []string {
	switch typed := template.(type) {
	case string:
		other, ok := resolved.(string)
		if !ok || !ContainsSecretRef(typed) {
			return values
		}

		for _, value := range extractStringValues(typed, other) {
			values = appendValue(values, value)
		}
	case map[string]any:
		other, ok := resolved.(map[string]any)
		if !ok {
			return values
		}

		for key, nested := range typed {
			values = pairValues(nested, other[key], values)
		}
	case []any:
		other, ok := resolved.([]any)
		if !ok || len(other) != len(typed) {
			return values
		}

		for i, nested := range typed {
			values = pairValues(nested, other[i], values)
		}
	}

	return values
}

// refRun is a run of adjacent references in a template string.
type refRun struct {
	start, end int
	secret     bool
}

// extractStringValues returns the text standing in, in resolved, for each run
// of adjacent references of template holding a secret one. Literal text is
// matched left to right, each at its first occurrence.
func extractStringValues(template, resolved string) []string {
	matches := Pattern.FindAllStringSubmatchIndex(template, -1)
	if len(matches) == 0 {
		return nil
	}

	runs := make([]refRun, 0, len(matches))

	for _, match := range matches {
		secret := IsSecretScheme(template[match[2]:match[3]])

		if last := len(runs) - 1; last >= 0 && runs[last].end == match[0] {
			runs[last].end = match[1]
			runs[last].secret = runs[last].secret || secret

			continue
		}

		runs = append(runs, refRun{start: match[0], end: match[1], secret: secret})
	}

	prefix := template[:runs[0].start]
	suffix := template[runs[len(runs)-1].end:]

	if len(resolved) < len(prefix)+len(suffix) ||
		!strings.HasPrefix(resolved, prefix) || !strings.HasSuffix(resolved, suffix) {
		return nil
	}

	body := resolved[len(prefix) : len(resolved)-len(suffix)]

	var values []string

	for i, run := range runs {
		value := body

		if i < len(runs)-1 {
			literal := template[run.end:runs[i+1].start]

			idx := strings.Index(body, literal)
			if idx < 0 {
				return nil
			}

			value = body[:idx]
			body = body[idx+len(literal):]
		}

		// A reference left unresolved (skipped) stands for itself.
		if run.secret && value != template[run.start:run.end] {
			values = append(values, value)
		}
	}

	return values
}

func appendValue(values []string, value string) []string {
	if value == "" || slices.Contains(values, value) {
		return values
	}

	return append(values, value)
}
