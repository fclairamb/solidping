package s3fs_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/s3fs"
)

func TestS3FS_ParseURI(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	backend := s3fs.New("bucket", "prefix", nil)
	orgUID := uuid.New()
	fileID := uuid.New().String()
	uri := "s3://" + orgUID.String() + "/reports/" + fileID

	gotOrg, gotGroup, gotFile, err := backend.ParseURI(uri)
	r.NoError(err)
	r.Equal(orgUID, gotOrg)
	r.Equal(filestorage.GroupTypeReports, gotGroup)
	r.Equal(fileID, gotFile)

	_, _, _, err = backend.ParseURI("file://" + orgUID.String() + "/reports/" + fileID) //nolint:dogsled // 4 returns
	r.ErrorIs(err, filestorage.ErrInvalidURI)
}
