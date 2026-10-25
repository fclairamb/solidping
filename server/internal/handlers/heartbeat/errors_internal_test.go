package heartbeat

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
		testsupport.Case(ErrOrganizationNotFound, 404),
		testsupport.Case(ErrCheckNotFound, 404),
		testsupport.Case(ErrNotHeartbeatCheck, 400),
		testsupport.Case(ErrMissingToken, 401),
		testsupport.Case(ErrInvalidToken, 401),
		testsupport.Case(ErrInvalidStatus, 400),
		testsupport.Case(ErrBodyTooLarge, 400),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
