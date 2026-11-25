// Package archive writes the results a compaction is about to delete as
// Vortex files in the file storage (spec 2026-11-25-01). The raw rows of an
// hour go to <orgUID>/archives/<checkUID>/<YYYY-MM-DD>/<HH>.vortex. When the
// day is compacted, all the raw rows of the day (the union of its hour files)
// go to <orgUID>/archives/<checkUID>/<YYYY-MM-DD>.vortex (UTC) and the hour
// files are deleted; an hour with no raw row in the archive contributes its
// hour rollup row instead.
// The files are write-only from SolidPing's side: they are for external tools
// (DuckDB, Spark), nothing in the server reads them back except to merge a
// late row into an existing file.
package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// MimeType is the media type of an archive file.
const MimeType = "application/vnd.apache.vortex"

// Archiver archives the rows a compaction deletes. Archive must return an
// error whenever the rows are not durably stored: the caller then keeps them.
type Archiver interface {
	// Archive stores rows, the measurable source rows of the bucket of check
	// checkUID starting at start that is compacted into targetPeriod. A tier
	// that is not archived (month) or an empty rows is a no-op.
	Archive(ctx context.Context, orgUID, checkUID, targetPeriod string, start time.Time,
		rows []*models.Result) error
}

// StorageArchiver is the Archiver over a filestorage.FileStorage.
type StorageArchiver struct {
	storage filestorage.FileStorage
}

// New returns an Archiver writing to storage.
func New(storage filestorage.FileStorage) *StorageArchiver {
	return &StorageArchiver{storage: storage}
}

// FromConfig returns the Archiver of the configuration: nil when
// archive.enabled is false, else one over the configured file storage.
func FromConfig(cfg *config.Config) (Archiver, error) {
	if cfg == nil || !cfg.Archive.Enabled {
		return nil, nil //nolint:nilnil // nil archiver means archiving is off
	}

	storage, err := filestorage.NewFileStorage(StorageConfig(&cfg.FileStorage))
	if err != nil {
		return nil, fmt.Errorf("archive storage: %w", err)
	}

	return New(storage), nil
}

// StorageConfig maps the file storage configuration to the backend factory's.
func StorageConfig(cfg *config.FileStorageConfig) *filestorage.Config {
	return &filestorage.Config{
		Type:           cfg.Type,
		LocalRoot:      cfg.LocalRoot,
		S3Bucket:       cfg.S3Bucket,
		S3Region:       cfg.S3Region,
		S3Prefix:       cfg.S3Prefix,
		S3Endpoint:     cfg.S3Endpoint,
		S3UsePathStyle: cfg.S3UsePathStyle,
		S3AccessKey:    cfg.S3AccessKey,
		S3SecretKey:    cfg.S3SecretKey,
	}
}

// Archive stores the rows a compaction is about to delete. For an hour
// compaction it merges the raw rows into the hour file. For a day compaction
// it builds the day file from every raw row of the day (the existing day file
// plus the day's hour files), adds the rollup row of each hour that has no
// raw row in the archive, writes the day file and only then deletes the
// day's hour files. Any storage error is returned: the caller then keeps the
// rows in the database and retries later.
func (a *StorageArchiver) Archive(
	ctx context.Context, orgUIDText, checkUID, targetPeriod string, start time.Time, rows []*models.Result,
) error {
	if len(rows) == 0 || (targetPeriod != models.PeriodTypeHour && targetPeriod != models.PeriodTypeDay) {
		return nil
	}

	orgUID, err := uuid.Parse(orgUIDText)
	if err != nil {
		return fmt.Errorf("organization uid: %w", err)
	}

	incoming, err := rowsFromResults(rows)
	if err != nil {
		return err
	}

	var archivedRows, written int

	if targetPeriod == models.PeriodTypeHour {
		archivedRows, written, err = a.archiveHour(ctx, orgUID, HourFileID(checkUID, start), incoming)
	} else {
		archivedRows, written, err = a.archiveDay(ctx, orgUID, checkUID, start, incoming)
	}

	if err != nil {
		prommetrics.RecordArchiveFailure(targetPeriod)

		return err
	}

	prommetrics.RecordArchived(targetPeriod, archivedRows, written)

	return nil
}

