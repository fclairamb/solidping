package checkerdef

import (
	"context"
	"strings"
)

// resolvedRefsCtxKeyType makes the context key unique within the binary.
type resolvedRefsCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for a context key
var resolvedRefsCtxKey = resolvedRefsCtxKeyType{}

// WithResolvedReferenceValues hands a checker the values the `${param:…}` and
// `${env:…}` references of its config resolved to (spec 2026-11-15-01). The
// docs treat a referenced value as a secret, so a checker that follows
// redirects withholds, on a hop to another origin, every header whose value
// contains one of them, exactly as it does for a declared secret header.
//
// Empty strings are dropped: an empty value would match every header. The
// values are never logged nor put in any output; only the header NAMES a hop
// dropped are reported.
func WithResolvedReferenceValues(ctx context.Context, values []string) context.Context {
	kept := make([]string, 0, len(values))

	for _, value := range values {
		if value != "" {
			kept = append(kept, value)
		}
	}

	if len(kept) == 0 {
		return ctx
	}

	return context.WithValue(ctx, resolvedRefsCtxKey, kept)
}

// ResolvedReferenceValuesFrom returns the values WithResolvedReferenceValues
// put on ctx, nil when there are none.
func ResolvedReferenceValuesFrom(ctx context.Context) []string {
	values, _ := ctx.Value(resolvedRefsCtxKey).([]string)

	return values
}

// ContainsResolvedReferenceValue reports whether value contains one of values.
func ContainsResolvedReferenceValue(value string, values []string) bool {
	for _, ref := range values {
		if ref != "" && strings.Contains(value, ref) {
			return true
		}
	}

	return false
}
