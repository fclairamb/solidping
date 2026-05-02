package system

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// Handler provides HTTP handlers for system parameter endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new system handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListParameters handles GET /api/v1/system/parameters.
func (h *Handler) ListParameters(writer http.ResponseWriter, req bunrouter.Request) error {
	params, err := h.svc.ListParameters(req.Context())
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, params)
}

// GetParameter handles GET /api/v1/system/parameters/:key.
func (h *Handler) GetParameter(writer http.ResponseWriter, req bunrouter.Request) error {
	key := req.Param("key")

	param, err := h.svc.GetParameter(req.Context(), key)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, param)
}

// SetParameter handles PUT /api/v1/system/parameters/:key.
func (h *Handler) SetParameter(writer http.ResponseWriter, req bunrouter.Request) error {
	key := req.Param("key")

	var setReq SetParameterRequest
	if err := json.NewDecoder(req.Body).Decode(&setReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	// Default secret to false if not provided
	secret := false
	if setReq.Secret != nil {
		secret = *setReq.Secret
	}

	param, err := h.svc.SetParameter(req.Context(), key, setReq.Value, secret)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, param)
}

// DeleteParameter handles DELETE /api/v1/system/parameters/:key.
func (h *Handler) DeleteParameter(writer http.ResponseWriter, req bunrouter.Request) error {
	key := req.Param("key")

	if err := h.svc.DeleteParameter(req.Context(), key); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// TestEmail handles POST /api/v1/system/test-email.
func (h *Handler) TestEmail(writer http.ResponseWriter, req bunrouter.Request) error {
	var body TestEmailRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	if body.Recipient == "" {
		return h.WriteValidationError(writer, "Recipient is required", []base.ValidationErrorField{
			{Name: "recipient", Message: "Recipient email address is required"},
		})
	}

	result, err := h.svc.TestEmail(req.Context(), body.Recipient)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// handleError translates service errors to HTTP responses.
func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrParameterNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Parameter not found")
	default:
		return h.WriteInternalError(writer, err)
	}
}

// EmailInboxStatusResponse mirrors jmap.Status for the API surface.
type EmailInboxStatusResponse struct {
	Enabled       bool       `json:"enabled"`
	Connected     bool       `json:"connected"`
	LastSyncedAt  *time.Time `json:"lastSyncedAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	AddressDomain string     `json:"addressDomain,omitempty"`
	AccountID     string     `json:"accountId,omitempty"`
}

// EmailInboxTestRequest is the optional request body for POST /email-inbox/test.
type EmailInboxTestRequest struct {
	SessionURL    string `json:"sessionUrl,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	AddressDomain string `json:"addressDomain,omitempty"`
}

// EmailInboxPublic handles GET /api/v1/system/parameters/email_inbox/public.
// Returns just the addressDomain so any authenticated user can render the
// per-check email address. Empty when the inbox isn't configured.
func (h *Handler) EmailInboxPublic(writer http.ResponseWriter, req bunrouter.Request) error {
	domain, err := h.svc.EmailInboxPublicAddressDomain(req.Context())
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"addressDomain": domain})
}

// EmailInboxStatus handles GET /api/v1/system/email-inbox/status.
func (h *Handler) EmailInboxStatus(writer http.ResponseWriter, _ bunrouter.Request) error {
	status, err := h.svc.EmailInboxStatus()
	if err != nil {
		return h.handleEmailInboxError(writer, err)
	}

	resp := EmailInboxStatusResponse{
		Enabled:       status.Enabled,
		Connected:     status.Connected,
		LastSyncedAt:  status.LastSyncedAt,
		LastError:     status.LastError,
		AddressDomain: status.AddressDomain,
		AccountID:     status.AccountID,
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// EmailInboxTest handles POST /api/v1/system/email-inbox/test.
func (h *Handler) EmailInboxTest(writer http.ResponseWriter, req bunrouter.Request) error {
	var body EmailInboxTestRequest

	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&body) // empty body is valid
	}

	var cfg *jmap.Config
	if body.SessionURL != "" || body.Username != "" {
		cfg = &jmap.Config{
			Enabled:       true,
			SessionURL:    body.SessionURL,
			Username:      body.Username,
			Password:      body.Password,
			AddressDomain: body.AddressDomain,
		}
	}

	mboxes, err := h.svc.EmailInboxTest(req.Context(), cfg)
	if err != nil {
		return h.handleEmailInboxError(writer, err)
	}

	mailboxNames := []string{}
	if mboxes.Inbox != nil {
		mailboxNames = append(mailboxNames, mboxes.Inbox.Name)
	}

	if mboxes.Processed != nil {
		mailboxNames = append(mailboxNames, mboxes.Processed.Name)
	}

	if mboxes.Trash != nil {
		mailboxNames = append(mailboxNames, mboxes.Trash.Name)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{
		"ok":        true,
		"mailboxes": mailboxNames,
	})
}

// EmailInboxSync handles POST /api/v1/system/email-inbox/sync.
func (h *Handler) EmailInboxSync(writer http.ResponseWriter, req bunrouter.Request) error {
	if err := h.svc.EmailInboxSync(req.Context()); err != nil {
		return h.handleEmailInboxError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleEmailInboxError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrEmailInboxNotConfigured):
		return h.WriteError(writer, http.StatusBadRequest,
			base.ErrorCodeEmailInboxNotConfigured, "Email inbox not configured")
	case errors.Is(err, ErrEmailInboxDisabled):
		return h.WriteError(writer, http.StatusBadRequest,
			base.ErrorCodeEmailInboxDisabled, "Email inbox is disabled")
	case errors.Is(err, ErrEmailInboxNotAvailable):
		return h.WriteError(writer, http.StatusServiceUnavailable,
			base.ErrorCodeEmailInboxNotAvailable, "Email inbox manager not initialized")
	default:
		return h.WriteError(writer, http.StatusBadRequest,
			base.ErrorCodeEmailInboxTestFailed, err.Error())
	}
}
