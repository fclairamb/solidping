package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// seededRegions is the region catalog the seeding tests validate against.
const seededRegions = `[{"slug": "eu-west-1", "name": "EU"}, {"slug": "us-east-1", "name": "US"}]`

// newSystemAgentSeedServer builds a minimal Server over in-memory SQLite with a
// known region catalog — SeedSystemAgentEnrollmentTokens only touches dbService.
func newSystemAgentSeedServer(ctx context.Context, t *testing.T) *Server {
	t.Helper()
	r := require.New(t)

	srv, dbSvc := newRegionsSeedServer(ctx, t)
	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, []regions.RegionDefinition{
		{Slug: "eu-west-1", Name: "EU"},
		{Slug: "us-east-1", Name: "US"},
	}, false))

	return srv
}

// liveSystemTokens reads back the live platform tokens, keyed by region.
func liveSystemTokens(ctx context.Context, t *testing.T, srv *Server) map[string]*models.AgentEnrollmentToken {
	t.Helper()
	r := require.New(t)

	tokens, err := srv.dbService.ListSystemAgentEnrollmentTokens(ctx)
	r.NoError(err)

	byRegion := make(map[string]*models.AgentEnrollmentToken, len(tokens))
	for _, token := range tokens {
		byRegion[token.Region] = token
	}

	return byRegion
}

// TestSeedSystemAgentEnrollmentTokens covers the SP_SYSTEM_AGENT_ENROLLMENT_TOKENS
// reconciliation: parsing, region validation, idempotency, and the
// revoke-on-removal path that makes deleting a fly secret the revocation
// mechanism. Not parallel: the subtests mutate the process env via t.Setenv.
func TestSeedSystemAgentEnrollmentTokens(t *testing.T) {
	ctx := context.Background()

	const tokenEU = "spe_" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const tokenUS = "spe_" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	t.Run("unset env is a complete no-op", func(t *testing.T) {
		r := require.New(t)
		srv := newSystemAgentSeedServer(ctx, t)

		// Pre-existing token that must survive an unset variable.
		r.NoError(srv.dbService.UpsertSystemAgentEnrollmentToken(ctx,
			models.NewSystemAgentEnrollmentToken("eu-west-1",
				agentcrypto.HashEnrollmentToken(tokenEU), time.Now().Add(time.Hour), nil)))

		// t.Setenv registers the restore; removing it afterwards is how we get
		// a genuinely absent variable inside the subtest.
		t.Setenv(envSystemAgentEnrollmentTokens, "")
		r.NoError(os.Unsetenv(envSystemAgentEnrollmentTokens))
		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))

		r.Len(liveSystemTokens(ctx, t, srv), 1, "an unset variable must not revoke anything")
	})

	t.Run("valid pairs are seeded as multi-use system tokens", func(t *testing.T) {
		r := require.New(t)
		srv := newSystemAgentSeedServer(ctx, t)

		t.Setenv(envSystemAgentEnrollmentTokens, "eu-west-1="+tokenEU+", us-east-1="+tokenUS)
		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))

		live := liveSystemTokens(ctx, t, srv)
		r.Len(live, 2)

		eu := live["eu-west-1"]
		r.NotNil(eu)
		r.Equal(models.AgentKindSystem, eu.Kind)
		r.Nil(eu.OrganizationUID, "a system token has no owning organization")
		r.Nil(eu.MaxUses, "seeded platform tokens are unlimited-use")
		r.Equal(agentcrypto.HashEnrollmentToken(tokenEU), eu.TokenHash)
		r.NotContains(eu.TokenHash, tokenEU, "the token itself is never stored")
		r.True(eu.HasUsesLeft())
	})

	t.Run("re-seeding is idempotent", func(t *testing.T) {
		r := require.New(t)
		srv := newSystemAgentSeedServer(ctx, t)

		t.Setenv(envSystemAgentEnrollmentTokens, "eu-west-1="+tokenEU)
		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))
		first := liveSystemTokens(ctx, t, srv)["eu-west-1"]
		r.NotNil(first)

		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))
		second := liveSystemTokens(ctx, t, srv)
		r.Len(second, 1, "the same token must not be duplicated")
		r.Equal(first.UID, second["eu-west-1"].UID, "the existing row is refreshed, not replaced")
	})

	t.Run("removing an entry revokes its token", func(t *testing.T) {
		r := require.New(t)
		srv := newSystemAgentSeedServer(ctx, t)

		t.Setenv(envSystemAgentEnrollmentTokens, "eu-west-1="+tokenEU+",us-east-1="+tokenUS)
		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))
		r.Len(liveSystemTokens(ctx, t, srv), 2)

		// The fly secret drops the US token.
		t.Setenv(envSystemAgentEnrollmentTokens, "eu-west-1="+tokenEU)
		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))

		live := liveSystemTokens(ctx, t, srv)
		r.Len(live, 1)
		r.Contains(live, "eu-west-1")

		// And the revoked one can no longer be used to enroll.
		_, err := srv.dbService.GetAgentEnrollmentTokenByHash(ctx, agentcrypto.HashEnrollmentToken(tokenUS))
		r.Error(err)
	})

	t.Run("an empty value revokes every platform token", func(t *testing.T) {
		r := require.New(t)
		srv := newSystemAgentSeedServer(ctx, t)

		t.Setenv(envSystemAgentEnrollmentTokens, "eu-west-1="+tokenEU)
		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))
		r.Len(liveSystemTokens(ctx, t, srv), 1)

		t.Setenv(envSystemAgentEnrollmentTokens, "")
		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))
		r.Empty(liveSystemTokens(ctx, t, srv))
	})

	// Invalid entries are skipped without failing the boot and, crucially,
	// without ever creating a token bound to a region no job can carry.
	invalid := map[string]string{
		"unknown region":  "nope-1=" + tokenEU,
		"private region":  "@acme/dc1=" + tokenEU,
		"missing '='":     tokenEU,
		"missing token":   "eu-west-1=",
		"missing region":  "=" + tokenEU,
		"no spe_ prefix":  "eu-west-1=nottoken",
		"empty entries":   ",,,",
		"blank-ish value": "   ",
	}
	for name, value := range invalid {
		t.Run("invalid entry is skipped: "+name, func(t *testing.T) {
			r := require.New(t)
			srv := newSystemAgentSeedServer(ctx, t)

			t.Setenv(envSystemAgentEnrollmentTokens, value)
			r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx),
				"an invalid entry must not prevent the server from booting")
			r.Empty(liveSystemTokens(ctx, t, srv))
		})
	}

	t.Run("a valid entry survives an invalid sibling", func(t *testing.T) {
		r := require.New(t)
		srv := newSystemAgentSeedServer(ctx, t)

		t.Setenv(envSystemAgentEnrollmentTokens, "@acme/dc1="+tokenUS+",eu-west-1="+tokenEU)
		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))

		live := liveSystemTokens(ctx, t, srv)
		r.Len(live, 1)
		r.Contains(live, "eu-west-1")
	})
}
