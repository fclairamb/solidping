// Package s3fs implements the FileStorage interface backed by S3 via
// aws-sdk-go-v2. It targets AWS S3 by default but also any S3-compatible store
// (MinIO, Cloudflare R2, Garage, Ceph/RGW, Backblaze B2) when a custom endpoint
// and/or path-style addressing are configured. Credentials default to the
// standard AWS chain (env, IAM role, shared config); static access/secret keys
// can be pinned via filestorage.Config for self-hosted deployments. The bucket,
// region, endpoint and credentials are all read from filestorage.Config.
package s3fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

const scheme = "s3://"

// Backend writes blobs to an S3 bucket under an optional Prefix.
type Backend struct {
	Bucket string
	Prefix string
	Client *s3.Client
}

// New constructs a Backend from a fully-formed S3 client. Tests can pass a
// stubbed client; production wires through Register.
func New(bucket, prefix string, client *s3.Client) *Backend {
	return &Backend{Bucket: bucket, Prefix: prefix, Client: client}
}

// ErrBucketRequired is returned when the S3 backend is selected but no bucket is configured.
var ErrBucketRequired = errors.New("s3fs: S3Bucket is required")

// BuildClientOptions returns the LoadDefaultConfig options and the s3.Options
// mutators implied by cfg. It is pure — no network — so it is unit-testable.
//
// Region is always set. Static credentials are added only when both access and
// secret keys are present (otherwise the ambient AWS chain is used). A custom
// endpoint (and its path-style flag) is applied only when S3Endpoint is set,
// which is what makes MinIO/R2/Garage/Ceph work while leaving AWS-native
// behavior untouched by default.
func BuildClientOptions(cfg *filestorage.Config) ([]func(*awsconfig.LoadOptions) error, []func(*s3.Options)) {
	loadOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.S3Region)}
	if cfg.S3AccessKey != "" && cfg.S3SecretKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		))
	}

	var s3Opts []func(*s3.Options)
	if cfg.S3Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
			o.UsePathStyle = cfg.S3UsePathStyle
		})
	}

	return loadOpts, s3Opts
}

// Register installs the S3 factory under the "s3://" scheme. Bootstrap calls
// this once at startup; the factory then resolves a client from configuration
// each time it's invoked (cheap — only on writes).
func Register() {
	filestorage.RegisterStorageFactory(scheme, func(cfg *filestorage.Config) (filestorage.FileStorage, error) {
		if cfg.S3Bucket == "" {
			return nil, ErrBucketRequired
		}

		loadOpts, s3Opts := BuildClientOptions(cfg)

		awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
		if err != nil {
			return nil, fmt.Errorf("s3fs: load aws config: %w", err)
		}

		client := s3.NewFromConfig(awsCfg, s3Opts...)

		return New(cfg.S3Bucket, cfg.S3Prefix, client), nil
	})
}

// objectKey assembles the bucket key from prefix + path.
func (b *Backend) objectKey(rel string) string {
	if b.Prefix == "" {
		return rel
	}

	return b.Prefix + "/" + rel
}

// WriteFile uploads the bytes via PutObject. The Content-Type from meta is
// preserved so a later GET serves the right type without re-checking.
func (b *Backend) WriteFile(
	ctx context.Context, orgUID uuid.UUID, group filestorage.GroupType, fileID string,
	reader io.Reader, meta filestorage.FileMetadata,
) (string, error) {
	rel := filestorage.BuildPath(orgUID, group, fileID)
	key := b.objectKey(rel)

	// PutObject expects an io.ReadSeeker for accurate Content-Length /
	// retry; buffer in memory since uploads are small (screenshots).
	body, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(b.Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(meta.MimeType),
	}

	if _, err := b.Client.PutObject(ctx, input); err != nil {
		return "", fmt.Errorf("put object: %w", err)
	}

	return scheme + rel, nil
}

// ReadFile returns the object's body. The caller is responsible for closing
// the returned ReadCloser.
func (b *Backend) ReadFile(
	ctx context.Context, orgUID uuid.UUID, group filestorage.GroupType, fileID string,
) (io.ReadCloser, *filestorage.FileMetadata, error) {
	rel := filestorage.BuildPath(orgUID, group, fileID)
	key := b.objectKey(rel)

	out, err := b.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, nil, filestorage.ErrFileNotFound
		}

		return nil, nil, fmt.Errorf("get object: %w", err)
	}

	meta := &filestorage.FileMetadata{}
	if out.ContentType != nil {
		meta.MimeType = *out.ContentType
	}

	if out.ContentLength != nil {
		meta.Size = *out.ContentLength
	}

	return out.Body, meta, nil
}

// DeleteFile removes the object. S3's DeleteObject is already idempotent —
// deleting a key that does not exist succeeds — which matches the contract
// the interface asks for.
func (b *Backend) DeleteFile(
	ctx context.Context, orgUID uuid.UUID, group filestorage.GroupType, fileID string,
) error {
	key := b.objectKey(filestorage.BuildPath(orgUID, group, fileID))

	if _, err := b.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.Bucket),
		Key:    aws.String(key),
	}); err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil
		}

		return fmt.Errorf("delete object: %w", err)
	}

	return nil
}

// ParseURI splits "s3://orgUID/group/fileID" back into its parts.
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
