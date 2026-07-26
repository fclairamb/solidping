package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// seedBlobResult inserts one raw result carrying non-empty metrics and output
// blobs, and returns it.
func seedBlobResult(t *testing.T, s *Service, orgUID, checkUID string) *models.Result {
	t.Helper()

	r := require.New(t)

	result := models.NewResult(orgUID, checkUID, models.ResultStatusUp, 123)
	result.PeriodStart = time.Now().Add(time.Hour)
	result.Metrics = models.JSONMap{"handshake_ms": 12.5}
	result.Output = models.JSONMap{"body": "hello", "headers": "a-fairly-large-blob"}
	r.NoError(s.CreateResult(t.Context(), result))

	return result
}

// TestListResults_SkipBlobsProjection proves the SkipBlobs projection really
// drops the two JSON blob columns from the SELECT (spec 2026-07-24-02 §5): the
// scanned rows come back with Metrics/Output empty even though both are
// populated in the table, while every other field survives. The default
// (SkipBlobs: false) run is the positive control — without it, an empty
// Metrics/Output could just mean the seed never landed.
func TestListResults_SkipBlobsProjection(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = s.Close() })
	r.NoError(s.Initialize(ctx))

	org := models.NewOrganization("projection-org", "Projection Org")
	r.NoError(s.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "projection-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	seeded := seedBlobResult(t, s, org.UID, check.UID)

	list := func(skipBlobs bool) *models.Result {
		resp, listErr := s.ListResults(ctx, &models.ListResultsFilter{
			OrganizationUID: org.UID,
			CheckUIDs:       []string{check.UID},
			PeriodTypes:     []string{models.PeriodTypeRaw},
			Limit:           10,
			SkipBlobs:       skipBlobs,
		})
		r.NoError(listErr)

		for _, row := range resp.Results {
			if row.UID == seeded.UID {
				return row
			}
		}

		r.FailNow("seeded result not returned by ListResults")

		return nil
	}

	// Positive control: the default projection returns the blobs, so the row
	// really does carry them in the database.
	full := list(false)
	r.InDelta(12.5, full.Metrics["handshake_ms"], 0.001, "default projection must return metrics")
	r.Equal("hello", full.Output["body"], "default projection must return output")

	// SkipBlobs drops both columns from the projection.
	trimmed := list(true)
	r.Empty(trimmed.Metrics, "SkipBlobs must leave metrics empty")
	r.Empty(trimmed.Output, "SkipBlobs must leave output empty")

	// Everything else still comes back — the projection only sheds the blobs.
	r.Equal(seeded.UID, trimmed.UID)
	r.Equal(check.UID, trimmed.CheckUID)
	r.Equal(models.PeriodTypeRaw, trimmed.PeriodType)
	r.NotNil(trimmed.Status)
	r.Equal(int(models.ResultStatusUp), *trimmed.Status)
	r.NotNil(trimmed.Duration)
	r.InDelta(123, *trimmed.Duration, 0.001)
}
