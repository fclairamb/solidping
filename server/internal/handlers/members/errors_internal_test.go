package members

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

	testsupport.AssertErrorMapping(t, h.handleProvisioningError, []testsupport.ErrorCase{
		testsupport.Case(ErrContactTypeNotProvisionable, testsupport.ClientError),
		testsupport.Case(ErrInvalidContactValue, testsupport.ClientError),
		testsupport.Case(ErrContactAlreadyExists, 409),
		testsupport.Case(ErrEmailSenderNotConfigured, 400),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
