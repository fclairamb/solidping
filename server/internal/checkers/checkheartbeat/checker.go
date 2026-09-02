// Package checkheartbeat implements the heartbeat check type for passive monitoring.
package checkheartbeat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const tokenLength = 16 // 16 bytes = 32 hex characters

// ErrNotExecutable is returned when Execute is called on a heartbeat checker.
// Heartbeat checks are passive and handled specially by the worker.
var ErrNotExecutable = errors.New("heartbeat checks are passive and cannot be executed directly")

// HeartbeatChecker implements the Checker interface for heartbeat checks.
type HeartbeatChecker struct{}

// Type returns the check type identifier.
func (c *HeartbeatChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeHeartbeat
}

// Validate checks if the configuration is valid and auto-generates a token if not present.
func (c *HeartbeatChecker) Validate(spec *checkerdef.CheckSpec) error {
	if spec.Config == nil {
		spec.Config = make(map[string]any)
	}

	// Auto-generate token if not present
	if _, ok := spec.Config["token"].(string); !ok || spec.Config["token"] == "" {
		token, err := GenerateToken()
		if err != nil {
			return checkerdef.NewConfigError("token", "failed to generate token")
		}

		spec.Config["token"] = token
	}

	// require_hmac must be a real boolean. Rejecting a mistyped value (rather
	// than treating it as absent) is deliberate: this is a security setting,
	// and "the string \"true\" quietly meant false" is not an acceptable way
	// for a check to end up accepting plaintext-token beats.
	if _, err := RequireHMACFromConfig(spec.Config); err != nil {
		return err
	}

	// Auto-generate name and slug if not provided
	if spec.Name == "" {
		spec.Name = "heartbeat"
	}

	if spec.Slug == "" {
		spec.Slug = "heartbeat"
	}

	return nil
}

// Execute is not used for heartbeat checks. The worker handles them specially.
func (c *HeartbeatChecker) Execute(_ context.Context, _ checkerdef.Config) (*checkerdef.Result, error) {
	return nil, ErrNotExecutable
}

// GenerateToken generates a random hex token. Exported so callers outside
// this package (the checks service's rotate-token endpoint) can mint a fresh
// token without duplicating the generation logic.
func GenerateToken() (string, error) {
	b := make([]byte, tokenLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}
