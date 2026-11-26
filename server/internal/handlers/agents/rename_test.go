package agents_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

func strPtr(s string) *string { return &s }

func TestUpdateAgentRenames(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	minted, err := s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	agent, err := s.dbSvc.EnrollAgent(
		ctx, agentcrypto.HashEnrollmentToken(minted.Token), "3f2a9c1b7d4e", "ed1", "age1x", "fp-abc",
	)
	r.NoError(err)

	resp, err := s.svc.UpdateAgent(ctx, "acme", agent.UID, &agents.UpdateAgentRequest{Name: "  nas-paris  "})
	r.NoError(err)
	r.Equal("nas-paris", resp.Name)
	r.Equal("@dc1", resp.Region)

	list, err := s.svc.ListAgents(ctx, "acme")
	r.NoError(err)
	r.Equal("nas-paris", list.Data[0].Name)

	for _, bad := range []string{"", "   ", strings.Repeat("a", agents.MaxNameLength+1)} {
		_, err = s.svc.UpdateAgent(ctx, "acme", agent.UID, &agents.UpdateAgentRequest{Name: bad})
		r.ErrorIs(err, agents.ErrInvalidName)
	}

	_, err = s.svc.UpdateAgent(ctx, "acme", "00000000-0000-0000-0000-000000000000",
		&agents.UpdateAgentRequest{Name: "x"})
	r.ErrorIs(err, agents.ErrAgentNotFound)
}

func TestUpdateAgentIsOrgScoped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	other := models.NewOrganization("globex", "Other")
	r.NoError(s.dbSvc.CreateOrganization(ctx, other))

	_, err := s.svc.CreatePrivateRegion(ctx, "globex", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	minted, err := s.svc.MintEnrollmentToken(ctx, "globex", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	agent, err := s.dbSvc.EnrollAgent(ctx, agentcrypto.HashEnrollmentToken(minted.Token), "box", "ed1", "x1", "fp-1")
	r.NoError(err)

	_, err = s.svc.UpdateAgent(ctx, "acme", agent.UID, &agents.UpdateAgentRequest{Name: "stolen"})
	r.ErrorIs(err, agents.ErrAgentNotFound)

	got, err := s.dbSvc.GetAgent(ctx, agent.UID)
	r.NoError(err)
	r.Equal("box", got.Name)
}

func TestUpdatePrivateRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newMonitorSetup(t, nil)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "home", Name: "Home"})
	r.NoError(err)

	// Only the emoji: the name is kept.
	resp, err := s.svc.UpdatePrivateRegion(ctx, "acme", "home", &agents.UpdatePrivateRegionRequest{Emoji: strPtr("🏢")})
	r.NoError(err)
	r.Equal("Home", resp.Name)
	r.Equal("🏢", resp.Emoji)
	r.Equal("home", resp.Slug)

	resp, err = s.svc.UpdatePrivateRegion(ctx, "acme", "home",
		&agents.UpdatePrivateRegionRequest{Name: strPtr("Paris office")})
	r.NoError(err)
	r.Equal("Paris office", resp.Name)
	r.Equal("🏢", resp.Emoji)

	// The liveness monitor follows the rename.
	monitors := s.monitors(t)
	r.Len(monitors, 1)
	r.NotNil(monitors[0].Name)
	r.Equal("Private location: Paris office", *monitors[0].Name)

	// A monitor renamed by hand keeps its name.
	custom := "My NAS watch"
	r.NoError(s.dbSvc.UpdateCheck(ctx, monitors[0].UID, &models.CheckUpdate{Name: &custom}))
	_, err = s.svc.UpdatePrivateRegion(ctx, "acme", "home", &agents.UpdatePrivateRegionRequest{Name: strPtr("Lyon")})
	r.NoError(err)
	r.Equal(custom, *s.monitors(t)[0].Name)

	_, err = s.svc.UpdatePrivateRegion(ctx, "acme", "home", &agents.UpdatePrivateRegionRequest{Name: strPtr(" ")})
	r.ErrorIs(err, agents.ErrInvalidName)

	_, err = s.svc.UpdatePrivateRegion(ctx, "acme", "home",
		&agents.UpdatePrivateRegionRequest{Emoji: strPtr(strings.Repeat("🏠", 17))})
	r.ErrorIs(err, agents.ErrInvalidEmoji)

	_, err = s.svc.UpdatePrivateRegion(ctx, "acme", "nope", &agents.UpdatePrivateRegionRequest{Name: strPtr("x")})
	r.ErrorIs(err, agents.ErrRegionNotFound)
}

func TestUpdateRejectsAnUnknownOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.UpdateAgent(ctx, "nope", "00000000-0000-0000-0000-000000000000",
		&agents.UpdateAgentRequest{Name: "x"})
	r.ErrorIs(err, agents.ErrOrgNotFound)

	_, err = s.svc.UpdatePrivateRegion(ctx, "nope", "home", &agents.UpdatePrivateRegionRequest{Name: strPtr("x")})
	r.ErrorIs(err, agents.ErrOrgNotFound)
}

// An empty emoji falls back to the default one, and a location whose liveness
// monitor is gone renames fine.
func TestUpdatePrivateRegionDefaultsTheEmojiWithoutAMonitor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newMonitorSetup(t, nil)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{
		Slug: "home", Name: "Home", Emoji: "🏢",
	})
	r.NoError(err)

	monitors := s.monitors(t)
	r.Len(monitors, 1)
	r.NoError(s.dbSvc.DeleteCheck(ctx, monitors[0].UID))

	resp, err := s.svc.UpdatePrivateRegion(ctx, "acme", "home", &agents.UpdatePrivateRegionRequest{
		Name: strPtr("Lyon"), Emoji: strPtr("  "),
	})
	r.NoError(err)
	r.Equal("Lyon", resp.Name)
	r.Equal("🏠", resp.Emoji)
}

// TestUpdateHandlers drives both PATCH routes over HTTP: success, a bad body
// and an unknown target.
func TestUpdateHandlers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	minted, err := s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	agent, err := s.dbSvc.EnrollAgent(ctx, agentcrypto.HashEnrollmentToken(minted.Token), "box", "ed1", "x1", "fp-1")
	r.NoError(err)

	handler := agents.NewHandler(s.svc, &config.Config{})
	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org")
	group.PATCH("/agents/:uid", handler.UpdateAgent)
	group.PATCH("/private-regions/:slug", handler.UpdatePrivateRegion)

	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantName   string
	}{
		{"rename agent", "/agents/" + agent.UID, `{"name":"nas"}`, http.StatusOK, "nas"},
		{"agent bad body", "/agents/" + agent.UID, `{`, http.StatusBadRequest, ""},
		{"agent empty name", "/agents/" + agent.UID, `{"name":""}`, http.StatusBadRequest, ""},
		{"unknown agent", "/agents/00000000-0000-0000-0000-000000000000", `{"name":"x"}`, http.StatusNotFound, ""},
		{"rename region", "/private-regions/dc1", `{"name":"Datacenter"}`, http.StatusOK, "Datacenter"},
		{"region bad body", "/private-regions/dc1", `{`, http.StatusBadRequest, ""},
		{"unknown region", "/private-regions/nope", `{"name":"x"}`, http.StatusNotFound, ""},
	}

	for _, tc := range tests {
		req := httptest.NewRequestWithContext(ctx, http.MethodPatch, "/api/v1/orgs/acme"+tc.path,
			strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		r.Equal(tc.wantStatus, rec.Code, "%s: %s", tc.name, rec.Body.String())

		if tc.wantName != "" {
			var body struct {
				Name string `json:"name"`
			}
			r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
			r.Equal(tc.wantName, body.Name, tc.name)
		}
	}
}
