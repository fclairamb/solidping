package checks

import "net/http"

// RegionHealth handles GET /api/v1/system/regions/health.
//
// Super-admin only, read-only companion of POST /system/regions/migrate (spec
// 2026-08-24-08): one row per region slug seen anywhere, so an operator can
// see exactly which `from` slugs a migration needs to target before ever
// running one. Registered on the same systemActions group, so RequireAuth +
// RequireSuperAdmin already apply.
func (h *Handler) RegionHealth(writer http.ResponseWriter, req *http.Request) error {
	report, err := h.svc.RegionHealth(req.Context())
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, report)
}
