package notifications

import "github.com/fclairamb/solidping/server/internal/db/models"

// GetFailureReasonForTest exposes the unexported getFailureReason so external
// (notifications_test) tests can verify it end to end against a real
// incidents.Service-created incident. It has to live in this in-package
// export_test.go (rather than the external test file itself) because
// internal/handlers/incidents imports internal/jobs/jobtypes, which imports
// this package — an external test file that imports incidents directly would
// be fine, but this package's own in-package tests importing incidents would
// not (import cycle).
func GetFailureReasonForTest(incident *models.Incident) string {
	return getFailureReason(incident)
}
