// Package localfs implements the FileStorage interface backed by a
// directory on the local filesystem. Layout is "<root>/<orgUID>/<group>/<fileID>";
// no per-file metadata sidecar — the `files` table is the truth.
package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

const scheme = "file://"

// Backend writes blobs under Root.
type Backend struct {
	Root string
}

// New constructs a Backend rooted at root. The directory is created on
// first write; we do not pre-create here so a misconfigured root surfaces
// as a write error rather than a startup error.
func New(root string) *Backend {
	return &Backend{Root: root}
}

// Register installs the local FS factory under the "file://" scheme. Called
// from server bootstrap.
func Register() {
	filestorage.RegisterStorageFactory(scheme, func(cfg *filestorage.Config) (filestorage.FileStorage, error) {
		root := cfg.LocalRoot
		if root == "" {
			root = "./data/files"
		}

		return New(root), nil
	})
}

// WriteFile writes the reader's bytes to Root/orgUID/group/fileID and returns
// a "file://orgUID/group/fileID" URI.
func (b *Backend) WriteFile(
	_ context.Context, orgUID uuid.UUID, group filestorage.GroupType, fileID string,
	reader io.Reader, _ filestorage.FileMetadata,
) (string, error) {
	rel := filestorage.BuildPath(orgUID, group, fileID)
	full := filepath.Join(b.Root, rel)

	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}

	file, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}

	defer func() { _ = file.Close() }()

	if _, err := io.Copy(file, reader); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	return scheme + rel, nil
}

// ReadFile opens the underlying file. The returned ReadCloser is the file
// itself — the caller closes it.
func (b *Backend) ReadFile(
	_ context.Context, orgUID uuid.UUID, group filestorage.GroupType, fileID string,
) (io.ReadCloser, *filestorage.FileMetadata, error) {
	rel := filestorage.BuildPath(orgUID, group, fileID)
	full := filepath.Join(b.Root, rel)

	file, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, filestorage.ErrFileNotFound
		}

		return nil, nil, fmt.Errorf("open file: %w", err)
	}

	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()

		return nil, nil, fmt.Errorf("stat file: %w", err)
	}

	meta := &filestorage.FileMetadata{
		Filename: filepath.Base(full),
		Size:     stat.Size(),
	}

	return file, meta, nil
}

// ParseURI splits "file://orgUID/group/fileID" back into its parts.
func (b *Backend) ParseURI(uri string) (uuid.UUID, filestorage.GroupType, string, error) {
	prefix, rest, err := filestorage.SchemeFromURI(uri)
	if err != nil {
		return uuid.Nil, "", "", err
	}

	if prefix != scheme {
		return uuid.Nil, "", "", filestorage.ErrInvalidURI
	}

	return filestorage.ParsePath(rest)
}
