package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/files/signedurl"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/nettrace"
)

// MaxAttachmentBytes caps a single uploaded attachment. Mirrors the browser
// checker's own capture cap so an in-process capture and an agent upload of the
// same screenshot are subject to the same ceiling — a per-path cap would mean
// "what fits" depends on where the check happened to run.
const MaxAttachmentBytes int64 = 4 * 1024 * 1024

// DownloadURLTTL is how long a signed attachment URL stays valid. Short: the
// dashboard re-fetches the incident (and therefore re-signs) whenever it
// renders it, so a long-lived URL would only widen the window in which a
// copied link keeps working outside the session that produced it.
const DownloadURLTTL = time.Hour

// Attachment content types. Three raster formats for a screenshot, JSON for a
// path capture — and each is accepted ONLY under the topic kind that owns it
// (see sniffMime).
const (
	mimePNG  = "image/png"
	mimeJPEG = "image/jpeg"
	mimeWebP = "image/webp"
	mimeJSON = "application/json"
)

// Magic-byte signatures. Content types are sniffed, not believed: a declared
// type is caller-controlled and the storage backend serves what it was given.
//
//nolint:gochecknoglobals // immutable byte constants
var (
	pngMagic  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
	riffMagic = []byte("RIFF")
	webpMagic = []byte("WEBP")
)

// webpHeaderBytes is how much of a RIFF container has to be present before the
// "WEBP" form type can be read: 4 bytes of "RIFF", 4 of chunk size, 4 of form.
const webpHeaderBytes = 12

// screenshotSignatures is the allowlist for KindScreenshot, in the order it is
// tried. THREE formats, deliberately (spec 2026-09-13-01): the capture path
// used to emit JPEG while this table knew only PNG, so every browser-check
// screenshot ever taken was refused here and silently lost.
//
// Anything not on this table is still refused — the sniff fails closed, which
// is what lets files.safeInlineMIME serve the result inline under `nosniff`.
// A format added here MUST also be on that allowlist, or the browser will
// refuse to decode what we stored.
//
//nolint:gochecknoglobals // immutable lookup table
var screenshotSignatures = []struct {
	mimeType string
	match    func(body []byte) bool
}{
	{mimePNG, func(body []byte) bool { return bytes.HasPrefix(body, pngMagic) }},
	{mimeJPEG, func(body []byte) bool { return bytes.HasPrefix(body, jpegMagic) }},
	{mimeWebP, func(body []byte) bool {
		return len(body) >= webpHeaderBytes &&
			bytes.Equal(body[:4], riffMagic) && bytes.Equal(body[8:webpHeaderBytes], webpMagic)
	}},
}

// extensionFor is the filename suffix for a SNIFFED media type.
//
// It is keyed on the sniff rather than on the caller's intent so a downloaded
// attachment always opens: a WebP saved as `.png` is a file the operating
// system refuses to preview, which is the download-side twin of the
// broken-image icon this spec removed from the incident card.
func extensionFor(mimeType string) string {
	switch mimeType {
	case mimePNG:
		return ".png"
	case mimeJPEG:
		return ".jpg"
	case mimeWebP:
		return ".webp"
	case mimeJSON:
		return ".json"
	default:
		return ""
	}
}

// Details keys written on an attachment's metadata bag. camelCase, matching
// the JSON they are served as.
const (
	DetailKeyCapturedAt = "capturedAt"
	DetailKeyRegion     = "region"
	DetailKeyCheckUID   = "checkUid"
	DetailKeyTrigger    = "trigger"
)

// Trigger values recorded in an attachment's details bag.
const (
	// TriggerIncidentOpen marks the capture that opened an incident.
	TriggerIncidentOpen = "incident-open"
	// TriggerIncidentReopen marks the capture of a relapse's onset.
	TriggerIncidentReopen = "incident-reopen"
	// TriggerAgentUpload marks an artifact that arrived through the agent
	// upload endpoint rather than being written in-process.
	TriggerAgentUpload = "agent-upload"
	// TriggerCheckFailure marks a check-scoped capture of a failing run that
	// neither opened nor reopened an incident (spec 2026-09-25-34): a
	// validating run, a blip, a regional failure, a run of an outage whose
	// incident already holds its onset capture.
	TriggerCheckFailure = "check-failure"
	// TriggerCaptureNow marks the capture of an on-demand run ("Capture now").
	TriggerCaptureNow = "capture-now"
)

