package checkworker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// The capabilities an agent reports carry one `check-type-*` per registered
// built-in type its activation gate enables (spec 2026-11-26-02).
func TestReportedCapabilitiesListBuiltinTypes(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	selfHosted := newCheckWorker(&config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeSelfHosted}}, newClaimFailBackend(nil))
	types := selfHosted.advertisedBuiltinTypes()
	caps := backend.Capabilities(checkjobsvc.WithBuiltinTypes(context.Background(), types), nil)

	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		r.Contains(caps, models.CheckTypeCapability(string(checkType)), "type %s is missing", checkType)
	}

	r.NoError(models.ValidateCapabilitySet(caps), "every reported name is a valid capability slug")

	// Without any built-in type advertised, no check-type-* is reported.
	r.Empty(checkjobsvc.CheckTypesFromCapabilities(backend.Capabilities(context.Background(), nil)))

	// A type the activation gate disables is not advertised.
	saas := newCheckWorker(&config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeSaaS}}, newClaimFailBackend(nil))
	if saas.activation != nil {
		for _, checkType := range checkerdef.ListCheckTypes(nil) {
			if !saas.activation.IsTypeEnabled(checkType, nil) {
				r.NotContains(saas.advertisedBuiltinTypes(), string(checkType))
			}
		}
	}
}
