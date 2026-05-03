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
