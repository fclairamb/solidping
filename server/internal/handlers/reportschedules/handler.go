package reportschedules

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

const (
	fieldBody      = "body"
	msgInvalidJSON = "Invalid JSON format"
	msgValidation  = "Validation error"
)

// Handler provides HTTP handlers for report-schedule endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new report-schedules handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: service}
}

// List handles listing an organization's report schedules.
func (h *Handler) List(writer http.ResponseWriter, req *http.Request) error {
	rows, err := h.svc.List(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": rows})
}

// Create handles creating a report schedule.
func (h *Handler) Create(writer http.ResponseWriter, req *http.Request) error {
	var createReq CreateRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	row, err := h.svc.Create(req.Context(), httpx.Param(req, "org"), &createReq)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, row)
}

// Get handles retrieving a report schedule.
func (h *Handler) Get(writer http.ResponseWriter, req *http.Request) error {
	row, err := h.svc.Get(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"))
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, row)
}

// Update handles updating a report schedule.
func (h *Handler) Update(writer http.ResponseWriter, req *http.Request) error {
	var updateReq UpdateRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	row, err := h.svc.Update(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"), updateReq)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, row)
}

// Delete handles deleting a report schedule.
func (h *Handler) Delete(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.Delete(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid")); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// TestSendRequest optionally overrides the destination of a test send.
type TestSendRequest struct {
	Recipient string `json:"recipient"`
}

// TestSend renders the report and mails it to the caller.
//
// The default destination is the AUTHENTICATED CALLER's own address, never the
// schedule's recipient list — "preview this" must not be a button that mails
// the whole distribution list.
func (h *Handler) TestSend(writer http.ResponseWriter, req *http.Request) error {
	var testReq TestSendRequest

	if req.Body != nil {
		// A body is optional here; an unparseable one is simply ignored in
		// favor of the caller's own address.
		_ = json.NewDecoder(req.Body).Decode(&testReq)
	}

	recipient := testReq.Recipient
	if recipient == "" {
		if user, ok := middleware.GetUserFromContext(req.Context()); ok && user != nil {
			recipient = user.Email
		}
	}

	err := h.svc.TestSend(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"), recipient, time.Now(),
	)
	if err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusAccepted)

	return nil
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrReportScheduleNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeReportScheduleNotFound, "Report schedule not found", err)
	case errors.Is(err, ErrNameRequired):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "name", Message: "Name is required"},
		})
	case errors.Is(err, ErrInvalidFrequency):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "frequency", Message: ErrInvalidFrequency.Error()},
		})
	case errors.Is(err, ErrInvalidTimezone):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "timezone", Message: ErrInvalidTimezone.Error()},
		})
	case errors.Is(err, ErrTooManyRecipients):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "recipients", Message: ErrTooManyRecipients.Error()},
		})
	case errors.Is(err, ErrInvalidRecipient):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "recipients", Message: ErrInvalidRecipient.Error()},
		})
	case errors.Is(err, ErrNoTestRecipient):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "recipient", Message: ErrNoTestRecipient.Error()},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}
