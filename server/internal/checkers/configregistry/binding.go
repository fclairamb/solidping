package configregistry

import (
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// BindingCheckFor describes the job a sealed plaintext is about to be used
// for: its type, public config, target and secret fields (specs 2026-11-11-01
// and 2026-11-11-02). A type unknown to this process has no secret field
// list: only keys absent from its public config are then allowed.
func BindingCheckFor(checkType string, config map[string]any) credentials.BindingCheck {
	check := credentials.BindingCheck{Type: checkType, Config: config}

	cfg, known := ParseConfig(checkerdef.CheckType(checkType))
	if known {
		check.TargetFields = credentials.TargetFieldsFor(cfg)
		check.SecretFields = append([]string{}, credentials.SecretFieldsFor(cfg)...)
	} else {
		check.TargetFields = credentials.TargetFieldsFor(nil)
	}

	return check
}
