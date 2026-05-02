// Package checkemail implements the email check type for passive monitoring
// driven by incoming emails to a unique per-check address.
package checkemail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// tokenByteLength is the number of random bytes (24 → 48 hex chars).
const tokenByteLength = 24

// ErrNotExecutable is returned when Execute is called on an email checker.
// Email checks are passive and handled specially by the worker.
var ErrNotExecutable = errors.New("email checks are passive and cannot be executed directly")

// EmailChecker implements the Checker interface for email passive checks.
type EmailChecker struct{}

// Type returns the check type identifier.
func (c *EmailChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeEmail
}

// Validate ensures a token is present (auto-generating one if missing) and
// fills sane defaults for name/slug.
func (c *EmailChecker) Validate(spec *checkerdef.CheckSpec) error {
	if spec.Config == nil {
		spec.Config = make(map[string]any)
	}

	if _, ok := spec.Config["token"].(string); !ok || spec.Config["token"] == "" {
		token, err := generateToken()
		if err != nil {
			return checkerdef.NewConfigError("token", "failed to generate token")
		}

		spec.Config["token"] = token
	}

	if spec.Name == "" {
		spec.Name = "email"
	}

	if spec.Slug == "" {
		spec.Slug = "email"
	}

	return nil
}

// Execute is not used for email checks. The worker handles them passively.
func (c *EmailChecker) Execute(_ context.Context, _ checkerdef.Config) (*checkerdef.Result, error) {
	return nil, ErrNotExecutable
}

// generateToken returns a 48-character random hex string. 24 random bytes is
// long enough that we don't need org-scoping in the lookup — the token alone
// identifies the check globally with negligible collision risk.
func generateToken() (string, error) {
	b := make([]byte, tokenByteLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}
