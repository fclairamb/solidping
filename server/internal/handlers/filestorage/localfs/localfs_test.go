package localfs_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
)

func TestLocalFS_WriteRead(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	root := t.TempDir()
	backend := localfs.New(root)

	orgUID := uuid.New()
	fileID := uuid.New().String()
	payload := []byte("hello world")
	meta := filestorage.FileMetadata{
		Filename: "hello.txt",
		MimeType: "text/plain",
		Size:     int64(len(payload)),
	}

	uri, err := backend.WriteFile(
		context.Background(),
		orgUID,
		filestorage.GroupTypeReports,
		fileID,
		bytes.NewReader(payload),
		meta,
	)
	r.NoError(err)
	r.Contains(uri, "file://"+orgUID.String())

	body, _, err := backend.ReadFile(
		context.Background(),
		orgUID,
		filestorage.GroupTypeReports,
		fileID,
	)
	r.NoError(err)

	defer func() { _ = body.Close() }()

	got, err := io.ReadAll(body)
	r.NoError(err)
	r.Equal(payload, got)

	r.FileExists(filepath.Join(root, orgUID.String(), "reports", fileID))
}

func TestLocalFS_ParseURI(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	backend := localfs.New(t.TempDir())
	orgUID := uuid.New()
	fileID := uuid.New().String()
	uri := "file://" + orgUID.String() + "/reports/" + fileID

	gotOrg, gotGroup, gotFile, err := backend.ParseURI(uri)
	r.NoError(err)
	r.Equal(orgUID, gotOrg)
	r.Equal(filestorage.GroupTypeReports, gotGroup)
	r.Equal(fileID, gotFile)

	_, _, _, err = backend.ParseURI("garbage") //nolint:dogsled // signature has 4 returns
	r.Error(err)

	_, _, _, err = backend.ParseURI("s3://something") //nolint:dogsled // signature has 4 returns
	r.Error(err)
}

func TestLocalFS_ReadMissing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	backend := localfs.New(t.TempDir())
	_, _, err := backend.ReadFile(
		context.Background(),
		uuid.New(),
		filestorage.GroupTypeReports,
		"missing",
	)
	r.ErrorIs(err, filestorage.ErrFileNotFound)
}

// TestLocalFS_DeletePrefix covers the reap of key-only blobs (spec
// 2026-11-25-01): a fileID with "/" lands in sub-directories, a prefix
// removes exactly what is under it, and another org is left alone.
func TestLocalFS_DeletePrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	backend := localfs.New(t.TempDir())

	orgA, orgB := uuid.New(), uuid.New()
	write := func(org uuid.UUID, fileID string) {
		_, err := backend.WriteFile(ctx, org, filestorage.GroupTypeArchives, fileID,
			bytes.NewReader([]byte("x")), filestorage.FileMetadata{})
		r.NoError(err)
	}

	write(orgA, "check-1/2026-11-25/10.vortex")
	write(orgA, "check-1/2026-11-25.vortex")
	write(orgA, "check-2/2026-11-25/10.vortex")
	write(orgB, "check-1/2026-11-25/10.vortex")

	exists := func(org uuid.UUID, fileID string) bool {
		rc, _, err := backend.ReadFile(ctx, org, filestorage.GroupTypeArchives, fileID)
		if err != nil {
			r.ErrorIs(err, filestorage.ErrFileNotFound)

			return false
		}

		_ = rc.Close()

		return true
	}

	count, err := backend.DeletePrefix(ctx, orgA, filestorage.GroupTypeArchives, "check-1/")
	r.NoError(err)
	r.Equal(2, count)
	r.False(exists(orgA, "check-1/2026-11-25/10.vortex"))
	r.True(exists(orgA, "check-2/2026-11-25/10.vortex"))

	count, err = backend.DeletePrefix(ctx, orgA, filestorage.GroupTypeArchives, "")
	r.NoError(err)
	r.Equal(1, count)
	r.False(exists(orgA, "check-2/2026-11-25/10.vortex"))
	r.True(exists(orgB, "check-1/2026-11-25/10.vortex"), "another org must be untouched")

	count, err = backend.DeletePrefix(ctx, orgA, filestorage.GroupTypeArchives, "")
	r.NoError(err, "a group already gone is no error")
	r.Zero(count)

	for _, bad := range []string{"check-1", "../", "/abs/", "a//", "./"} {
		_, err = backend.DeletePrefix(ctx, orgA, filestorage.GroupTypeArchives, bad)
		r.ErrorIs(err, filestorage.ErrInvalidPrefix, bad)
	}
}
