package checkgroups

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

	testsupport.AssertErrorMapping(t, h.handleUpdateError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, 404),
		testsupport.Case(ErrCheckGroupNotFound, 404),
		testsupport.Case(ErrSlugConflict, testsupport.ClientError),
		testsupport.Case(ErrInvalidSlugFormat, testsupport.ClientError),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
