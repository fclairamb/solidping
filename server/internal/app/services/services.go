// Package services provides centralized service registry for dependency injection.
package services

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/support"
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

// DegradedEvaluator runs one sweep of the degraded-detection rules and returns
// how many checks were evaluated.
//
// An interface here for exactly the reason SLOBurnEvaluator is one: the periodic
// job lives in jobs/jobtypes, jobtypes -> jobdef -> app/services, and the
// evaluator -> handlers/incidents -> jobtypes. A concrete field would close that
// loop into an import cycle.
type DegradedEvaluator interface {
	EvaluateDegraded(ctx context.Context, now time.Time) (int, error)
}

// FreshnessSweeper runs one sweep of the check-freshness rule (spec
// 2026-09-25-02) and returns how many checks it moved to stale. An interface
// for the same import-cycle reason as DegradedEvaluator.
type FreshnessSweeper interface {
	SweepStale(ctx context.Context, now time.Time) (int, error)
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
	// Degraded evaluates the per-check degraded-detection rules once a minute
	// (spec 2026-09-22-03). Nil in tests and in processes that run no job
	// worker — the job checks before calling.
	Degraded DegradedEvaluator
	// Freshness moves silent checks to the `stale` status once a minute (spec
	// 2026-09-25-02). Nil in tests and in processes that run no job worker —
	// the job checks before calling.
	Freshness FreshnessSweeper
	// SMS resolves, per org and per capability, whether a phone send goes
	// through the org's own Twilio integration (bring-your-own) or the
	// instance-level provider (server-provided, the default). Nil only in
	// tests that never page a phone.
	SMS *sms.Resolver

	// Support is the instance support inbox (spec 2026-08-22-02): capture of
	// inbound human messages our bots cannot parse, and the retention sweep
	// over them. Nil-guarded by every consumer.
	Support *support.Service

	// Checks is the check write path, exposed to jobs that must delete a check
	// the way a user's DELETE does — resolving open incidents, removing check
	// jobs and waking realtime subscribers — rather than with a raw soft
	// delete. Used by the demo cleanup sweep (spec 2026-09-06-02).
	//
	// An interface rather than *checks.Service so this package keeps no
	// dependency on the handler layer. Nil in processes that build no check
	// service; every consumer nil-guards.
	Checks CheckDeleter

	// PrivateLocationMonitors backfills the liveness monitor of every private
	// location at startup (spec 2026-09-25-05). Same *checks.Service as Checks,
	// behind its own narrow interface. Nil-guarded by the startup job.
	PrivateLocationMonitors PrivateLocationMonitorBackfiller

	// EgressGuard is this process's outbound-connection policy for
	// destinations a user chose: today, notification sender URLs (spec
	// 2026-09-25-20), reusing the guard package check workers already dial
	// through (spec 2026-09-25-19). Built once at startup from
	// config.Config.EgressAllowsPrivateTargets, so every notification send
	// shares one pooled, guarded HTTP transport. Nil in tests that build a
	// bare Registry — every consumer treats nil as "no policy" (allow
	// everything), matching a nil *egress.Guard's own contract.
	EgressGuard *egress.Guard
}

// PrivateLocationMonitorBackfiller is the startup half of the private-location
// liveness monitor lifecycle: every private location gets exactly one monitor
// unless its org opted out.
type PrivateLocationMonitorBackfiller interface {
	BackfillPrivateLocationMonitors(ctx context.Context) (int, error)
}

// CheckDeleter is the narrow slice of the checks service a background sweep
// needs. Kept minimal on purpose: a job that could reach the whole check
// service would be one refactor away from creating checks too.
type CheckDeleter interface {
	DeleteCheck(ctx context.Context, orgSlug, identifier string) error
}

// NewRegistry creates a new services registry.
func NewRegistry() *Registry {
	return &Registry{}
}
