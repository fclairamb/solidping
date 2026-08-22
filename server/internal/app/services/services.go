// Package services provides centralized service registry for dependency injection.
package services

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// SLOBurnEvaluator runs one sweep of the SLO burn-rate alert policies and
// returns how many were evaluated.
//
// Declared here as an interface rather than as the concrete
// handlers/sloalerts.Service for one hard reason: the periodic job that calls
// it lives in jobs/jobtypes, jobtypes -> jobdef -> app/services, and
// sloalerts -> handlers/incidents -> jobtypes. A concrete field would close
// that loop into an import cycle. The interface breaks it while keeping the
// evaluator itself a normal, directly-testable service.
type SLOBurnEvaluator interface {
	EvaluateBurnRates(ctx context.Context, now time.Time) (int, error)
}

// Registry holds all application services for dependency injection.
type Registry struct {
	Jobs           jobsvc.Service
	CheckJobs      checkjobsvc.Service
	EventNotifier  notifier.EventNotifier
	EmailSender    email.Sender
	EmailFormatter email.Formatter
	// Realtime publishes org-scoped live hint events onto the notifier bus.
	// Nil when SP_REALTIME_ENABLED=false — all Publisher methods are
	// nil-receiver safe, so callers publish unconditionally.
	Realtime *realtime.Publisher
	// Credentials encrypts/decrypts secret JSON keys at rest. Always
	// non-nil; .Enabled() reports whether a master key is configured.
	Credentials credentials.Service
	// Entitlements gates per-org limits (MaxUsers / MaxChecksPerMinute).
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
	// SLOBurn evaluates SLO burn-rate alert policies once a minute. Nil in
	// tests and in processes that run no job worker — the job checks before
	// calling.
	SLOBurn SLOBurnEvaluator
	// SMS resolves, per org and per capability, whether a phone send goes
	// through the org's own Twilio integration (bring-your-own) or the
	// instance-level provider (server-provided, the default). Nil only in
	// tests that never page a phone.
	SMS *sms.Resolver
}

// NewRegistry creates a new services registry.
func NewRegistry() *Registry {
	return &Registry{}
}
