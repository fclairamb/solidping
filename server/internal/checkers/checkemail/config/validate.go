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
