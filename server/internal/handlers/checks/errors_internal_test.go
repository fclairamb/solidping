package checks

import (
	"net/http"
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestCheckErrorMappersCoverEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}
	slug := []testsupport.ErrorCase{
		testsupport.Case(ErrSlugConflict, testsupport.ClientError),
		testsupport.Case(errCheckNameRequired, testsupport.ClientError),
		testsupport.Case(ErrInvalidSlugFormat, testsupport.ClientError),
	}
	demo := []testsupport.ErrorCase{
		testsupport.Case(ErrDemoReadOnly, testsupport.ClientError),
		testsupport.Case(ErrDemoCheckTypeNotAllowed, testsupport.ClientError),
		testsupport.Case(ErrDemoPeriodTooShort, testsupport.ClientError),
		testsupport.Case(ErrDemoPrivateRegion, testsupport.ClientError),
	}
	common := []testsupport.ErrorCase{
		testsupport.Case(ErrOrganizationNotFound, http.StatusNotFound),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	}

	cases := map[string][]testsupport.ErrorCase{
		"clone": append(append(append([]testsupport.ErrorCase{
			testsupport.Case(ErrCheckNotFound, http.StatusNotFound),
			testsupport.Case(&entcore.QuotaError{}, http.StatusPaymentRequired),
		}, demo...), slug...), common...),
		"create": append(append(append([]testsupport.ErrorCase{
			testsupport.Case(ErrInternalFieldNotWritable, testsupport.ClientError),
			testsupport.Case(ErrInvalidCheckType, http.StatusBadRequest),
			testsupport.Case(ErrNoAgentsToSealTo, http.StatusBadRequest),
			testsupport.Case(errIncidentPeriodOutOfRange, http.StatusBadRequest),
			testsupport.Case(&entcore.QuotaError{}, http.StatusPaymentRequired),
		}, demo...), slug...), common...),
		"update": append(append(append([]testsupport.ErrorCase{
			testsupport.Case(ErrInternalFieldNotWritable, testsupport.ClientError),
			testsupport.Case(ErrCheckNotFound, http.StatusNotFound),
			testsupport.Case(ErrTunnelRegionNarrowed, http.StatusConflict),
			testsupport.Case(ErrNoAgentsToSealTo, http.StatusBadRequest),
			testsupport.Case(errReminderIntervalOutOfRange, http.StatusBadRequest),
		}, demo...), slug...), common...),
		"upsert": append(append([]testsupport.ErrorCase{
			testsupport.Case(ErrInternalFieldNotWritable, testsupport.ClientError),
			testsupport.Case(ErrInvalidCheckType, testsupport.ClientError),
			testsupport.Case(errRegionSpreadOutOfRange, http.StatusBadRequest),
		}, demo...), common...),
		"delete": append(append([]testsupport.ErrorCase{
			testsupport.Case(ErrTunnelInUse, http.StatusConflict),
			testsupport.Case(ErrCheckNotFound, http.StatusNotFound),
		}, demo...), common...),
	}

	mappers := map[string]func(http.ResponseWriter, *http.Request, error) error{
		"clone":  h.handleCloneError,
		"create": h.handleCreateError,
		"update": h.handleUpdateError,
		"upsert": h.handleUpsertError,
		"delete": h.handleDeleteError,
	}

	for name, mapper := range mappers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testsupport.AssertErrorMapping(t, mapper, cases[name])
		})
	}
}
