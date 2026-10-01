package events_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
)

func TestEventsHandler(t *testing.T) { //nolint:tparallel // subtests share one fixture
	t.Parallel()

	f := newFixture(t)
	h := events.NewHandler(f.svc, &config.Config{})

	f.seed(t.Context(), t, models.EventType("check.created"), &f.admin.UID, time.Now().Add(-time.Hour), "10.0.0.1")

	cases := []struct {
		name   string
		fn     func(http.ResponseWriter, *http.Request) error
		org    string
		query  string
		user   *models.User
		wantSt int
	}{
		{"list defaults", h.ListEvents, f.orgSlug, "", f.admin, http.StatusOK},
		{"list anonymous", h.ListEvents, f.orgSlug, "", nil, http.StatusOK},
		{"list viewer", h.ListEvents, f.orgSlug, "", f.viewer, http.StatusOK},
		{
			"list all filters", h.ListEvents, f.orgSlug,
			"eventType=check.created,&type=check&actorUserUid=u1&targetUid=t&targetType=check&target=%20foo%20" +
				"&sourceIp=10.0.0.1&since=2020-01-01T00:00:00Z&until=2099-01-01T00:00:00Z" +
				"&checkUid=c1&incidentUid=i1&cursor=abc&limit=5", f.admin, http.StatusOK,
		},
		{"list actorUid alias", h.ListEvents, f.orgSlug, "actorUid=u1", f.admin, http.StatusOK},
		{"list bad since", h.ListEvents, f.orgSlug, "since=yesterday", f.admin, http.StatusBadRequest},
		{"list bad until", h.ListEvents, f.orgSlug, "until=tomorrow", f.admin, http.StatusBadRequest},
		{"list bad limit", h.ListEvents, f.orgSlug, "limit=abc", f.admin, http.StatusBadRequest},
		{"list ghost org", h.ListEvents, "ghost", "", f.admin, http.StatusNotFound},
		{"incident events ok", h.ListIncidentEvents, f.orgSlug, "cursor=x&limit=3", f.admin, http.StatusOK},
		{"incident events bad limit", h.ListIncidentEvents, f.orgSlug, "limit=-1", f.admin, http.StatusBadRequest},
		{"incident events ghost", h.ListIncidentEvents, "ghost", "", f.admin, http.StatusNotFound},
		{"check events ok", h.ListCheckEvents, f.orgSlug, "cursor=x&limit=3", f.admin, http.StatusOK},
		{"check events bad limit", h.ListCheckEvents, f.orgSlug, "limit=zzz", f.admin, http.StatusBadRequest},
		{"check events ghost", h.ListCheckEvents, "ghost", "", f.admin, http.StatusNotFound},
	}

	for _, tc := range cases { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.user != nil {
				ctx = context.WithValue(ctx, base.ContextKeyUser, tc.user)
			}

			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("org", tc.org)
			rctx.URLParams.Add("uid", "inc1")
			rctx.URLParams.Add("checkUid", "chk1")
			ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)

			target := "/x"
			if tc.query != "" {
				target += "?" + tc.query
			}

			req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			rec := httptest.NewRecorder()
			require.NoError(t, tc.fn(rec, req))
			require.Equal(t, tc.wantSt, rec.Code, rec.Body.String())
		})
	}
}
