package base_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/errorreport/errorreporttest"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

var errDatabaseExploded = errors.New("database exploded")

// errorWriteCase drives one call into the HandlerBase error funnel and says how
// many Sentry events it must produce.
type errorWriteCase struct {
	name       string
	write      func(h *base.HandlerBase, w http.ResponseWriter, r *http.Request) error
	wantStatus int
	wantEvents int
}

// TestHandlerBase_ErrorWritesReportOnly5xx pins the whole rule in one table:
// server faults are reported, client faults never are. The 4xx rows are the
// positive controls — if reporting were wired to the funnel rather than to the
// status, they would fail.
func TestHandlerBase_ErrorWritesReportOnly5xx(t *testing.T) {
	t.Parallel()

	cases := []errorWriteCase{
		{
			name: "WriteInternalError reports",
			write: func(h *base.HandlerBase, w http.ResponseWriter, r *http.Request) error {
				return h.WriteInternalError(w, r, errDatabaseExploded)
			},
			wantStatus: http.StatusInternalServerError,
			wantEvents: 1,
		},
		{
			name: "WriteErrorErr at 5xx reports",
			write: func(h *base.HandlerBase, w http.ResponseWriter, r *http.Request) error {
				return h.WriteErrorErr(w, r, http.StatusServiceUnavailable,
					base.ErrorCodeInternalError, "Upstream unavailable", errDatabaseExploded)
			},
			wantStatus: http.StatusServiceUnavailable,
			wantEvents: 1,
		},
		{
			name: "WriteErrorErr at 4xx stays silent",
			write: func(h *base.HandlerBase, w http.ResponseWriter, r *http.Request) error {
				return h.WriteErrorErr(w, r, http.StatusNotFound,
					base.ErrorCodeNotFound, "Check not found", errDatabaseExploded)
			},
			wantStatus: http.StatusNotFound,
			wantEvents: 0,
		},
		{
			name: "WriteErrorErr at 422 stays silent",
			write: func(h *base.HandlerBase, w http.ResponseWriter, r *http.Request) error {
				return h.WriteErrorErr(w, r, http.StatusUnprocessableEntity,
					base.ErrorCodeValidationError, "Invalid payload", errDatabaseExploded)
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantEvents: 0,
		},
		{
			name: "WriteError stays silent",
			write: func(h *base.HandlerBase, w http.ResponseWriter, _ *http.Request) error {
				return h.WriteError(w, http.StatusForbidden, base.ErrorCodeForbidden, "Nope")
			},
			wantStatus: http.StatusForbidden,
			wantEvents: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			hub, transport, err := errorreporttest.NewHub(t)
			r.NoError(err)

			handlerBase := base.NewHandlerBase(nil)
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/checks", http.NoBody)
			req = req.WithContext(sentry.SetHubOnContext(req.Context(), hub))

			r.NoError(tc.write(&handlerBase, rec, req))
			r.Equal(tc.wantStatus, rec.Code)
			r.Equal(tc.wantEvents, transport.Len())

			if tc.wantEvents > 0 {
				r.Equal("database exploded", transport.Events()[0].Exception[0].Value)
			}
		})
	}
}

// A handler that writes a 500 outside an HTTP chain (a unit test, a non-HTTP
// caller) has no hub on its request and must not blow up or report.
func TestHandlerBase_WriteInternalErrorWithoutHubIsSafe(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, transport, err := errorreporttest.NewHub(t)
	r.NoError(err)

	handlerBase := base.NewHandlerBase(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/checks", http.NoBody)

	r.NoError(handlerBase.WriteInternalError(rec, req, errDatabaseExploded))
	r.Equal(http.StatusInternalServerError, rec.Code)
	r.Equal(0, transport.Len())
}

// The 500 body shape is what Recoverer reuses for a panic, so pin it here.
func TestHandlerBase_WriteInternalErrorBodyShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	handlerBase := base.NewHandlerBase(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/checks", http.NoBody)

	r.NoError(handlerBase.WriteInternalError(rec, req, errDatabaseExploded))

	var body base.ErrorResponse

	r.NoError(json.NewDecoder(rec.Body).Decode(&body))
	r.Equal(string(base.ErrorCodeInternalError), body.Code)
	r.Equal("Internal server error", body.Title)
	r.Equal("application/json", rec.Header().Get("Content-Type"))
}
