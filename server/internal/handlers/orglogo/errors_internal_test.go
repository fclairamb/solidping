package orglogo

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

	testsupport.AssertErrorMapping(t, h.writeError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, 404), testsupport.Case(ErrUnsupportedLogoType, testsupport.ClientError), testsupport.Case(ErrEmptyLogo, testsupport.ClientError), testsupport.Case(ErrLogoTooLarge, 413),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
