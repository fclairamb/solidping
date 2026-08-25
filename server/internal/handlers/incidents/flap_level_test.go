package incidents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestFlapLevelRecordedOnCreateAndReopen is the acceptance test for spec
// 2026-08-24-05's incidents.flap_level: an incident must carry the flap
// level it actually opened (or reopened) at, on BOTH paths —
// createIncident and reopenIncident — not just one. Reuses the flapSetup
// helper from service_test.go (same package) so the flap-count mechanics
// under test are identical to TestReopenCountsAsFlapAndAdaptsRecovery.
func TestFlapLevelRecordedOnCreateAndReopen(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newFlapSetup(t)

	// createIncident path: the first-ever outage opens at flap level 0 (not
	// yet flapping — flap_count only escalates from the SECOND outage inside
	// the window).
	s.submit(t, models.ResultStatusDown)
	inc := s.activeIncident(t)
	r.NotNil(inc, "first failure opens an incident")
	r.Equal(0, inc.FlapLevel, "createIncident: first-ever outage opens at flap level 0")

	// Recover, then relapse inside the reopen cooldown: reopenIncident path.
	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	r.Nil(s.activeIncident(t), "success resolves the incident")
	s.reload(t)

	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusDown)
	inc = s.activeIncident(t)
	r.NotNil(inc)
	r.Equal(1, inc.FlapLevel, "reopenIncident: fast relapse reopens at the escalated flap level")

	// A second relapse escalates again — the reopen path keeps the incident's
	// FlapLevel in step with the check's own flap count on every reopen, not
	// just the first.
	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusUp)
	s.reload(t)
	s.clk.Advance(30 * time.Second)
	s.submit(t, models.ResultStatusDown)
	inc = s.activeIncident(t)
	r.NotNil(inc)
	r.Equal(2, inc.FlapLevel, "reopenIncident: a second relapse escalates FlapLevel again")
}
