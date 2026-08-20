package sloghook

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCallsiteFromContextRoundTrip proves WithCallsite/callsiteFromContext
// round-trip the label set by the calling package.
func TestCallsiteFromContextRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ctx := WithCallsite(t.Context(), "uptimebar.bucket_availability")
	r.Equal("uptimebar.bucket_availability", callsiteFromContext(ctx))
}

// TestCallsiteFromContextFallsBackToUnlabelled proves a context nobody
// annotated (and an explicitly empty label) both fall back to "unlabelled" —
// the metric must never be left without a callsite value.
func TestCallsiteFromContextFallsBackToUnlabelled(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(unlabelledCallsite, callsiteFromContext(t.Context()))
	r.Equal(unlabelledCallsite, callsiteFromContext(WithCallsite(t.Context(), "")))
}

// TestWithCallsiteDoesNotMutateParentContext proves annotating a derived
// context leaves the parent's callsite (or lack of one) untouched — a
// sub-call using a fresh context.Background() branch must not inherit a
// caller's callsite by accident.
func TestWithCallsiteDoesNotMutateParentContext(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	parent := t.Context()
	_ = WithCallsite(parent, "checks.list")

	r.Equal(unlabelledCallsite, callsiteFromContext(parent))

	//nolint:usetesting // proves independence from any test-scoped context, not just t.Context()
	bg := context.Background()
	r.Equal(unlabelledCallsite, callsiteFromContext(bg))
}
