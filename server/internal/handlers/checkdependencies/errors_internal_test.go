package checkdependencies

import (
	"net/http"
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestErrorMapperCoversEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.handleError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, http.StatusNotFound),
		testsupport.Case(ErrCheckNotFound, http.StatusNotFound),
		testsupport.Case(ErrDependencyNotFound, http.StatusNotFound),
		testsupport.Case(ErrSelfEdge, http.StatusBadRequest),
		testsupport.Case(ErrCrossOrg, http.StatusBadRequest),
		testsupport.Case(ErrCycle, http.StatusBadRequest),
		testsupport.Case(ErrDuplicate, http.StatusConflict),
		testsupport.Case(ErrInvalidKind, http.StatusBadRequest),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
