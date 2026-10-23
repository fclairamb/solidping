package notifications

import "github.com/fclairamb/solidping/server/internal/db/models"

// Wording for an incident closed because its check was deleted (spec
// 2026-10-08-02). Like a degraded incident closed by turning detection off, it
// must not say the check recovered: nobody knows, the check is simply gone.
const (
	checkDeletedTag      = "[CHECK DELETED] "
	recoveredTag         = "[RECOVERED] "
	checkDeletedSentence = "The check was deleted, so this incident was closed. It did not necessarily recover."

	// Same idea for a check disabled by hand with resolveOpenIncidents (spec
	// 2026-10-23-01).
	checkDisabledTag      = "[CHECK DISABLED] "
	checkDisabledSentence = "The check was disabled, so this incident was closed. It did not necessarily recover."
)

// ResolvedByCheckDisable reports whether the incident was closed because its
// check was disabled.
func ResolvedByCheckDisable(incident *models.Incident) bool {
	return incident != nil && incident.ResolutionType != nil &&
		*incident.ResolutionType == models.ResolutionTypeCheckDisabled
}

// closedWithoutRecovery is true when the incident was closed because its check
// was deleted or disabled: the check did not necessarily recover.
func closedWithoutRecovery(incident *models.Incident) bool {
	return ResolvedByCheckDeletion(incident) || ResolvedByCheckDisable(incident)
}

// closedSentence is the one-line reason for a closedWithoutRecovery incident.
func closedSentence(incident *models.Incident) string {
	if ResolvedByCheckDisable(incident) {
		return checkDisabledSentence
	}

	return checkDeletedSentence
}

// closedTag is the title prefix for a closedWithoutRecovery incident.
func closedTag(incident *models.Incident) string {
	if ResolvedByCheckDisable(incident) {
		return checkDisabledTag
	}

	return checkDeletedTag
}

// ResolvedByCheckDeletion reports whether the incident was closed because its
// check was deleted.
func ResolvedByCheckDeletion(incident *models.Incident) bool {
	return incident != nil && incident.ResolutionType != nil &&
		*incident.ResolutionType == models.ResolutionTypeCheckDeleted
}

// resolvedTag is the title prefix of a resolved notification: "[RECOVERED] ",
// or "[CHECK DELETED] " when the check was deleted rather than recovered.
func resolvedTag(incident *models.Incident) string {
	if closedWithoutRecovery(incident) {
		return closedTag(incident)
	}

	return recoveredTag
}

// closedKind ("deleted" or "disabled") and closedPill ("Deleted" or
// "Disabled") pick the email i18n keys of a closedWithoutRecovery incident.
func closedKind(incident *models.Incident) string {
	if ResolvedByCheckDisable(incident) {
		return "disabled"
	}

	return "deleted"
}

func closedPill(incident *models.Incident) string {
	if ResolvedByCheckDisable(incident) {
		return "Disabled"
	}

	return "Deleted"
}
