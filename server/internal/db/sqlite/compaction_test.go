package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// errInjectedCompaction is returned by the CompactResults failpoint to prove the
// transaction rolls the rollup upsert back when a step after it fails.
var errInjectedCompaction = errors.New("injected compaction failure")

// compactionRegion is the region seeded rows and the rollup share, exercising
// the COALESCE(region, ”) bucket-key path.
const compactionRegion = "us"

// newCompactionEnv builds an in-memory SQLite service with one organization and
// one check.
func newCompactionEnv(t *testing.T) (*Service, *models.Organization, *models.Check) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("compaction-org", "Compaction Org")
	r.NoError(s.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "compaction-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	return s, org, check
}

// seedCompactionBucket inserts n measurable raw rows for checkUID in a single
// past hour bucket and returns their UIDs, the bucket-scoped fetch filter, and
// the bucket start.
func seedCompactionBucket(
	t *testing.T, s *Service, orgUID, checkUID string, n int,
) ([]string, *models.ListResultsFilter, time.Time) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	region := compactionRegion

	bucketStart := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Hour)

	uids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		res := models.NewResult(orgUID, checkUID, models.ResultStatusUp, float32(100+i))
		res.PeriodStart = bucketStart.Add(time.Duration(i+1) * time.Minute)
		res.Region = &region
		r.NoError(s.CreateResult(ctx, res))
		uids = append(uids, res.UID)
	}

	fetchEnd := bucketStart.Add(time.Hour)
	filter := &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        []string{checkUID},
		PeriodTypes:      []string{models.PeriodTypeRaw},
		Regions:          []string{region},
		PeriodStartAfter: &bucketStart,
		PeriodEndBefore:  &fetchEnd,
	}

	return uids, filter, bucketStart
}

// hourRollupAggregate returns an AggregateResultsFunc that rolls the given raw
// rows into a single hour row for bucketStart and deletes exactly those rows.
func hourRollupAggregate(orgUID, checkUID string, bucketStart time.Time) models.AggregateResultsFunc {
	region := compactionRegion

	return func(sources []*models.Result) (*models.Result, []string, error) {
		uids := make([]string, 0, len(sources))
		for _, src := range sources {
			uids = append(uids, src.UID)
		}

		end := bucketStart.Add(time.Hour).Add(-time.Millisecond)
		status := int(models.ResultStatusUp)
		total := len(sources)

		rollup := &models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			PeriodType:      models.PeriodTypeHour,
			PeriodStart:     bucketStart,
			PeriodEnd:       &end,
			Region:          &region,
			Status:          &status,
			TotalChecks:     &total,
			CreatedAt:       time.Now(),
		}

		return rollup, uids, nil
	}
}

// compactionHourRows lists the hour rollup rows for the check.
func compactionHourRows(t *testing.T, s *Service, orgUID, checkUID string) []*models.Result {
	t.Helper()

	resp, err := s.ListResults(t.Context(), &models.ListResultsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       []string{checkUID},
		PeriodTypes:     []string{models.PeriodTypeHour},
		Limit:           100,
	})
	require.New(t).NoError(err)

	return resp.Results
}

// TestCompactResults_HappyPath proves a normal compaction commits atomically:
// the rollup row exists and every measurable source row is deleted.
func TestCompactResults_HappyPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, org, check := newCompactionEnv(t)
	uids, filter, bucketStart := seedCompactionBucket(t, s, org.UID, check.UID, 3)

	outcome, err := s.CompactResults(ctx, filter, hourRollupAggregate(org.UID, check.UID, bucketStart))
	r.NoError(err)
	r.True(outcome.Compacted)
	r.Equal(3, outcome.Fetched)
	r.Equal(3, outcome.SourceCount)
	r.Equal(int64(3), outcome.DeletedCount)

	// Raw source rows are gone.
	for _, uid := range uids {
		_, getErr := s.GetResult(ctx, uid)
		r.Error(getErr, "raw source row must be deleted after compaction")
	}

	// Exactly one rollup row exists.
	hourRows := compactionHourRows(t, s, org.UID, check.UID)
	r.Len(hourRows, 1)
	r.NotNil(hourRows[0].TotalChecks)
	r.Equal(3, *hourRows[0].TotalChecks)
}

// TestCompactResults_RollsBackOnFailureBetweenUpsertAndDelete injects a failure
// after the rollup upsert and before the source delete, and asserts the whole
// transaction unwinds: no rollup row is committed and every raw source row
// survives. This is the rollup+raw double-count hybrid the spec eliminates.
func TestCompactResults_RollsBackOnFailureBetweenUpsertAndDelete(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, org, check := newCompactionEnv(t)
	uids, filter, bucketStart := seedCompactionBucket(t, s, org.UID, check.UID, 3)

	s.compactFailpoint = func(context.Context) error { return errInjectedCompaction }

	outcome, err := s.CompactResults(ctx, filter, hourRollupAggregate(org.UID, check.UID, bucketStart))
	r.ErrorIs(err, errInjectedCompaction)
	r.False(outcome.Compacted)
	r.Zero(outcome.DeletedCount)

	// No rollup was committed.
	r.Empty(compactionHourRows(t, s, org.UID, check.UID),
		"a failed compaction must not leave a rollup row")

	// Every raw source row is still present and still raw.
	for _, uid := range uids {
		got, getErr := s.GetResult(ctx, uid)
		r.NoError(getErr, "every raw source row must survive a rolled-back compaction")
		r.Equal(models.PeriodTypeRaw, got.PeriodType)
	}
}

// TestCompactResults_NoSourceUIDsWritesNothing proves the DB contract for a
// bucket the aggregate declines to roll up (nil rollup / no source UIDs, e.g. a
// marker-only bucket): nothing is written, the sources are left in place, and
// Compacted is false.
func TestCompactResults_NoSourceUIDsWritesNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, org, check := newCompactionEnv(t)
	uids, filter, _ := seedCompactionBucket(t, s, org.UID, check.UID, 2)

	noop := func(_ []*models.Result) (*models.Result, []string, error) {
		return nil, nil, nil
	}

	outcome, err := s.CompactResults(ctx, filter, noop)
	r.NoError(err)
	r.False(outcome.Compacted)
	r.Equal(2, outcome.Fetched)
	r.Zero(outcome.SourceCount)
	r.Zero(outcome.DeletedCount)

	r.Empty(compactionHourRows(t, s, org.UID, check.UID))
	for _, uid := range uids {
		_, getErr := s.GetResult(ctx, uid)
		r.NoError(getErr, "sources must survive when nothing is compacted")
	}
}
