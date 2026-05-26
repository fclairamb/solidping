package usernotifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// EmailSender is the minimal interface needed to send a test email.
type EmailSender interface {
	SendTestEmail(ctx context.Context, recipient string) error
}

// SlackDMSender is the minimal interface needed to send a Slack DM test.
type SlackDMSender interface {
	SendDMTest(ctx context.Context, ch *models.Channel, slackUserID string) error
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
	default:
		return h.WriteInternalError(writer, err)
	}
}

func userFromContext(req bunrouter.Request) (*models.User, bool) {
	v := req.Context().Value(base.ContextKeyUser)
	if v == nil {
		return nil, false
	}

	u, ok := v.(*models.User)

	return u, ok
}

// ListRoutes handles GET /api/v1/orgs/:org/users/me/notification-routes.
func (h *Handler) ListRoutes(writer http.ResponseWriter, req bunrouter.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	resp, err := h.svc.ListRoutes(req.Context(), req.Param("org"), user)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// CreateContact handles POST /api/v1/orgs/:org/users/me/notification-contacts.
func (h *Handler) CreateContact(writer http.ResponseWriter, req bunrouter.Request) error {
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

	route, err := h.svc.CreateContact(req.Context(), req.Param("org"), user, body)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, route)
}

// PatchRoute handles PATCH /api/v1/orgs/:org/users/me/notification-routes/:routeUid.
func (h *Handler) PatchRoute(writer http.ResponseWriter, req bunrouter.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	var body PatchRouteRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON")
	}

	routeUID := req.Param("routeUid")

	route, err := h.svc.PatchRoute(req.Context(), req.Param("org"), user, routeUID, body)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, route)
}

// DeleteContact handles DELETE /api/v1/orgs/:org/users/me/notification-contacts/:contactUid.
func (h *Handler) DeleteContact(writer http.ResponseWriter, req bunrouter.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	contactUID := req.Param("contactUid")

	if err := h.svc.DeleteContact(req.Context(), req.Param("org"), user, contactUID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// TestRoute handles POST /api/v1/orgs/:org/users/me/notification-routes/:routeUid/test.
func (h *Handler) TestRoute(writer http.ResponseWriter, req bunrouter.Request) error {
	user, ok := userFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Not authenticated")
	}

	routeUID := req.Param("routeUid")

	err := h.svc.SendTestNotification(
		req.Context(), req.Param("org"), user, routeUID, h.emailSender, h.slackSender, h.webPushOptions,
	)
	if err != nil {
		// 422 for "provider not configured" style errors.
		errMsg := "test failed: " + err.Error()
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, errMsg)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}
