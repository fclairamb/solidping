// Package filestorage defines the storage seam for file blobs and the
// pluggable backend factory. Backends self-register by URI scheme prefix
// (file://, s3://). Callers should use NewFileStorage to get the configured
// "write" backend, and GetStorageForURI to resolve any blob's URI back to
// the backend that can read it.
package filestorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// GroupType is a coarse logical grouping under which files are stored. The
// only required guarantee is that the same fileID is unique within a
// (orgUID, group) pair. Adding a new group is a code change so we have a
// single source of truth on what's expected to live on disk.
type GroupType string

// Defined groups. Add new ones here as new use cases land.
const (
	GroupTypeReports GroupType = "reports"
)

// FileMetadata is the small bag of attributes a backend may need to write
// the blob (Content-Type for S3, name for diagnostics). It is not the
// authoritative metadata — that's the `files` table row.
type FileMetadata struct {
	Filename string
	MimeType string
	Size     int64
}

// FileStorage abstracts the bytes-only side of file storage. The metadata
// (size, sha, mime, name) lives in the `files` table; this interface only
// touches the blob.
type FileStorage interface {
	WriteFile(ctx context.Context, orgUID uuid.UUID, group GroupType, fileID string,
		r io.Reader, meta FileMetadata) (uri string, err error)
	ReadFile(ctx context.Context, orgUID uuid.UUID, group GroupType, fileID string) (
		io.ReadCloser, *FileMetadata, error)
	ParseURI(uri string) (orgUID uuid.UUID, group GroupType, fileID string, err error)
}

// Errors returned by storage backends and the factory.
var (
	ErrInvalidURI         = errors.New("invalid file URI")
	ErrUnknownBackend     = errors.New("no storage backend registered for URI prefix")
	ErrFileNotFound       = errors.New("file not found in storage")
	ErrUnsupportedBackend = errors.New("unsupported file storage backend")
)

// BuildPath returns the conventional "<orgUID>/<group>/<fileID>" key used by
// the local FS and S3 backends. Centralised so the two backends agree.
func BuildPath(orgUID uuid.UUID, group GroupType, fileID string) string {
	return fmt.Sprintf("%s/%s/%s", orgUID.String(), string(group), fileID)
}

// ParsePath inverts BuildPath. Returns ErrInvalidURI on malformed input.
func ParsePath(rest string) (uuid.UUID, GroupType, string, error) {
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return uuid.Nil, "", "", ErrInvalidURI
	}

	orgUID, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("%w: %s", ErrInvalidURI, err)
	}

	return orgUID, GroupType(parts[1]), parts[2], nil
}

// SchemeFromURI returns the "<scheme>://" prefix of a URI, or an error if
// the URI does not have one.
func SchemeFromURI(uri string) (string, string, error) {
	idx := strings.Index(uri, "://")
	if idx < 0 {
		return "", "", ErrInvalidURI
	}

	return uri[:idx+3], uri[idx+3:], nil
}

// factoryRegistry holds the backend factories keyed by URI scheme prefix
// (e.g. "file://"). It is package-level so backends can register from their
// own init().
var (
	factoryMu sync.RWMutex
	factories = map[string]StorageFactory{}
)

// StorageFactory builds a backend instance from configuration.
type StorageFactory func(cfg Config) (FileStorage, error)

// Config is the subset of *config.Config that the storage factories need.
// We pass it as a struct (not the full *config.Config) to keep this package
// free of an import cycle on the config package.
type Config struct {
	Type      string
	LocalRoot string
	S3Bucket  string
	S3Region  string
	S3Prefix  string
}

// RegisterStorageFactory registers a backend factory under a URI scheme
// prefix (e.g. "file://", "s3://"). Idempotent — re-registering replaces
// the previous entry.
func RegisterStorageFactory(prefix string, factory StorageFactory) {
	factoryMu.Lock()
	defer factoryMu.Unlock()

	factories[prefix] = factory
}

// GetStorageForURI resolves the right backend to read a previously written
// URI. Each call constructs a fresh backend from cfg — backends are tiny
// stateless adapters, no connection pooling here.
func GetStorageForURI(uri string, cfg Config) (FileStorage, error) {
	prefix, _, err := SchemeFromURI(uri)
	if err != nil {
		return nil, err
	}

	factoryMu.RLock()
	factory, ok := factories[prefix]
	factoryMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownBackend, prefix)
	}

	return factory(cfg)
}

// NewFileStorage returns the backend selected by cfg.Type. Used to resolve
// the "current" backend for new writes.
func NewFileStorage(cfg Config) (FileStorage, error) {
	prefix, ok := schemeForType(cfg.Type)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedBackend, cfg.Type)
	}

	factoryMu.RLock()
	factory, registered := factories[prefix]
	factoryMu.RUnlock()

	if !registered {
		return nil, fmt.Errorf("%w: %q (no factory registered)", ErrUnsupportedBackend, cfg.Type)
	}

	return factory(cfg)
}

// schemeForType maps cfg.Type ("local", "s3") to its URI scheme prefix.
// Centralised so adding a new type means changing exactly two lines.
func schemeForType(typ string) (string, bool) {
	switch typ {
	case "local", "":
		return "file://", true
	case "s3":
		return "s3://", true
	default:
		return "", false
	}
}
