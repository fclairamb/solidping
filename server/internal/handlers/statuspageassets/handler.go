package statuspageassets

import (
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Error codes for status-page asset operations.
const (
	errCodeAssetTooLarge base.ErrorCode = "STATUS_PAGE_ASSET_TOO_LARGE"
)

// formField is the multipart field carrying the image. It is named after the
// slot ("logo" / "favicon") so the two endpoints read naturally in a curl
// invocation and in the dash0 FormData.
func formField(kind Kind) string { return string(kind) }

// Handler exposes the status-page asset HTTP routes.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler constructs the status-page asset handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: svc}
}

// Response is the status-page subset returned by the asset endpoints. It
// carries only the two public URLs, so the dashboard can feed it straight back
// into the page it already holds without a refetch.
type Response struct {
	UID         string  `json:"uid"`
	Slug        string  `json:"slug"`
	LogoURL     *string `json:"logoUrl"`
	FaviconURL  *string `json:"faviconUrl"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

func newResponse(page *models.StatusPage) Response {
	return Response{
		UID:         page.UID,
		Slug:        page.Slug,
		Name:        page.Name,
		Description: page.Description,
		LogoURL:     PublicURL(page.Settings.LogoFileUID()),
		FaviconURL:  PublicURL(page.Settings.FaviconFileUID()),
	}
}

// UploadLogo handles POST /api/v1/orgs/:org/status-pages/:statusPageUid/logo.
func (h *Handler) UploadLogo(writer http.ResponseWriter, req *http.Request) error {
	return h.upload(writer, req, KindLogo)
}

// UploadFavicon handles POST /api/v1/orgs/:org/status-pages/:statusPageUid/favicon.
func (h *Handler) UploadFavicon(writer http.ResponseWriter, req *http.Request) error {
	return h.upload(writer, req, KindFavicon)
}

// DeleteLogo handles DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/logo.
func (h *Handler) DeleteLogo(writer http.ResponseWriter, req *http.Request) error {
	return h.clear(writer, req, KindLogo)
}

// DeleteFavicon handles DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/favicon.
func (h *Handler) DeleteFavicon(writer http.ResponseWriter, req *http.Request) error {
	return h.clear(writer, req, KindFavicon)
}

// upload parses the multipart body and stores the asset.
//
// The body is bounded by http.MaxBytesReader BEFORE parsing: the declared part
// size is client-supplied and cannot be trusted to enforce the cap on its own,
// so an oversized upload is refused while streaming rather than after it has
// been buffered.
func (h *Handler) upload(writer http.ResponseWriter, req *http.Request, kind Kind) error {
	maxSize := MaxSizeFor(kind)
	field := formField(kind)

	req.Body = http.MaxBytesReader(writer, req.Body, maxSize)

	if err := req.ParseMultipartForm(maxSize); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return h.WriteErrorErr(writer, req, http.StatusRequestEntityTooLarge, errCodeAssetTooLarge,
				tooLargeMessage(kind), err)
		}

		return h.WriteValidationError(writer, "Invalid multipart body", []base.ValidationErrorField{
			{Name: field, Message: "A multipart upload with a '" + field + "' file part is required"},
		})
	}

	headers := req.MultipartForm.File[field]
	if len(headers) == 0 {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: field, Message: "A '" + field + "' file part is required"},
		})
	}

	header := headers[0]

	opened, err := header.Open()
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	defer func() { _ = opened.Close() }()

	page, err := h.svc.Upload(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "statusPageUid"), kind, Upload{
		Name:     header.Filename,
		MIMEType: header.Header.Get("Content-Type"),
		Size:     header.Size,
		Body:     opened,
	}, uploaderUID(req))
	if err != nil {
		return h.writeError(writer, req, err, kind)
	}

	return h.WriteJSON(writer, http.StatusOK, newResponse(page))
}

func (h *Handler) clear(writer http.ResponseWriter, req *http.Request, kind Kind) error {
	page, err := h.svc.Clear(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "statusPageUid"), kind)
	if err != nil {
		return h.writeError(writer, req, err, kind)
	}

	return h.WriteJSON(writer, http.StatusOK, newResponse(page))
}

// uploaderUID returns the authenticated user's UID for the file's createdBy
// column, or nil when the request carried no user.
func uploaderUID(req *http.Request) *string {
	user, ok := req.Context().Value(base.ContextKeyUser).(*models.User)
	if !ok || user == nil {
		return nil
	}

	return &user.UID
}

func tooLargeMessage(kind Kind) string {
	if kind == KindLogo {
		return "Logo must be 1 MB or smaller"
	}

	return "Favicon must be 256 KB or smaller"
}

func unsupportedTypeMessage(kind Kind) string {
	if kind == KindLogo {
		return "Logo must be a PNG, JPEG, WebP, GIF or SVG image"
	}

	return "Favicon must be an ICO, PNG, WebP, GIF or SVG image"
}

func (h *Handler) writeError(
	writer http.ResponseWriter, request *http.Request, err error, kind Kind,
) error {
	field := formField(kind)

	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found", err)
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteErrorErr(writer, request, http.StatusNotFound, base.ErrorCodeStatusPageNotFound,
			"Status page not found", err)
	case errors.Is(err, ErrUnsupportedAssetType), errors.Is(err, ErrUnknownKind):
		return h.WriteValidationError(writer, "Unsupported image type", []base.ValidationErrorField{
			{Name: field, Message: unsupportedTypeMessage(kind)},
		})
	case errors.Is(err, ErrEmptyAsset):
		return h.WriteValidationError(writer, "Empty upload", []base.ValidationErrorField{
			{Name: field, Message: "The uploaded file is empty"},
		})
	case errors.Is(err, ErrAssetTooLarge):
		return h.WriteErrorErr(writer, request, http.StatusRequestEntityTooLarge, errCodeAssetTooLarge,
			tooLargeMessage(kind), err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
