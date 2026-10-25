package checks

import (
	"net/http"
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestRegionMigrationErrorMapper(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.writeRegionMigrationError, []testsupport.ErrorCase{
		testsupport.Case(ErrRegionMigrationMissingSlug, testsupport.ClientError), testsupport.Case(ErrRegionMigrationSameSlug, testsupport.ClientError),
		testsupport.Case(ErrRegionMigrationPrivateToCloud, testsupport.ClientError), testsupport.Case(ErrRegionMigrationTargetUnknown, testsupport.ClientError),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
