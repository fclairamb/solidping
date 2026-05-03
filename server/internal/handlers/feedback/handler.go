package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// MaxReportSize caps the multipart body size accepted by the report endpoint.
// Bigger payloads are rejected with 413 — they can't be a screenshot of a
// useful UI bug at that point.
const MaxReportSize int64 = 10 * 1024 * 1024

// Custom error codes specific to the feedback endpoint.
const (
	errCodeBadRequest    base.ErrorCode = "BAD_REQUEST"
	errCodeRequestTooBig base.ErrorCode = "REQUEST_TOO_LARGE"
)

// Handler exposes the public POST /api/mgmt/report endpoint.
type Handler struct {
	base.HandlerBase
	svc  *Service
	auth *auth.Service
}

// NewHandler builds the handler. authSvc may be nil if attribution is not
// needed (the endpoint still works without auth).
func NewHandler(svc *Service, authSvc *auth.Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
		auth:        authSvc,
	}
}

// SubmitReport handles POST /api/mgmt/report.
func (h *Handler) SubmitReport(writer http.ResponseWriter, req bunrouter.Request) error {
	req.Body = http.MaxBytesReader(writer, req.Body, MaxReportSize)

	if err := req.ParseMultipartForm(MaxReportSize); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return h.WriteErrorErr(writer, http.StatusRequestEntityTooLarge,
				errCodeRequestTooBig, "Report exceeds 10 MB", err)
		}

		return h.WriteErrorErr(writer, http.StatusBadRequest, errCodeBadRequest, "Invalid multipart body", err)
	}

	form := req.MultipartForm

	submission := &SubmitReportRequest{
		URL:         formValue(form, "url"),
		Comment:     formValue(form, "comment"),
		OrgSlug:     formValue(form, "org"),
		Annotations: formValue(form, "annotations"),
	}

	if submission.URL == "" {
		return h.WriteErrorErr(writer, http.StatusBadRequest, errCodeBadRequest, "Missing 'url' field", ErrURLRequired)
	}

	if rawCtx := formValue(form, "context"); rawCtx != "" {
		_ = json.Unmarshal([]byte(rawCtx), &submission.Context)
	}

	h.attachUserIfAuthenticated(req.Context(), req.Header.Get("Authorization"), submission)

	if files := form.File["screenshot"]; len(files) > 0 {
		header := files[0]

		opened, err := header.Open()
		if err != nil {
			return h.WriteInternalError(writer, err)
		}

		defer func() { _ = opened.Close() }()

		submission.Screenshot = opened
		submission.ScreenshotName = header.Filename
		submission.ScreenshotSize = header.Size
		submission.ScreenshotMIME = header.Header.Get("Content-Type")
	}

	resp, err := h.svc.SubmitReport(req.Context(), submission)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteErrorErr(writer, http.StatusNotFound,
				base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		case errors.Is(err, ErrURLRequired):
			return h.WriteErrorErr(writer, http.StatusBadRequest, errCodeBadRequest, "Missing 'url' field", err)
		default:
			return h.WriteInternalError(writer, err)
		}
	}

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// attachUserIfAuthenticated parses the optional bearer token. Failures are
// silent — the endpoint is intentionally permissive on auth.
func (h *Handler) attachUserIfAuthenticated(ctx context.Context, authHeader string, sub *SubmitReportRequest) {
	if h.auth == nil || authHeader == "" {
		return
	}

	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return
	}

	token := strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
	if token == "" {
		return
	}

	claims, err := h.auth.ValidateToken(ctx, token)
	if err != nil {
		return
	}

	sub.UserUID = claims.UserUID

	user, err := h.svc.db.GetUser(ctx, claims.UserUID)
	if err == nil && user != nil {
		sub.UserEmail = user.Email
	}
}

func formValue(form *multipart.Form, key string) string {
	if form == nil {
		return ""
	}

	if values, ok := form.Value[key]; ok && len(values) > 0 {
		return values[0]
	}

	return ""
}
