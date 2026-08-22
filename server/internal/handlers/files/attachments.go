package files

import (
	"bytes"
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/attachments"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

// AttachmentStore adapts *Service to the small interfaces other packages
// declare for the attachments rail (incidents.AttachmentStore today).
//
// The adapter exists so those packages never import this one: the dependency
// runs one way only, and an incident must not be able to fail because a blob
// could not be written.
type AttachmentStore struct {
	svc *Service
	// group is the storage group new attachments are written under. One
	// store per group keeps "what lives on disk where" a construction-time
	// decision rather than a per-call argument.
	group filestorage.GroupType
	// urlTTL bounds how long a minted download link stays valid.
	urlTTL time.Duration
}

// DefaultAttachmentURLTTL bounds a signed attachment link. Short enough that a
// URL pasted into a chat stops working within the working day, long enough
// that an incident page left open over lunch still renders its screenshot.
const DefaultAttachmentURLTTL = 6 * time.Hour

// NewAttachmentStore builds the adapter for one storage group.
func NewAttachmentStore(svc *Service, group filestorage.GroupType) *AttachmentStore {
	return &AttachmentStore{svc: svc, group: group, urlTTL: DefaultAttachmentURLTTL}
}

// CreateAttachment writes bytes as an attachment filed under topic and returns
// the new file's UID.
func (a *AttachmentStore) CreateAttachment(
	ctx context.Context, orgUID uuid.UUID, name, mimeType, topic string,
	details models.JSONMap, body []byte,
) (string, error) {
	file, err := a.svc.CreateFile(
		ctx, orgUID, a.group, name, mimeType,
		nil, // createdBy: nobody — this is machine-generated evidence
		bytes.NewReader(body), int64(len(body)),
		WithTopic(topic), WithDetails(details),
	)
	if err != nil {
		return "", err
	}

	return file.UID, nil
}

// DeleteAttachments soft-deletes every attachment under a topic prefix.
func (a *AttachmentStore) DeleteAttachments(ctx context.Context, orgUID, prefix string) (int64, error) {
	return a.svc.DeleteAttachments(ctx, orgUID, prefix)
}

// ListAttachmentViews returns the attachments under an exact topic, each with
// a freshly signed download URL rooted at baseURL. The URL is minted per call
// and expires — see attachments.View.DownloadURL.
func (a *AttachmentStore) ListAttachmentViews(
	ctx context.Context, orgUID, topic, baseURL string,
) ([]attachments.View, error) {
	files, err := a.svc.ListAttachments(ctx, orgUID, topic)
	if err != nil {
		return nil, err
	}

	views := make([]attachments.View, 0, len(files))
	for _, file := range files {
		views = append(views, attachments.View{
			UID:         file.UID,
			Name:        file.Name,
			Kind:        attachments.Kind(file.Topic),
			MimeType:    file.MimeType,
			Size:        file.Size,
			DownloadURL: a.svc.SignedURL(file, baseURL, a.urlTTL),
			Details:     file.Details,
			CreatedAt:   file.CreatedAt,
		})
	}

	return views, nil
}
