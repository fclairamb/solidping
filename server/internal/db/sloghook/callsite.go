package sloghook

import "context"

// ctxKeyCallsite is the context key under which WithCallsite stores the
// low-cardinality DB-metrics callsite label.
type ctxKeyCallsite struct{}

// unlabelledCallsite is the fallback value recorded on solidping_db_query_duration_seconds
// when the calling package never annotated its context.
const unlabelledCallsite = "unlabelled"

// WithCallsite returns a context carrying a bounded callsite label for
// db_query_duration_seconds. Callers must pass a value drawn from a small,
// known set (e.g. "uptimebar.bucket_availability", "results.list") — never
// anything derived from the raw SQL string or from request input, or the
// metric's cardinality stops being bounded.
func WithCallsite(ctx context.Context, callsite string) context.Context {
	return context.WithValue(ctx, ctxKeyCallsite{}, callsite)
}

// callsiteFromContext returns the callsite label set by WithCallsite, or
// unlabelledCallsite when the context carries none.
func callsiteFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyCallsite{}).(string); ok && v != "" {
		return v
	}

	return unlabelledCallsite
}
