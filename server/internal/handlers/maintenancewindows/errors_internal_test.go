package maintenancewindows

import (
	"net/http"
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestCreateErrorMapperCoversEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.handleCreateError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, 404),
		testsupport.Case(ErrTitleRequired, testsupport.ClientError),
		testsupport.Case(ErrInvalidTimeRange, testsupport.ClientError),
		testsupport.Case(ErrInvalidRecurrence, testsupport.ClientError),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}

func TestUpdateErrorMapperCoversEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.handleUpdateError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, 404), testsupport.Case(ErrMaintenanceWindowNotFound, 404),
		testsupport.Case(ErrInvalidTimeRange, testsupport.ClientError),
		testsupport.Case(ErrInvalidRecurrence, testsupport.ClientError),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
