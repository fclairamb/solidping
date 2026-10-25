package severities

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

	testsupport.AssertErrorMapping(t, h.handleErr, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, 404),
		testsupport.Case(ErrSeverityNotFound, 404),
		testsupport.Case(ErrSlugConflict, testsupport.ClientError),
		testsupport.Case(ErrInvalidSlug, testsupport.ClientError),
		testsupport.Case(ErrInvalidChannel, testsupport.ClientError),
		testsupport.Case(ErrCannotDeleteDefault, 409),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
