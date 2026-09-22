// Package checkheartbeat implements the heartbeat check type for passive monitoring.
package checkheartbeat

import (
	"context"
	"errors"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkheartbeat/config"
)

// ErrNotExecutable is returned when Execute is called on a heartbeat checker.
// Heartbeat checks are passive and handled specially by the worker.
var ErrNotExecutable = errors.New("heartbeat checks are passive and cannot be executed directly")

// HeartbeatChecker implements the Checker interface for heartbeat checks.
type HeartbeatChecker struct{}

// Type returns the check type identifier.
func (c *HeartbeatChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeHeartbeat
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *HeartbeatChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute is not used for heartbeat checks. The worker handles them specially.
func (c *HeartbeatChecker) Execute(_ context.Context, _ checkerdef.Config) (*checkerdef.Result, error) {
	return nil, ErrNotExecutable
}
