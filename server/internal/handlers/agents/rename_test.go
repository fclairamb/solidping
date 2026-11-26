package agents_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
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
