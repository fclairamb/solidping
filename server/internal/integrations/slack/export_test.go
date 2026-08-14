package slack

import "github.com/fclairamb/solidping/server/internal/db/models"

// GetFailureReasonForTest exposes the unexported getFailureReason so external
// (slack_test) tests can verify it end to end against a real
// incidents.Service-created incident. It has to live in this in-package
// export_test.go (rather than the external test file itself) because
// internal/handlers/incidents imports internal/jobs/jobtypes, which imports
// this package (slackclient) — an external test file that imports incidents
// directly would be fine, but this package's own in-package tests importing
// incidents would not be (import cycle).
func GetFailureReasonForTest(incident *models.Incident) string {
	return getFailureReason(incident)
}
