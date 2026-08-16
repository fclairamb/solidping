package usernotifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// EmailSender is the minimal interface needed to send a test email.
type EmailSender interface {
	SendTestEmail(ctx context.Context, recipient string) error
}

// SlackDMSender is the minimal interface needed to send a Slack DM test.
type SlackDMSender interface {
	SendDMTest(ctx context.Context, ch *models.Integration, slackUserID string) error
}

// Handler exposes the user notification routes REST API.
type Handler struct {
	base.HandlerBase
	svc            *Service
	emailSender    EmailSender
	slackSender    SlackDMSender
	webPushOptions webpush.Options
}

// NewHandler builds a handler.
func NewHandler(
	svc *Service, cfg *config.Config,
	email EmailSender, slack SlackDMSender, wpOpts webpush.Options,
) *Handler {
	return &Handler{
		HandlerBase:    base.NewHandlerBase(cfg),
		svc:            svc,
		emailSender:    email,
		slackSender:    slack,
		webPushOptions: wpOpts,
	}
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrgNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrRouteNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Notification route not found")
	case errors.Is(err, ErrContactNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Notification contact not found")
	case errors.Is(err, ErrInvalidWhatsAppNumber):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	case errors.Is(err, ErrTelegramContactNotDirect):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	case errors.Is(err, ErrTelegramNotEnabled):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, err)
	}
}

func userFromContext(req *http.Request) (*models.User, bool) {
	v := req.Context().Value(base.ContextKeyUser)
	if v == nil {
		return nil, false
	}

	u, ok := v.(*models.User)

	return u, ok
}

// ListRoutes handles GET /api/v1/orgs/:org/users/me/notification-routes.
func (h *Handler) ListRoutes(writer http.ResponseWriter, req *http.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	resp, err := h.svc.ListRoutes(req.Context(), httpx.Param(req, "org"), user)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// CreateContact handles POST /api/v1/orgs/:org/users/me/notification-contacts.
func (h *Handler) CreateContact(writer http.ResponseWriter, req *http.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	var body CreateContactRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON")
	}

	if body.Type == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "type is required")
	}

	if body.Value == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "value is required")
	}

	route, err := h.svc.CreateContact(req.Context(), httpx.Param(req, "org"), user, body)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, route)
}

// CreateTelegramLink handles POST /api/v1/orgs/:org/users/me/telegram/link.
//
// It mints a single-use connect token and returns the t.me deep link carrying
// it. Nothing is created here: the contact only comes into existence when the
// user actually presses Start in Telegram and our webhook sees the token come
// back from a real chat.
func (h *Handler) CreateTelegramLink(writer http.ResponseWriter, req *http.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	resp, err := h.svc.CreateTelegramLink(req.Context(), httpx.Param(req, "org"), user)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// PatchRoute handles PATCH /api/v1/orgs/:org/users/me/notification-routes/:routeUid.
func (h *Handler) PatchRoute(writer http.ResponseWriter, req *http.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	var body PatchRouteRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON")
	}

	routeUID := httpx.Param(req, "routeUid")

	route, err := h.svc.PatchRoute(req.Context(), httpx.Param(req, "org"), user, routeUID, body)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, route)
}

// DeleteContact handles DELETE /api/v1/orgs/:org/users/me/notification-contacts/:contactUid.
func (h *Handler) DeleteContact(writer http.ResponseWriter, req *http.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	contactUID := httpx.Param(req, "contactUid")

	if err := h.svc.DeleteContact(req.Context(), httpx.Param(req, "org"), user, contactUID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// VerifyContact handles POST /api/v1/orgs/:org/users/me/notification-contacts/:contactUid/verify.
// It issues and sends a fresh verification code for a phone contact.
func (h *Handler) VerifyContact(writer http.ResponseWriter, req *http.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	contactUID := httpx.Param(req, "contactUid")

	if err := h.svc.VerifyContact(req.Context(), httpx.Param(req, "org"), user, contactUID); err != nil {
		return h.handleVerifyError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// ConfirmVerify handles POST /api/v1/orgs/:org/users/me/notification-contacts/:contactUid/verify/confirm.
func (h *Handler) ConfirmVerify(writer http.ResponseWriter, req *http.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON")
	}

	if body.Code == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "code is required")
	}

	contactUID := httpx.Param(req, "contactUid")

	if err := h.svc.ConfirmVerify(req.Context(), httpx.Param(req, "org"), user, contactUID, body.Code); err != nil {
		return h.handleVerifyError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// handleVerifyError maps verification-flow errors to HTTP responses.
func (h *Handler) handleVerifyError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrContactNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Notification contact not found")
	case errors.Is(err, ErrOrgNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrNoProvider), errors.Is(err, ErrNoWhatsAppProvider):
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, err.Error())
	case errors.Is(err, ErrSMSDestinationNotAllowed):
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError,
			ErrSMSDestinationNotAllowed.Error())
	case errors.Is(err, ErrResendTooSoon), errors.Is(err, ErrTooManyAttempts),
		errors.Is(err, ErrSMSInstanceCapReached):
		return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeValidationError,
			http429Message(err))
	case errors.Is(err, ErrNotAPhoneContact),
		errors.Is(err, ErrAlreadyVerified),
		errors.Is(err, ErrNoPendingCode),
		errors.Is(err, ErrCodeExpired),
		errors.Is(err, ErrCodeMismatch):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, err)
	}
}

// TestRoute handles POST /api/v1/orgs/:org/users/me/notification-routes/:routeUid/test.
func (h *Handler) TestRoute(writer http.ResponseWriter, req *http.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	routeUID := httpx.Param(req, "routeUid")

	err := h.svc.SendTestNotification(
		req.Context(), httpx.Param(req, "org"), user, routeUID, h.emailSender, h.slackSender, h.webPushOptions,
	)
	if err != nil {
		// An instance-spend guard refusal is a rate limit, not a misconfigured
		// provider, and its wrapped cause carries operator-side detail (the cap
		// figure) that a member of one organization has no business reading.
		if errors.Is(err, ErrSMSInstanceCapReached) {
			return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeValidationError,
				"test failed: "+ErrSMSInstanceCapReached.Error())
		}

		if errors.Is(err, ErrSMSDestinationNotAllowed) {
			return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError,
				"test failed: "+ErrSMSDestinationNotAllowed.Error())
		}

		// 422 for "provider not configured" style errors.
		errMsg := "test failed: " + err.Error()

		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, errMsg)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// http429Message keeps a rate-limit response to the sentinel's own wording:
// the wrapped cause carries operator-side detail (a country code, a cap
// figure) that a member of one organization has no business reading.
func http429Message(err error) string {
	switch {
	case errors.Is(err, ErrSMSInstanceCapReached):
		return ErrSMSInstanceCapReached.Error()
	case errors.Is(err, ErrResendTooSoon):
		return ErrResendTooSoon.Error()
	case errors.Is(err, ErrTooManyAttempts):
		return ErrTooManyAttempts.Error()
	default:
		return err.Error()
	}
}