// Errors returned by the service.
var (
	// ErrUnsupportedMediaType means the declared or sniffed content type is
	// not on the allowlist.
	ErrUnsupportedMediaType = errors.New("unsupported attachment media type")
	// ErrAttachmentTooLarge means the body is past MaxAttachmentBytes.
	ErrAttachmentTooLarge = errors.New("attachment too large")
	// ErrEmptyAttachment means the body carried no bytes.
	ErrEmptyAttachment = errors.New("attachment is empty")
)

// errNoFiles is returned by the check-scoped prune when the service was built
// without a files service (never in production).
var errNoFiles = errors.New("attachment service has no files service")

// Service owns the attachment half of the files layer: writing an attachment
// under a topic, reaping by prefix, and rendering one for the API with a signed
// download URL.
type Service struct {
	files *files.Service
	dbSvc db.Service
	cfg   *config.Config
}

// NewService builds the attachment service on top of the files service.
func NewService(filesSvc *files.Service, dbSvc db.Service, cfg *config.Config) *Service {
	return &Service{files: filesSvc, dbSvc: dbSvc, cfg: cfg}
}

// Response is the JSON shape of one attachment. It carries a signed download
// URL rather than the bytes: the incident payload stays small, and the URL
// expires.
//
// NEVER PUBLIC. An attachment is org-operational evidence exactly like
// incidents.details — a screenshot of an internal admin page is not something
// a status page may serve. The never-public audit
// (handlers/statuspages/details_never_public_test.go) pins that structurally.
type Response struct {
	UID  string `json:"uid"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// MimeType is the stored content type, sniffed at write time.
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	// DownloadURL is RELATIVE (`/pub/files/<uid>?exp=…&sig=…`) so it resolves
	// against whichever host the dashboard is being served from — SolidPing
	// answers on several, and an absolute URL baked from server.base_url would
	// send a tenant to the wrong one.
	DownloadURL string    `json:"downloadUrl"`
	CreatedAt   time.Time `json:"createdAt"`
	// CapturedAt / Region / CheckUID / Trigger are lifted out of the details
	// bag so a client does not have to know the bag's schema.
	CapturedAt *time.Time `json:"capturedAt,omitempty"`
	Region     string     `json:"region,omitempty"`
	CheckUID   string     `json:"checkUid,omitempty"`
	Trigger    string     `json:"trigger,omitempty"`
}

// CheckScreenshot is one entry of a check's screenshot listing (spec
// 2026-09-25-34): an incident's capture or a check-scoped one.
//
// NEVER PUBLIC, for exactly the reasons Response is not: operator-only
// evidence, served with a short-lived signed URL.
type CheckScreenshot struct {
	UID      string `json:"uid"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	// DownloadURL is the same relative signed `/pub/files/<uid>?exp=…&sig=…`
	// URL an incident's attachment carries.
	DownloadURL string `json:"downloadUrl"`
	// CapturedAt is when the probe took the capture, or — for an agent
	// upload, which carries no capture time — when the server stored it.
	CapturedAt time.Time `json:"capturedAt"`
	Region     string    `json:"region,omitempty"`
	Trigger    string    `json:"trigger,omitempty"`
	// IncidentUID names the incident the capture is attached to, taken from
	// the topic. Absent for a check-scoped capture.
	IncidentUID string `json:"incidentUid,omitempty"`
}

// ListCheckScreenshots returns a check's latest screenshots, newest first,
// with signed download URLs. Empty (never an error) when there are none.
func (s *Service) ListCheckScreenshots(
	ctx context.Context, orgUID, checkUID string, limit int,
) ([]CheckScreenshot, error) {
	rows, err := s.dbSvc.ListCheckScreenshotFiles(ctx, orgUID, checkUID, limit)
	if err != nil {
		return nil, fmt.Errorf("list check screenshots: %w", err)
	}

	out := make([]CheckScreenshot, 0, len(rows))

	for _, row := range rows {
		resp := s.toResponse(row)
		shot := CheckScreenshot{
			UID:         resp.UID,
			MimeType:    resp.MimeType,
			Size:        resp.Size,
			DownloadURL: resp.DownloadURL,
			CapturedAt:  resp.CreatedAt,
			Region:      resp.Region,
			Trigger:     resp.Trigger,
		}

		if resp.CapturedAt != nil {
			shot.CapturedAt = *resp.CapturedAt
		}

		if row.Topic != nil {
			if parsed, parseErr := ParseTopic(*row.Topic); parseErr == nil && parsed.Entity == EntityIncidents {
				shot.IncidentUID = parsed.EntityUID
			}
		}

		out = append(out, shot)
	}

	return out, nil
}

