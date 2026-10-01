package incidentpublications_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

type pubHandlerEnv struct {
	*pubSetup
	router *httpx.Router
	h      *incidentpublications.Handler
}

func newPubHandlerEnv(t *testing.T, opts setupOptions) *pubHandlerEnv {
	t.Helper()

	setup := newPubSetup(t, opts)
	handler := incidentpublications.NewHandler(setup.pubs, &config.Config{})

	router := httpx.New()
	pages := router.NewGroup("/api/v1/orgs/:org/status-pages/:statusPageUid/incidents")
	pages.GET("", handler.List)
	pages.POST("", handler.Create)
	pages.GET("/:uid", handler.Get)
	pages.PATCH("/:uid", handler.Update)
	pages.POST("/:uid/updates", handler.AppendUpdate)

	incs := router.NewGroup("/api/v1/orgs/:org/incidents/:incidentUid/publications")
	incs.GET("", handler.ListForIncident)
	incs.POST("", handler.PublishIncident)
	incs.DELETE("/:uid", handler.Unpublish)

	return &pubHandlerEnv{pubSetup: setup, router: router, h: handler}
}

func (e *pubHandlerEnv) do(t *testing.T, method, path, rawBody string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewBufferString(rawBody))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	return rec
}

func (e *pubHandlerEnv) pagesPath() string {
	return fmt.Sprintf("/api/v1/orgs/%s/status-pages/%s/incidents", e.org.Slug, e.page.UID)
}

func TestHandlerListQueryValidation(t *testing.T) {
	t.Parallel()

	env := newPubHandlerEnv(t, setupOptions{})

	tests := []struct {
		name  string
		query string
		code  int
	}{
		{"no filter", "", http.StatusOK},
		{"all filters", "?state=resolved&active=true&stale=true&limit=5&offset=0", http.StatusOK},
		{"limit not a number", "?limit=abc", http.StatusBadRequest},
		{"limit zero", "?limit=0", http.StatusBadRequest},
		{"limit too high", "?limit=201", http.StatusBadRequest},
		{"negative offset", "?offset=-1", http.StatusBadRequest},
		{"offset not a number", "?offset=x", http.StatusBadRequest},
		{"unknown page", "", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := env.pagesPath() + tt.query
			if tt.name == "unknown page" {
				path = "/api/v1/orgs/" + env.org.Slug + "/status-pages/nope/incidents"
			}

			rec := env.do(t, http.MethodGet, path, "")
			require.Equal(t, tt.code, rec.Code, rec.Body.String())
		})
	}
}

func TestHandlerPublicationLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newPubHandlerEnv(t, setupOptions{})

	// Create
	rec := env.do(t, http.MethodPost, env.pagesPath(),
		`{"title":"Payments degraded","state":"investigating","severity":"minor","bodyMarkdown":"Looking into it"}`)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))

	uid, ok := created["uid"].(string)
	r.True(ok)

	// Get
	rec = env.do(t, http.MethodGet, env.pagesPath()+"/"+uid, "")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	// Update
	rec = env.do(t, http.MethodPatch, env.pagesPath()+"/"+uid, `{"state":"identified","severity":"major"}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	// Append update
	rec = env.do(t, http.MethodPost, env.pagesPath()+"/"+uid+"/updates",
		`{"kind":"monitoring","bodyMarkdown":"A fix was deployed"}`)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	// List
	rec = env.do(t, http.MethodGet, env.pagesPath()+"?active=true", "")
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), uid)
}

func TestHandlerWriteErrors(t *testing.T) {
	t.Parallel()

	env := newPubHandlerEnv(t, setupOptions{})
	base := env.pagesPath()

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		code   int
	}{
		{"create bad json", http.MethodPost, base, `{`, http.StatusUnprocessableEntity},
		{"create empty title", http.MethodPost, base, `{"title":""}`, http.StatusUnprocessableEntity},
		{"create long title", http.MethodPost, base, `{"title":"` + string(bytes.Repeat([]byte("a"), 300)) + `"}`,
			http.StatusUnprocessableEntity},
		{"create invalid state", http.MethodPost, base, `{"title":"x","state":"bogus"}`, http.StatusUnprocessableEntity},
		{"create invalid severity", http.MethodPost, base, `{"title":"x","severity":"bogus"}`,
			http.StatusUnprocessableEntity},
		{"create unknown incident", http.MethodPost, base, `{"title":"x","incidentUid":"nope"}`,
			http.StatusNotFound},
		{"get unknown", http.MethodGet, base + "/nope", "", http.StatusNotFound},
		{"update bad json", http.MethodPatch, base + "/nope", `{`, http.StatusUnprocessableEntity},
		{"update unknown", http.MethodPatch, base + "/nope", `{"title":"x"}`, http.StatusNotFound},
		{"append bad json", http.MethodPost, base + "/nope/updates", `{`, http.StatusUnprocessableEntity},
		{"append unknown", http.MethodPost, base + "/nope/updates",
			`{"kind":"info","bodyMarkdown":"x"}`, http.StatusNotFound},
		{"unknown org", http.MethodGet, "/api/v1/orgs/nope/status-pages/x/incidents", "", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := env.do(t, tt.method, tt.path, tt.body)
			require.Equal(t, tt.code, rec.Code, rec.Body.String())
		})
	}
}

func TestHandlerAppendUpdateValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newPubHandlerEnv(t, setupOptions{})

	rec := env.do(t, http.MethodPost, env.pagesPath(), `{"title":"Outage"}`)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))

	uid, _ := created["uid"].(string)
	path := env.pagesPath() + "/" + uid

	tests := []struct {
		name string
		body string
	}{
		{"invalid kind", `{"kind":"bogus","bodyMarkdown":"x"}`},
		{"empty body", `{"kind":"info","bodyMarkdown":""}`},
		{"body too long", `{"kind":"info","bodyMarkdown":"` + string(bytes.Repeat([]byte("a"), 17000)) + `"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := env.do(t, http.MethodPost, path+"/updates", tt.body)
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		})
	}

	rec = env.do(t, http.MethodPatch, path, `{"state":"bogus"}`)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
}

func TestHandlerIncidentPublications(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newPubHandlerEnv(t, setupOptions{})

	env.submit(models.ResultStatusDown)

	inc := env.activeIncident()
	r.NotNil(inc)

	incPath := fmt.Sprintf("/api/v1/orgs/%s/incidents/%s/publications", env.org.Slug, inc.UID)

	// Missing page / bad json.
	r.Equal(http.StatusUnprocessableEntity, env.do(t, http.MethodPost, incPath, `{`).Code)
	r.Equal(http.StatusUnprocessableEntity, env.do(t, http.MethodPost, incPath, `{}`).Code)

	// Unknown incident.
	rec := env.do(t, http.MethodPost,
		"/api/v1/orgs/"+env.org.Slug+"/incidents/nope/publications",
		fmt.Sprintf(`{"statusPageUid":%q}`, env.page.UID))
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())

	// Publish.
	rec = env.do(t, http.MethodPost, incPath,
		fmt.Sprintf(`{"statusPageUid":%q,"title":"Payments issue","severity":"major"}`, env.page.UID))
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var pub map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &pub))

	uid, _ := pub["uid"].(string)

	// Publishing twice conflicts.
	rec = env.do(t, http.MethodPost, incPath, fmt.Sprintf(`{"statusPageUid":%q}`, env.page.UID))
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())

	// List for incident.
	rec = env.do(t, http.MethodGet, incPath, "")
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), uid)

	// Unpublish, then unknown publication.
	r.Equal(http.StatusNoContent, env.do(t, http.MethodDelete, incPath+"/"+uid, "").Code)
	r.Equal(http.StatusNotFound, env.do(t, http.MethodDelete, incPath+"/nope", "").Code)
}

func TestIncidentPublicationsServiceValidatesInput(t *testing.T) {
	t.Parallel()

	env := newPubHandlerEnv(t, setupOptions{})
	ctx := t.Context()

	_, err := env.pubs.ListForIncident(ctx, "nope", "x")
	require.ErrorIs(t, err, incidentpublications.ErrOrganizationNotFound)

	list, err := env.pubs.ListForIncident(ctx, env.org.Slug, "missing")
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestHandlerErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code int
	}{
		{"org", incidentpublications.ErrOrganizationNotFound, http.StatusNotFound},
		{"page", incidentpublications.ErrStatusPageNotFound, http.StatusNotFound},
		{"locked", statuspagelock.ErrLocked, http.StatusUnauthorized},
		{"publication", incidentpublications.ErrPublicationNotFound, http.StatusNotFound},
		{"incident", incidentpublications.ErrIncidentNotFound, http.StatusNotFound},
		{"already", incidentpublications.ErrAlreadyPublished, http.StatusConflict},
		{"title", incidentpublications.ErrTitleRequired, http.StatusUnprocessableEntity},
		{"title long", incidentpublications.ErrTitleTooLong, http.StatusUnprocessableEntity},
		{"state", incidentpublications.ErrInvalidState, http.StatusUnprocessableEntity},
		{"severity", incidentpublications.ErrInvalidSeverity, http.StatusUnprocessableEntity},
		{"kind", incidentpublications.ErrInvalidKind, http.StatusUnprocessableEntity},
		{"body", incidentpublications.ErrBodyRequired, http.StatusUnprocessableEntity},
		{"body long", incidentpublications.ErrBodyTooLong, http.StatusUnprocessableEntity},
		{"wrapped", fmt.Errorf("ctx: %w", incidentpublications.ErrIncidentNotFound), http.StatusNotFound},
		{"unknown", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := incidentpublications.NewHandler(nil, &config.Config{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

			require.NoError(t, incidentpublications.HandleErrorForTest(h, rec, req, tt.err))
			require.Equal(t, tt.code, rec.Code)
		})
	}
}

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sqlite", errors.New("UNIQUE constraint failed: x.y"), true},
		{"postgres", errors.New("duplicate key value violates"), true},
		{"sqlstate", errors.New("ERROR (SQLSTATE 23505)"), true},
		{"other", errors.New("boom"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, incidentpublications.IsUniqueViolationForTest(tt.err))
		})
	}
}
