package statuspages

import (
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestStatusPageErrorMappers(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}
	ce := testsupport.ClientError

	t.Run("update page", func(t *testing.T) {
		t.Parallel()
		testsupport.AssertErrorMapping(t, h.handleUpdatePageError, []testsupport.ErrorCase{
			testsupport.Case(ErrOrganizationNotFound, 404),
			testsupport.Case(ErrStatusPageNotFound, 404),
			testsupport.Case(ErrSlugConflict, ce),
			testsupport.Case(ErrInvalidSlugFormat, ce),
			testsupport.Case(ErrInvalidAutoResolve, ce),
			testsupport.Case(ErrInvalidAutoPublishDelay, ce),
			testsupport.Case(ErrInvalidHistoryPeriod, ce),
			testsupport.Case(http.ErrAbortHandler, 500),
		})
	})

	t.Run("unlock", func(t *testing.T) {
		t.Parallel()
		testsupport.AssertErrorMapping(t, h.handleUnlockError, []testsupport.ErrorCase{
			testsupport.Case(ErrOrganizationNotFound, 404),
			testsupport.Case(ErrStatusPageNotFound, 404),
			testsupport.Case(ErrWrongPassword, 401),
			testsupport.Case(ErrTooManyUnlockAttempts, 429),
			testsupport.Case(http.ErrAbortHandler, 500),
		})
	})

	t.Run("update section", func(t *testing.T) {
		t.Parallel()
		testsupport.AssertErrorMapping(t, h.handleUpdateSectionError, []testsupport.ErrorCase{
			testsupport.Case(ErrOrganizationNotFound, 404),
			testsupport.Case(ErrStatusPageNotFound, 404),
			testsupport.Case(ErrStatusPageSectionNotFound, 404),
			testsupport.Case(ErrSlugConflict, ce),
			testsupport.Case(ErrInvalidSlugFormat, ce),
			testsupport.Case(ErrSelectorInvalid, ce),
			testsupport.Case(http.ErrAbortHandler, 500),
		})
	})

	t.Run("custom domain", func(t *testing.T) {
		t.Parallel()
		testsupport.AssertErrorMapping(t, func(w http.ResponseWriter, r *http.Request, err error) error {
			handled, werr := h.mapCustomDomainError(w, r, err)
			if !handled {
				return h.WriteInternalError(w, r, err)
			}

			return werr
		}, []testsupport.ErrorCase{
			testsupport.Case(&entcore.QuotaError{}, 402),
			testsupport.Case(ErrCustomDomainInvalid, ce),
			testsupport.Case(ErrCustomDomainSelfShadow, ce),
			testsupport.Case(ErrCustomDomainTaken, 409),
			testsupport.Case(ErrCustomDomainNotSet, ce),
			testsupport.Case(ErrCustomDomainRateLimited, 429),
			testsupport.Case(http.ErrAbortHandler, 500),
		})
	})
}

func TestRequestScheme(t *testing.T) {
	t.Parallel()

	plain, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://x/", nil)
	require.NoError(t, err)
	require.Equal(t, "http", requestScheme(plain))

	fwd := plain.Clone(t.Context())
	fwd.Header.Set("X-Forwarded-Proto", "https")
	require.Equal(t, "https", requestScheme(fwd))

	fwd.Header.Set("X-Forwarded-Proto", "https, http")
	require.Equal(t, "https", requestScheme(fwd))

	blank := plain.Clone(t.Context())
	blank.Header.Set("X-Forwarded-Proto", " ")
	blank.TLS = &tls.ConnectionState{}
	require.Equal(t, "https", requestScheme(blank))
}
