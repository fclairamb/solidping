// Package archive writes the results a compaction is about to delete as
// Vortex files in the file storage (spec 2026-11-25-01). The raw rows of an
// hour go to <orgUID>/archives/<checkUID>/<YYYY-MM-DD>/<HH>.vortex, the hour
// rollups of a day to <orgUID>/archives/<checkUID>/<YYYY-MM-DD>.vortex (UTC).
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

// Archive reads the existing file of the bucket, if any, merges rows into it
// by uid and writes the union back. A bucket compacted again after a late row
// thus keeps its first batch.
func (a *StorageArchiver) Archive(
	ctx context.Context, orgUID, checkUID, targetPeriod string, start time.Time, rows []*models.Result,
) error {
	fileID, archived := FileID(checkUID, targetPeriod, start)
	if !archived || len(rows) == 0 {
		return nil
	}

	written, err := a.archive(ctx, orgUID, fileID, rows)
	if err != nil {
		prommetrics.RecordArchiveFailure(targetPeriod)

		return err
	}

	prommetrics.RecordArchived(targetPeriod, len(rows), written)

	return nil
}

func (a *StorageArchiver) archive(
	ctx context.Context, orgUIDText, fileID string, results []*models.Result,
) (int, error) {
	orgUID, err := uuid.Parse(orgUIDText)
	if err != nil {
		return 0, fmt.Errorf("organization uid: %w", err)
	}

	incoming := make([]Row, 0, len(results))

	for _, res := range results {
		row, rowErr := RowFromResult(res)
		if rowErr != nil {
			return 0, rowErr
		}

		incoming = append(incoming, row)
	}

	existing, err := a.read(ctx, orgUID, fileID)
	if err != nil {
		return 0, err
	}

	data, err := Encode(Merge(existing, incoming))
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
