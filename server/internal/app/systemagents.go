package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// envSystemAgentEnrollmentTokens is read manually with os.LookupEnv: koanf
// collapses underscores in SP_* names into dots, so multi-word seed vars follow
// the same manual pattern as SP_REGIONS (regions_seed.go) and the
// SP_ENTITLEMENTS_* seeds (saas.go).
const envSystemAgentEnrollmentTokens = "SP_SYSTEM_AGENT_ENROLLMENT_TOKENS"

// systemAgentTokenTTL is how long a seeded platform token stays valid. It is
// long because the token is a deployment secret whose lifecycle is the fly
// secret's, not a human-minted one-shot: it is refreshed on every boot, and
// removing it from the environment (not letting it expire) is the revocation
// path. The expiry only bounds a token that outlives the deployment entirely.
const systemAgentTokenTTL = 10 * 365 * 24 * time.Hour

// SeedSystemAgentEnrollmentTokens reconciles the platform (kind='system')
// enrollment tokens with SP_SYSTEM_AGENT_ENROLLMENT_TOKENS.
//
// System agents are SolidPing-operated check workers running outside the
// cluster (fly.io machines) over the deported-agent WebSocket transport. Their
// enrollment tokens are platform-operator material, NOT org-admin material, so
// they are never mintable through the org-scoped
// /orgs/:org/agent-enrollment-tokens routes: they are seeded declaratively from
// the deployment environment (spec 2026-07-27-01, Decisions).
//
// Format: comma-separated `region=spe_…` pairs, e.g.
//
//	SP_SYSTEM_AGENT_ENROLLMENT_TOKENS="eu-west-1=spe_ab…,us-east-1=spe_cd…"
//
// Each region is validated with regions.ValidateWorkerRegion, so a `@org/…`
// private slug or a region that is not in the `regions` parameter is refused —
// a mistyped or stale entry can never mint an agent bound to nothing, or to
// somebody's private location. Only the SHA-256 hash of the token is stored.
//
// Reconciliation semantics:
//   - unset variable → complete no-op (an install that does not run platform
//     agents never has its tokens touched);
//   - set (even to an empty string) → the live system tokens become EXACTLY the
//     valid entries listed. Tokens no longer present are soft-deleted, which is
//     how removing a fly secret revokes a fleet's ability to enroll. Existing
//     agents keep working — revoking a token is not revoking an agent.
//
// Tokens are multi-use (max_uses NULL = unlimited) because every machine of a
// fly fleet generates its own keypair and enrolls on boot, so no private key is
// ever shared between machines. Malformed entries are logged and skipped rather
// than failing the boot; only a database error is returned.
func (s *Server) SeedSystemAgentEnrollmentTokens(ctx context.Context) error {
	raw, present := os.LookupEnv(envSystemAgentEnrollmentTokens)
	if !present {
		return nil
	}

	regionSvc := regions.NewService(s.dbService)
	expiresAt := time.Now().Add(systemAgentTokenTTL)
	keepHashes := make([]string, 0, strings.Count(raw, ",")+1)

	for _, entry := range strings.Split(raw, ",") {
		region, token, ok := parseSystemAgentTokenEntry(ctx, entry)
		if !ok {
			continue
		}

		if err := regionSvc.ValidateWorkerRegion(ctx, region); err != nil {
			slog.ErrorContext(ctx, "Ignoring "+envSystemAgentEnrollmentTokens+" entry: invalid region",
				"region", region, "error", err)

			continue
		}

		hash := agentcrypto.HashEnrollmentToken(token)
		if err := s.dbService.UpsertSystemAgentEnrollmentToken(ctx,
			models.NewSystemAgentEnrollmentToken(region, hash, expiresAt, nil),
		); err != nil {
			return fmt.Errorf("seed system agent enrollment token for region %q: %w", region, err)
		}

		keepHashes = append(keepHashes, hash)
	}

	revoked, err := s.dbService.RevokeSystemAgentEnrollmentTokensExcept(ctx, keepHashes)
	if err != nil {
		return fmt.Errorf("revoke removed system agent enrollment tokens: %w", err)
	}

	slog.InfoContext(ctx, "Reconciled system agent enrollment tokens",
		"seeded", len(keepHashes), "revoked", revoked)

	return nil
}

// parseSystemAgentTokenEntry splits one `region=spe_…` entry. It never logs the
// token itself — only the region and the reason — so a boot log can be shared
// without leaking enrollment material. An empty entry (trailing comma, or the
// whole variable set to "") is skipped silently: "no tokens" is a legitimate,
// deliberate configuration meaning "revoke everything".
func parseSystemAgentTokenEntry(ctx context.Context, entry string) (string, string, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", "", false
	}

	region, token, found := strings.Cut(entry, "=")
	region = strings.TrimSpace(region)
	token = strings.TrimSpace(token)

	switch {
	case !found || region == "" || token == "":
		slog.ErrorContext(ctx, "Ignoring malformed "+envSystemAgentEnrollmentTokens+
			" entry (expected `region=spe_…`)", "region", region)

		return "", "", false
	case !strings.HasPrefix(token, agentcrypto.EnrollmentTokenPrefix):
		slog.ErrorContext(ctx, "Ignoring "+envSystemAgentEnrollmentTokens+
			" entry: token is missing the "+agentcrypto.EnrollmentTokenPrefix+" prefix", "region", region)

		return "", "", false
	}

	return region, token, true
}
