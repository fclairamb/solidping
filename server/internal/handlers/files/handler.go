package files

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/files/signedurl"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Standard error codes for file operations.
const (
	errCodeFileNotFound base.ErrorCode = "FILE_NOT_FOUND"
	errCodeFileTooLarge base.ErrorCode = "FILE_TOO_LARGE"
	errCodeBadSignature base.ErrorCode = "BAD_SIGNATURE"
	errCodeURLExpired   base.ErrorCode = "URL_EXPIRED"
)

// Handler exposes the org-scoped HTTP routes for file management.
type Handler struct {
	base.HandlerBase
	svc *Service
	cfg *config.Config
}

// NewHandler constructs the files handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
		cfg:         cfg,
	}
}

// List handles GET /api/v1/orgs/:org/files.
func (h *Handler) List(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	query := req.URL.Query()

	limit := 50
	if v := query.Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}

	offset := 0
	if v := query.Get("offset"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	resp, err := h.svc.ListFiles(req.Context(), orgSlug, query.Get("q"), offset, limit)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Get handles GET /api/v1/orgs/:org/files/:uid.
func (h *Handler) Get(writer http.ResponseWriter, req *http.Request) error {
	resp, err := h.svc.GetFile(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"))
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// GetContent handles GET /api/v1/orgs/:org/files/:uid/content (auth, org-scoped).
func (h *Handler) GetContent(writer http.ResponseWriter, req *http.Request) error {
	file, body, err := h.svc.GetFileContent(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"))
	if err != nil {
		return h.handleError(writer, err)
	}

	defer func() { _ = body.Close() }()

	return WriteContent(writer, file.MimeType, file.Name, body)
}

// Delete handles DELETE /api/v1/orgs/:org/files/:uid.
func (h *Handler) Delete(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.DeleteFile(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid")); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// PublicGet handles GET /pub/files/:uid?exp=&sig= — no auth, signature gates access.
func (h *Handler) PublicGet(writer http.ResponseWriter, req *http.Request) error {
	uid := httpx.Param(req, "uid")

	fileUID, err := uuid.Parse(uid)
	if err != nil {
		return h.WriteError(writer, http.StatusNotFound, errCodeFileNotFound, "File not found")
	}

	query := req.URL.Query()

	exp, err := strconv.ParseInt(query.Get("exp"), 10, 64)
	if err != nil {
		return h.WriteError(writer, http.StatusForbidden, errCodeBadSignature, "Invalid signature")
	}

	sig := query.Get("sig")

	verifyErr := signedurl.Verify([]byte(h.cfg.Auth.JWTSecret), fileUID, exp, sig, time.Now())
	switch {
	case errors.Is(verifyErr, signedurl.ErrSignedURLBadSignature):
		return h.WriteError(writer, http.StatusForbidden, errCodeBadSignature, "Invalid signature")
	case errors.Is(verifyErr, signedurl.ErrSignedURLExpired):
		return h.WriteError(writer, http.StatusGone, errCodeURLExpired, "Signed URL has expired")
	case verifyErr != nil:
		return h.WriteInternalError(writer, verifyErr)
	}

	file, err := h.svc.GetFileByUID(req.Context(), uid)
	if err != nil {
		if errors.Is(err, ErrFileNotFound) {
			return h.WriteError(writer, http.StatusNotFound, errCodeFileNotFound, "File not found")
		}

		return h.WriteInternalError(writer, err)
	}

	body, err := h.svc.OpenContent(req.Context(), file)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	defer func() { _ = body.Close() }()

	return WriteContent(writer, file.MimeType, file.Name, body)
}

// safeInlineMIME is the allowlist of stored content types that may be served
// with `Content-Disposition: inline`, i.e. rendered as a top-level document on
// our own origin.
//
// It contains raster images only. `image/svg+xml` is deliberately absent: an
// SVG is an XML document that can carry <script>, so serving an uploaded one
// inline is stored XSS on the application's origin — and the MIME type is
// attacker-supplied (it comes from the multipart part header). SVG still works
// everywhere it is meant to, because Content-Disposition does not affect
// subresource loads: an <img src="...svg"> logo renders fine, and scripts
// inside an SVG referenced by <img> never execute.
func safeInlineMIME(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp":
		return true
	default:
		return false
	}
}

// WriteContent streams a stored file with hardened response headers. Exported
// so every route that serves user-uploaded bytes (org logos included) shares
// exactly one set of security headers.
func WriteContent(writer http.ResponseWriter, mimeType, name string, body io.Reader) error {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	writer.Header().Set("Content-Type", mimeType)
	// nosniff stops a browser from second-guessing the stored type and
	// executing, say, a .png that is really HTML.
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Disposition",
		contentDisposition(mimeType)+`; filename="`+sanitizeFilename(name)+`"`)
	writer.WriteHeader(http.StatusOK)

	_, err := io.Copy(writer, body)

	return err
}

// contentDisposition returns "inline" only for the safe raster allowlist.
func contentDisposition(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	if safeInlineMIME(strings.ToLower(strings.TrimSpace(base))) {
		return "inline"
	}

	return "attachment"
}

// sanitizeFilename strips the characters that would let a stored filename break
// out of the quoted-string in the Content-Disposition header (or inject a
// second header line).
func sanitizeFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '"', r == '\\', r == '\r', r == '\n', r < 0x20, r == 0x7f:
			return -1
		default:
			return r
		}
	}, name)

	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "download"
	}

	return cleaned
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrFileNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, errCodeFileNotFound, "File not found", err)
	case errors.Is(err, ErrFileTooLarge):
		return h.WriteErrorErr(writer, http.StatusRequestEntityTooLarge, errCodeFileTooLarge, "File too large", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}
