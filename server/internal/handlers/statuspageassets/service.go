// Package statuspageassets implements per-status-page brand assets — the
// uploaded logo and favicon (spec 2026-08-21-07) — as an admin-gated upload
// endpoint plus the unsigned public route that serves the stored image.
//
// It sits between the status page (which stores the two file UIDs in its
// `settings -> branding` section) and the generic files subsystem (which owns
// the bytes and, through the file's TOPIC, the public-access rule), so neither
// has to know about the other.
//
// Since spec 2026-08-22-03 this package no longer serves anything: the unsigned
// public route is files.Handler.PublicAssetGet, authorized by
// files.IsPublicTopic for every asset kind at once.
package statuspageassets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"strings"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

// Kind names the asset slot on a status page. There are exactly two, and each
// one is a single column — a page has one logo and one favicon, not a
// collection, which is why this is an enum rather than a free-form label.
type Kind string

// Asset kinds.
const (
	KindLogo    Kind = "logo"
	KindFavicon Kind = "favicon"
)

// Service errors.
var (
	// ErrOrganizationNotFound is returned when the org slug does not resolve.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrStatusPageNotFound is returned when the page UID/slug does not resolve.
	ErrStatusPageNotFound = errors.New("status page not found")
	// ErrUnsupportedAssetType is returned for a content type outside the allowlist.
	ErrUnsupportedAssetType = errors.New("unsupported asset content type")
	// ErrAssetTooLarge is returned when the upload exceeds the kind's cap.
	ErrAssetTooLarge = errors.New("asset too large")
	// ErrEmptyAsset is returned for a zero-byte upload.
	ErrEmptyAsset = errors.New("asset is empty")
	// ErrUnknownKind is returned for an asset kind outside the two supported slots.
	ErrUnknownKind = errors.New("unknown status page asset kind")
)

// Size caps. A logo is a small brand asset, not a photo, and a favicon is
// smaller still: tight bounds are the cheapest defense against an org filling
// the storage backend, and they keep the whole file comfortably bufferable by
// the S3 backend.
const (
	// MaxLogoSize caps a status-page logo upload.
	MaxLogoSize int64 = 1 << 20 // 1 MB
	// MaxFaviconSize caps a favicon upload — an icon that needs more than
	// 256 KB is not a favicon.
	MaxFaviconSize int64 = 256 << 10 // 256 KB
)

// PublicPathPrefix is the public URL prefix a status-page asset is addressed
// by. It is an ALIAS of files.PublicAssetPathPrefix: the same generic handler
// and the same allowlist check answer on both paths.
//
// It survives the move to topic-based authorization because the status-page
// payload contract pins this exact string in `logoUrl` / `faviconUrl`, and this
// change is a storage-and-authorization refactor — not a URL change. It is NOT
// a second implementation: there is exactly one public-asset handler.
const PublicPathPrefix = "/pub/status-page-assets/"

// Topic returns the attachment topic a page's asset is stored under. It is
// what makes the blob publicly readable — files.IsPublicTopic allowlists
// exactly `status-pages/<uid>/{logo,favicon}`.
func Topic(pageUID string, kind Kind) string {
	return files.StatusPageAssetTopic(pageUID, string(kind))
}

// MaxSizeFor returns the upload cap for an asset kind. An unknown kind gets
// the smaller of the two — failing closed on a size limit is free.
func MaxSizeFor(kind Kind) int64 {
	if kind == KindLogo {
		return MaxLogoSize
	}

	return MaxFaviconSize
}

