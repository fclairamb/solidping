package incidents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// An incident the check state machine closes on its own (the check recovered)
// must carry resolution_type = "auto": the incident insights page counts its
// "auto-recovered" bucket from that column, and it used to stay NULL.
func TestCheckRecoveryResolvesWithAutoType(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	s := newHoldSetup(t, dbSvc, "auto-resolve")
	check := s.check(t, "api", func(c *models.Check) { c.ConfirmationPeriodSeconds = 0 })

	s.at(0)
	check = s.fail(t, check)
	inc := s.activeIncident(t, check)

	s.at(10 * time.Second)
	s.succeed(t, check)

	got := s.reload(t, inc)
	r.Equal(models.IncidentStateResolved, got.State)
	r.NotNil(got.ResolutionType, "an auto-resolved incident must record how it closed")
	r.Equal(models.ResolutionTypeAuto, *got.ResolutionType)
}
