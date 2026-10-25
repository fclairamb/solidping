package statusupdates

import (
	"net/http"
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestHandleErrorMapsEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.handleError, []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, http.StatusNotFound),
		testsupport.Case(ErrStatusUpdateNotFound, http.StatusNotFound),
		testsupport.Case(ErrStatusPageNotFound, http.StatusNotFound),
		testsupport.Case(ErrStatusPageForbidden, http.StatusForbidden),
		testsupport.Case(ErrSectionNotFound, http.StatusNotFound),
		testsupport.Case(ErrCheckNotFound, http.StatusNotFound),
		testsupport.Case(ErrIncidentNotFound, http.StatusNotFound),
		testsupport.Case(ErrInvalidKind, http.StatusUnprocessableEntity),
		testsupport.Case(ErrTitleRequired, http.StatusUnprocessableEntity),
		testsupport.Case(ErrTitleTooLong, http.StatusUnprocessableEntity),
		testsupport.Case(ErrBodyRequired, http.StatusUnprocessableEntity),
		testsupport.Case(ErrBodyTooLong, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidLinkURL, http.StatusUnprocessableEntity),
		testsupport.Case(ErrCheckUIDMismatch, http.StatusUnprocessableEntity),
		testsupport.Case(ErrSectionUIDRequired, http.StatusUnprocessableEntity),
		testsupport.Case(ErrCheckUIDRequired, http.StatusUnprocessableEntity),
		testsupport.Case(ErrIncidentUIDRequired, http.StatusUnprocessableEntity),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
