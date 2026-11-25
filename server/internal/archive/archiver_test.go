package archive_test

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/archive"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
)

func rawResult(uid string, at time.Time) *models.Result {
	return &models.Result{
		UID: uid, CheckUID: "check-1", PeriodType: models.PeriodTypeRaw, PeriodStart: at,
		Status: ptr(int(models.ResultStatusUp)), Duration: ptr(float32(1)), CreatedAt: at,
	}
}

func readArchive(t *testing.T, storage filestorage.FileStorage, orgUID uuid.UUID, fileID string) []archive.Row {
	t.Helper()

	reader, _, err := storage.ReadFile(t.Context(), orgUID, filestorage.GroupTypeArchives, fileID)
	require.NoError(t, err)

	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	rows, err := archive.Decode(t.Context(), data)
	require.NoError(t, err)

	return rows
}

func TestStorageArchiverMergesLateRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	storage := localfs.New(t.TempDir())
	archiver := archive.New(storage)

	orgUID := uuid.New()
	hour := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	first := []*models.Result{rawResult("r1", hour.Add(time.Minute)), rawResult("r2", hour.Add(2*time.Minute))}
	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeHour, hour, first))

	// A late row arrives and the bucket is compacted again; r2 comes back too
	// (as when two regions share a file) and must not be duplicated.
	late := []*models.Result{rawResult("r3", hour.Add(30*time.Minute)), rawResult("r2", hour.Add(2*time.Minute))}
	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeHour, hour, late))

	rows := readArchive(t, storage, orgUID, "check-1/2026-11-25/10.vortex")
	r.Len(rows, 3)
	r.Equal([]string{"r1", "r2", "r3"}, []string{rows[0].UID, rows[1].UID, rows[2].UID})
}

func TestStorageArchiverNoOps(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	storage := localfs.New(t.TempDir())
	archiver := archive.New(storage)
	orgUID := uuid.New()
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeMonth, day,
		[]*models.Result{rawResult("r1", day)}))
	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeHour, day, nil))

	count, err := storage.DeletePrefix(ctx, orgUID, filestorage.GroupTypeArchives, "")
	r.NoError(err)
	r.Zero(count, "nothing must be written for a month compaction or an empty bucket")
}

func TestStorageArchiverCorruptFileFails(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	storage := localfs.New(t.TempDir())
	orgUID := uuid.New()
	hour := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	fileID := archive.HourFileID("check-1", hour)
	_, err := storage.WriteFile(ctx, orgUID, filestorage.GroupTypeArchives, fileID,
		strings.NewReader("not a vortex file"), filestorage.FileMetadata{})
	r.NoError(err)

	// An existing file that cannot be read must fail the archive rather than be
	// overwritten: overwriting would lose the rows it holds.
	err = archive.New(storage).Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeHour, hour,
		[]*models.Result{rawResult("r1", hour)})
	r.ErrorContains(err, "decode archive")
}

func TestDeleteOrganizationArchives(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	storage := localfs.New(t.TempDir())
	archiver := archive.New(storage)
	orgA, orgB := uuid.New(), uuid.New()
	hour := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	rows := []*models.Result{rawResult("r1", hour)}

	r.NoError(archiver.Archive(ctx, orgA.String(), "check-1", models.PeriodTypeHour, hour, rows))
	r.NoError(archiver.Archive(ctx, orgA.String(), "check-1", models.PeriodTypeDay, hour, rows))
	r.NoError(archiver.Archive(ctx, orgA.String(), "check-2", models.PeriodTypeHour, hour, rows))
	r.NoError(archiver.Archive(ctx, orgB.String(), "check-1", models.PeriodTypeHour, hour, rows))

	// org A holds check-1's day file (its hour file went into it) and check-2's
	// hour file.
	count, err := archive.DeleteOrganization(ctx, storage, orgA.String())
	r.NoError(err)
	r.Equal(2, count)

	r.Len(readArchive(t, storage, orgB, "check-1/2026-11-25/10.vortex"), 1, "another org must be untouched")
}

func TestFromConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	archiver, err := archive.FromConfig(&config.Config{})
	r.NoError(err)
	r.Nil(archiver, "archiving is off by default")

	localfs.Register()

	archiver, err = archive.FromConfig(&config.Config{
		Archive:     config.ArchiveConfig{Enabled: true},
		FileStorage: config.FileStorageConfig{Type: "local", LocalRoot: t.TempDir()},
	})
	r.NoError(err)
	r.NotNil(archiver)

	_, err = archive.FromConfig(&config.Config{
		Archive:     config.ArchiveConfig{Enabled: true},
		FileStorage: config.FileStorageConfig{Type: "ftp"},
	})
	r.ErrorIs(err, filestorage.ErrUnsupportedBackend)
}

func hourRollup(uid string, region *string, at time.Time) *models.Result {
	end := at.Add(time.Hour - time.Millisecond)
	total := 60

	return &models.Result{
		UID: uid, CheckUID: "check-1", Region: region, PeriodType: models.PeriodTypeHour, PeriodStart: at,
		PeriodEnd: &end, Status: ptr(int(models.ResultStatusUp)), TotalChecks: &total, SuccessfulChecks: &total,
		CreatedAt: at,
	}
}

