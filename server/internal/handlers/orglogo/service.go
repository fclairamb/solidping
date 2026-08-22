// Package orglogo implements the organization logo: an owner-only upload
// endpoint.
//
// It sits between the org profile (which owns `organizations.logo_url`) and the
// generic files subsystem (which owns the bytes and, through the file's TOPIC,
// the public-access rule), so neither has to know about the other.
//
// Since spec 2026-08-22-03 this package no longer serves anything. The bespoke
// `/pub/org-logos/:uid` route and its "is this some live org's current logo"
// state query are gone; an uploaded logo is served by the one generic public
// asset route (files.Handler.PublicAssetGet), authorized by the file's topic
// `organizations/<uid>/logo`.
package orglogo

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

// Service errors.
var (
	// ErrOrganizationNotFound is returned when the org slug does not resolve.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrUnsupportedLogoType is returned for a content type outside the allowlist.
	ErrUnsupportedLogoType = errors.New("unsupported logo content type")
	// ErrLogoTooLarge is returned when the upload exceeds MaxLogoSize.
	ErrLogoTooLarge = errors.New("logo too large")
	// ErrEmptyLogo is returned for a zero-byte upload.
	ErrEmptyLogo = errors.New("logo is empty")
)

// MaxLogoSize caps a logo upload. A logo is a small brand asset, not a photo:
// a tight bound is the cheapest defense against an org filling the storage
// backend, and it keeps the whole file comfortably bufferable by the S3 backend.
const MaxLogoSize int64 = 1 << 20 // 1 MB

// LogoPublicPathPrefix is the public URL prefix an uploaded org logo is
// addressed by. It is files.PublicAssetPathPrefix — the same route, the same
// handler and the same allowlist check that serve status-page assets.
const LogoPublicPathPrefix = files.PublicAssetPathPrefix

// allowedLogoType is the upload allowlist. SVG is accepted because logos are
// commonly vector, but it is served with Content-Disposition: attachment and
// nosniff (files.WriteContent) so it can never execute as a document on our
// origin — an uploaded SVG is an XML file that may carry <script>.
func allowedLogoType(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp", "image/gif", "image/svg+xml":
		return true
	default:
		return false
	}
}

// Service implements the logo upload/clear/serve operations.
type Service struct {
	db    db.Service
	files *files.Service
}

// NewService constructs the org-logo service.
func NewService(dbService db.Service, filesService *files.Service) *Service {
	return &Service{db: dbService, files: filesService}
}

// Upload stores a new logo for the organization and points logo_url at it.
//
// A previously uploaded logo is retired in the same step: its file row is
// soft-deleted, which also removes it from the public asset route (that route
// only serves LIVE file rows). An external-URL logo has no file to retire and
// is simply overwritten.
func (s *Service) Upload(
	ctx context.Context, orgSlug string, upload Upload, uploadedBy *string,
) (*models.Organization, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	mimeType, err := NormalizeLogoMIME(upload.MIMEType)
	if err != nil {
		return nil, err
	}

	if upload.Size <= 0 {
		return nil, ErrEmptyLogo
	}

	if upload.Size > MaxLogoSize {
		return nil, ErrLogoTooLarge
	}

	orgUUID, err := uuid.Parse(org.UID)
	if err != nil {
		return nil, fmt.Errorf("parse organization uid: %w", err)
	}

	file, err := s.files.CreateFile(
		ctx, orgUUID, filestorage.GroupTypeOrgLogos,
		upload.Name, mimeType, uploadedBy, upload.Body, upload.Size,
		// The topic is the authorization: without it the blob is private and
		// the public asset route 404s it.
		files.WithTopic(files.OrganizationLogoTopic(org.UID)),
	)
	if err != nil {
		return nil, fmt.Errorf("store logo: %w", err)
	}

	logoURL := LogoPublicPathPrefix + file.UID
	update := models.OrganizationUpdate{LogoURL: &logoURL, LogoFileUID: &file.UID}

	if updErr := s.db.UpdateOrganization(ctx, org.UID, update); updErr != nil {
		return nil, fmt.Errorf("update organization logo: %w", updErr)
	}

	s.retireFile(ctx, org, file.UID)

	return s.db.GetOrganization(ctx, org.UID)
}

// Clear removes the organization's logo, retiring the uploaded file if any.
func (s *Service) Clear(ctx context.Context, orgSlug string) (*models.Organization, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	update := models.OrganizationUpdate{ClearLogoURL: true, ClearLogoFileUID: true}
	if updErr := s.db.UpdateOrganization(ctx, org.UID, update); updErr != nil {
		return nil, fmt.Errorf("clear organization logo: %w", updErr)
	}

	s.retireFile(ctx, org, "")

	return s.db.GetOrganization(ctx, org.UID)
}

// retireFile soft-deletes the org's previous logo file, unless it is the one
// that just replaced it. Failures are logged, never fatal: the org already
// points at the new logo and a stale row is only wasted storage — though it IS
// also what un-publishes the old blob, so a failure here leaves a superseded
// logo readable until the GC sweep.
func (s *Service) retireFile(ctx context.Context, org *models.Organization, keepUID string) {
	if org.LogoFileUID == nil || *org.LogoFileUID == "" || *org.LogoFileUID == keepUID {
		return
	}

	if err := s.db.DeleteFile(ctx, org.UID, *org.LogoFileUID); err != nil {
		slog.WarnContext(ctx, "failed to retire previous organization logo",
			"error", err, "orgUID", org.UID, "fileUID", *org.LogoFileUID)
	}
}

// Upload carries a multipart logo part into the service.
type Upload struct {
	Name     string
	MIMEType string
	Size     int64
	Body     io.Reader
}

// NormalizeLogoMIME validates the declared content type against the allowlist
// and strips any parameters (e.g. `image/svg+xml; charset=utf-8`), so the value
// stored on the file row — and later echoed as a Content-Type header — is
// always one of a handful of known-safe strings rather than arbitrary
// client-supplied text.
func NormalizeLogoMIME(declared string) (string, error) {
	value := strings.TrimSpace(declared)
	if value == "" {
		return "", ErrUnsupportedLogoType
	}

	parsed, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", ErrUnsupportedLogoType
	}

	parsed = strings.ToLower(parsed)
	if !allowedLogoType(parsed) {
		return "", ErrUnsupportedLogoType
	}

	return parsed, nil
}
