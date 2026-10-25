package system

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
		testsupport.Case(ErrParameterNotFound, 404),
		testsupport.Case(ErrInvalidParameterKey, 400),
		testsupport.Case(ErrInvalidParameter, testsupport.ClientError),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
