package incidents_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// TestServiceReportsAClosedDatabase: every service method that touches the
// database answers a lost connection with an error instead of panicking or
// reporting success.
func TestServiceReportsAClosedDatabase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	r.NoError(dbSvc.DB().Close())

	called, failed := testsupport.CallErrorMethods(t, svc)
	r.Greater(called, 5)
	r.Greater(failed, called/2, "methods that surfaced the closed database: %d of %d", failed, called)
}
