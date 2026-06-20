package checkerdef

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGradedExpiryStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		days         int
		warning      int
		critical     int
		wantStatus   Status
		wantSeverity string
	}{
		{
			name:         "inside critical → down/critical",
			days:         5,
			warning:      30,
			critical:     7,
			wantStatus:   StatusDown,
			wantSeverity: SeverityCritical,
		},
		{
			name:         "exactly critical → down/critical",
			days:         7,
			warning:      30,
			critical:     7,
			wantStatus:   StatusDown,
			wantSeverity: SeverityCritical,
		},
		{
			name:         "between tiers → warning/warning",
			days:         20,
			warning:      30,
			critical:     7,
			wantStatus:   StatusWarning,
			wantSeverity: SeverityWarning,
		},
		{
			name:         "exactly warning → warning/warning",
			days:         30,
			warning:      30,
			critical:     7,
			wantStatus:   StatusWarning,
			wantSeverity: SeverityWarning,
		},
		{
			name:         "beyond both → up/none",
			days:         90,
			warning:      30,
			critical:     7,
			wantStatus:   StatusUp,
			wantSeverity: SeverityNone,
		},
		{
			name:         "equal tiers (default) → no warning band, down at threshold",
			days:         30,
			warning:      30,
			critical:     30,
			wantStatus:   StatusDown,
			wantSeverity: SeverityCritical,
		},
		{
			name:         "equal tiers (default) → up above threshold",
			days:         31,
			warning:      30,
			critical:     30,
			wantStatus:   StatusUp,
			wantSeverity: SeverityNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			status, severity := GradedExpiryStatus(tt.days, tt.warning, tt.critical)
			r.Equal(tt.wantStatus, status)
			r.Equal(tt.wantSeverity, severity)
		})
	}
}
