package checktypes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/plugins"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

// fakeOrgTypes holds reported types per org UID.
type fakeOrgTypes map[string][]*plugins.RemoteType

func (f fakeOrgTypes) List(_ context.Context, orgUID string) ([]*plugins.RemoteType, error) {
	return f[orgUID], nil
}

func (f fakeOrgTypes) Lookup(_ context.Context, orgUID, checkType string) (*plugins.RemoteType, bool, error) {
	for _, remote := range f[orgUID] {
		if string(remote.Meta().Type) == checkType {
			return remote, true, nil
		}
	}

	return nil, false, nil
}

// orgRouter serves the two org routes, with the org UID taken from the path
// the way the org middleware resolves it.
func orgRouter(t *testing.T, store OrgPluginTypes) http.Handler {
	t.Helper()

	resolver := checkerdef.NewActivationResolver(&config.CheckersConfig{}, config.DeploymentModeSaaS)
	handler := NewHandler(NewService(resolver, ""), &config.Config{})
	handler.SetOrgPluginTypes(store)

	wrap := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return func(writer http.ResponseWriter, req *http.Request) {
			org := &models.Organization{UID: chi.URLParam(req, "org"), Slug: chi.URLParam(req, "org")}
			req = req.WithContext(context.WithValue(req.Context(), base.ContextKeyOrganization, org))

			if err := fn(writer, req); err != nil {
				http.Error(writer, err.Error(), http.StatusInternalServerError)
			}
		}
	}

	router := chi.NewRouter()
	router.Get("/api/v1/orgs/{org}/check-types", wrap(handler.ListOrgCheckTypes))
	router.Get("/api/v1/orgs/{org}/check-types/{type}/schema", wrap(handler.GetOrgConfigSchema))

	return router
}

// The org listing and the org schema route serve an agent-reported type to
// its org only (spec 2026-10-17-05 §18). An exec type is enabled there even
// in SaaS mode: it runs on the org's own agents.
func TestOrgRoutesServeAgentTypesToTheirOrgOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	remote, err := plugins.CompileRemote(&protocol.TypeManifest{
		Type: "x-agent-listed", Plugin: "acme", Version: "2.0.0", Protocol: "exec",
		Description: "Agent type", DefaultPeriod: "2m",
		Schema: json.RawMessage(
			`{"type":"object","properties":{"key":{"type":"string"}},"x-solidping-secret-fields":["key"]}`),
	})
	r.NoError(err)

	router := orgRouter(t, fakeOrgTypes{"org-a": {remote}})

	find := func(org string) *CheckTypeResponse {
		rec := get(t, router, "/api/v1/orgs/"+org+"/check-types")
		r.Equal(http.StatusOK, rec.Code, rec.Body.String())

		var listing ListCheckTypesResponse
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &listing))
		r.NotEmpty(listing.Data, "the built-in types are always listed")

		for i := range listing.Data {
			if listing.Data[i].Type == "x-agent-listed" {
				return &listing.Data[i]
			}
		}

		return nil
	}

	listed := find("org-a")
	r.NotNil(listed)
	r.True(listed.Enabled)
	r.True(listed.AgentReported)
	r.True(listed.ExcludedFromTally)
	r.Equal(&checkerdef.PluginRef{Name: "acme", Version: "2.0.0"}, listed.Plugin)
	r.Equal([]string{"key"}, listed.SecretFields)
	r.Equal(120, listed.DefaultPeriodSeconds)
	r.Contains(listed.Labels, "unsafe")

	r.Nil(find("org-b"), "org B must not list org A's agent type")

	rec := get(t, router, "/api/v1/orgs/org-a/check-types/x-agent-listed/schema")
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), `"x-solidping-plugin"`)

	rec = get(t, router, "/api/v1/orgs/org-b/check-types/x-agent-listed/schema")
	r.Equal(http.StatusNotFound, rec.Code, "org B must not read org A's schema")

	// A built-in schema is served on the org route too.
	rec = get(t, router, "/api/v1/orgs/org-b/check-types/http/schema")
	r.Equal(http.StatusOK, rec.Code)
}
