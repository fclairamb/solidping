package archive_test

import (
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
	r.NoError(archiver.Archive(ctx, orgB.String(), "check-1", models.PeriodTypeHour, hour, rows))

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
