package incidents_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

func newFullIncidentRouter(f *ackHandlerFixture) *httpx.Router {
	jobs := jobsvc.NewService(f.dbSvc.DB(), f.dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(f.dbSvc, jobs, clock.Real{}, nil)
	h := incidents.NewHandler(svc, &config.Config{Auth: config.AuthConfig{JWTSecret: ackHandlerTestSecret}})

	router := httpx.New()
	router.GET("/orgs/:org/incidents", h.ListIncidents)
	router.GET("/orgs/:org/incidents/:uid", h.GetIncident)
	router.POST("/orgs/:org/incidents/:uid/ack", h.AcknowledgeIncident)
	router.POST("/orgs/:org/incidents/:uid/unack", h.UnacknowledgeIncident)
	router.POST("/orgs/:org/incidents/:uid/snooze", h.SnoozeIncident)
	router.POST("/orgs/:org/incidents/:uid/unsnooze", h.UnsnoozeIncident)
	router.POST("/orgs/:org/incidents/:uid/resolve", h.ResolveIncident)
	router.POST("/orgs/:org/incidents/:uid/comments", h.AddComment)

	return router
}

func TestIncidentHandlerEndpoints(t *testing.T) {
	t.Parallel()

	f := newAckHandlerFixture(t)
	router := newFullIncidentRouter(f)
	org := f.org.Slug
	inc := "/orgs/" + org + "/incidents/" + f.incident.UID

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"list", http.MethodGet, "/orgs/" + org + "/incidents", "", http.StatusOK},
		{"list filters", http.MethodGet, "/orgs/" + org + "/incidents?checkUid=a,b&checkGroupUid=g&memberCheckUid=m" +
			"&state=open&kind=check,degraded&since=2020-01-01T00:00:00Z&until=2099-01-01T00:00:00Z" +
			"&cursor=zzz&limit=5&with=check,members&hideSuppressed=true&causedByIncidentUid=x", "", http.StatusOK},
		{"list ghost org", http.MethodGet, "/orgs/ghost/incidents", "", http.StatusNotFound},
		{"list bad kind", http.MethodGet, "/orgs/" + org + "/incidents?kind=bogus", "", http.StatusBadRequest},
		{"list bad since", http.MethodGet, "/orgs/" + org + "/incidents?since=x", "", http.StatusBadRequest},
		{"list bad until", http.MethodGet, "/orgs/" + org + "/incidents?until=x", "", http.StatusBadRequest},
		{"list bad limit", http.MethodGet, "/orgs/" + org + "/incidents?limit=x", "", http.StatusBadRequest},
		{"get", http.MethodGet, inc + "?with=check,members", "", http.StatusOK},
		{"get missing", http.MethodGet, "/orgs/" + org + "/incidents/nope", "", http.StatusNotFound},
		{"get ghost org", http.MethodGet, "/orgs/ghost/incidents/nope", "", http.StatusNotFound},
		{"comment bad json", http.MethodPost, inc + "/comments", "{", http.StatusBadRequest},
		{"comment empty", http.MethodPost, inc + "/comments", `{"text":""}`, http.StatusBadRequest},
		{
			"comment missing incident", http.MethodPost, "/orgs/" + org + "/incidents/nope/comments",
			`{"text":"hi"}`, http.StatusNotFound,
		},
		{"comment ok", http.MethodPost, inc + "/comments", `{"text":"looking into it"}`, http.StatusCreated},
		{"snooze bad json", http.MethodPost, inc + "/snooze", "{", http.StatusBadRequest},
		{"snooze bad duration", http.MethodPost, inc + "/snooze", `{"duration":"abc"}`, http.StatusBadRequest},
		{"snooze missing duration", http.MethodPost, inc + "/snooze", `{}`, http.StatusBadRequest},
		{"snooze past", http.MethodPost, inc + "/snooze", `{"until":"2000-01-01T00:00:00Z"}`, http.StatusBadRequest},
		{"snooze too long", http.MethodPost, inc + "/snooze", `{"duration":"99999h"}`, http.StatusBadRequest},
		{
			"snooze missing incident", http.MethodPost, "/orgs/" + org + "/incidents/nope/snooze",
			`{"duration":"1h"}`, http.StatusNotFound,
		},
		{"snooze ok", http.MethodPost, inc + "/snooze", `{"duration":"1h","reason":"deploy"}`, http.StatusOK},
		{"unsnooze ok", http.MethodPost, inc + "/unsnooze", "", http.StatusOK},
		{"unsnooze missing", http.MethodPost, "/orgs/" + org + "/incidents/nope/unsnooze", "", http.StatusNotFound},
		{"ack ok", http.MethodPost, inc + "/ack", `{"note":"on it"}`, http.StatusOK},
		{"ack missing", http.MethodPost, "/orgs/" + org + "/incidents/nope/ack", "", http.StatusNotFound},
		{"unack ok", http.MethodPost, inc + "/unack", "", http.StatusOK},
		{"unack missing", http.MethodPost, "/orgs/" + org + "/incidents/nope/unack", "", http.StatusNotFound},
		{"resolve missing", http.MethodPost, "/orgs/" + org + "/incidents/nope/resolve", "", http.StatusNotFound},
		{"resolve ok", http.MethodPost, inc + "/resolve", `{"note":"fixed"}`, http.StatusOK},
	}

	for _, tc := range cases {
		// Sequential on purpose: later cases depend on earlier state changes.
		var body *strings.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		} else {
			body = strings.NewReader("")
		}

		req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, body)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, tc.want, rec.Code, "%s: %s", tc.name, rec.Body.String())
	}
}
