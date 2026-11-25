package jobtypes

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/archive"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

var errArchiveDown = errors.New("storage is down")

// failingArchiver stands for a storage that refuses every write.
type failingArchiver struct{ calls int }

func (f *failingArchiver) Archive(context.Context, string, string, string, time.Time, []*models.Result) error {
	f.calls++

	return errArchiveDown
}

// archiveFixture is an in-memory SQLite with one org and one check whose
// seeded "created" marker has been removed.
type archiveFixture struct {
	db    *sqlite.Service
	org   *models.Organization
	check *models.Check
	jctx  *jobdef.JobContext
}

func newArchiveFixture(t *testing.T) *archiveFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("agg-archive-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "agg-archive-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID, CheckUIDs: []string{check.UID}, Limit: 10,
	})
	r.NoError(err)

	for _, row := range seeded.Results {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, []string{row.UID})
		r.NoError(delErr)
	}

	return &archiveFixture{
		db: dbSvc, org: org, check: check,
		jctx: &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()},
	}
}

func (f *archiveFixture) addResult(
	t *testing.T, periodType string, status models.ResultStatus, start time.Time,
) *models.Result {
	t.Helper()

	res := models.NewResult(f.org.UID, f.check.UID, status, 0.1)
	res.PeriodType = periodType
	res.PeriodStart = start

	if periodType != models.PeriodTypeRaw {
		end := start.Add(time.Hour - time.Millisecond)
		res.PeriodEnd = &end
		res.TotalChecks = new(int)
		*res.TotalChecks = 60
		res.SuccessfulChecks = new(int)
		*res.SuccessfulChecks = 60
	}

	require.NoError(t, f.db.CreateResult(t.Context(), res))

	return res
}

func (f *archiveFixture) countRows(t *testing.T, periodType string) int {
	t.Helper()

	rows, err := f.db.ListResults(t.Context(), &models.ListResultsFilter{
		OrganizationUID: f.org.UID, CheckUIDs: []string{f.check.UID},
		PeriodTypes: []string{periodType}, Limit: 100,
	})
	require.NoError(t, err)

	return len(rows.Results)
}

func readArchiveRows(t *testing.T, storage filestorage.FileStorage, orgUID, fileID string) []archive.Row {
	t.Helper()

	reader, _, err := storage.ReadFile(t.Context(), uuid.MustParse(orgUID), filestorage.GroupTypeArchives, fileID)
	require.NoError(t, err)

	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	rows, err := archive.Decode(t.Context(), data)
	require.NoError(t, err)

	return rows
}

func countFiles(t *testing.T, root string) int {
	t.Helper()

	count := 0

	require.NoError(t, filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() {
			count++
		}

		return nil
	}))

	return count
}

// TestAggregatePeriod_ArchiveFailureKeepsBucketRaw is the fail-closed case of
// spec 2026-11-25-01: a failing archive rolls the compaction back (no rollup,
// raw rows kept), and the next run with a working storage archives and
// compacts the bucket.
func TestAggregatePeriod_ArchiveFailureKeepsBucketRaw(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	fx := newArchiveFixture(t)

	baseHour := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Hour)
	up1 := fx.addResult(t, models.PeriodTypeRaw, models.ResultStatusUp, baseHour.Add(time.Minute))
	up2 := fx.addResult(t, models.PeriodTypeRaw, models.ResultStatusDown, baseHour.Add(2*time.Minute))

	failing := &failingArchiver{}
	run := &AggregationJobRun{archiver: failing, archiverSet: true}

	aggregated, _, err := run.aggregatePeriod(ctx, fx.jctx, fx.org.UID, periodRaw, periodHour)
	r.ErrorIs(err, errArchiveDown)
	r.False(aggregated)
	r.Equal(1, failing.calls)
	r.Equal(2, fx.countRows(t, periodRaw), "the raw rows must be kept when the archive fails")
	r.Zero(fx.countRows(t, periodHour), "no rollup row may be committed when the archive fails")

	storage := localfs.New(t.TempDir())
	run = &AggregationJobRun{archiver: archive.New(storage), archiverSet: true}

	aggregated, _, err = run.aggregatePeriod(ctx, fx.jctx, fx.org.UID, periodRaw, periodHour)
	r.NoError(err)
	r.True(aggregated)
	r.Zero(fx.countRows(t, periodRaw))
	r.Equal(1, fx.countRows(t, periodHour))

	rows := readArchiveRows(t, storage, fx.org.UID, archive.HourFileID(fx.check.UID, baseHour))
	r.Len(rows, 2)
	r.ElementsMatch([]string{up1.UID, up2.UID}, []string{rows[0].UID, rows[1].UID})
	r.Equal(models.PeriodTypeRaw, rows[0].PeriodType)
}

