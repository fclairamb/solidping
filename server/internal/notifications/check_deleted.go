package notifications

import "github.com/fclairamb/solidping/server/internal/db/models"

// Wording for an incident closed because its check was deleted (spec
// 2026-10-08-02). Like a degraded incident closed by turning detection off, it
// must not say the check recovered: nobody knows, the check is simply gone.
const (
	checkDeletedTag      = "[CHECK DELETED] "
	recoveredTag         = "[RECOVERED] "
	checkDeletedSentence = "The check was deleted, so this incident was closed. It did not necessarily recover."
)

// ResolvedByCheckDeletion reports whether the incident was closed because its
// check was deleted.
func ResolvedByCheckDeletion(incident *models.Incident) bool {
	return incident != nil && incident.ResolutionType != nil &&
		*incident.ResolutionType == models.ResolutionTypeCheckDeleted
}

// resolvedTag is the title prefix of a resolved notification: "[RECOVERED] ",
// or "[CHECK DELETED] " when the check was deleted rather than recovered.
func resolvedTag(incident *models.Incident) string {
	if ResolvedByCheckDeletion(incident) {
		return checkDeletedTag
	}

	return recoveredTag
}
