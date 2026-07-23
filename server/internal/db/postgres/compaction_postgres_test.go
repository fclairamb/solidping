package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portCompaction is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portCompaction = 15461

// errInjectedCompactionPG is returned by the CompactResults failpoint to prove
// the transaction rolls the rollup upsert back when a step after it fails.
var errInjectedCompactionPG = errors.New("injected compaction failure")

// compactionRegionPG is the region seeded rows and the rollup share.
const compactionRegionPG = "us"

// newCompactionServicePG starts embedded Postgres and returns a ready Service,
// self-skipping (like every embedded-postgres test in this package) when
// embedded Postgres is unavailable or under -short.
func newCompactionServicePG(t *testing.T) *Service {
	t.Helper()

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: portCompaction, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return s
}

// seedCompactionBucketPG inserts n measurable raw rows for checkUID in a single
// past hour bucket and returns their UIDs, the bucket-scoped fetch filter, and
// the bucket start.
func seedCompactionBucketPG(
	t *testing.T, s *Service, orgUID, checkUID string, n int,
) ([]string, *models.ListResultsFilter, time.Time) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	region := compactionRegionPG

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

// hourRollupAggregatePG returns an AggregateResultsFunc that rolls the given raw
// rows into a single hour row for bucketStart and deletes exactly those rows.
func hourRollupAggregatePG(orgUID, checkUID string, bucketStart time.Time) models.AggregateResultsFunc {
	region := compactionRegionPG

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

// compactionHourRowsPG lists the hour rollup rows for the check.
func compactionHourRowsPG(t *testing.T, s *Service, orgUID, checkUID string) []*models.Result {
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

// TestCompactResults_HappyPath_Postgres is the Postgres counterpart of the
// SQLite happy-path test: a normal compaction commits atomically — the rollup
// exists and every measurable source row is deleted.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction)
func TestCompactResults_HappyPath_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	r := require.New(t)
	ctx := t.Context()
	s := newCompactionServicePG(t)

	org := models.NewOrganization("compaction-pg-org", "Compaction PG Org")
	r.NoError(s.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "compaction-pg-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	uids, filter, bucketStart := seedCompactionBucketPG(t, s, org.UID, check.UID, 3)

	outcome, err := s.CompactResults(ctx, filter, hourRollupAggregatePG(org.UID, check.UID, bucketStart))
	r.NoError(err)
	r.True(outcome.Compacted)
	r.Equal(3, outcome.Fetched)
	r.Equal(3, outcome.SourceCount)
	r.Equal(int64(3), outcome.DeletedCount)

	for _, uid := range uids {
		_, getErr := s.GetResult(ctx, uid)
		r.Error(getErr, "raw source row must be deleted after compaction")
	}

	hourRows := compactionHourRowsPG(t, s, org.UID, check.UID)
	r.Len(hourRows, 1)
	r.NotNil(hourRows[0].TotalChecks)
	r.Equal(3, *hourRows[0].TotalChecks)
}

// TestCompactResults_RollsBack_Postgres is the Postgres counterpart of the
// rollback test: a failure injected after the rollup upsert and before the
// source delete unwinds the whole transaction — no rollup is committed and every
// raw source row survives.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction)
func TestCompactResults_RollsBack_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	r := require.New(t)
	ctx := t.Context()
	s := newCompactionServicePG(t)

	org := models.NewOrganization("compaction-pg-rb-org", "Compaction PG Rollback Org")
	r.NoError(s.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "compaction-pg-rb-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	uids, filter, bucketStart := seedCompactionBucketPG(t, s, org.UID, check.UID, 3)

	s.compactFailpoint = func(context.Context) error { return errInjectedCompactionPG }

	outcome, err := s.CompactResults(ctx, filter, hourRollupAggregatePG(org.UID, check.UID, bucketStart))
	r.ErrorIs(err, errInjectedCompactionPG)
	r.False(outcome.Compacted)
	r.Zero(outcome.DeletedCount)

	r.Empty(compactionHourRowsPG(t, s, org.UID, check.UID),
		"a failed compaction must not leave a rollup row")

	for _, uid := range uids {
		got, getErr := s.GetResult(ctx, uid)
		r.NoError(getErr, "every raw source row must survive a rolled-back compaction")
		r.Equal(models.PeriodTypeRaw, got.PeriodType)
	}
}