// PutCheckScreenshot writes a check-scoped screenshot (spec 2026-09-25-34): the
// capture of a failing run that opened no incident, or of a "Capture now" run.
// It APPENDS and keeps the last MaxCheckScreenshots — see Put.
func (s *Service) PutCheckScreenshot(
	ctx context.Context, orgUID, checkUID string, image []byte, details models.JSONMap,
) (string, error) {
	return s.Put(ctx, orgUID, CheckScreenshotTopic(checkUID), "check-"+checkUID+"-screenshot", image, details)
}

// PutIncidentTraceroute writes a serialized nettrace.Capture as the incident's
// path-diagnostics attachment.
//
// REPLACE-ON-WRITE like the screenshot: a topic names ONE current artifact, and
// a relapse's trace supersedes the previous onset's rather than stacking.
func (s *Service) PutIncidentTraceroute(
	ctx context.Context, orgUID, incidentUID string, capture []byte, details models.JSONMap,
) (string, error) {
	return s.Put(
		ctx, orgUID,
		IncidentTracerouteTopic(incidentUID),
		"incident-"+incidentUID+"-traceroute",
		capture, details,
	)
}

// PutIncidentScreenshot writes an image as the incident's screenshot
// attachment. It REPLACES any previous one (see Put): the caller's contract is
// "this is the evidence for the current onset", so the prior capture is retired
// first.
//
// The extension is NOT the caller's to choose — Put appends the one matching
// the sniffed type (spec 2026-09-13-01).
func (s *Service) PutIncidentScreenshot(
	ctx context.Context, orgUID, incidentUID string, image []byte, details models.JSONMap,
) (string, error) {
	return s.Put(
		ctx, orgUID,
		IncidentScreenshotTopic(incidentUID),
		"incident-"+incidentUID+"-screenshot",
		image, details,
	)
}

// DeleteIncidentAttachments soft-deletes every attachment hanging off an
// incident. The reaper for incident deletion and the replace half of an
// on-reopen overwrite.
func (s *Service) DeleteIncidentAttachments(ctx context.Context, orgUID, incidentUID string) (int, error) {
	return s.files.DeleteAttachments(ctx, orgUID, IncidentTopicPrefix(incidentUID))
}

// DeleteIncidentAttachment soft-deletes ONE kind of attachment on an incident.
//
// The per-kind variant exists because an incident now carries more than one
// artifact: reaping "the stale screenshot" through the entity prefix would take
// the path capture with it, and reaping "the stale path capture" would take the
// screenshot.
func (s *Service) DeleteIncidentAttachment(
	ctx context.Context, orgUID, incidentUID, kind string,
) (int, error) {
	return s.files.DeleteAttachmentsByTopic(ctx, orgUID, IncidentTopicPrefix(incidentUID)+kind)
}

// ListIncidentAttachments returns the incident's live attachments, rendered
// with signed download URLs. Empty (never an error) when there are none.
//
// Listed by the incident's topic PREFIX rather than by one exact topic: an
// incident now carries a screenshot AND a path capture, and the next kind
// should surface without editing this function. Reaping stays exact-topic —
// see files.DeleteAttachmentsByTopic for why the asymmetry is deliberate.
func (s *Service) ListIncidentAttachments(
	ctx context.Context, orgUID, incidentUID string,
) ([]Response, error) {
	rows, err := s.files.ListAttachmentsByPrefix(ctx, orgUID, IncidentTopicPrefix(incidentUID))
	if err != nil {
		return nil, err
	}

	out := make([]Response, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.toResponse(row))
	}

	return out, nil
}

