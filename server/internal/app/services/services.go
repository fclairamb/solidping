// Package services provides centralized service registry for dependency injection.
package services

import (
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// Registry holds all application services for dependency injection.
type Registry struct {
	Jobs           jobsvc.Service
	CheckJobs      checkjobsvc.Service
	EventNotifier  notifier.EventNotifier
	EmailSender    email.Sender
	EmailFormatter email.Formatter
	// Credentials encrypts/decrypts secret JSON keys at rest. Always
	// non-nil; .Enabled() reports whether a master key is configured.
	Credentials credentials.Service
	// Entitlements gates per-org limits (MaxSSOUsers / MaxChecksPerMinute).
	// Always non-nil after server bootstrap; safe to call regardless of
	// deployment mode (callers honor nil caps as "unlimited").
	Entitlements *entitlements.Service
	// Clock is the time source for business-logic comparisons (confirmation
	// windows, recovery periods, escalation repeat intervals). Tests inject
	// a Fake to advance time deterministically; production uses Real.
	Clock clock.Clock
	// WebPushOptions holds VAPID credentials for Web Push dispatch. Zero
	// value means "not configured" — callers check VAPIDPublicKey != "".
	WebPushOptions webpush.Options
}

// NewRegistry creates a new services registry.
func NewRegistry() *Registry {
	return &Registry{}
}