func rowsFromResults(results []*models.Result) ([]Row, error) {
	rows := make([]Row, 0, len(results))

	for _, res := range results {
		row, err := RowFromResult(res)
		if err != nil {
			return nil, err
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// archiveHour merges the raw rows of an hour into its hour file. A bucket
// compacted again after a late row thus keeps its first batch.
func (a *StorageArchiver) archiveHour(
	ctx context.Context, orgUID uuid.UUID, fileID string, incoming []Row,
) (int, int, error) {
	existing, err := a.read(ctx, orgUID, fileID)
	if err != nil {
		return 0, 0, err
	}

	written, err := a.write(ctx, orgUID, fileID, Merge(existing, incoming))
	if err != nil {
		return 0, 0, err
	}

	return len(incoming), written, nil
}

// archiveDay writes the day file of the day starting at start from the
// existing day file, the day's hour files and, for each (region, hour) that
// has no raw row among them, its hour rollup from rollups. It then deletes the
// day's hour files: the day file already holds all they held, so a failure
// after the write loses nothing and the retry is a plain merge.
func (a *StorageArchiver) archiveDay(
	ctx context.Context, orgUID uuid.UUID, checkUID string, start time.Time, rollups []Row,
) (int, int, error) {
	utc := start.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	dayFileID := DayFileID(checkUID, day)

	existing, err := a.read(ctx, orgUID, dayFileID)
	if err != nil {
		return 0, 0, err
	}

	var hourRows []Row

	for hour := range 24 {
		rows, readErr := a.read(ctx, orgUID, HourFileID(checkUID, day.Add(time.Duration(hour)*time.Hour)))
		if readErr != nil {
			return 0, 0, readErr
		}

		hourRows = append(hourRows, rows...)
	}

	merged := WithRollupFallback(Merge(existing, hourRows), rollups)

	written, err := a.write(ctx, orgUID, dayFileID, merged)
	if err != nil {
		return 0, 0, err
	}

	if _, err := a.storage.DeletePrefix(ctx, orgUID, filestorage.GroupTypeArchives,
		HourDirPrefix(checkUID, day)); err != nil {
		return 0, 0, fmt.Errorf("delete hour archives: %w", err)
	}

	return len(merged) - len(existing), written, nil
}

// write encodes rows and stores them under fileID, returning the size written.
func (a *StorageArchiver) write(ctx context.Context, orgUID uuid.UUID, fileID string, rows []Row) (int, error) {
	data, err := Encode(rows)
	if err != nil {
		return 0, err
	}

	if _, err := a.storage.WriteFile(ctx, orgUID, filestorage.GroupTypeArchives, fileID,
		bytes.NewReader(data), filestorage.FileMetadata{
			Filename: fileID,
			MimeType: MimeType,
			Size:     int64(len(data)),
		}); err != nil {
		return 0, fmt.Errorf("write archive: %w", err)
	}

	return len(data), nil
}

// read returns the rows of the existing archive file, none when it does not
// exist yet.
func (a *StorageArchiver) read(ctx context.Context, orgUID uuid.UUID, fileID string) ([]Row, error) {
	reader, _, err := a.storage.ReadFile(ctx, orgUID, filestorage.GroupTypeArchives, fileID)
	if errors.Is(err, filestorage.ErrFileNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}

	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}

	rows, err := Decode(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("decode archive: %w", err)
	}

	return rows, nil
}

// DeleteOrganizationArchives removes every archive of an organization from the
// configured file storage. It runs whatever archive.enabled says: archives
// written while it was on must still go when the org is deleted (GDPR).
func DeleteOrganizationArchives(ctx context.Context, cfg *config.Config, orgUID string) (int, error) {
	storage, err := filestorage.NewFileStorage(StorageConfig(&cfg.FileStorage))
	if err != nil {
		return 0, fmt.Errorf("archive storage: %w", err)
	}

	return DeleteOrganization(ctx, storage, orgUID)
}

// DeleteOrganization removes every archive of an organization from storage.
// The archives have no `files` row, so the reap of the org's files does not
// see them: they go by prefix.
func DeleteOrganization(ctx context.Context, storage filestorage.FileStorage, orgUIDText string) (int, error) {
	orgUID, err := uuid.Parse(orgUIDText)
	if err != nil {
		return 0, fmt.Errorf("organization uid: %w", err)
	}

	return storage.DeletePrefix(ctx, orgUID, filestorage.GroupTypeArchives, "")
}
