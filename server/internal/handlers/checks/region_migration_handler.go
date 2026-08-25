package checks

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

// regionMigrationFromField / regionMigrationToField are the JSON field names
// reported in validation errors.
const (
	regionMigrationFromField = "from"
	regionMigrationToField   = "to"
)

// MigrateRegion handles POST /api/v1/system/regions/migrate.
//
// Super-admin only and SERVER-scope, not org-scope: a region rename is a
// deployment-level operation, and the jobs it strands are spread across every
// organization. Recovering from one must not need per-org admin credentials
// (that limitation is exactly why the live 2026-08-24 incident left ~125 jobs
// stranded after the manual remediation). Registered on the existing
// systemActions group, same as GET /system/agents.
func (h *Handler) MigrateRegion(writer http.ResponseWriter, req *http.Request) error {
	var body RegionMigrationRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	actorUID := ""
	if user, ok := mw.GetUserFromContext(req.Context()); ok && user != nil {
		actorUID = user.UID
	}

	report, err := h.svc.MigrateRegion(req.Context(), body, actorUID)
	if err != nil {
		return h.writeRegionMigrationError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, report)
}

// writeRegionMigrationError maps the migration's domain errors onto the
// repository's error shape. Every precondition failure is a 422
// VALIDATION_ERROR naming the offending field; anything else is internal.
func (h *Handler) writeRegionMigrationError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrRegionMigrationMissingSlug):
		return h.WriteValidationError(writer, err.Error(), []base.ValidationErrorField{
			{Name: regionMigrationFromField, Message: "A source region slug is required"},
			{Name: regionMigrationToField, Message: "A target region slug is required"},
		})
	case errors.Is(err, ErrRegionMigrationSameSlug):
		return h.WriteValidationError(writer, err.Error(), []base.ValidationErrorField{
			{Name: regionMigrationToField, Message: "Must differ from 'from'"},
		})
	case errors.Is(err, ErrRegionMigrationPrivateToCloud):
		return h.WriteValidationError(writer, err.Error(), []base.ValidationErrorField{
			{Name: regionMigrationToField, Message: "A private region can only migrate to another private region"},
		})
	case errors.Is(err, ErrRegionMigrationTargetUnknown):
		// err.Error() already names the known regions — the operator needs to
		// see the list to spot the typo.
		return h.WriteValidationError(writer, err.Error(), []base.ValidationErrorField{
			{Name: regionMigrationToField, Message: err.Error()},
		})
	default:
		return h.WriteInternalError(writer, req, err)
	}
}