// Put writes an arbitrary attachment under an already-AUTHORIZED topic. The
// org must be the authorizer's answer, never a caller-supplied value.
//
// REPLACE-ON-WRITE, exactly like PutIncidentScreenshot. A topic names ONE
// current artifact ("this incident's screenshot"), not a log of every artifact
// ever produced for it, so a second write under the same topic retires the
// first. Without this an agent retrying an upload — or simply capturing on
// consecutive relapses — would stack live rows the incident card renders one
// under another, bounded only by the per-agent rate limit and unreachable by
// the GC sweep for as long as the incident exists.
//
// Scoped to the EXACT topic, never the entity prefix: a future `har` or `pcap`
// attachment on the same incident is a different artifact and must not be
// collateral damage of a screenshot upload.
//
// baseName carries NO extension: the stored filename gets the one matching the
// SNIFFED media type, so the name a user downloads can never disagree with the
// bytes inside it.
//
// ONE EXCEPTION: `checks/<uid>/screenshot` (spec 2026-09-25-34) names the
// check's recent captures, not one current artifact. It appends, then prunes
// to MaxCheckScreenshots — see appendCapped. Deciding it HERE rather than in
// each caller is what makes the in-process path and the agent upload share
// the cap.
func (s *Service) Put(
	ctx context.Context, orgUID, topic, baseName string, body []byte, details models.JSONMap,
) (string, error) {
	if isCheckScreenshotTopic(topic) {
		return s.appendCapped(ctx, orgUID, topic, baseName, body, details, MaxCheckScreenshots)
	}

	if _, err := s.files.DeleteAttachmentsByTopic(ctx, orgUID, topic); err != nil {
		return "", err
	}

	return s.put(ctx, orgUID, topic, baseName, body, details)
}

// appendCapped writes one more attachment under topic, then retires every row
// past the newest `keep` — the just-written one is always kept, whatever the
// timestamps say.
//
// Retired rows are PURGED (row soft-deleted and blob removed), not merely
// soft-deleted: this runs on every write past the cap, so leaving blobs for a
// GC pass that does not exist would bound the rows and not the bill. The prune
// is best-effort — the capture is already stored, and a failed prune is
// corrected by the next write — so it logs rather than failing the write.
func (s *Service) appendCapped(
	ctx context.Context, orgUID, topic, baseName string, body []byte, details models.JSONMap, keep int,
) (string, error) {
	fileUID, err := s.put(ctx, orgUID, topic, baseName, body, details)
	if err != nil {
		return "", err
	}

	if err := s.pruneTopic(ctx, orgUID, topic, fileUID, keep); err != nil {
		slog.WarnContext(ctx, "Failed to prune check-scoped attachments",
			"topic", topic, "error", err)
	}

	return fileUID, nil
}

// pruneTopic purges every live attachment under topic past the newest `keep`,
// never the one named by justWritten.
func (s *Service) pruneTopic(ctx context.Context, orgUID, topic, justWritten string, keep int) error {
	if s.files == nil {
		return errNoFiles
	}

	rows, err := s.files.ListAttachments(ctx, orgUID, topic)
	if err != nil {
		return err
	}

	kept := 1 // justWritten

	for _, row := range rows {
		if row.UID == justWritten {
			continue
		}

		if kept < keep {
			kept++

			continue
		}

		if purgeErr := s.files.PurgeFile(ctx, row); purgeErr != nil {
			return purgeErr
		}
	}

	return nil
}

func (s *Service) put(
	ctx context.Context, orgUID, topic, baseName string, body []byte, details models.JSONMap,
) (string, error) {
	if len(body) == 0 {
		return "", ErrEmptyAttachment
	}

	if int64(len(body)) > MaxAttachmentBytes {
		return "", ErrAttachmentTooLarge
	}

	mimeType, err := sniffMime(topic, body)
	if err != nil {
		return "", err
	}

	parsedOrg, err := uuid.Parse(orgUID)
	if err != nil {
		return "", fmt.Errorf("parse organization uid: %w", err)
	}

	file, err := s.files.CreateFile(
		ctx, parsedOrg, filestorage.GroupTypeScreenshots,
		baseName+extensionFor(mimeType), mimeType, nil, bytes.NewReader(body), int64(len(body)),
		files.WithTopic(topic), files.WithDetails(details),
	)
	if err != nil {
		return "", fmt.Errorf("write attachment: %w", err)
	}

	return file.UID, nil
}

