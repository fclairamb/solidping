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

	// Topic is the ATTACHMENT KEY (spec 2026-08-21-01): a path-like
	// `<entity>/<uid>/<kind>`, e.g.
	// `incidents/9a1eb273-0a95-4d6b-b967-9af076c1f8e8/screenshot`.
	//
	// NIL IS THE NORM. A file that is not an attachment — an org logo, a
	// feedback screenshot — carries no topic and is invisible to every
	// attachment query. The path shape is what makes both accesses cheap on
	// one index: an exact match lists one entity's attachments of one kind, a
	// prefix match reaps everything hanging off an entity when it is deleted.
	Topic *string `bun:"topic"`

	// Details is a free metadata bag for the attachment kind — for a
	// screenshot: capturedAt, region, checkUid, trigger. Unconstrained on
	// purpose so the next attachment kind needs no migration.
	//
	// SECURITY: operator-facing evidence, exactly like incidents.details. It
	// must never be serialized onto a public surface.
	Details JSONMap `bun:"details,type:jsonb,nullzero"`
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
	Q string
	// Topic restricts the listing to attachments with this EXACT topic.
	// Mutually exclusive with TopicPrefix in practice; both may be set and
	// both are then applied.
	Topic string
	// TopicPrefix restricts the listing to attachments whose topic starts with
	// this string — the entity-scoped form, e.g. `incidents/<uid>/`.
	TopicPrefix string
	Offset      int
	Limit       int
}
