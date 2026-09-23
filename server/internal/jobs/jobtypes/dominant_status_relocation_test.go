package jobtypes

import (
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// calculateDominantStatus is what this package's selector used to BE: a private
// function in job_aggregation.go. Spec 2026-09-22-06 moved the body to
// uptimebar.DominantStatus so the status page's response-time seam classifies a
// bin exactly as the aggregation job classifies the hour rollup that will
// replace it.
//
// This alias exists so the relocation is pinned by the tests that already
// covered the behavior, with their bodies UNCHANGED
// (TestCalculateDominantStatusPromotesWarning and the aggregateResults-level
// cases in job_aggregation_warning_test.go). Rewriting those tests to call the
// new name would have meant "the new function passes new tests", which proves
// nothing about whether the move changed an answer; this way the pre-existing
// table runs against the shared implementation verbatim.
//
// It is test-only on purpose: production code in this package calls
// uptimebar.DominantStatus directly.
//
// func would defeat the point, which is that the OLD tests call the NEW code with
// no indirection of their own.
//
//nolint:gochecknoglobals // a test-only alias for a relocated function; a wrapper
var calculateDominantStatus = uptimebar.DominantStatus
