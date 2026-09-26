// Package checkprivatelocation implements the private-location liveness
// monitor (spec 2026-09-25-05): a passive check type that reports whether the
// agents of one of the org's private locations are connected.
//
// It makes no outbound request. It is evaluated on the jobs node by the
// passive evaluator (spec 2026-09-25-04), from agents.last_seen_at, never
// inside the private region it watches — an agent cannot report its own
// absence.
package checkprivatelocation

import (
	"context"
	"errors"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkprivatelocation/config"
)

// PrivateLocationConfig is this check's configuration. It lives in the light
// `config` sub-package; this alias is the name the registry uses.
type PrivateLocationConfig = checkconfig.PrivateLocationConfig

// ErrNotExecutable is returned when Execute is called: the monitor is passive
// and evaluated by the jobs node's passive evaluator.
var ErrNotExecutable = errors.New("private-location checks are passive and cannot be executed directly")

// PrivateLocationChecker implements the Checker interface for the monitor.
type PrivateLocationChecker struct{}

// Type returns the check type identifier.
func (c *PrivateLocationChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypePrivateLocation
}

// Validate checks the configuration offline (see the config sub-package).
func (c *PrivateLocationChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute is not used: the passive evaluator handles the type.
func (c *PrivateLocationChecker) Execute(_ context.Context, _ checkerdef.Config) (*checkerdef.Result, error) {
	return nil, ErrNotExecutable
}
