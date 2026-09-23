// Package checkemail implements the email check type for passive monitoring
// driven by incoming emails to a unique per-check address.
package checkemail

import (
	"context"
	"errors"

	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkemail/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ErrNotExecutable is returned when Execute is called on an email checker.
// Email checks are passive and handled specially by the worker.
var ErrNotExecutable = errors.New("email checks are passive and cannot be executed directly")

// EmailChecker implements the Checker interface for email passive checks.
type EmailChecker struct{}

// Type returns the check type identifier.
func (c *EmailChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeEmail
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *EmailChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute is not used for email checks. The worker handles them passively.
func (c *EmailChecker) Execute(_ context.Context, _ checkerdef.Config) (*checkerdef.Result, error) {
	return nil, ErrNotExecutable
}
