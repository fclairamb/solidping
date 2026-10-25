package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// RunClosed closes the service's connection pool, then calls every
// error-returning method with placeholder arguments. A database that has gone
// away must surface as an error from the method, never as a panic or a silent
// success, on every dialect. svc must be freshly initialized; it is unusable
// afterwards.
func RunClosed(t *testing.T, svc db.Service) {
	t.Helper()

	must := require.New(t)

	must.NoError(svc.DB().Close())

	called, failed := testsupport.CallErrorMethods(t, svc, "Close", "DB")

	// Most methods query the database before anything else, so the vast
	// majority must report the closed pool.
	must.Greater(called, 100)
	must.Greater(failed, called/2, "methods that surfaced the closed database: %d of %d", failed, called)
}