// sniffMime validates the bytes against the allowlist FOR THIS TOPIC'S KIND.
//
// Keying on the kind rather than on a global allowlist is what stops the
// traceroute kind from widening the screenshot kind: an agent that POSTs JSON
// under `incidents/<uid>/screenshot` is refused even though JSON is now an
// accepted type somewhere. Each kind declares what it is, and anything with no
// declared type is refused outright — fail closed.
func sniffMime(topic string, body []byte) (string, error) {
	parsed, err := ParseTopic(topic)
	if err != nil {
		return "", err
	}

	switch parsed.Kind {
	case KindScreenshot:
		for _, signature := range screenshotSignatures {
			if signature.match(body) {
				return signature.mimeType, nil
			}
		}

		return "", fmt.Errorf("%w: %s attachments must be %s, %s or %s",
			ErrUnsupportedMediaType, KindScreenshot, mimePNG, mimeJPEG, mimeWebP)

	case KindTraceroute:
		// Not "is it JSON" but "is it a CAPTURE": the bytes come off the wire
		// from a deported agent, and a blob the dashboard cannot render is
		// worse stored than refused.
		if _, parseErr := nettrace.ParseCapture(body); parseErr != nil {
			return "", fmt.Errorf("%w: %s", ErrUnsupportedMediaType, parseErr.Error())
		}

		return mimeJSON, nil

	default:
		return "", fmt.Errorf("%w: no media type is defined for kind %q", ErrUnsupportedMediaType, parsed.Kind)
	}
}

// StampTracerouteRegion rewrites an agent-uploaded path capture so it names the
// region the SERVER knows the uploading agent serves.
//
// THE AGENT IS NEVER THE AUTHORITY ON WHERE IT RAN. That rule is why the agent
// leaves Region empty on the capture it uploads — but leaving it empty is only
// half a design: the dashboard reads the region out of the capture JSON, so an
// unstamped capture renders with no vantage point at all. On a private-location
// incident "which agent produced this path?" is the entire question, so the
// blank was worse than the risk it was avoiding.
//
// Stamping here restores both properties at once: the value comes from the
// agent's enrolled row rather than from anything it sent, and the stored
// artifact is self-describing for anyone who downloads the raw JSON later.
//
// Trigger is deliberately NOT stamped. At upload time the server knows the
// topic but not whether the incident it names was opened or reopened by this
// capture, and inventing one of the two would be a guess written into evidence.
// The attachment's details bag records `agent-upload`, which is the honest
// answer to "how did this arrive?".
//
// A body that does not parse is returned UNCHANGED rather than rejected: the
// media sniff downstream is the one place that decides what is acceptable, and
// duplicating that judgment here would make the two able to disagree.
func StampTracerouteRegion(body []byte, region string) []byte {
	if region == "" {
		return body
	}

	capture, err := nettrace.ParseCapture(body)
	if err != nil {
		return body
	}

	capture.Region = region

	stamped, err := capture.Marshal()
	if err != nil {
		return body
	}

	return stamped
}

// ReadCapped reads at most MaxAttachmentBytes+1 bytes and reports
// ErrAttachmentTooLarge when the body exceeds the cap. Reading one byte past
// the cap is what distinguishes "exactly at the limit" from "truncated".
func ReadCapped(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, MaxAttachmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read attachment body: %w", err)
	}

	if int64(len(body)) > MaxAttachmentBytes {
		return nil, ErrAttachmentTooLarge
	}

	return body, nil
}

// toResponse renders one attachment row, signing a fresh download URL.
func (s *Service) toResponse(row *models.File) Response {
	resp := Response{
		UID:       row.UID,
		Name:      row.Name,
		MimeType:  row.MimeType,
		Size:      row.Size,
		CreatedAt: row.CreatedAt,
	}

	if row.Topic != nil {
		if parsed, err := ParseTopic(*row.Topic); err == nil {
			resp.Kind = parsed.Kind
		}
	}

	if fileUID, err := uuid.Parse(row.UID); err == nil {
		exp, sig := signedurl.Sign([]byte(s.cfg.Auth.JWTSecret), fileUID, DownloadURLTTL)
		resp.DownloadURL = signedurl.BuildURL("", fileUID, exp, sig)
	}

	applyDetails(&resp, row.Details)

	return resp
}

