package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestPeriodTypesTierSide guards the classification the results query relies on
// to restate a partial index's own predicate (spec 2026-08-22-04). Getting
// PeriodTierMixed wrong in either direction is a real defect: claiming "rollup"
// for a list containing raw would add `period_type != 'raw'` and silently drop
// every raw row from the answer, while claiming "mixed" for a single-side list
// only costs the index.
func TestPeriodTypesTierSide(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		periodTypes []string
		want        models.PeriodTierSide
	}{
		{"empty constrains nothing", nil, models.PeriodTierMixed},
		{"empty slice", []string{}, models.PeriodTierMixed},
		{"raw alone", []string{"raw"}, models.PeriodTierRaw},
		{"raw repeated", []string{"raw", "raw"}, models.PeriodTierRaw},
		{"hour alone", []string{"hour"}, models.PeriodTierRollup},
		{"hour and day — the month chart's rollup tier", []string{"hour", "day"}, models.PeriodTierRollup},
		{"every rollup tier", []string{"hour", "day", "month"}, models.PeriodTierRollup},
		{"raw plus hour — the pre-fix week chart", []string{"raw", "hour"}, models.PeriodTierMixed},
		{"raw plus every rollup — the pre-fix month chart", []string{"raw", "hour", "day"}, models.PeriodTierMixed},
		{"rollup first, raw last", []string{"day", "raw"}, models.PeriodTierMixed},
		// An unknown tier is not raw, so it sits on the aggregated side —
		// which is where the partial index puts it too (`!= 'raw'`).
		{"unknown tier counts as non-raw", []string{"week"}, models.PeriodTierRollup},
		{"unknown tier alongside raw is still mixed", []string{"week", "raw"}, models.PeriodTierMixed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, models.PeriodTypesTierSide(tc.periodTypes))
		})
	}
}
