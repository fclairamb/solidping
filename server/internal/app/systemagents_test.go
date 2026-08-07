package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

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

// captureSlog swaps the default slog logger for one writing into a buffer and
// restores it when the test ends. Not safe for parallel tests.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return buf
}

// TestParseSystemAgentTokenEntryNeverLogsTheToken pins the leak that made this
// worth a test: strings.Cut returns the WHOLE entry as the "region" when the
// entry has no `=`, so an operator who sets
// SP_SYSTEM_AGENT_ENROLLMENT_TOKENS=spe_… (forgetting the `region=` prefix)
// used to have a live multi-use enrollment token written to the boot log.
// Not parallel: it swaps the process-wide default slog logger.
func TestParseSystemAgentTokenEntryNeverLogsTheToken(t *testing.T) { //nolint:paralleltest // slog.SetDefault
	const secret = "spe_deadbeefcafebabe0123456789abcdef"

	cases := []struct {
		name  string
		entry string
	}{
		{"no region prefix at all", secret},
		{"empty region", "=" + secret},
		{"trailing separator", secret + "="},
	}

	for _, tc := range cases { //nolint:paralleltest // slog.SetDefault forbids parallel subtests
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			buf := captureSlog(t)

			region, token, ok := parseSystemAgentTokenEntry(context.Background(), 3, tc.entry)

			r.False(ok)
			r.Empty(region)
			r.Empty(token)

			logged := buf.String()
			r.NotEmpty(logged, "the malformed entry must still be reported")
			r.NotContains(logged, secret, "the enrollment token must never reach the logs")
			r.NotContains(logged, "deadbeef", "not even a substring of the token")
			r.Contains(logged, "entry_index=3", "the entry is identified by position instead")
		})
	}
}

// TestParseSystemAgentTokenEntryLogCaptureWorks is the POSITIVE CONTROL for the
// test above: the same capture plumbing must be able to see a legitimately
// logged, non-secret value (the region), otherwise the NotContains assertions
// would pass trivially on an empty buffer.
// Not parallel: it swaps the process-wide default slog logger.
func TestParseSystemAgentTokenEntryLogCaptureWorks(t *testing.T) { //nolint:paralleltest // slog.SetDefault
	r := require.New(t)
	buf := captureSlog(t)

	region, token, ok := parseSystemAgentTokenEntry(context.Background(), 1, "eu-west-1=not-a-token")

	r.False(ok)
	r.Empty(region)
	r.Empty(token)

	logged := buf.String()
	r.Contains(logged, "eu-west-1", "positive control: a non-secret region IS logged and IS captured")
	r.Contains(logged, "missing the "+agentcrypto.EnrollmentTokenPrefix+" prefix")
}

// TestParseSystemAgentTokenEntryValidEntry is the second positive control: a
// well-formed entry still parses, so the redaction did not break the happy path
// and the secret used above is a realistic token value.
// Not parallel: it swaps the process-wide default slog logger.
func TestParseSystemAgentTokenEntryValidEntry(t *testing.T) { //nolint:paralleltest // slog.SetDefault
	r := require.New(t)
	buf := captureSlog(t)

	const token = "spe_deadbeefcafebabe0123456789abcdef"

	region, parsed, ok := parseSystemAgentTokenEntry(context.Background(), 0, " eu-west-1 = "+token+" ")

	r.True(ok)
	r.Equal("eu-west-1", region)
	r.Equal(token, parsed)
	r.Empty(buf.String(), "a valid entry logs nothing at all")
}

// TestSeedSystemAgentEnrollmentTokensNeverLogsTokens is the end-to-end proof
// for the whole seeding path: whatever an operator mistypes into
// SP_SYSTEM_AGENT_ENROLLMENT_TOKENS, no live token text reaches the boot log —
// including the `spe_…=spe_…` shape, which parses cleanly and is only rejected
// later by region validation (whose "region" field would otherwise carry the
// pasted token). Not parallel: t.Setenv and slog.SetDefault.
func TestSeedSystemAgentEnrollmentTokensNeverLogsTokens(t *testing.T) { //nolint:paralleltest // env + slog
	ctx := context.Background()

	const secret = "spe_" + "cccccccccccccccccccccccccccccccc"

	for _, raw := range []string{
		secret,                     // forgot the `region=` prefix entirely
		secret + "=" + secret,      // token pasted on both sides
		"nrt-1=" + secret + "=x",   // stray separator
		"unknown-region=" + secret, // valid shape, region not in the catalog
	} {
		r := require.New(t)
		srv := newSystemAgentSeedServer(ctx, t)
		buf := captureSlog(t)

		t.Setenv(envSystemAgentEnrollmentTokens, raw)
		r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))

		logged := buf.String()
		r.NotContains(logged, secret, "no enrollment token may reach the logs for entry %q", raw)
		r.NotContains(logged, "cccccccc", "not even a substring of the token")
		r.Empty(liveSystemTokens(ctx, t, srv), "nothing malformed may be seeded")
	}
}

// TestSeedSystemAgentEnrollmentTokensLogsRegions is the positive control for
// the test above: a rejected but non-secret region IS logged and IS captured,
// so the NotContains assertions cannot pass on an empty buffer.
// Not parallel: t.Setenv and slog.SetDefault.
func TestSeedSystemAgentEnrollmentTokensLogsRegions(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)
	srv := newSystemAgentSeedServer(ctx, t)
	buf := captureSlog(t)

	t.Setenv(envSystemAgentEnrollmentTokens, "not-a-known-region=spe_"+"dddddddddddddddddddddddddddddddd")
	r.NoError(srv.SeedSystemAgentEnrollmentTokens(ctx))

	r.Contains(buf.String(), "not-a-known-region", "positive control: a plain region is logged and captured")
	r.Contains(buf.String(), "invalid region")
}
