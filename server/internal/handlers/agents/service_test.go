package agents_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/regions"
)

type setup struct {
	svc   *agents.Service
	dbSvc *sqlite.Service
	org   *models.Organization
}

func newSetup(t *testing.T) *setup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Disabled credentials service (no master key): ResealRegion no-ops, which
	// is fine — these tests cover the admin lifecycle, not the reseal path.
	creds, err := credentials.NewService(nil, credentials.ParamStore{})
	r.NoError(err)

	return &setup{svc: agents.NewService(dbSvc, creds, nil), dbSvc: dbSvc, org: org}
}

// newSetupWithEntitlements is newSetup but wires a real entitlements.Service
// with the given MaxDeportedAgents cap, so MintEnrollmentToken's quota guard
// can be exercised.
func newSetupWithEntitlements(t *testing.T, maxDeportedAgents int) *setup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(nil, credentials.ParamStore{})
	r.NoError(err)

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(entSvc.Set(ctx, org.UID, entcore.Entitlements{
		Limits: entcore.Limits{MaxDeportedAgents: entcore.Int(maxDeportedAgents)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	return &setup{svc: agents.NewService(dbSvc, creds, entSvc), dbSvc: dbSvc, org: org}
}

func TestCreateAndListPrivateRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	created, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{
		Slug: "dc1", Name: "Paris DC", Emoji: "🏢",
	})
	r.NoError(err)
	r.Equal("dc1", created.Slug)
	// The stored/dispatched region string is namespaced with the reserved prefix.
	r.Equal("@dc1", created.Region)

	list, err := s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.Len(list.Data, 1)
	r.Equal("@dc1", list.Data[0].Region)
	r.Equal("Paris DC", list.Data[0].Name)
	r.Equal(0, list.Data[0].AgentCount)
}

func TestCreatePrivateRegionRejectsDuplicateAndBadSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	_, err = s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.ErrorIs(err, agents.ErrRegionExists)

	// A slug may never smuggle in the reserved prefix or separator.
	for _, bad := range []string{"@dc", "dc/1", "DC1", ""} {
		_, err = s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: bad})
		r.ErrorIsf(err, regions.ErrInvalidPrivateRegionSlug, "slug %q must be rejected", bad)
	}
}

func TestMintEnrollmentTokenShownOnceAndHashedAtRest(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	before := time.Now()
	minted, err := s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)
	r.True(strings.HasPrefix(minted.Token, agentcrypto.EnrollmentTokenPrefix))
	r.Equal("@dc1", minted.Region)
	// Defaults to the documented 24h TTL.
	r.WithinDuration(before.Add(agents.DefaultEnrollmentTokenTTL), minted.ExpiresAt, time.Minute)

	// The listing never re-exposes the secret; only the hash is stored.
	listed, err := s.svc.ListEnrollmentTokens(ctx, "acme")
	r.NoError(err)
	r.Len(listed.Data, 1)
	r.Equal(minted.UID, listed.Data[0].UID)
	r.Equal(agents.EnrollmentTokenStatusPending, listed.Data[0].Status)
	r.Nil(listed.Data[0].UsedAt)

	rows, err := s.dbSvc.ListAgentEnrollmentTokens(ctx, s.org.UID)
	r.NoError(err)
	r.Len(rows, 1)
	r.NotEqual(minted.Token, rows[0].TokenHash)
	r.Equal(agentcrypto.HashEnrollmentToken(minted.Token), rows[0].TokenHash)
}

func TestMintEnrollmentTokenRequiresKnownRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)

	_, err := s.svc.MintEnrollmentToken(
		t.Context(), "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "nope"}, nil,
	)
	r.ErrorIs(err, agents.ErrRegionNotFound)
}

func TestMintEnrollmentTokenValidatesExpiresIn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	for _, bad := range []string{"0s", "-1h", "9999h", "not-a-duration"} {
		_, err = s.svc.MintEnrollmentToken(
			ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1", ExpiresIn: bad}, nil,
		)
		r.Errorf(err, "expiresIn %q must be rejected", bad)
	}
}

func TestDeletePrivateRegionRefusesWhileAgentsEnrolled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	minted, err := s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	agent, err := s.dbSvc.EnrollAgent(
		ctx, agentcrypto.HashEnrollmentToken(minted.Token), "a1", "ed1", "age1x", "fp1",
	)
	r.NoError(err)

	// Region is occupied -> refuse to delete.
	err = s.svc.DeletePrivateRegion(ctx, "acme", "dc1")
	r.ErrorIs(err, agents.ErrRegionHasAgents)

	// Revoking the agent frees the region.
	r.NoError(s.svc.RevokeAgent(ctx, "acme", agent.UID))
	r.NoError(s.svc.DeletePrivateRegion(ctx, "acme", "dc1"))

	list, err := s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.Empty(list.Data)
}

