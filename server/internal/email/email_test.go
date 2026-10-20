package email

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimestamp(t *testing.T) {
	t.Parallel()

	paris := time.FixedZone("CEST", 2*60*60)

	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"utc", time.Date(2026, 7, 5, 10, 0, 59, 0, time.UTC), "Jul 5, 10:00 UTC"},
		{"converted to utc", time.Date(2026, 7, 5, 12, 4, 0, 0, paris), "Jul 5, 10:04 UTC"},
		{"two-digit day", time.Date(2026, 12, 24, 23, 59, 0, 0, time.UTC), "Dec 24, 23:59 UTC"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, Timestamp(tc.in))
		})
	}
}

func TestIncidentThreadID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal("incident.inc-123", IncidentThreadID("inc-123"))
	r.Empty(IncidentThreadID(""), "no incident, no thread")
}
