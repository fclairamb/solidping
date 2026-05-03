package models

import (
	"time"

	"github.com/google/uuid"
)

// File represents a stored file blob and its metadata. The actual bytes live
// behind the storage backend identified by FileURI's scheme (file://, s3://).
type File struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string     `bun:"organization_uid,notnull"`
	Name            string     `bun:"name,notnull"`
	MimeType        string     `bun:"mime_type,notnull"`
	Size            int64      `bun:"size,notnull"`
	FileURI         string     `bun:"file_uri,notnull"`
	SHA256          *string    `bun:"sha256"`
	CreatedBy       *string    `bun:"created_by"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewFile creates a new file record with a generated UID.
func NewFile(orgUID, name, mimeType, fileURI string, size int64, createdBy *string) *File {
	return &File{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Name:            name,
		MimeType:        mimeType,
		Size:            size,
		FileURI:         fileURI,
		CreatedBy:       createdBy,
		CreatedAt:       time.Now(),
	}
}

// ListFilesFilter provides filtering options for listing files.
type ListFilesFilter struct {
	Q      string
	Offset int
	Limit  int
}
