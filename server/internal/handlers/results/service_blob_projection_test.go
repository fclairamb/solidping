package results

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// blobProjectionRecorder wraps a real DB service so the filter the results
// service builds — and the rows the projection actually returned — can both be
// inspected, without stubbing out the query itself.
type blobProjectionRecorder struct {
	db.Service

	filter *models.ListResultsFilter
	rows   []*models.Result
}

func (b *blobProjectionRecorder) ListResults(
	ctx context.Context, filter *models.ListResultsFilter,
) (*models.ListResultsResponse, error) {
	b.filter = filter

	resp, err := b.Service.ListResults(ctx, filter)
	if resp != nil {
		b.rows = resp.Results
	}

	return resp, err
}

// TestListResults_SkipsBlobsUnlessRequested pins spec 2026-08-22-04 §3.
// `metrics` and `output` are the two widest columns on a results row; the chart
// fetches 1 000 rows at a time and names neither, so every page used to ship
// and jsonb-decode two blobs per row that convertResultToResponse then threw
// away. The projection now drops them — but ONLY when they were not asked for,
// which is the half that a "returns nil" assertion cannot prove on its own.
// Hence the positive controls: a request naming metrics, and one naming output,
// must still come back populated. Without them, setting SkipBlobs
// unconditionally would pass.
func TestListResults_SkipsBlobsUnlessRequested(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("blob-projection-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "blob-projection-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 12.5)
	res.PeriodStart = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	res.Metrics = models.JSONMap{"dnsMs": 4}
	res.Output = models.JSONMap{"body": "pong"}
	r.NoError(dbSvc.CreateResult(ctx, res))

	// Exactly the `with` list the chart sends (CHART_WITH_FIELDS in
	// web/dash0/src/components/checks/response-time-chart.tsx).
	chartWith := []string{
		"durationMs", "region", "durationMinMs", "durationMaxMs",
		"durationAvgMs", "durationP95Ms", "totalChecks",
	}

	tests := []struct {
		name          string
		with          []string
		wantSkipBlobs bool
		wantMetrics   bool
		wantOutput    bool
	}{
		{"chart with-list names no blob", chartWith, true, false, false},
		{"empty with-list", nil, true, false, false},
		{"metrics requested", []string{"durationMs", "metrics"}, false, true, false},
		{"output requested", []string{"output"}, false, false, true},
		{"both requested", []string{"metrics", "output"}, false, true, true},
		{"case-insensitive, as the with-set is", []string{"Metrics"}, false, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			rec := &blobProjectionRecorder{Service: dbSvc}

			resp, listErr := NewService(rec).ListResults(t.Context(), org.Slug, &ListResultsOptions{
				Checks: []string{check.UID},
				Size:   100,
				With:   tc.with,
			})
			rr.NoError(listErr)
			rr.NotNil(rec.filter)
			rr.Equal(tc.wantSkipBlobs, rec.filter.SkipBlobs,
				"the projection decision must follow the `with` list")

			row := findResultRow(rec.rows, res.UID)
			rr.NotNil(row, "the seeded row must come back")

			// The projection itself, at the model level: a dropped column is
			// nil on the way out of the DB, not merely omitted at serialization.
			if tc.wantSkipBlobs {
				rr.Empty(row.Metrics, "metrics must not be read when unrequested")
				rr.Empty(row.Output, "output must not be read when unrequested")
			}

			apiRow := findResponseRow(resp.Data, res.UID)
			rr.NotNil(apiRow, "the seeded row must appear in the response")

			if tc.wantMetrics {
				rr.InDelta(float64(4), apiRow.Metrics["dnsMs"], 1e-9,
					"a requested blob must survive the projection")
			} else {
				rr.Nil(apiRow.Metrics)
			}

			if tc.wantOutput {
				rr.Equal("pong", apiRow.Output["body"], "a requested blob must survive the projection")
			} else {
				rr.Nil(apiRow.Output)
			}
		})
	}
}

func findResultRow(rows []*models.Result, uid string) *models.Result {
	for _, row := range rows {
		if row.UID == uid {
			return row
		}
	}

	return nil
}

func findResponseRow(rows []ResultResponse, uid string) *ResultResponse {
	for i := range rows {
		if rows[i].UID == uid {
			return &rows[i]
		}
	}

	return nil
}
