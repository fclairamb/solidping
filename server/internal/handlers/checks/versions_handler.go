package checks

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const (
	fieldVersion       = "version"
	msgPositiveInteger = "must be a positive integer"
)

// Check version history endpoints (spec 2026-10-03-06), under
// /api/v1/orgs/:org/checks/:checkUid/versions.

func (h *Handler) versionParam(writer http.ResponseWriter, req *http.Request) (int, bool, error) {
	version, err := strconv.Atoi(httpx.Param(req, fieldVersion))
	if err != nil || version < 1 {
		return 0, false, h.WriteValidationError(writer, "Invalid version", []base.ValidationErrorField{
			{Name: fieldVersion, Message: msgPositiveInteger},
		})
	}

	return version, true, nil
}

// ListCheckVersions handles GET .../versions.
func (h *Handler) ListCheckVersions(writer http.ResponseWriter, req *http.Request) error {
	limit := 0

	if raw := req.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return h.WriteValidationError(writer, "Invalid limit", []base.ValidationErrorField{
				{Name: "limit", Message: msgPositiveInteger},
			})
		}

		limit = parsed
	}

	result, err := h.svc.ListCheckVersions(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"), limit)
	if err != nil {
		return h.handleVersionError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// GetCheckVersion handles GET .../versions/:version.
func (h *Handler) GetCheckVersion(writer http.ResponseWriter, req *http.Request) error {
	version, ok, err := h.versionParam(writer, req)
	if !ok {
		return err
	}

	result, err := h.svc.GetCheckVersion(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"), version)
	if err != nil {
		return h.handleVersionError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// DiffCheckVersion handles GET .../versions/:version/diff?against=N.
func (h *Handler) DiffCheckVersion(writer http.ResponseWriter, req *http.Request) error {
	version, ok, err := h.versionParam(writer, req)
	if !ok {
		return err
	}

	var against *int

	if raw := req.URL.Query().Get("against"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 {
			return h.WriteValidationError(writer, "Invalid against", []base.ValidationErrorField{
				{Name: "against", Message: msgPositiveInteger},
			})
		}

		against = &parsed
	}

	result, err := h.svc.DiffCheckVersion(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"), version, against)
	if err != nil {
		return h.handleVersionError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// RestoreCheckVersion handles POST .../versions/:version/restore.
func (h *Handler) RestoreCheckVersion(writer http.ResponseWriter, req *http.Request) error {
	version, ok, err := h.versionParam(writer, req)
	if !ok {
		return err
	}

	result, err := h.svc.RestoreCheckVersion(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"), version)
	if err != nil {
		return h.handleVersionError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// ApproveCheckVersion handles POST .../versions/:version/approve.
func (h *Handler) ApproveCheckVersion(writer http.ResponseWriter, req *http.Request) error {
	version, ok, err := h.versionParam(writer, req)
	if !ok {
		return err
	}

	result, err := h.svc.ApproveCheckVersion(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"), version)
	if err != nil {
		return h.handleVersionError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// RejectCheckVersion handles POST .../versions/:version/reject.
func (h *Handler) RejectCheckVersion(writer http.ResponseWriter, req *http.Request) error {
	version, ok, err := h.versionParam(writer, req)
	if !ok {
		return err
	}

	result, err := h.svc.RejectCheckVersion(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"), version)
	if err != nil {
		return h.handleVersionError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

func (h *Handler) handleVersionError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrCheckVersionNotFound):
		return h.WriteErrorErr(writer, req, http.StatusNotFound, base.ErrorCodeNotFound, "Check version not found", err)
	case errors.Is(err, ErrCheckVersionNotApplied),
		errors.Is(err, ErrCheckVersionNotProposed),
		errors.Is(err, ErrCheckVersionStale):
		return h.WriteErrorErr(writer, req, http.StatusConflict, base.ErrorCodeConflict, err.Error(), err)
	default:
		return h.handleUpdateError(writer, req, err)
	}
}
