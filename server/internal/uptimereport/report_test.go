package uptimereport

import (
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
