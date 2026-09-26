package checks

import (
	"strings"
	"testing"

	models "github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestCheckStatusStringIsDeclaredInOpenAPI pins every CheckStatus wire name —
// stale included (spec 2026-09-25-02) — to the Check.status,
// Check.lastStatusChange.status and CheckGroup.status enums, so a generated
// client never rejects a genuine response.
func TestCheckStatusStringIsDeclaredInOpenAPI(t *testing.T) {
	t.Parallel()

	statuses := []models.CheckStatus{
		models.CheckStatusCreated,
		models.CheckStatusUp,
		models.CheckStatusDown,
		models.CheckStatusValidating,
		models.CheckStatusDegraded,
		models.CheckStatusWarning,
		models.CheckStatusStale,
		models.CheckStatus(9999),
	}

	for _, status := range statuses {
		wire := status.String()

		if !client.CheckStatus(wire).Valid() {
			t.Errorf("Check.status does not declare %q", wire)
		}

		if !client.CheckLastStatusChangeStatus(strings.ToUpper(wire)).Valid() {
			t.Errorf("Check.lastStatusChange.status does not declare %q", strings.ToUpper(wire))
		}
	}

	// Every status the group rollup can produce.
	for _, status := range []models.CheckStatus{
		models.CheckStatusCreated, models.CheckStatusUp, models.CheckStatusDown,
		models.CheckStatusValidating, models.CheckStatusDegraded, models.CheckStatusWarning,
		models.CheckStatusStale,
	} {
		if !client.CheckGroupStatus(status.String()).Valid() {
			t.Errorf("CheckGroup.status does not declare %q", status.String())
		}
	}
}
