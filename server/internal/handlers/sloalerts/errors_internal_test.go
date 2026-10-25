package sloalerts

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
		testsupport.Case(ErrSLONotFound, http.StatusNotFound),
		testsupport.Case(ErrPolicyNotFound, http.StatusNotFound),
		testsupport.Case(ErrInvalidWindows, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidThreshold, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidSeverity, http.StatusUnprocessableEntity),
		testsupport.Case(ErrInvalidMinSamples, http.StatusUnprocessableEntity),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
