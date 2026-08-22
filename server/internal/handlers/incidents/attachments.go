package incidents

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/attachments"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// AttachmentStore is the incident pipeline's view of the generic attachments
// rail (spec 2026-08-21-01). It is an INTERFACE, and a small one, for the same
// reason PublicationHook is: this package must never import the files service,
// and the incident state machine must never be able to fail because a blob
// could not be written.
//
// Nil-safe at every call site — a server with file storage disabled, or a
// test that does not care, simply attaches nothing.
type AttachmentStore interface {
	// CreateAttachment writes bytes as an attachment filed under topic and
	// returns the new file's UID.
	CreateAttachment(
		ctx context.Context, orgUID uuid.UUID, name, mimeType, topic string,
		details models.JSONMap, body []byte,
	) (string, error)
	// DeleteAttachments soft-deletes every attachment under a topic prefix.
	DeleteAttachments(ctx context.Context, orgUID, prefix string) (int64, error)
	// ListAttachmentViews returns the attachments filed under an exact topic,
	// each carrying a freshly signed, short-lived download URL rooted at
	// baseURL (multi-tenant: a link minted for one host is useless on
	// another).
	ListAttachmentViews(
		ctx context.Context, orgUID, topic, baseURL string,
	) ([]attachments.View, error)
}

// loadAttachments fills the incident detail response's `attachments` array.
//
// Best-effort like every other enrichment on this path: a storage backend
// that cannot be reached costs the reader a screenshot, never the incident
// page itself.
func (s *Service) loadAttachments(
	ctx context.Context, orgUID, incidentUID, baseURL string, response *IncidentResponse,
) {
	if s.attachments == nil {
		return
	}

	topic := models.AttachmentTopic(
		models.AttachmentEntityIncidents, incidentUID, models.AttachmentKindScreenshot,
	)

	views, err := s.attachments.ListAttachmentViews(ctx, orgUID, topic, baseURL)
	if err != nil {
		slog.WarnContext(ctx, "Failed to load incident attachments",
			"incidentUid", incidentUID, "error", err)

		return
	}

	if len(views) > 0 {
		response.Attachments = views
	}
}

// SetAttachmentStore wires the attachments rail. Optional: unset means
// screenshots are captured, travel as far as this pipeline, and are dropped.
func (s *Service) SetAttachmentStore(store AttachmentStore) {
	s.attachments = store
}

// screenshotMimeType is the only capture format, today and by the endpoint's
// allowlist. Declared once so the writer and the sniffer agree.
const screenshotMimeType = "image/png"

// unnamedCheckSlug is the filename stem used when a check has no slug. Only
// reachable for a check row written before slugs were mandatory.
const unnamedCheckSlug = "check"

// persistScreenshot files the triggering result's in-memory capture against
// the incident that this result just opened or reopened.
//
// It is the ONLY place a screenshot becomes durable. A capture that arrives
// on any other failing run has already been discarded by the time the result
// row is written — which is what bounds storage to a handful of blobs per
// incident rather than one per probe.
//
// trigger is models.AttachmentTriggerIncidentOpen or ...Reopen. On a reopen
// the previous onset's screenshot is soft-deleted FIRST: the new onset is the
// evidence, exactly as lastFailureDetails drops a stale failureResponse.
//
// Best-effort throughout. Every failure here logs and returns; none of them
// can fail the incident transition that called it.
func (s *Service) persistScreenshot(
	ctx context.Context, check *models.Check, result *models.Result, incidentUID, trigger string,
) {
	if s.attachments == nil {
		return
	}

	// A reopen clears the previous onset's evidence UNCONDITIONALLY, before
	// anything else and whether or not the relapse produced a capture of its
	// own. This is the same judgement lastFailureDetails makes about a stale
	// failureResponse: a screenshot of a 503 page shown next to an incident
	// whose current onset is a DNS timeout is worse than no screenshot.
	if trigger == models.AttachmentTriggerIncidentReopen {
		prefix := models.AttachmentTopicPrefix(models.AttachmentEntityIncidents, incidentUID)
		if _, delErr := s.attachments.DeleteAttachments(ctx, check.OrganizationUID, prefix); delErr != nil {
			// Not fatal: a stale screenshot is a confusing incident page, a
			// failed reopen is an outage nobody is paged for. Log, continue.
			slog.WarnContext(ctx, "Failed to clear previous incident screenshot",
				"incidentUid", incidentUID, "error", delErr)
		}
	}

	if result == nil || result.Diagnostics == nil {
		return
	}

	shot := result.Diagnostics.Screenshot
	if shot == nil || len(shot.PNG) == 0 {
		return
	}

	orgUID, err := uuid.Parse(check.OrganizationUID)
	if err != nil {
		slog.WarnContext(ctx, "Skipping incident screenshot: bad organization uid",
			"incidentUid", incidentUID, "error", err)

		return
	}

	details := models.JSONMap{
		models.AttachmentDetailCapturedAt: shot.CapturedAt,
		models.AttachmentDetailCheckUID:   check.UID,
		models.AttachmentDetailTrigger:    trigger,
	}

	// Region is stamped SERVER-SIDE from the persisted result row, never from
	// the checker — a deported agent must not be the authority on where it
	// ran. Same rule failureResponseCapture follows.
	if result.Region != nil {
		details[models.AttachmentDetailRegion] = *result.Region
	}

	topic := models.AttachmentTopic(
		models.AttachmentEntityIncidents, incidentUID, models.AttachmentKindScreenshot,
	)

	fileUID, err := s.attachments.CreateAttachment(
		ctx, orgUID, screenshotFileName(check), screenshotMimeType, topic, details, shot.PNG,
	)
	if err != nil {
		slog.WarnContext(ctx, "Failed to attach incident screenshot",
			"incidentUid", incidentUID, "checkUid", check.UID, "error", err)

		return
	}

	slog.InfoContext(ctx, "Attached incident screenshot",
		"incidentUid", incidentUID, "checkUid", check.UID,
		"fileUid", fileUID, "bytes", len(shot.PNG), "trigger", trigger)
}

// screenshotFileName is what a human sees when they download the attachment.
// Derived from the check's slug so a saved file is still identifiable a week
// later; the uniqueness that matters lives in the file's UID, not here.
func screenshotFileName(check *models.Check) string {
	slug := unnamedCheckSlug
	if check != nil && check.Slug != nil && *check.Slug != "" {
		slug = *check.Slug
	}

	return slug + "-screenshot.png"
}
