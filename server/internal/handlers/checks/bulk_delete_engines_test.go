package checks_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portBulkDeletePG is distinct from every other fixed test port in the repo.
const portBulkDeletePG = 15878

// TestBulkDeleteMixed_Postgres runs the mixed bulk delete (including the
// tunnel-in-use guard, a JSONB predicate on Postgres) on a real Postgres.
// Self-skips under -short and on embedded-startup errors.
func TestBulkDeleteMixed_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portBulkDeletePG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	svc, _, org := checksServiceOn(t, dbSvc)
	runBulkDeleteMixed(t, svc, dbSvc, org)
}

func bulkDeleteRequest(t *testing.T, handler http.Handler, org, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/orgs/"+org+"/checks",
		strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// TestBulkDeleteHandlerRejectsBadRequests: empty list, too many UIDs and an
// oversized body are all refused and delete nothing.
func TestBulkDeleteHandlerRejectsBadRequests(t *testing.T) {
	t.Parallel()

	svc, _, org := setupTunnelChecksService(t)
	kept, err := svc.CreateCheck(t.Context(), org.Slug, httpCheckReq())
	require.NoError(t, err)

	router := httpx.New()
	router.DELETE("/api/v1/orgs/:org/checks", checks.NewHandler(svc, &config.Config{}).BulkDeleteChecks)

	tooMany := `{"checkUids":["` +
		strings.Repeat(missingCheckUID+`","`, checks.MaxBulkUpdateChecks) + missingCheckUID + `"]}`
	oversized := `{"checkUids":["` + strings.Repeat("a", checks.MaxBulkRequestBytes+1) + `"]}`

	cases := map[string]struct {
		body   string
		status int
	}{
		"empty list":     {`{"checkUids":[]}`, http.StatusBadRequest},
		"missing list":   {`{}`, http.StatusBadRequest},
		"too many uids":  {tooMany, http.StatusBadRequest},
		"oversized body": {oversized, http.StatusUnprocessableEntity},
		"invalid json":   {`{`, http.StatusUnprocessableEntity},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := bulkDeleteRequest(t, router, org.Slug, tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
		})
	}

	_, err = svc.GetCheck(t.Context(), org.Slug, kept.UID, checks.GetCheckOptions{})
	require.NoError(t, err, "a refused request deletes nothing")

	ok := bulkDeleteRequest(t, router, org.Slug, `{"checkUids":["`+kept.UID+`"]}`)
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
}
