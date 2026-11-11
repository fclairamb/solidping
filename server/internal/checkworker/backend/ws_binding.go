package backend

import (
	"context"
	"log/slog"
	"sync"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// unboundWarnings remembers the checks whose unbound browser blob was already
// logged, so the warning is printed once per check and process.
var unboundWarnings sync.Map //nolint:gochecknoglobals // process-wide once-per-check log guard

// bindingCheckFor describes the job a sealed plaintext is about to be used
// for: its type, public config, target and secret fields.
func bindingCheckFor(checkType string, config map[string]any) credentials.BindingCheck {
	check := credentials.BindingCheck{Type: checkType, Config: config}

	cfg, known := registry.ParseConfig(checkerdef.CheckType(checkType))
	if known {
		check.TargetFields = credentials.TargetFieldsFor(cfg)
		check.SecretFields = append([]string{}, credentials.SecretFieldsFor(cfg)...)
	} else {
		check.TargetFields = credentials.TargetFieldsFor(nil)
	}

	return check
}

// openBoundSecrets checks the plaintext of a sealed envelope against the job
// it is used for (spec 2026-11-11-01) and returns the secrets to merge:
//
//   - a blob with a `_binding` must have been sealed for this type and these
//     target fields, and carry only secret fields of the type;
//   - a browser blob without one (sealed before the binding existed) is
//     accepted for now, with a warning logged once per check, but still
//     may not override a public field;
//   - a server blob without one is used as is: the server sealed it from
//     plaintext it held, public `${param:}` overlays included.
func openBoundSecrets(
	ctx context.Context, logger *slog.Logger, checkUID, checkType, envelope string, config, plain map[string]any,
) (map[string]any, error) {
	check := bindingCheckFor(checkType, config)

	secrets, bound, err := credentials.VerifyBinding(plain, check)
	if err != nil || bound {
		return secrets, err
	}

	if credentials.SealedOrigin(envelope) != credentials.SealOriginBrowser {
		return plain, nil
	}

	if strictErr := credentials.StrictSecretKeys(plain, check); strictErr != nil {
		return nil, strictErr
	}

	if _, warned := unboundWarnings.LoadOrStore(checkUID, struct{}{}); !warned {
		logger.WarnContext(ctx,
			"sealed secrets are not bound to their target: seal them again (Re-seal page) to bind them",
			"check_uid", checkUID)
	}

	return plain, nil
}
