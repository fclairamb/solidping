package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestResultColumnsWithoutBlobs pins the projection RecentResultsPerCheck uses
// in both dialects: every persisted column of models.Result except metrics and
// output, alias-qualified. It is derived from the model's bun tags, so a new
// column joins the projection automatically — this test is what proves the
// derivation still holds (a hand-written list would silently scan a new column
// as its zero value).
func TestResultColumnsWithoutBlobs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	columns := models.ResultColumnsWithoutBlobs("")

	r.NotContains(columns, "metrics", "the response-time chart never reads the blobs")
	r.NotContains(columns, "output")
	r.NotContains(columns, "check_slug", "scan-only join aliases are not columns of `results`")
	r.NotContains(columns, "check_name")

	// A representative slice of every column family the chart or the merge
	// actually reads. Nothing here may drop out of the projection unnoticed.
	for _, want := range []string{
		"uid", "organization_uid", "check_uid", "period_type", "period_start",
		"period_end", "region", "status", "duration", "maintenance",
		"total_checks", "successful_checks", "duration_p95", "duration_avg", "created_at",
	} {
		r.Contains(columns, want)
	}

	aliased := models.ResultColumnsWithoutBlobs("res")
	r.Len(aliased, len(columns), "aliasing must not change the column set")
	r.Contains(aliased, "res.period_start")
}

// TestRecentResultsPerCheckFilterValidate is the structural guard for spec
// 2026-08-22-05's single most important constraint. A per-check
// LATERAL/correlated fetch with NO tier predicate is not a small regression:
// it turns one 662 ms scan into one scan PER CHECK (measured 12 274 ms for 20
// checks). Validate is what makes that unwritable — the filter simply cannot
// express "any period type".
func TestRecentResultsPerCheckFilterValidate(t *testing.T) {
	t.Parallel()

	since := time.Now().UTC().Add(-time.Hour)

	base := func() *models.RecentResultsPerCheckFilter {
		return &models.RecentResultsPerCheckFilter{
			OrganizationUID:      "org",
			CheckUIDs:            []string{"check"},
			Tiers:                []models.RecentResultsTier{{PeriodTypes: []string{models.PeriodTypeRaw}, Since: since}},
			DefaultPerCheckLimit: 100,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*models.RecentResultsPerCheckFilter)
		wantErr error
	}{
		{"valid raw tier", func(*models.RecentResultsPerCheckFilter) {}, nil},
		{
			"valid rollup tier",
			func(f *models.RecentResultsPerCheckFilter) {
				f.Tiers = []models.RecentResultsTier{{
					PeriodTypes: []string{models.PeriodTypeHour, models.PeriodTypeDay, models.PeriodTypeMonth},
					Since:       since,
				}}
			},
			nil,
		},
		{
			"tier straddling the split",
			func(f *models.RecentResultsPerCheckFilter) {
				f.Tiers[0].PeriodTypes = []string{models.PeriodTypeRaw, models.PeriodTypeHour}
			},
			models.ErrRecentResultsMixedTier,
		},
		{
			"tier constraining nothing",
			func(f *models.RecentResultsPerCheckFilter) { f.Tiers[0].PeriodTypes = nil },
			models.ErrRecentResultsMixedTier,
		},
		{
			"tier without a lower bound",
			func(f *models.RecentResultsPerCheckFilter) { f.Tiers[0].Since = time.Time{} },
			models.ErrRecentResultsNoSince,
		},
		{
			"no tiers at all",
			func(f *models.RecentResultsPerCheckFilter) { f.Tiers = nil },
			models.ErrRecentResultsNoTiers,
		},
		{
			"no organization",
			func(f *models.RecentResultsPerCheckFilter) { f.OrganizationUID = "" },
			models.ErrRecentResultsNoOrganization,
		},
		{
			"unbounded per-check budget",
			func(f *models.RecentResultsPerCheckFilter) { f.DefaultPerCheckLimit = 0 },
			models.ErrRecentResultsNoLimit,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			filter := base()
			test.mutate(filter)

			err := filter.Validate()
			if test.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

// TestRecentResultsPerCheckFilterLimitFor pins the per-check budget lookup: an
// explicit entry wins, anything missing or non-positive falls back to the
// default. A check silently getting a 0 budget would return no points at all.
func TestRecentResultsPerCheckFilterLimitFor(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	filter := &models.RecentResultsPerCheckFilter{
		PerCheckLimits:       map[string]int{"dense": 400, "broken": 0},
		DefaultPerCheckLimit: 100,
	}

	r.Equal(400, filter.LimitFor("dense"))
	r.Equal(100, filter.LimitFor("missing"))
	r.Equal(100, filter.LimitFor("broken"))
}
