package uistate

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// Handler serves GET/PUT/DELETE /api/v1/me/ui-state/:key.
//
// There is no `:org` in the path on purpose: the entry belongs to the
// authenticated user, and the organization it is about is part of the key.
type Handler struct {
	base.HandlerBase

	svc *Service
}

// NewHandler constructs a Handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

// Response is the body of a successful GET/PUT.
type Response struct {
	Value models.JSONMap `json:"value"`
}

// Get returns the caller's stored value for the key, or 404.
func (h *Handler) Get(writer http.ResponseWriter, req *http.Request) error {
	userUID, ok := h.userUID(writer, req)
	if !ok {
		return nil
	}

	value, err := h.svc.Get(req.Context(), userUID, httpx.Param(req, "key"))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, Response{Value: value})
}

// Put stores a JSON object for the caller under the key.
func (h *Handler) Put(writer http.ResponseWriter, req *http.Request) error {
	userUID, ok := h.userUID(writer, req)
	if !ok {
		return nil
	}

	// Read through a limited reader rather than trusting Content-Length: a
	// chunked request carries none, so a size check on the header alone would
	// be trivially bypassed. One extra byte is allowed through so an
	// exactly-at-the-cap body is accepted and an over-cap one is detected.
	body, err := io.ReadAll(io.LimitReader(req.Body, MaxValueBytes+1))
	if err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Could not read request body")
	}

	if len(body) > MaxValueBytes {
		return h.handleError(writer, req, ErrValueTooLarge)
	}

	var value models.JSONMap
	if unmarshalErr := json.Unmarshal(body, &value); unmarshalErr != nil {
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Body must be a JSON object",
		)
	}

	if err := h.svc.Set(req.Context(), userUID, httpx.Param(req, "key"), value); err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, Response{Value: value})
}

// Delete removes the caller's entry. Idempotent: deleting an absent entry
// still answers 204.
func (h *Handler) Delete(writer http.ResponseWriter, req *http.Request) error {
	userUID, ok := h.userUID(writer, req)
	if !ok {
		return nil
	}

	if err := h.svc.Delete(req.Context(), userUID, httpx.Param(req, "key")); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// userUID pulls the authenticated user out of the request context, writing a
// 401 and reporting false when there is none. RequireAuth guards every route
// below, so this is a belt-and-braces check.
func (h *Handler) userUID(writer http.ResponseWriter, req *http.Request) (string, bool) {
	claims, ok := middleware.GetClaimsFromContext(req.Context())
	if !ok || claims.UserUID == "" {
		_ = h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")

		return "", false
	}

	return claims.UserUID, true
}

// handleError maps service errors onto HTTP responses.
func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrInvalidKey):
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Unsupported ui-state key",
		)
	case errors.Is(err, ErrValueTooLarge):
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Value is too large",
		)
	case errors.Is(err, ErrOrgNotFound):
		return h.WriteError(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found",
		)
	case errors.Is(err, ErrNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "No value stored")
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
