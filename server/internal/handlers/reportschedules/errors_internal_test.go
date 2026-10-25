package reportschedules

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
		testsupport.Case(ErrReportScheduleNotFound, http.StatusNotFound),
		testsupport.Case(ErrNameRequired, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidFrequency, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidTimezone, http.StatusUnprocessableEntity),
		testsupport.Case(ErrTooManyRecipients, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidRecipient, http.StatusUnprocessableEntity),
		testsupport.Case(ErrNoTestRecipient, http.StatusUnprocessableEntity),
		testsupport.Case(ErrRecipientSuppressed, http.StatusConflict),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
