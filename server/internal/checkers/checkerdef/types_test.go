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

func TestAdvertisedCheckTypes(t *testing.T) {
	t.Parallel()

	advertised := map[CheckType]bool{}
	for _, ct := range AdvertisedCheckTypes() {
		advertised[ct] = true
	}

	tests := []struct {
		name string
		typ  CheckType
		want bool
	}{
		{"sleep is excluded", CheckTypeSleep, false},
		{"private-location is excluded", CheckTypePrivateLocation, false},
		{"http is counted", CheckTypeHTTP, true},
		{"heartbeat is counted", CheckTypeHeartbeat, true},
		{"email is counted", CheckTypeEmail, true},
		{"crawl is counted", CheckTypeCrawl, true},
		{"health is counted", CheckTypeHealth, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, advertised[tt.typ])
		})
	}

	excluded := 0

	for _, meta := range ListCheckTypeMetas() {
		if meta.ExcludedFromTally {
			excluded++
		}
	}

	// A third exclusion must be a deliberate edit to this test.
	require.Equal(t, 2, excluded)
	require.Equal(t, len(ListCheckTypeMetas())-excluded, AdvertisedCheckTypeCount())
}