func TestListAgentsAndRevoke(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	minted, err := s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	agent, err := s.dbSvc.EnrollAgent(
		ctx, agentcrypto.HashEnrollmentToken(minted.Token), "paris-1", "ed1", "age1x", "fp-abc",
	)
	r.NoError(err)

	list, err := s.svc.ListAgents(ctx, "acme")
	r.NoError(err)
	r.Len(list.Data, 1)
	r.Equal("paris-1", list.Data[0].Name)
	r.Equal("fp-abc", list.Data[0].Fingerprint)
	r.Equal(models.AgentStatusActive, list.Data[0].Status)
	r.Equal("@dc1", list.Data[0].Region)

	r.NoError(s.svc.RevokeAgent(ctx, "acme", agent.UID))

	list, err = s.svc.ListAgents(ctx, "acme")
	r.NoError(err)
	r.Len(list.Data, 1)
	r.Equal(models.AgentStatusRevoked, list.Data[0].Status)

	// The region's active count drops to zero once revoked.
	regionsList, err := s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.Equal(0, regionsList.Data[0].AgentCount)
}

// TestListEnrollmentTokensReportsRecentlyUsed asserts a consumed token stays
// listed for a viewing window, reporting which agent enrolled with it — the
// register wizard relies on this to tell "used by my agent" apart from
// "canceled elsewhere" — then ages out of the list. View-only: the spent
// token can never enroll a second agent.
func TestListEnrollmentTokensReportsRecentlyUsed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	minted, err := s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	agent, err := s.dbSvc.EnrollAgent(
		ctx, agentcrypto.HashEnrollmentToken(minted.Token), "a1", "ed1", "age1x", "fp1",
	)
	r.NoError(err)

	// Still listed after use, now reporting its outcome.
	listed, err := s.svc.ListEnrollmentTokens(ctx, "acme")
	r.NoError(err)
	r.Len(listed.Data, 1)
	r.Equal(agents.EnrollmentTokenStatusUsed, listed.Data[0].Status)
	r.NotNil(listed.Data[0].UsedAt)
	r.NotNil(listed.Data[0].UsedByAgentUID)
	r.Equal(agent.UID, *listed.Data[0].UsedByAgentUID)

	// Viewing is not usability: a second enrollment with the same token fails.
	_, err = s.dbSvc.EnrollAgent(
		ctx, agentcrypto.HashEnrollmentToken(minted.Token), "a2", "ed2", "age1y", "fp2",
	)
	r.ErrorIs(err, db.ErrEnrollmentTokenInvalid)

	// Past the viewing window the used token drops out of the list.
	_, err = s.dbSvc.DB().NewUpdate().
		Model((*models.AgentEnrollmentToken)(nil)).
		Set("used_at = ?", time.Now().Add(-db.UsedEnrollmentTokenListWindow-time.Minute)).
		Where("uid = ?", minted.UID).
		Exec(ctx)
	r.NoError(err)

	listed, err = s.svc.ListEnrollmentTokens(ctx, "acme")
	r.NoError(err)
	r.Empty(listed.Data)
}

// TestListEnrollmentTokensExcludesCancelled asserts an admin-canceled token
// disappears from the list immediately — the wizard reads that absence as
// "canceled elsewhere".
func TestListEnrollmentTokensExcludesCancelled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	minted, err := s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	r.NoError(s.svc.DeleteEnrollmentToken(ctx, "acme", minted.UID))

	listed, err := s.svc.ListEnrollmentTokens(ctx, "acme")
	r.NoError(err)
	r.Empty(listed.Data)
}

// TestRevokeAgentIsOrgScoped asserts an agent cannot be revoked through another
// org's route.
func TestRevokeAgentIsOrgScoped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	other := models.NewOrganization("other", "Other")
	r.NoError(s.dbSvc.CreateOrganization(ctx, other))

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)
	minted, err := s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)
	agent, err := s.dbSvc.EnrollAgent(
		ctx, agentcrypto.HashEnrollmentToken(minted.Token), "a1", "ed1", "age1x", "fp1",
	)
	r.NoError(err)

	err = s.svc.RevokeAgent(ctx, "other", agent.UID)
	r.ErrorIs(err, agents.ErrAgentNotFound)
}

// TestMintEnrollmentTokenAllowedUnderCap asserts a mint under the
// MaxDeportedAgents cap succeeds.
func TestMintEnrollmentTokenAllowedUnderCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetupWithEntitlements(t, 2)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	_, err = s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)
}

// TestMintEnrollmentTokenRejectedAtCap asserts minting a token once the org is
// at its MaxDeportedAgents cap fails with a QuotaError (mapped to 402
// QUOTA_EXCEEDED at the handler) — the early-UX guard, distinct from the
// enrollment-time guard in agentws.
func TestMintEnrollmentTokenRejectedAtCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSetupWithEntitlements(t, 1)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	// Enroll one agent to reach the cap of 1.
	minted, err := s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)
	_, err = s.dbSvc.EnrollAgent(
		ctx, agentcrypto.HashEnrollmentToken(minted.Token), "a1", "ed1", "age1x", "fp1",
	)
	r.NoError(err)

	// A second mint is rejected at cap.
	_, err = s.svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.Error(err)
	r.ErrorIs(err, entcore.ErrEntitlementExceeded)

	var quotaErr *entcore.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("MaxDeportedAgents", quotaErr.LimitName)
	r.Equal(1, quotaErr.Limit)
	r.Equal(1, quotaErr.CurrentUsage)
}
