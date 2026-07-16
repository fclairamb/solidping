// Package agentmode runs the standard SolidPing binary as a deported
// (org-scoped) check agent (SP_NODE_ROLE=agent, spec 2026-07-16-02): no
// database, no migrations, no HTTP server — the check worker loop over an
// outbound WebSocket to the master.
//
// Identity/keys lifecycle: on first run the agent generates its two keypairs
// (Ed25519 identity + X25519/age encryption), enrolls with the one-shot
// SP_AGENT_ENROLLMENT_TOKEN, and persists the resulting identity JSON to
// SP_AGENT_KEYS_FILE when writable. It ALWAYS logs the base64 of the same JSON
// so env-only deployments (k8s secrets) can pin it via SP_AGENT_KEYS instead —
// both persistence modes are first-class.
package agentmode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/config"
)

// ErrNoIdentityNoToken is returned when the agent has no persisted identity
// and no enrollment token to mint one with.
var ErrNoIdentityNoToken = errors.New(
	"agent has no persisted identity and no SP_AGENT_ENROLLMENT_TOKEN to enroll with",
)

// fallbackKeysFile is used when the configured keys file's directory is not
// writable (e.g. /data does not exist on a bare-metal run).
const fallbackKeysFile = "./agent-keys.json"

// Run starts agent mode and blocks until ctx is canceled.
func Run(ctx context.Context, cfg *config.Config) error {
	logger := slog.Default().With("component", "agent_mode")

	identity, fromEnv, err := loadIdentity(cfg)
	if err != nil {
		return err
	}

	if identity == nil {
		if cfg.Agent.EnrollmentToken == "" {
			return ErrNoIdentityNoToken
		}

		keys, genErr := agents.GenerateAgentKeys()
		if genErr != nil {
			return genErr
		}

		identity = &backend.Identity{AgentKeys: *keys}
		logger.InfoContext(ctx, "Generated fresh agent keypairs (Ed25519 identity + X25519 encryption)")
	}

	name := cfg.Agent.Name
	if name == "" {
		if hostname, hostErr := os.Hostname(); hostErr == nil {
			name = hostname
		} else {
			name = "agent"
		}
	}

	persist := func(id *backend.Identity) {
		persistIdentity(ctx, cfg, id, fromEnv, logger)
	}

	wsBackend := backend.NewWSBackend(
		cfg.Agent.ServerURL, cfg.Agent.EnrollmentToken, name, identity, persist,
	)

	logger.InfoContext(ctx, "Starting deported agent",
		"server", cfg.Agent.ServerURL,
		"enrolled", identity.AgentUID != "",
		"region", identity.Region)

	worker := checkworker.NewAgentCheckWorker(cfg, wsBackend)

	return worker.Run(ctx)
}

// loadIdentity loads the persisted identity: SP_AGENT_KEYS (base64 JSON) wins,
// then the keys file. Returns (nil, …) when neither exists yet.
func loadIdentity(cfg *config.Config) (*backend.Identity, bool, error) {
	if cfg.Agent.Keys != "" {
		raw, err := base64.StdEncoding.DecodeString(cfg.Agent.Keys)
		if err != nil {
			return nil, false, fmt.Errorf("decode SP_AGENT_KEYS: %w", err)
		}

		var identity backend.Identity
		if err := json.Unmarshal(raw, &identity); err != nil {
			return nil, false, fmt.Errorf("parse SP_AGENT_KEYS: %w", err)
		}

		return &identity, true, nil
	}

	for _, path := range []string{cfg.Agent.KeysFile, fallbackKeysFile} {
		raw, err := os.ReadFile(path) //nolint:gosec // operator-configured path
		if err != nil {
			continue
		}

		var identity backend.Identity
		if err := json.Unmarshal(raw, &identity); err != nil {
			return nil, false, fmt.Errorf("parse %s: %w", path, err)
		}

		return &identity, false, nil
	}

	return nil, false, nil
}

// persistIdentity writes the identity JSON to the keys file (best effort,
// falling back next to the binary) and ALWAYS logs the base64 for env-only
// (SP_AGENT_KEYS) deployments.
func persistIdentity(
	ctx context.Context, cfg *config.Config, identity *backend.Identity, fromEnv bool, logger *slog.Logger,
) {
	raw, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		logger.ErrorContext(ctx, "Failed to serialize agent identity", "error", err)

		return
	}

	encoded := base64.StdEncoding.EncodeToString(raw)

	if !fromEnv {
		written := false

		for _, path := range []string{cfg.Agent.KeysFile, fallbackKeysFile} {
			if path == "" {
				continue
			}

			_ = os.MkdirAll(filepath.Dir(path), 0o700)

			if writeErr := os.WriteFile(path, raw, 0o600); writeErr == nil {
				logger.InfoContext(ctx, "Agent identity persisted", "path", path)

				written = true

				break
			}
		}

		if !written {
			logger.WarnContext(ctx,
				"Could not write the agent keys file anywhere — set SP_AGENT_KEYS to survive restarts")
		}
	}

	// Env-based operation is always possible: the same JSON, base64-encoded.
	logger.InfoContext(ctx,
		"To run this agent from environment only (e.g. a Kubernetes secret), set SP_AGENT_KEYS to the value below",
		"SP_AGENT_KEYS", encoded)
}