// applyDetails lifts the known keys out of the free-form details bag. Unknown
// keys are ignored rather than echoed: the bag is written by the server today,
// but the agent endpoint accepts one from the wire, and a response that
// reflected arbitrary caller JSON back at the dashboard is an XSS surface.
func applyDetails(resp *Response, details models.JSONMap) {
	if len(details) == 0 {
		return
	}

	if raw, ok := details[DetailKeyCapturedAt].(string); ok {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			resp.CapturedAt = &parsed
		}
	}

	if raw, ok := details[DetailKeyRegion].(string); ok {
		resp.Region = raw
	}

	if raw, ok := details[DetailKeyCheckUID].(string); ok {
		resp.CheckUID = raw
	}

	if raw, ok := details[DetailKeyTrigger].(string); ok {
		resp.Trigger = raw
	}
}

// NewIncidentAuthorizer builds the `incidents/<uid>/…` topic authorizer.
//
// This is the hard part of the agent upload endpoint, and it is written to be
// read as a chain of refusals:
//
//  1. The incident must exist. Its row is what names the organization — the
//     request never does, so a forged topic cannot pick an org.
//  2. An ORG agent may only write to its own org's incidents.
//  3. The incident's check must be served by the region this agent is bound
//     to. Without this, any enrolled agent could attach evidence to any
//     incident in reach, which is how a screenshot becomes a defacement
//     vector on somebody else's incident page.
func NewIncidentAuthorizer(dbSvc db.Service) TopicAuthorizer {
	return AuthorizerFunc(func(
		ctx context.Context, topic ParsedTopic, who UploaderIdentity,
	) (string, error) {
		incident, err := dbSvc.GetIncidentAny(ctx, topic.EntityUID)
		if err != nil || incident == nil {
			return "", fmt.Errorf("%w: unknown incident", ErrTopicForbidden)
		}

		if who.AgentOrgUID != "" && who.AgentOrgUID != incident.OrganizationUID {
			return "", fmt.Errorf("%w: incident belongs to another organization", ErrTopicForbidden)
		}

		check, err := dbSvc.GetCheck(ctx, incident.OrganizationUID, incident.CheckUID)
		if err != nil || check == nil {
			return "", fmt.Errorf("%w: incident has no readable check", ErrTopicForbidden)
		}

		if !regionServesCheck(who.AgentRegion, check.Regions) {
			return "", fmt.Errorf("%w: this agent's region does not serve the incident's check", ErrTopicForbidden)
		}

		return incident.OrganizationUID, nil
	})
}

// NewCheckAuthorizer builds the `checks/<uid>/…` topic authorizer (spec
// 2026-09-25-34). The same chain of refusals as NewIncidentAuthorizer, one
// step shorter because the topic names the check directly:
//
//  1. The check must exist (live). Its row names the organization.
//  2. An ORG agent may only write to its own org's checks.
//  3. The check must be served by the region this agent is bound to.
func NewCheckAuthorizer(dbSvc db.Service) TopicAuthorizer {
	return AuthorizerFunc(func(
		ctx context.Context, topic ParsedTopic, who UploaderIdentity,
	) (string, error) {
		check, err := dbSvc.GetCheckAny(ctx, topic.EntityUID)
		if err != nil || check == nil {
			return "", fmt.Errorf("%w: unknown check", ErrTopicForbidden)
		}

		if who.AgentOrgUID != "" && who.AgentOrgUID != check.OrganizationUID {
			return "", fmt.Errorf("%w: check belongs to another organization", ErrTopicForbidden)
		}

		if !regionServesCheck(who.AgentRegion, check.Regions) {
			return "", fmt.Errorf("%w: this agent's region does not serve the check", ErrTopicForbidden)
		}

		return check.OrganizationUID, nil
	})
}

// regionServesCheck reports whether an agent bound to `region` runs this check.
//
// An empty check region list means "any region", which is the default for a
// check nobody pinned — an agent in the org may legitimately have produced the
// failure. An agent with no region at all never matches: it could not have run
// anything.
func regionServesCheck(agentRegion string, checkRegions []string) bool {
	if agentRegion == "" {
		return false
	}

	if len(checkRegions) == 0 {
		return true
	}

	return slices.Contains(checkRegions, agentRegion)
}