func archiveExists(t *testing.T, storage filestorage.FileStorage, orgUID uuid.UUID, fileID string) bool {
	t.Helper()

	reader, _, err := storage.ReadFile(t.Context(), orgUID, filestorage.GroupTypeArchives, fileID)
	if err == nil {
		_ = reader.Close()

		return true
	}

	require.ErrorIs(t, err, filestorage.ErrFileNotFound)

	return false
}

func uidsOf(rows []archive.Row) []string {
	uids := make([]string, 0, len(rows))
	for i := range rows {
		uids = append(uids, rows[i].UID)
	}

	return uids
}

// TestStorageArchiverDayStepMergesHourFiles: the 24 hour files of a day become
// one day file holding every raw row, and the hour files are gone.
func TestStorageArchiverDayStepMergesHourFiles(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	storage := localfs.New(t.TempDir())
	archiver := archive.New(storage)
	orgUID := uuid.New()
	day := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	eu, us := "eu", "us"

	wantUIDs := make([]string, 0, 72)

	rollups := make([]*models.Result, 0, 48)

	for hour := range 24 {
		start := day.Add(time.Duration(hour) * time.Hour)
		rows := make([]*models.Result, 0, 3)

		for minute := range 3 {
			res := rawResult(fmt.Sprintf("raw-%02d-%d", hour, minute), start.Add(time.Duration(minute)*time.Minute))
			res.Region = &eu

			if minute == 2 {
				res.Region = &us
			}

			rows = append(rows, res)
			wantUIDs = append(wantUIDs, res.UID)
		}

		r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeHour, start, rows))
		r.True(archiveExists(t, storage, orgUID, archive.HourFileID("check-1", start)))

		rollups = append(rollups, hourRollup(fmt.Sprintf("hour-eu-%02d", hour), &eu, start))
	}

	// The day compaction of region eu: its hour rollups all have raw rows in
	// the archive, so none of them lands in the day file.
	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeDay, day, rollups))

	rows := readArchive(t, storage, orgUID, archive.DayFileID("check-1", day))
	r.Len(rows, 72)
	r.ElementsMatch(wantUIDs, uidsOf(rows))

	for i := range rows {
		r.Equal(models.PeriodTypeRaw, rows[i].PeriodType)
	}

	for hour := range 24 {
		r.False(archiveExists(t, storage, orgUID, archive.HourFileID("check-1", day.Add(time.Duration(hour)*time.Hour))),
			"hour file %02d must be deleted once in the day file", hour)
	}

	// The day compaction of region us comes next: the hour files are gone but
	// the day file holds its raw rows, so no rollup is added either.
	usRollup := hourRollup("hour-us-05", &us, day.Add(5*time.Hour))
	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeDay, day,
		[]*models.Result{usRollup}))
	r.ElementsMatch(wantUIDs, uidsOf(readArchive(t, storage, orgUID, archive.DayFileID("check-1", day))))
}

// TestStorageArchiverDayStepRollupFallback: an hour with no hour file (compacted
// before archiving was on) contributes its hour rollup row to the day file.
func TestStorageArchiverDayStepRollupFallback(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	storage := localfs.New(t.TempDir())
	archiver := archive.New(storage)
	orgUID := uuid.New()
	day := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	hour3, hour4 := day.Add(3*time.Hour), day.Add(4*time.Hour)

	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeHour, hour3,
		[]*models.Result{rawResult("raw-3a", hour3), rawResult("raw-3b", hour3.Add(time.Minute))}))

	rollups := []*models.Result{hourRollup("hour-3", nil, hour3), hourRollup("hour-4", nil, hour4)}
	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeDay, day, rollups))

	rows := readArchive(t, storage, orgUID, archive.DayFileID("check-1", day))
	r.Equal([]string{"raw-3a", "raw-3b", "hour-4"}, uidsOf(rows))
	r.Equal(models.PeriodTypeHour, rows[2].PeriodType)
	r.Equal(int64(60), rows[2].TotalChecks.V)
}

// TestStorageArchiverDayStepTwice: a late row compacted after the day file was
// written gives the same day file plus the late row, without duplicates; a
// rollup row upserted again (new uid) replaces the first one.
func TestStorageArchiverDayStepTwice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	storage := localfs.New(t.TempDir())
	archiver := archive.New(storage)
	orgUID := uuid.New()
	day := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	hour1, hour2 := day.Add(time.Hour), day.Add(2*time.Hour)

	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeHour, hour1,
		[]*models.Result{rawResult("raw-1a", hour1)}))
	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeDay, day,
		[]*models.Result{hourRollup("hour-1", nil, hour1), hourRollup("hour-2", nil, hour2)}))

	// A late raw row of hour 1 is compacted into a new hour file, then the day
	// is compacted again with the rollups upserted under new uids.
	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeHour, hour1,
		[]*models.Result{rawResult("raw-1late", hour1.Add(30*time.Minute))}))
	r.NoError(archiver.Archive(ctx, orgUID.String(), "check-1", models.PeriodTypeDay, day,
		[]*models.Result{hourRollup("hour-1-bis", nil, hour1), hourRollup("hour-2-bis", nil, hour2)}))

	rows := readArchive(t, storage, orgUID, archive.DayFileID("check-1", day))
	r.Equal([]string{"raw-1a", "raw-1late", "hour-2-bis"}, uidsOf(rows))
	r.False(archiveExists(t, storage, orgUID, archive.HourFileID("check-1", hour1)))
}