// TestAggregatePeriod_ArchiveDisabledWritesNothing: archive.enabled false
// compacts exactly as before and writes no blob, even with a file storage
// configured.
func TestAggregatePeriod_ArchiveDisabledWritesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	fx := newArchiveFixture(t)
	root := t.TempDir()

	fx.jctx.AppConfig = &config.Config{
		FileStorage: config.FileStorageConfig{Type: "local", LocalRoot: root},
	}

	baseHour := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Hour)
	fx.addResult(t, models.PeriodTypeRaw, models.ResultStatusUp, baseHour.Add(time.Minute))

	run := &AggregationJobRun{}

	aggregated, _, err := run.aggregatePeriod(ctx, fx.jctx, fx.org.UID, periodRaw, periodHour)
	r.NoError(err)
	r.True(aggregated)
	r.Zero(fx.countRows(t, periodRaw))
	r.Equal(1, fx.countRows(t, periodHour))
	r.Zero(countFiles(t, root), "no blob may be written when archiving is off")
}

// TestAggregatePeriod_ArchiveEnabledFromConfig runs both archived tiers
// through archive.enabled and the local file storage: the raw rows of an hour
// land in <date>/<hour>.vortex, the hour rollups of a day in <date>.vortex.
func TestAggregatePeriod_ArchiveEnabledFromConfig(t *testing.T) {
	t.Parallel()

	localfs.Register()

	r := require.New(t)
	ctx := t.Context()
	fx := newArchiveFixture(t)
	root := t.TempDir()

	fx.jctx.AppConfig = &config.Config{
		Archive:     config.ArchiveConfig{Enabled: true},
		FileStorage: config.FileStorageConfig{Type: "local", LocalRoot: root},
	}

	baseHour := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Hour)
	raw := fx.addResult(t, models.PeriodTypeRaw, models.ResultStatusUp, baseHour.Add(time.Minute))

	// Hour rollups old enough for the hour to day compaction (7 days kept).
	baseDay := time.Now().UTC().AddDate(0, 0, -10).Truncate(24 * time.Hour)
	hourA := fx.addResult(t, models.PeriodTypeHour, models.ResultStatusUp, baseDay.Add(3*time.Hour))
	hourB := fx.addResult(t, models.PeriodTypeHour, models.ResultStatusUp, baseDay.Add(4*time.Hour))

	run := &AggregationJobRun{}

	aggregated, _, err := run.aggregatePeriod(ctx, fx.jctx, fx.org.UID, periodRaw, periodHour)
	r.NoError(err)
	r.True(aggregated)

	aggregated, _, err = run.aggregatePeriod(ctx, fx.jctx, fx.org.UID, periodHour, periodDay)
	r.NoError(err)
	r.True(aggregated)

	storage := localfs.New(root)

	hourRows := readArchiveRows(t, storage, fx.org.UID, archive.HourFileID(fx.check.UID, baseHour))
	r.Len(hourRows, 1)
	r.Equal(raw.UID, hourRows[0].UID)

	dayRows := readArchiveRows(t, storage, fx.org.UID, archive.DayFileID(fx.check.UID, baseDay))
	r.Len(dayRows, 2)
	r.Equal([]string{hourA.UID, hourB.UID}, []string{dayRows[0].UID, dayRows[1].UID})
	r.Equal(models.PeriodTypeHour, dayRows[0].PeriodType)
	r.Equal(int64(60), dayRows[0].TotalChecks.V)
}

// TestAggregatePeriod_ArchiveSkipsMarkerOnlyBucket: a bucket of lifecycle
// markers deletes nothing, so nothing is archived.
func TestAggregatePeriod_ArchiveSkipsMarkerOnlyBucket(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	fx := newArchiveFixture(t)
	baseHour := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Hour)
	fx.addResult(t, models.PeriodTypeRaw, models.ResultStatusCreated, baseHour.Add(time.Minute))
	fx.addResult(t, models.PeriodTypeRaw, models.ResultStatusRunning, baseHour.Add(2*time.Minute))

	failing := &failingArchiver{}
	run := &AggregationJobRun{archiver: failing, archiverSet: true}

	aggregated, _, err := run.aggregatePeriod(ctx, fx.jctx, fx.org.UID, periodRaw, periodHour)
	r.NoError(err)
	r.False(aggregated)
	r.Zero(failing.calls, "a marker-only bucket must not reach the archiver")
	r.Equal(2, fx.countRows(t, periodRaw))
}
