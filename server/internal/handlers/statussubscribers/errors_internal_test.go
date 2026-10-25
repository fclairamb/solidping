package statussubscribers

import (
	"net/http"
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestErrorMapperCoversEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.handleError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, http.StatusNotFound),
		testsupport.Case(ErrStatusPageNotFound, http.StatusNotFound),
		testsupport.Case(statuspagelock.ErrLocked, http.StatusUnauthorized),
		testsupport.Case(ErrSubscriberNotFound, http.StatusNotFound),
		testsupport.Case(ErrIncidentNotFound, http.StatusNotFound),
		testsupport.Case(ErrInvalidEmail, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidScope, http.StatusUnprocessableEntity),
		testsupport.Case(ErrIncidentRequired, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidChannel, http.StatusUnprocessableEntity),
		testsupport.Case(ErrPublicChannelNotAllowed, http.StatusUnprocessableEntity),
		testsupport.Case(ErrEndpointRequired, http.StatusUnprocessableEntity),
		testsupport.Case(ErrSlackWebhookExpected, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidEndpointURL, http.StatusUnprocessableEntity),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
