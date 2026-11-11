package backend

import (
	"context"
	"log/slog"
	"sync"

	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// unboundWarnings remembers the checks whose unbound browser blob was already
// logged, so the warning is printed once per check and process.
var unboundWarnings sync.Map //nolint:gochecknoglobals // process-wide once-per-check log guard

// openBoundSecrets checks the plaintext of a sealed envelope against the job
// it is used for (credentials.OpenSealedPlaintext, specs 2026-11-11-01 and
// 2026-11-11-02) and returns the secrets to merge. A legacy browser blob
// without a binding is logged once per check.
func openBoundSecrets(
	ctx context.Context, logger *slog.Logger, checkUID, checkType, envelope string, config, plain map[string]any,
) (map[string]any, error) {
	secrets, legacyUnbound, err := credentials.OpenSealedPlaintext(
		envelope, plain, configregistry.BindingCheckFor(checkType, config))
	if err != nil {
		return nil, err
	}

	if legacyUnbound {
		if _, warned := unboundWarnings.LoadOrStore(checkUID, struct{}{}); !warned {
			logger.WarnContext(ctx,
				"sealed secrets are not bound to their target: seal them again (Re-seal page) to bind them",
				"check_uid", checkUID)
		}
	}

	return secrets, nil
}
