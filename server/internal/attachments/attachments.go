// Package attachments holds the shared vocabulary of the generic file
// attachments rail (spec 2026-08-21-01): the API-facing view of one
// attachment.
//
// It exists so a producer (internal/handlers/files, which owns the blobs) and
// a consumer (internal/handlers/incidents, which owns the entity they hang
// off) can agree on a type without either importing the other. The dependency
// between those two must stay absent in both directions: an incident must not
// be able to fail because a blob could not be written, and the files service
// must not know what an incident is.
package attachments

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// View is one attachment as an authenticated API consumer sees it: enough to
// render and download it, and nothing that says where the bytes live.
//
// NEVER PUBLIC. An attachment is org-operational evidence exactly like a
// captured failure response — a screenshot of a rendered page can carry
// customer data, session-bound content and internal hostnames — so this shape
// must never appear on a status page, in a subscriber payload, or anywhere
// else an unauthenticated reader can reach. The never-public audit in
// internal/handlers/statuspages pins that structurally, by type and by field
// name, so adding the field to a public response fails the build's tests
// rather than leaking on a Tuesday.
type View struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	// Kind is the topic's last segment (`screenshot`, ...): what this
	// attachment IS, so no caller has to parse a topic string.
	Kind     string `json:"kind"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	// DownloadURL is a short-lived SIGNED URL, and the only way to reach the
	// bytes. It EXPIRES — it is not a stable identifier, and it must not be
	// stored, indexed or shared as one.
	DownloadURL string `json:"downloadUrl,omitempty"`
	// Details is the capture metadata bag: capturedAt, region, checkUid,
	// trigger for a screenshot. Untyped because a future attachment kind
	// carries other keys — that is the point of the rail.
	Details   models.JSONMap `json:"details,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

// Kind returns the last segment of a topic — the attachment's kind. Empty for
// a nil or malformed topic, which a caller renders as a bare "attachment"
// rather than guessing.
func Kind(topic *string) string {
	if topic == nil {
		return ""
	}

	idx := strings.LastIndex(*topic, "/")
	if idx < 0 || idx == len(*topic)-1 {
		return ""
	}

	return (*topic)[idx+1:]
}
