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

	// Topic makes this row an ATTACHMENT of something: a path-like key
	// `<entity>/<uid>/<kind>`, e.g. `incidents/<uid>/screenshot`. Nil for
	// files that are attached to nothing (org logos, bug-report screenshots),
	// which is why the whole attachments rail is additive — a file with no
	// topic behaves exactly as it did before this column existed.
	Topic *string `bun:"topic"`
	// Details is free attachment metadata. For a screenshot: capturedAt,
	// region, checkUid, trigger. Deliberately untyped — a new attachment kind
	// is a new set of keys, not a schema change.
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
	Q      string
	Offset int
	Limit  int
}

// AttachmentTopic builds the canonical `<entity>/<uid>/<kind>` topic key.
// Centralized so the writer, the reader and the topic authorizer can never
// disagree on the separator or the segment order.
func AttachmentTopic(entity, uid, kind string) string {
	return entity + "/" + uid + "/" + kind
}

// AttachmentTopicPrefix builds the `<entity>/<uid>/` prefix that names every
// attachment of one entity. It is what entity-deletion reaping deletes under.
func AttachmentTopicPrefix(entity, uid string) string {
	return entity + "/" + uid + "/"
}

// Attachment entities and kinds. Kept as constants because they are part of
// the on-disk topic format and of the agent-upload authorizer's routing.
const (
	// AttachmentEntityIncidents is the topic's first segment for anything
	// attached to an incident.
	AttachmentEntityIncidents = "incidents"
	// AttachmentKindScreenshot is a page capture taken by a browser check.
	AttachmentKindScreenshot = "screenshot"
)

// Keys inside a screenshot attachment's Details bag.
const (
	// AttachmentDetailCapturedAt is when the probe took the capture (RFC3339).
	AttachmentDetailCapturedAt = "capturedAt"
	// AttachmentDetailRegion is the probing region, stamped server-side.
	AttachmentDetailRegion = "region"
	// AttachmentDetailCheckUID is the check whose execution produced it.
	AttachmentDetailCheckUID = "checkUid"
	// AttachmentDetailTrigger is what caused the capture to be PERSISTED:
	// `incident-open` or `incident-reopen`. Every other capture is dropped.
	AttachmentDetailTrigger = "trigger"
)

// Attachment trigger values written under AttachmentDetailTrigger.
const (
	// AttachmentTriggerIncidentOpen marks the capture that opened an incident.
	AttachmentTriggerIncidentOpen = "incident-open"
	// AttachmentTriggerIncidentReopen marks the capture of a relapse onset.
	AttachmentTriggerIncidentReopen = "incident-reopen"
	// AttachmentTriggerAgentUpload marks a capture a DEPORTED AGENT pushed
	// through the upload endpoint rather than one the server wrote inline.
	//
	// A distinct value rather than reusing incident-open: on the agent path
	// the server has not (yet) told the agent which transition prompted the
	// upload — that is the WS request-frame half of spec 2026-08-21-01 §6 —
	// so claiming a transition here would be asserting something unverified.
	AttachmentTriggerAgentUpload = "agent-upload"
)
