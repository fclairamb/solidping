package usernotifications

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

	testsupport.AssertErrorMapping(t, h.handleVerifyError, []testsupport.ErrorCase{
		testsupport.Case(ErrContactNotFound, 404),
		testsupport.Case(ErrOrgNotFound, 404),
		testsupport.Case(ErrNoProvider, 422),
		testsupport.Case(ErrNoWhatsAppProvider, 422),
		testsupport.Case(ErrSMSDestinationNotAllowed, 422),
		testsupport.Case(ErrResendTooSoon, 429),
		testsupport.Case(ErrTooManyAttempts, 429),
		testsupport.Case(ErrSMSInstanceCapReached, 429),
		testsupport.Case(ErrNotAPhoneContact, 400),
		testsupport.Case(ErrAlreadyVerified, 400),
		testsupport.Case(ErrNoPendingCode, 400),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
