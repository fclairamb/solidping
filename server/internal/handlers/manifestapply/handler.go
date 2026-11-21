package manifestapply

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const queryTrue = "true"

// Handler serves the kind-neutral apply and export endpoints. The service is
// wired after the routes are registered (it depends on services built later in
// server setup), hence the atomic pointer.
type Handler struct {
	base.HandlerBase
	svc atomic.Pointer[Service]
}

// NewHandler builds the handler; call SetService before serving.
func NewHandler(cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg)}
}

// SetService wires the orchestrator.
func (h *Handler) SetService(svc *Service) { h.svc.Store(svc) }

// Apply handles POST /orgs/:org/apply and POST /orgs/:org/checks/apply.
func (h *Handler) Apply(writer http.ResponseWriter, req *http.Request) error {
	svc := h.svc.Load()
	if svc == nil {
		return h.WriteError(writer, http.StatusServiceUnavailable, base.ErrorCodeInternalError, "Apply is not ready")
	}

	query := req.URL.Query()

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return h.WriteValidationError(writer, "Invalid body", []base.ValidationErrorField{
			{Name: "body", Message: "unreadable body"},
		})
	}

	doc, err := checks.ParseManifest(body, req.Header.Get("Content-Type"))
	if err != nil {
		return h.WriteValidationError(writer, "Invalid manifest", []base.ValidationErrorField{
			{Name: "body", Message: err.Error()},
		})
	}

	opts := checks.ApplyOptions{
		DryRun: query.Get("dryRun") == queryTrue,
		Prune:  query.Get("prune") == queryTrue,
		Force:  query.Get("force") == queryTrue,
	}
	if capStr := query.Get("deletionCap"); capStr != "" {
		if parsed, convErr := strconv.Atoi(capStr); convErr == nil && parsed >= 0 {
			opts.DeletionCap = parsed
		}
	}

	result, err := svc.Apply(req.Context(), httpx.Param(req, "org"), doc, opts)
	if err != nil {
		switch {
		case errors.Is(err, checks.ErrOrganizationNotFound):
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		case errors.Is(err, checks.ErrDeletionCapExceeded):
			return h.WriteErrorErr(writer, req, http.StatusConflict, base.ErrorCodeConflict, err.Error(), err)
		default:
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// Export handles GET /orgs/:org/export: every kind, as a v2 document.
func (h *Handler) Export(writer http.ResponseWriter, req *http.Request) error {
	svc := h.svc.Load()
	if svc == nil {
		return h.WriteError(writer, http.StatusServiceUnavailable, base.ErrorCodeInternalError, "Export is not ready")
	}

	doc, err := svc.Export(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		if errors.Is(err, checks.ErrOrganizationNotFound) {
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		}

		return h.WriteInternalError(writer, req, err)
	}

	doc.Version = checks.ExportVersionV2

	body, err := checks.MarshalExportDocument(doc)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, err = writer.Write(body)

	return err
}
