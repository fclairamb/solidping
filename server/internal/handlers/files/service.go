// Package files provides HTTP handlers and a programmatic service for the
// generic file-storage layer. The service owns CRUD against the `files`
// table and proxies bytes to the configured filestorage backend.
package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

// Service errors.
var (
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrFileNotFound         = errors.New("file not found")
	ErrFileTooLarge         = errors.New("file too large")
)

// MaxFileSize is a hard ceiling on file size to prevent runaway uploads.
// Bug-report screenshots are well under this; future use cases that need
// more (e.g. parquet rollups) should call WriteFile against the storage
// backend directly without going through the service.
const MaxFileSize int64 = 25 * 1024 * 1024 // 25 MB

// Service exposes file CRUD and a programmatic CreateFile for internal callers.
type Service struct {
	db  db.Service
	cfg *config.Config
}

// NewService constructs a Service.
func NewService(dbService db.Service, cfg *config.Config) *Service {
	return &Service{db: dbService, cfg: cfg}
}

// FileResponse is the JSON shape returned for a File.
type FileResponse struct {
	UID             string    `json:"uid"`
	OrganizationUID string    `json:"organizationUid"`
	Name            string    `json:"name"`
	MimeType        string    `json:"mimeType"`
	Size            int64     `json:"size"`
	CreatedBy       *string   `json:"createdBy,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	// Topic is the attachment key (spec 2026-08-21-01). Omitted entirely for
	// the ordinary, unattached file so existing clients see no change.
	Topic *string `json:"topic,omitempty"`
	// Details is the attachment kind's metadata bag. Operator-facing only.
	Details models.JSONMap `json:"details,omitempty"`
}

// ListResponse is the paginated wrapper returned by the list endpoint.
type ListResponse struct {
	Data  []FileResponse `json:"data"`
	Total int64          `json:"total"`
}

// toResponse maps a model to the JSON response shape (drops storage URI).
func toResponse(file *models.File) FileResponse {
	return FileResponse{
		UID:             file.UID,
		OrganizationUID: file.OrganizationUID,
		Name:            file.Name,
		MimeType:        file.MimeType,
		Size:            file.Size,
		CreatedBy:       file.CreatedBy,
		CreatedAt:       file.CreatedAt,
		Topic:           file.Topic,
		Details:         file.Details,
	}
}

// ListFiles returns files for an org slug.
func (s *Service) ListFiles(
	ctx context.Context, orgSlug, query string, offset, limit int,
) (*ListResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	files, total, err := s.db.ListFiles(ctx, org.UID, models.ListFilesFilter{
		Q:      query,
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}

	resp := &ListResponse{Total: total, Data: make([]FileResponse, 0, len(files))}
	for _, file := range files {
		resp.Data = append(resp.Data, toResponse(file))
	}

	return resp, nil
}

// GetFile returns a file's metadata.
func (s *Service) GetFile(ctx context.Context, orgSlug, fileUID string) (*FileResponse, error) {
	file, err := s.lookup(ctx, orgSlug, fileUID)
	if err != nil {
		return nil, err
	}

	resp := toResponse(file)

	return &resp, nil
}

// GetFileContent returns the file metadata and an open ReadCloser to its bytes.
func (s *Service) GetFileContent(
	ctx context.Context, orgSlug, fileUID string,
) (*models.File, io.ReadCloser, error) {
	file, err := s.lookup(ctx, orgSlug, fileUID)
	if err != nil {
		return nil, nil, err
	}

	rc, err := s.openContent(ctx, file)
	if err != nil {
		return nil, nil, err
	}

	return file, rc, nil
}

// GetFileByUID returns a file by its UID with no org scoping. Used by the
// public signed-URL handler — the signature is the authorization.
func (s *Service) GetFileByUID(ctx context.Context, fileUID string) (*models.File, error) {
	file, err := s.db.GetFileAny(ctx, fileUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrFileNotFound
		}

		return nil, err
	}

	return file, nil
}

// OpenContent opens the bytes for a file (used by the public handler too).
func (s *Service) OpenContent(ctx context.Context, file *models.File) (io.ReadCloser, error) {
	return s.openContent(ctx, file)
}

func (s *Service) openContent(ctx context.Context, file *models.File) (io.ReadCloser, error) {
	storage, err := filestorage.GetStorageForURI(file.FileURI, s.storageConfig())
	if err != nil {
		return nil, fmt.Errorf("resolve storage: %w", err)
	}

	orgUID, group, fileID, err := storage.ParseURI(file.FileURI)
	if err != nil {
		return nil, fmt.Errorf("parse uri: %w", err)
	}

	rc, _, err := storage.ReadFile(ctx, orgUID, group, fileID)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	return rc, nil
}

// DeleteFile soft-deletes a file. The blob in storage is left in place; a
// future GC job can sweep orphaned blobs.
func (s *Service) DeleteFile(ctx context.Context, orgSlug, fileUID string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}

		return err
	}

	if err := s.db.DeleteFile(ctx, org.UID, fileUID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFileNotFound
		}

		return err
	}

	return nil
}

// CreateFile is the programmatic entry point for internal callers. It writes
// the bytes via the configured backend and inserts the metadata row in one
// best-effort sequence (no two-phase commit — if the bytes land but the row
// fails, the GC job catches the orphan).
func (s *Service) CreateFile(
	ctx context.Context, orgUID uuid.UUID, group filestorage.GroupType,
	name, mimeType string, createdBy *string, body io.Reader, size int64,
	opts ...CreateFileOption,
) (*models.File, error) {
	if size > MaxFileSize {
		return nil, ErrFileTooLarge
	}

	settings := createFileSettings{}
	for _, opt := range opts {
		opt(&settings)
	}

	storage, err := filestorage.NewFileStorage(s.storageConfig())
	if err != nil {
		return nil, fmt.Errorf("storage backend: %w", err)
	}

	fileID := uuid.New().String()

	uri, err := storage.WriteFile(ctx, orgUID, group, fileID, body, filestorage.FileMetadata{
		Filename: name,
		MimeType: mimeType,
		Size:     size,
	})
	if err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	file := models.NewFile(orgUID.String(), name, mimeType, uri, size, createdBy)
	file.Topic = settings.topic
	file.Details = settings.details

	if err := s.db.CreateFile(ctx, file); err != nil {
		return nil, fmt.Errorf("create file row: %w", err)
	}

	return file, nil
}

// createFileSettings collects the optional attachment metadata a caller may
// hang on a new file.
type createFileSettings struct {
	topic   *string
	details models.JSONMap
}

// CreateFileOption is the functional-option shape CreateFile takes. Options
// rather than two more positional arguments: the two historical callers
// (feedback screenshots, org logos) attach nothing at all, and adding
// parameters they must pass nil for is how a signature rots.
type CreateFileOption func(*createFileSettings)

// WithTopic attaches the file to an entity under a path-like attachment key,
// `<entity>/<uid>/<kind>`. An empty topic is ignored — an attachment key that
// is the empty string would be matched by every prefix reap.
func WithTopic(topic string) CreateFileOption {
	return func(settings *createFileSettings) {
		if topic == "" {
			return
		}

		settings.topic = &topic
	}
}

// WithDetails hangs the attachment kind's metadata bag on the file.
func WithDetails(details models.JSONMap) CreateFileOption {
	return func(settings *createFileSettings) {
		if len(details) == 0 {
			return
		}

		settings.details = details
	}
}

// ListAttachments returns the live attachments of an org under an EXACT topic,
// newest first. Empty (never an error) when nothing is attached.
func (s *Service) ListAttachments(ctx context.Context, orgUID, topic string) ([]*models.File, error) {
	if topic == "" {
		return nil, nil
	}

	files, _, err := s.db.ListFiles(ctx, orgUID, models.ListFilesFilter{Topic: topic})
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}

	return files, nil
}

// DeleteAttachments soft-deletes every live attachment of an org under a topic
// PREFIX and returns how many rows changed. Two callers: entity-deletion
// reaping (`incidents/<uid>/`) and replace-on-reopen.
//
// The blobs are deliberately left in storage — same posture as DeleteFile —
// and swept later by the GC pass.
func (s *Service) DeleteAttachments(ctx context.Context, orgUID, topicPrefix string) (int, error) {
	if topicPrefix == "" {
		return 0, nil
	}

	count, err := s.db.DeleteFilesByTopicPrefix(ctx, orgUID, topicPrefix)
	if err != nil {
		return 0, fmt.Errorf("delete attachments: %w", err)
	}

	return count, nil
}

func (s *Service) lookup(ctx context.Context, orgSlug, fileUID string) (*models.File, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	file, err := s.db.GetFile(ctx, org.UID, fileUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrFileNotFound
		}

		return nil, err
	}

	return file, nil
}

func (s *Service) storageConfig() *filestorage.Config {
	return &filestorage.Config{
		Type:           s.cfg.FileStorage.Type,
		LocalRoot:      s.cfg.FileStorage.LocalRoot,
		S3Bucket:       s.cfg.FileStorage.S3Bucket,
		S3Region:       s.cfg.FileStorage.S3Region,
		S3Prefix:       s.cfg.FileStorage.S3Prefix,
		S3Endpoint:     s.cfg.FileStorage.S3Endpoint,
		S3UsePathStyle: s.cfg.FileStorage.S3UsePathStyle,
		S3AccessKey:    s.cfg.FileStorage.S3AccessKey,
		S3SecretKey:    s.cfg.FileStorage.S3SecretKey,
	}
}
