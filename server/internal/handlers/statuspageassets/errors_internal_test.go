package statuspageassets

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

	testsupport.AssertErrorMapping(t, func(w http.ResponseWriter, r *http.Request, err error) error {
		return h.writeError(w, r, err, KindLogo)
	}, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, 404),
		testsupport.Case(ErrStatusPageNotFound, 404),
		testsupport.Case(ErrUnsupportedAssetType, testsupport.ClientError),
		testsupport.Case(ErrUnknownKind, testsupport.ClientError),
		testsupport.Case(ErrEmptyAsset, testsupport.ClientError),
		testsupport.Case(ErrAssetTooLarge, 413),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
