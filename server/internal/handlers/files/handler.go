package files

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/files/signedurl"
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
func (h *Handler) List(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
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
func (h *Handler) Get(writer http.ResponseWriter, req bunrouter.Request) error {
	resp, err := h.svc.GetFile(req.Context(), req.Param("org"), req.Param("uid"))
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// GetContent handles GET /api/v1/orgs/:org/files/:uid/content (auth, org-scoped).
func (h *Handler) GetContent(writer http.ResponseWriter, req bunrouter.Request) error {
	file, body, err := h.svc.GetFileContent(req.Context(), req.Param("org"), req.Param("uid"))
	if err != nil {
		return h.handleError(writer, err)
	}

	defer func() { _ = body.Close() }()

	return writeFileContent(writer, file.MimeType, file.Name, body)
}

// Delete handles DELETE /api/v1/orgs/:org/files/:uid.
func (h *Handler) Delete(writer http.ResponseWriter, req bunrouter.Request) error {
	if err := h.svc.DeleteFile(req.Context(), req.Param("org"), req.Param("uid")); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// PublicGet handles GET /pub/files/:uid?exp=&sig= — no auth, signature gates access.
func (h *Handler) PublicGet(writer http.ResponseWriter, req bunrouter.Request) error {
	uid := req.Param("uid")

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

	return writeFileContent(writer, file.MimeType, file.Name, body)
}

func writeFileContent(writer http.ResponseWriter, mimeType, name string, body io.Reader) error {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	writer.Header().Set("Content-Type", mimeType)
	writer.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
	writer.WriteHeader(http.StatusOK)

	_, err := io.Copy(writer, body)

	return err
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
