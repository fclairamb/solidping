package incidents_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// getIncidentDetailsResponse decodes just the fields this test cares about
// off GET /orgs/:org/incidents/:uid.
type getIncidentDetailsResponse struct {
	UID     string         `json:"uid"`
	Details map[string]any `json:"details"`
}

// TestHandler_GetIncident_ReturnsDetails is the API-level pin for the
// spec: an incident opened with a failure-details snapshot must surface it
// through GET /orgs/:org/incidents/:uid as the `details` field.
func TestHandler_GetIncident_ReturnsDetails(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	s := newFailureSnapshotSetup(t)

	result := downResult(s.org.UID, s.check.UID, "api down: 503 from upstream")
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	handler := incidents.NewHandler(s.svc, &config.Config{})
	router := httpx.New()
	router.GET("/orgs/:org/incidents/:uid", handler.GetIncident)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/orgs/"+s.org.Slug+"/incidents/"+inc.UID, nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body getIncidentDetailsResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(inc.UID, body.UID)
	r.NotNil(body.Details, "GET /incidents/:uid must return details")
	r.Equal("api down: 503 from upstream", body.Details["failure_reason"])

	first, ok := body.Details["first_result"].(map[string]any)
	r.True(ok, "details.first_result must be an object")
	r.Equal(result.UID, first["resultUid"])

	output, ok := first["output"].(map[string]any)
	r.True(ok)
	r.Equal("api down: 503 from upstream", output[checkerdef.OutputKeyError])
}
