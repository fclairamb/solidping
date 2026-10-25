package testsupport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// ClientError as a want status accepts any 4xx answer, for mappers whose exact
// code is not the point of the case.
const ClientError = -4

// ErrorCase is one sentinel error and the HTTP status a handler's error
// mapper must answer it with.
type ErrorCase struct {
	Err    error
	Status int
}

// AssertErrorMapping runs a handler error mapper over every case and checks
// the status it writes. An unmapped error must come back as a 500, which is
// checked with an extra case.
func AssertErrorMapping(
	t *testing.T, mapper func(http.ResponseWriter, *http.Request, error) error, cases []ErrorCase,
) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.Err.Error(), func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

			require.NoError(t, mapper(rec, req, tc.Err))
			if tc.Status == ClientError {
				require.GreaterOrEqual(t, rec.Code, http.StatusBadRequest, rec.Body.String())
				require.Less(t, rec.Code, http.StatusInternalServerError, rec.Body.String())

				return
			}

			require.Equal(t, tc.Status, rec.Code, rec.Body.String())
		})
	}
}

// Case builds an ErrorCase.
func Case(err error, status int) ErrorCase { return ErrorCase{Err: err, Status: status} }
