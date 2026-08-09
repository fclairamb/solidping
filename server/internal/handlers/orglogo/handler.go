package orglogo

import (
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Error codes for logo operations.
const (
	errCodeLogoNotFound base.ErrorCode = "LOGO_NOT_FOUND"
	errCodeLogoTooLarge base.ErrorCode = "LOGO_TOO_LARGE"
)

// logoFormField is the multipart field carrying the image.
const logoFormField = "logo"

// Handler exposes the org logo HTTP routes.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler constructs the org-logo handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: svc}
}

// Response is the org profile subset returned by the logo endpoints. It
// deliberately mirrors the PATCH /orgs/:org payload so the dashboard can feed
// either response into the same state.
type Response struct {
	UID     string  `json:"uid"`
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	LogoURL *string `json:"logoUrl"`
}

func newResponse(org *models.Organization) Response {
	return Response{UID: org.UID, Slug: org.Slug, Name: org.Name, LogoURL: org.LogoURL}
}

// Upload handles POST /api/v1/orgs/:org/logo (multipart, owner-gated by
// middleware.RequireOrgOwner).
//
// The body is bounded by http.MaxBytesReader BEFORE parsing: the declared part
// size is client-supplied and cannot be trusted to enforce the cap on its own,
// so an oversized upload is refused while streaming rather than after it has
// been buffered.
func (h *Handler) Upload(writer http.ResponseWriter, req *http.Request) error {
	req.Body = http.MaxBytesReader(writer, req.Body, MaxLogoSize)

	if err := req.ParseMultipartForm(MaxLogoSize); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return h.WriteErrorErr(writer, http.StatusRequestEntityTooLarge, errCodeLogoTooLarge,
				"Logo must be 1 MB or smaller", err)
		}

		return h.WriteValidationError(writer, "Invalid multipart body", []base.ValidationErrorField{
			{Name: logoFormField, Message: "A multipart upload with a 'logo' file part is required"},
		})
	}

	headers := req.MultipartForm.File[logoFormField]
	if len(headers) == 0 {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: logoFormField, Message: "A 'logo' file part is required"},
		})
	}

	header := headers[0]

	opened, err := header.Open()
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	defer func() { _ = opened.Close() }()

	org, err := h.svc.Upload(req.Context(), httpx.Param(req, "org"), Upload{
		Name:     header.Filename,
		MIMEType: header.Header.Get("Content-Type"),
		Size:     header.Size,
		Body:     opened,
	}, uploaderUID(req))
	if err != nil {
		return h.writeError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, newResponse(org))
}

// Delete handles DELETE /api/v1/orgs/:org/logo.
func (h *Handler) Delete(writer http.ResponseWriter, req *http.Request) error {
	org, err := h.svc.Clear(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.writeError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, newResponse(org))
}

// PublicGet handles GET /pub/org-logos/:uid — unsigned and unauthenticated.
//
// There is no signature to check because the authorization is state-based: the
// service only opens a file that is the current logo of a live organization. A
// logo that has been replaced, cleared, or whose org was deleted stops
// resolving here immediately.
func (h *Handler) PublicGet(writer http.ResponseWriter, req *http.Request) error {
	file, body, err := h.svc.OpenLogo(req.Context(), httpx.Param(req, "uid"))
	if err != nil {
		if errors.Is(err, ErrLogoNotFound) {
			return h.WriteError(writer, http.StatusNotFound, errCodeLogoNotFound, "Logo not found")
		}

		return h.WriteInternalError(writer, err)
	}

	defer func() { _ = body.Close() }()

	// Short cache: the URL changes on every upload (it embeds the file UID), so
	// this only bounds staleness for a logo cleared without replacement.
	writer.Header().Set("Cache-Control", "public, max-age=300")

	// files.WriteContent is the single place that sets nosniff and refuses to
	// serve anything but a raster image inline — the SVG-XSS guard.
	return files.WriteContent(writer, file.MimeType, file.Name, body)
}

// uploaderUID returns the authenticated user's UID for the file's createdBy
// column, or nil when the request carried no user (it always does on the
// owner-gated route; this keeps the handler independent of that).
func uploaderUID(req *http.Request) *string {
	user, ok := req.Context().Value(base.ContextKeyUser).(*models.User)
	if !ok || user == nil {
		return nil
	}

	return &user.UID
}

func (h *Handler) writeError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found", err)
	case errors.Is(err, ErrUnsupportedLogoType):
		return h.WriteValidationError(writer, "Unsupported logo type", []base.ValidationErrorField{
			{Name: logoFormField, Message: "Logo must be a PNG, JPEG, WebP, GIF or SVG image"},
		})
	case errors.Is(err, ErrEmptyLogo):
		return h.WriteValidationError(writer, "Empty logo", []base.ValidationErrorField{
			{Name: logoFormField, Message: "The uploaded file is empty"},
		})
	case errors.Is(err, ErrLogoTooLarge):
		return h.WriteErrorErr(writer, http.StatusRequestEntityTooLarge, errCodeLogoTooLarge,
			"Logo must be 1 MB or smaller", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}
