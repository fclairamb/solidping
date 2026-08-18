package checks

import (
	"testing"

	models "github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestResultStatusStringIsDeclaredInOpenAPI pins resultStatusString's output
// set to the LastResult/LastResultListItem `status` enums in openapi.yaml.
// Those enums drive the generated LastResultStatus.Valid() /
// LastResultListItemStatus.Valid(), so a status the server emits but the spec
// omits makes a generated client reject a genuine response. The enums were out
// of sync for exactly that reason (warning/degraded/created/unknown were
// missing), hence the guard.
func TestResultStatusStringIsDeclaredInOpenAPI(t *testing.T) {
	t.Parallel()

	// Every stored status, plus a nil status and an out-of-range int, which
	// both fall through to the "unknown" default.
	statuses := []models.ResultStatus{
		models.ResultStatusCreated,
		models.ResultStatusRunning,
		models.ResultStatusUp,
		models.ResultStatusDown,
		models.ResultStatusTimeout,
		models.ResultStatusError,
		models.ResultStatusDegraded,
		models.ResultStatusWarning,
		models.ResultStatusAbandoned,
		models.ResultStatus(9999),
	}

	results := make([]*models.Result, 0, len(statuses)+1)

	for _, status := range statuses {
		raw := int(status)
		results = append(results, &models.Result{Status: &raw})
	}

	results = append(results, &models.Result{})

	for _, result := range results {
		got := resultStatusString(result)

		if !client.LastResultStatus(got).Valid() {
			t.Errorf("resultStatusString returned %q, which LastResult.status does not declare", got)
		}

		if !client.LastResultListItemStatus(got).Valid() {
			t.Errorf("resultStatusString returned %q, which LastResultListItem.status does not declare", got)
		}
	}
}
