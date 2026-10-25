package results

import (
	"net/http"
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestErrorMapperCoversEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.handleListError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, 404), testsupport.Case(ErrInvalidCursor, 400), testsupport.Case(regions.ErrForeignPrivateRegion, 400),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
