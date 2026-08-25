package watchdog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// registerLiveWorker registers a worker announcing `region` and heartbeats it,
// making it live against the REAL clock — which is the clock checks.Service's
// RegionHealth reads. The dark-region fixtures are therefore built relative to
// time.Now(), never to the pinned watchdog clock: the two live in different
// time bases and mixing them is what makes this kind of test flaky.
func (e *testEnv) registerLiveWorker(t *testing.T, identifier, region string) *models.Worker {
	t.Helper()

	worker := models.NewWorker(identifier, identifier)
	worker.Region = &region

	registered, err := e.db.RegisterOrUpdateWorker(t.Context(), worker)
	require.NoError(t, err)
	require.NoError(t, e.db.UpdateWorkerHeartbeat(t.Context(), registered.UID, []string{}, ""))

	return registered
}

// clearResults deletes every result row of a check.
//
// CreateCheck writes a one-time "Check created" marker into `results` stamped
// at creation time, which in a test is the real wall clock. Leaving it in
// place would make MAX(period_start) read "seconds ago" no matter how the
// fixture back-dates its own rows — a check created 3 months ago in
// production has no such problem, but a fixture created 3 milliseconds ago
// does.
func (e *testEnv) clearResults(t *testing.T, checkUID string) {
	t.Helper()

	_, err := e.db.DB().NewDelete().
		Model((*models.Result)(nil)).
		Where("check_uid = ?", checkUID).
		Exec(t.Context())
	require.NoError(t, err)
}

// openIncident opens an active incident on a check with a fixed short number,
// so assertions can name it ("#1") without depending on insertion order.
func (e *testEnv) openIncident(t *testing.T, checkUID string, startedAt time.Time, number int64) {
	t.Helper()

	incident := models.NewIncident(e.org.UID, checkUID, startedAt, "outage")
	incident.Number = number
	incident.State = models.IncidentStateActive
	incident.CreatedAt = startedAt
	incident.UpdatedAt = startedAt

	require.NoError(t, e.db.CreateIncident(t.Context(), incident))
}
