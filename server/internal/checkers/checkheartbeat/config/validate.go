package config

import (
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
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
