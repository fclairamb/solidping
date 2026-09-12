package checkerdef

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCheckTypeMetaDefaultPeriodNeverBelowMinPeriod pins the invariant
// server/internal/handlers/checks.defaultPeriodForType relies on: a type's
// own DefaultPeriod must never sit below its own MinPeriod. Every meta
// already agrees today, but nothing enforced it before spec 2026-09-11-07 —
// this is the test that catches a future meta that violates it, rather than
// letting a no-period create silently store a period below the type's own
// floor again, one layer up.
func TestCheckTypeMetaDefaultPeriodNeverBelowMinPeriod(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, meta := range ListCheckTypeMetas() {
		r.True(meta.DefaultPeriod == 0 || meta.DefaultPeriod >= meta.MinPeriod,
			"%s: DefaultPeriod (%s) is below its own MinPeriod (%s)",
			meta.Type, meta.DefaultPeriod, meta.MinPeriod)
	}
}
