package version

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Mutates the package-level Version, so t.Parallel is intentionally omitted.
//
//nolint:paralleltest // shares package-level state with other tests in this package
func TestGetStripsLeadingV(t *testing.T) {
	t.Cleanup(func() { Version = "dev" })

	r := require.New(t)

	Version = "v1.2.3"
	r.Equal("1.2.3", Get().Version)

	Version = "1.2.3"
	r.Equal("1.2.3", Get().Version)

	Version = "dev"
	r.Equal("dev", Get().Version)
}

func TestUptimeSince(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		now  time.Time
		want int64
	}{
		{"same instant", start, 0},
		{"sub-second truncates", start.Add(999 * time.Millisecond), 0},
		{"90 seconds", start.Add(90 * time.Second), 90},
		{"clock stepped backwards clamps to zero", start.Add(-time.Minute), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, uptimeSince(start, tt.now))
		})
	}
}

// A binary built without ldflags reports documented defaults, never empty
// strings, so /api/mgmt/version cannot render blanks or fail.
func TestGetDefaultsWithoutLdflags(t *testing.T) {
	t.Parallel()

	info := Get()
	require.Equal(t, "unknown", info.Commit)
	require.Equal(t, "unknown", info.GitTime)
	require.GreaterOrEqual(t, info.UptimeSeconds, int64(0))
}
