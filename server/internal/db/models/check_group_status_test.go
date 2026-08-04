package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestRollupGroupStatus covers every branch of the spec 2026-08-01-01 rollup
// rules, in priority order: empty/created-only -> created, all-down -> down,
// some-down -> degraded, warning -> warning, validating -> validating,
// up -> up.
func TestRollupGroupStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		counts map[models.CheckStatus]int
		want   models.CheckStatus
	}{
		{
			name:   "no considered members",
			counts: map[models.CheckStatus]int{},
			want:   models.CheckStatusCreated,
		},
		{
			name:   "nil map",
			counts: nil,
			want:   models.CheckStatusCreated,
		},
		{
			name:   "only created members",
			counts: map[models.CheckStatus]int{models.CheckStatusCreated: 3},
			want:   models.CheckStatusCreated,
		},
		{
			name:   "all considered members down",
			counts: map[models.CheckStatus]int{models.CheckStatusDown: 4},
			want:   models.CheckStatusDown,
		},
		{
			name: "some but not all down",
			counts: map[models.CheckStatus]int{
				models.CheckStatusDown: 1,
				models.CheckStatusUp:   3,
			},
			want: models.CheckStatusDegraded,
		},
		{
			name: "down alongside warning still degraded, not warning",
			counts: map[models.CheckStatus]int{
				models.CheckStatusDown:    1,
				models.CheckStatusWarning: 1,
			},
			want: models.CheckStatusDegraded,
		},
		{
			name: "no down, at least one warning",
			counts: map[models.CheckStatus]int{
				models.CheckStatusWarning: 1,
				models.CheckStatusUp:      2,
			},
			want: models.CheckStatusWarning,
		},
		{
			name: "no down or warning, at least one validating",
			counts: map[models.CheckStatus]int{
				models.CheckStatusValidating: 1,
				models.CheckStatusUp:         2,
			},
			want: models.CheckStatusValidating,
		},
		{
			name: "no down/warning/validating, at least one up",
			counts: map[models.CheckStatus]int{
				models.CheckStatusUp:      2,
				models.CheckStatusCreated: 1,
			},
			want: models.CheckStatusUp,
		},
		{
			name:   "single up member",
			counts: map[models.CheckStatus]int{models.CheckStatusUp: 1},
			want:   models.CheckStatusUp,
		},
		{
			name:   "single down member",
			counts: map[models.CheckStatus]int{models.CheckStatusDown: 1},
			want:   models.CheckStatusDown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, models.RollupGroupStatus(tc.counts))
		})
	}
}
