// Package agentmode runs the standard SolidPing binary as a deported
// (org-scoped) check agent (SP_NODE_ROLE=agent, spec 2026-07-16-02): no
// database, no migrations, no HTTP server — the check worker loop over an
// outbound WebSocket to the master.
//
// Identity/keys lifecycle: on first run the agent generates its two keypairs
// (Ed25519 identity + X25519/age encryption), enrolls with the one-shot
// SP_AGENT_ENROLLMENT_TOKEN, and persists the resulting identity JSON to
// SP_AGENT_KEYS_FILE when writable. It NEVER logs that JSON or its base64: the
// file holds private key material (the Ed25519 key is the agent's whole
// authentication, the X25519 identity decrypts every credential sealed to it),
// and stdout is aggregated by fly/Kubernetes/Docker/journald log drains.
//
// Env-only deployments (k8s secrets) pin the identity via SP_AGENT_KEYS; the
// documented way to obtain that value is to read the file the agent already
// wrote (e.g. `base64 -w0 /data/agent-keys.json`). When there is no readable
// file at all, the operator can opt in — deliberately, once — with
// SP_AGENT_PRINT_KEYS=true, which prints the base64 to stdout inside a loud
// banner and logs a key-free WARN that it did so. That flag is honored on every
// start, not only at enrollment, so an already-running agent's value can be
// recovered without shell access.
package agentmode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
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
	} else if cfg.Agent.PrintKeys {
		// Honor the opt-in on every start, not only at enrollment, so an
		// operator can recover the value from an already-enrolled agent
		// without shell access. The enrollment path prints via persist below,
		// and the two are mutually exclusive (no double print).
		printIdentityKeys(ctx, cfg, identity, logger, os.Stdout)
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
		persistIdentity(ctx, cfg, id, fromEnv, logger, os.Stdout)
	}

	wsBackend := backend.NewWSBackend(
		cfg.Agent.ServerURL, cfg.Agent.EnrollmentToken, name, identity, persist,
	)

	// Wire the SSH-tunnel resolver to this agent's in-memory unsealed snapshots.
	// The cloud dispatch path wires a DB-backed resolver in app/server.go; an
	// agent has no DB, so it resolves from the sealed tunnel blocks it unseals on
	// claim (spec 2026-07-18-07). Without this, tunneled jobs fail every
	// execution with "resolver is not configured".
	sshtunnel.ResolverFunc = wsBackend.ResolveTunnel

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
		raw, err := os.ReadFile(path)
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

// keyMaterialBanner delimits the SP_AGENT_PRINT_KEYS output so an operator
// cannot mistake private key material for routine startup noise.
const keyMaterialBanner = "!!! PRIVATE KEY MATERIAL — this will be captured by your log aggregator !!!"

// persistIdentity writes the identity JSON to the keys file (best effort,
// falling back next to the binary). It never logs the identity or its base64;
// the base64 is printed to out only when SP_AGENT_PRINT_KEYS is set.
func persistIdentity(
	ctx context.Context,
	cfg *config.Config,
	identity *backend.Identity,
	fromEnv bool,
	logger *slog.Logger,
	out io.Writer,
) {
	raw, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		logger.ErrorContext(ctx, "Failed to serialize agent identity", "error", err)

		return
	}

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
				"Could not write the agent keys file anywhere — this identity will be lost on restart. "+
					"Mount a writable volume, or start once with SP_AGENT_PRINT_KEYS=true to print the "+
					"SP_AGENT_KEYS value and store it as a secret")
		}
	}

	printKeyMaterial(ctx, cfg, raw, logger, out)
}

// printIdentityKeys serializes an already-loaded identity and hands it to
// printKeyMaterial (which no-ops unless SP_AGENT_PRINT_KEYS is set).
func printIdentityKeys(
	ctx context.Context, cfg *config.Config, identity *backend.Identity, logger *slog.Logger, out io.Writer,
) {
	raw, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		logger.ErrorContext(ctx, "Failed to serialize agent identity", "error", err)

		return
	}

	printKeyMaterial(ctx, cfg, raw, logger, out)
}

// printKeyMaterial emits the base64 identity to out — and only to out, never
// through slog — when the operator explicitly opted in with
// SP_AGENT_PRINT_KEYS. The accompanying WARN carries no key material.
func printKeyMaterial(
	ctx context.Context, cfg *config.Config, raw []byte, logger *slog.Logger, out io.Writer,
) {
	if !cfg.Agent.PrintKeys {
		return
	}

	logger.WarnContext(ctx,
		"SP_AGENT_PRINT_KEYS is set: this agent's private key material was printed to stdout. "+
			"Store it as a secret, then unset the variable and rotate this agent if the output was "+
			"captured by a log aggregator")

	if out == nil {
		return
	}

	encoded := base64.StdEncoding.EncodeToString(raw)

	// Best effort: a failed write to stdout is not worth failing the agent for.
	_, _ = fmt.Fprintf(out, "\n%s\nSP_AGENT_KEYS=%s\n%s\n\n", keyMaterialBanner, encoded, keyMaterialBanner)
}
