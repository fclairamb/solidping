package agents

import (
	"net/http"
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestErrorMapperCoversEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.writeServiceError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrgNotFound, http.StatusNotFound),
		testsupport.Case(ErrRegionNotFound, http.StatusNotFound),
		testsupport.Case(ErrAgentNotFound, http.StatusNotFound),
		testsupport.Case(ErrRegionExists, http.StatusConflict),
		testsupport.Case(ErrRegionHasAgents, http.StatusConflict),
		testsupport.Case(regions.ErrInvalidPrivateRegionSlug, http.StatusBadRequest),
		testsupport.Case(ErrInvalidExpiresIn, http.StatusBadRequest),
		testsupport.Case(entcore.ErrEntitlementExceeded, http.StatusInternalServerError),
		testsupport.Case(&entcore.QuotaError{}, http.StatusPaymentRequired),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
