package checkrunnow

import (
	"net/http"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestErrorMapperCoversEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.writeError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, http.StatusNotFound),
		testsupport.Case(ErrCheckNotFound, http.StatusNotFound),
		testsupport.Case(ErrNoScheduledJob, http.StatusConflict),
		testsupport.Case(&RateLimitedError{RetryAfter: 2 * time.Second}, http.StatusTooManyRequests),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
