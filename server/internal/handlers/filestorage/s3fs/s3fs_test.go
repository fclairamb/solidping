package s3fs_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
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

func TestBuildClientOptions(t *testing.T) {
	t.Parallel()

	t.Run("aws default: no endpoint, no static creds", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		loadOpts, s3Opts := s3fs.BuildClientOptions(&filestorage.Config{S3Region: "us-east-1"})
		// Region is always present; no static-credential option appended.
		r.Len(loadOpts, 1)
		r.Empty(s3Opts)
	})

	t.Run("minio: endpoint set applies BaseEndpoint + UsePathStyle", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		_, s3Opts := s3fs.BuildClientOptions(&filestorage.Config{
			S3Endpoint:     "https://minio.local:9000",
			S3UsePathStyle: true,
		})
		r.Len(s3Opts, 1)

		var o s3.Options
		s3Opts[0](&o)
		r.NotNil(o.BaseEndpoint)
		r.Equal("https://minio.local:9000", *o.BaseEndpoint)
		r.True(o.UsePathStyle)
	})

	t.Run("endpoint without path-style leaves UsePathStyle false", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		_, s3Opts := s3fs.BuildClientOptions(&filestorage.Config{
			S3Endpoint: "https://r2.example.com",
		})
		r.Len(s3Opts, 1)

		var o s3.Options
		s3Opts[0](&o)
		r.Equal("https://r2.example.com", *o.BaseEndpoint)
		r.False(o.UsePathStyle)
	})

	t.Run("static creds: both keys present appends a load option", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		loadOpts, _ := s3fs.BuildClientOptions(&filestorage.Config{
			S3Region:    "us-east-1",
			S3AccessKey: "AKIA",
			S3SecretKey: "secret",
		})
		// Region option + static-credentials option.
		r.Len(loadOpts, 2)
	})

	t.Run("static creds: only one key present is ignored", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		loadOpts, _ := s3fs.BuildClientOptions(&filestorage.Config{
			S3Region:    "us-east-1",
			S3AccessKey: "AKIA",
		})
		// Only the region option — incomplete creds fall back to the AWS chain.
		r.Len(loadOpts, 1)
	})
}
