package testapi

import (
	"fmt"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ResetChecksResponse represents the response from deleting all checks.
type ResetChecksResponse struct {
	Deleted int `json:"deleted"`
	Failed  int `json:"failed"`
}

// DeleteAllChecks deletes all checks for an organization.
// DELETE /api/v1/test/checks/all.
func (h *Handler) DeleteAllChecks(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.URL.Query().Get("org")
	if orgSlug == "" {
		orgSlug = defaultOrg
	}

	org, err := h.dbService.GetOrganizationBySlug(req.Context(), orgSlug)
	if err != nil {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR",
			fmt.Sprintf("Organization '%s' not found", orgSlug))
	}

	checks, _, err := h.dbService.ListChecks(req.Context(), org.UID, &models.ListChecksFilter{})
	if err != nil {
		return h.writeInternalError(writer, err)
	}

	resp := ResetChecksResponse{}

	for _, check := range checks {
		// Delete associated check_jobs first
		jobs, _ := h.dbService.ListCheckJobsByCheckUID(req.Context(), check.UID)
		for _, job := range jobs {
			_ = h.dbService.DeleteCheckJob(req.Context(), job.UID)
		}

		if deleteErr := h.dbService.DeleteCheck(req.Context(), check.UID); deleteErr != nil {
			resp.Failed++

			continue
		}

		resp.Deleted++
	}

	return h.writeJSON(writer, http.StatusOK, resp)
}
