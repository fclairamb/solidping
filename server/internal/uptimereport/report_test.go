package uptimereport

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFormatDurationDropsZeroUnits pins the report's span formatting at the
// boundaries where a unit falls to zero — "42m 0s" and "1h 0m" are what the
// naive two-unit format produced, sitting right next to availability figures in
// the digest.
func TestFormatDurationDropsZeroUnits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   int64
		want string
	}{
		{"seconds", 45, "45s"},
		{"whole minutes", 42 * 60, "42m"},
		{"minutes and seconds", 42*60 + 30, "42m 30s"},
		{"whole hours", 2 * 3600, "2h"},
		{"hours and minutes", 3600 + 5*60, "1h 5m"},
		{"whole days", 2 * 86400, "2d"},
		{"days and hours", 86400 + 2*3600, "1d 2h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, formatDuration(tc.in))
		})
	}
}

// TestAvailabilityTextColorAnchors pins the exact colors at the product's
// existing threshold boundaries (badges/service.go's availabilityColor) and
// proves interpolation actually moves within a segment rather than jumping
// straight to the next anchor.
func TestAvailabilityTextColorAnchors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("#15803d", availabilityTextColor(100))
	r.Equal("#15803d", availabilityTextColor(99.9))
	r.Equal("#b45309", availabilityTextColor(99.0))
	r.Equal("#b91c1c", availabilityTextColor(95))
	r.Equal("#b91c1c", availabilityTextColor(90))
	r.Equal("#b91c1c", availabilityTextColor(0))

	// 99.45 sits at the midpoint of the [99.0, 99.9) amber -> green segment —
	// it must differ from both anchors, proving the ramp is not a step
	// function that snaps straight to one endpoint.
	mid := availabilityTextColor(99.45)
	r.NotEqual("#b45309", mid)
	r.NotEqual("#15803d", mid)
}

// TestAvailabilityTextColorMonotonic sweeps the full range in small steps and
// asserts a higher percentage never renders redder — the naive linear-over-
// 0-100 ramp this replaces would fail this trivially since 99.0 and 99.9
// would render as indistinguishable shades of green, but a ramp with the
// wrong anchor ORDER could render a color reversal, which this test exists
// to catch.
func TestAvailabilityTextColorMonotonic(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	prevRedness := redness(t, availabilityTextColor(90))

	for pct := 90.0; pct <= 100.0; pct += 0.05 {
		cur := redness(t, availabilityTextColor(pct))
		r.LessOrEqualf(cur, prevRedness, "pct %.2f rendered redder (%d) than a lower pct (%d)", pct, cur, prevRedness)
		prevRedness = cur
	}
}

// redness scores a "#rrggbb" color by how red it reads relative to green —
// exactly the axis the product's red/amber/green state colors move along, so
// "never redder" is well-defined even though raw R alone is not monotonic
// across the red -> orange -> amber -> green chain (orange's R briefly rises
// above red's).
func redness(t *testing.T, hex string) int {
	t.Helper()

	require.Len(t, hex, 7)
	require.Equal(t, byte('#'), hex[0])

	var red, green, blue int

	_, err := fmt.Sscanf(hex[1:], "%02x%02x%02x", &red, &green, &blue)
	require.NoError(t, err)

	return red - green
}