// allowedType is the per-kind upload allowlist.
//
// SVG is accepted because brand assets are commonly vector, but it is served
// with Content-Disposition: attachment and nosniff (files.WriteContent) so it
// can never execute as a document on our origin — an uploaded SVG is an XML
// file that may carry <script>.
//
// The favicon list additionally accepts the two .ico media types, which is the
// whole point of a favicon slot, and drops JPEG: a lossy photo format has no
// business being a 32×32 icon.
func allowedType(kind Kind, mimeType string) bool {
	switch kind {
	case KindLogo:
		switch mimeType {
		case "image/png", "image/jpeg", "image/webp", "image/gif", "image/svg+xml":
			return true
		default:
			return false
		}
	case KindFavicon:
		switch mimeType {
		case "image/png", "image/webp", "image/gif", "image/svg+xml",
			"image/x-icon", "image/vnd.microsoft.icon":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// Service implements the status-page asset upload/clear/serve operations.
type Service struct {
	db    db.Service
	files *files.Service
}

// NewService constructs the status-page asset service.
func NewService(dbService db.Service, filesService *files.Service) *Service {
	return &Service{db: dbService, files: filesService}
}

// Upload carries a multipart asset part into the service.
type Upload struct {
	Name     string
	MIMEType string
	Size     int64
	Body     io.Reader
}

// Upload stores a new asset for the page and points the matching column at it.
//
// A previously uploaded asset in the same slot is retired in the same step: its
// file row is soft-deleted, which also removes it from the public asset route
// (that route only serves a page's CURRENT assets).
func (s *Service) Upload(
	ctx context.Context, orgSlug, pageIdentifier string, kind Kind, upload Upload, uploadedBy *string,
) (*models.StatusPage, error) {
	org, page, err := s.resolve(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return nil, err
	}

	mimeType, err := NormalizeMIME(kind, upload.MIMEType)
	if err != nil {
		return nil, err
	}

	if upload.Size <= 0 {
		return nil, ErrEmptyAsset
	}

	if upload.Size > MaxSizeFor(kind) {
		return nil, ErrAssetTooLarge
	}

	orgUUID, err := uuid.Parse(org.UID)
	if err != nil {
		return nil, fmt.Errorf("parse organization uid: %w", err)
	}

	file, err := s.files.CreateFile(
		ctx, orgUUID, filestorage.GroupTypeStatusPageAssets,
		upload.Name, mimeType, uploadedBy, upload.Body, upload.Size,
		// The topic is the authorization: without it the blob is private and
		// the public asset route 404s it.
		files.WithTopic(Topic(page.UID, kind)),
	)
	if err != nil {
		return nil, fmt.Errorf("store status page asset: %w", err)
	}

	previous := currentFileUID(page, kind)

	update := brandingUpdateFor(page, kind, &file.UID)
	if updErr := s.db.UpdateStatusPageBranding(ctx, page.UID, update); updErr != nil {
		return nil, fmt.Errorf("update status page branding: %w", updErr)
	}

	s.retireFile(ctx, org.UID, previous, file.UID)

	return s.db.GetStatusPage(ctx, org.UID, page.UID)
}

// Clear removes the page's asset in the given slot, retiring the uploaded file.
func (s *Service) Clear(
	ctx context.Context, orgSlug, pageIdentifier string, kind Kind,
) (*models.StatusPage, error) {
	org, page, err := s.resolve(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return nil, err
	}

	if !validKind(kind) {
		return nil, ErrUnknownKind
	}

	previous := currentFileUID(page, kind)

	update := brandingUpdateFor(page, kind, nil)
	if updErr := s.db.UpdateStatusPageBranding(ctx, page.UID, update); updErr != nil {
		return nil, fmt.Errorf("clear status page branding: %w", updErr)
	}

	s.retireFile(ctx, org.UID, previous, "")

	return s.db.GetStatusPage(ctx, org.UID, page.UID)
}

// resolve looks up the org and the page, mapping "no rows" to the package's
// own not-found errors so the handler never has to know about sql.ErrNoRows.
func (s *Service) resolve(
	ctx context.Context, orgSlug, pageIdentifier string,
) (*models.Organization, *models.StatusPage, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrOrganizationNotFound
		}

		return nil, nil, err
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, pageIdentifier)
	if err != nil || page == nil {
		return nil, nil, ErrStatusPageNotFound
	}

	return org, page, nil
}

// retireFile soft-deletes the page's previous asset, unless it is the one that
// just replaced it. Failures are logged, never fatal: the page already points
// at the new asset and a stale row is only wasted storage.
func (s *Service) retireFile(ctx context.Context, orgUID, previousUID, keepUID string) {
	if previousUID == "" || previousUID == keepUID {
		return
	}

	if err := s.db.DeleteFile(ctx, orgUID, previousUID); err != nil {
		slog.WarnContext(ctx, "failed to retire previous status page asset",
			"error", err, "orgUID", orgUID, "fileUID", previousUID)
	}
}

// validKind reports whether kind names one of the two asset slots.
func validKind(kind Kind) bool {
	return kind == KindLogo || kind == KindFavicon
}

// currentFileUID reads the page's file UID for the slot, "" when unset.
func currentFileUID(page *models.StatusPage, kind Kind) string {
	var current *string

	if kind == KindLogo {
		current = page.Settings.LogoFileUID()
	} else {
		current = page.Settings.FaviconFileUID()
	}

	if current == nil {
		return ""
	}

	return *current
}

// brandingUpdateFor builds the whole-section write for one slot, carrying the
// OTHER slot's current value — and the white-label opt-in — through unchanged.
// models.StatusPageBrandingUpdate replaces `settings -> branding` verbatim on
// purpose (nil clears), so everything the section holds has to be restated
// here; that is the cost of never leaving a stale, still publicly-reachable
// file UID behind.
func brandingUpdateFor(page *models.StatusPage, kind Kind, value *string) *models.StatusPageBrandingUpdate {
	update := &models.StatusPageBrandingUpdate{
		LogoFileUID:    page.Settings.LogoFileUID(),
		FaviconFileUID: page.Settings.FaviconFileUID(),
		HideBranding:   page.Settings.HideBranding(),
	}

	if kind == KindLogo {
		update.LogoFileUID = value
	} else {
		update.FaviconFileUID = value
	}

	return update
}

// PublicURL returns the stable public path for an asset file UID, or nil when
// the slot is unset. Shared with the statuspages response builder so the URL
// shape lives in exactly one place.
func PublicURL(fileUID *string) *string {
	if fileUID == nil || *fileUID == "" {
		return nil
	}

	url := PublicPathPrefix + *fileUID

	return &url
}

// NormalizeMIME validates the declared content type against the kind's
// allowlist and strips any parameters (e.g. `image/svg+xml; charset=utf-8`), so
// the value stored on the file row — and later echoed as a Content-Type header
// — is always one of a handful of known-safe strings rather than arbitrary
// client-supplied text.
func NormalizeMIME(kind Kind, declared string) (string, error) {
	if !validKind(kind) {
		return "", ErrUnknownKind
	}

	value := strings.TrimSpace(declared)
	if value == "" {
		return "", ErrUnsupportedAssetType
	}

	parsed, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", ErrUnsupportedAssetType
	}

	parsed = strings.ToLower(parsed)
	if !allowedType(kind, parsed) {
		return "", ErrUnsupportedAssetType
	}

	return parsed, nil
}
