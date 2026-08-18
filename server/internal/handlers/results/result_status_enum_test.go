package results

import (
	"testing"

	models "github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestStatusIntToStringIsDeclaredInOpenAPI pins statusIntToString's output set
// to the OrgResult `status` enum in openapi.yaml, which drives the generated
// OrgResultStatus.Valid(). A status the server emits but the spec omits makes
// a generated client reject a genuine response; the enum was missing
// created/running/warning/degraded for exactly that reason.
func TestStatusIntToStringIsDeclaredInOpenAPI(t *testing.T) {
	t.Parallel()

	// Every stored status, plus an out-of-range int and a nil status, which
	// both resolve to "unknown".
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

	service := &Service{}

	inputs := make([]*int, 0, len(statuses)+1)

	for _, status := range statuses {
		raw := int(status)
		inputs = append(inputs, &raw)
	}

	inputs = append(inputs, nil)

	for _, input := range inputs {
		got := service.statusIntToString(input)

		if !client.OrgResultStatus(got).Valid() {
			t.Errorf("statusIntToString returned %q, which OrgResult.status does not declare", got)
		}
	}
}
