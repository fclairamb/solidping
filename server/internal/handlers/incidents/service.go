// Package incidents provides incident management functionality.
package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifications"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Service errors.
var (
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrIncidentNotFound     = errors.New("incident not found")
	ErrSnoozeUntilInPast    = errors.New("snooze 'until' must be in the future")
	ErrSnoozeTooLong        = errors.New("snooze cannot exceed 7 days")
	ErrSnoozeMissingDur     = errors.New("snooze requires either 'until' or 'duration'")
	ErrSnoozeInvalidDur     = errors.New("snooze duration is not a valid Go duration")
	ErrCommentEmpty         = errors.New("comment text must not be empty")
	ErrCommentTooLong       = errors.New("comment text exceeds the maximum length")
)

// MaxCommentLength caps the size of a single incident comment body. Comments
// are free-text operator notes, not documents — 4 KB is generous while
// bounding the row size of the append-only events table.
const MaxCommentLength = 4096

// Comment source values recorded in the event payload.
const (
	// CommentSourceWeb marks a comment authored from the dashboard.
	CommentSourceWeb = "web"
	// CommentSourceSlack marks a comment ingested from a Slack thread reply.
	CommentSourceSlack = "slack"
	// CommentSourceTelegram marks a comment posted with the Telegram bot's
	// /comment command. Unlike Slack, the author is a verified linked contact,
	// so the comment is attributed to their SolidPing user via ActorUID.
	CommentSourceTelegram = "telegram"
)

// Comment event payload keys. camelCase to match the wire shape the dashboard
// reads straight off the event payload (unlike the snake_case lifecycle keys
// above, which predate this convention and are not surfaced verbatim).
const (
	payloadKeyCommentText   = "text"
	payloadKeyCommentSource = "source"
	payloadKeySlackUserID   = "slackUserId"
	payloadKeySlackUserName = "slackUserName"
	payloadKeySlackTeamID   = "slackTeamId"
	payloadKeySlackTs       = "slackTs"
	payloadKeyTelegramChat  = "telegramChatId"
	payloadKeyCommentAuthor = "authorName"
)

// MaxSnoozeDuration caps how long an operator can silence an incident in a
// single call. Beyond a week the request is almost certainly a mistake — for
// genuinely long-term silencing, use a maintenance window or resolve the
// incident manually.
const MaxSnoozeDuration = 7 * 24 * time.Hour

// payloadKeyVia identifies the channel an incident action came from in the
// event payload (web | slack | email | manual | auto). Centralizing the key
// avoids drift across events.
const payloadKeyVia = "via"

// payloadKeyActorUID is the payload key emitEvent reads when a caller wants
// the resulting event row attributed to a specific user instead of the
// default system actor. Used by the manual resolve / ack / snooze paths.
const payloadKeyActorUID = "actor_uid"

// viaWeb identifies an action taken from the dashboard.
const viaWeb = "web"

// JSON event metadata keys.
const (
	keyCheckUID                   = "check_uid"
	keyCheckSlug                  = "check_slug"
	keyCheckName                  = "check_name"
	keyStartedAt                  = "started_at"
	keyResultUID                  = "result_uid"
	keyResolvedAt                 = "resolved_at"
	keyDurationSeconds            = "duration_seconds"
	keyTotalFailures              = "total_failures"
	keyFailureCount               = "failure_count"
	keyEscalationThreshold        = "escalation_threshold"
	keyRelapseCount               = "relapse_count"
	keyEffectiveRecoveryThreshold = "effective_recovery_threshold"
)

// maintenanceWindowCacheTTL bounds how long a check's maintenance-window
// definitions are cached in-process before re-fetching. Windows are mutated
// infrequently and emit no event today, so a 60 s TTL trades a small staleness
// window (≤60 s after a create/edit/delete) for eliminating a DB round-trip on
// the per-result hot path. Active/inactive transitions stay exact because
// models.IsActiveAt(now) is re-evaluated on every result against the cached
// definitions.
const maintenanceWindowCacheTTL = 60 * time.Second

// mwCacheEntry is one check's cached maintenance-window definitions plus the
// clock time they were fetched at.
type mwCacheEntry struct {
	windows   []*models.MaintenanceWindow
	fetchedAt time.Time
}

// Service provides incident management functionality.
type Service struct {
	db      db.Service
	jobsSvc jobsvc.Service
	clock   clock.Clock

	// rt publishes org-scoped live dashboard hints: `results` (coalesced) on
	// every processed result, `checks` (immediate) on a visible status
	// transition, `incidents`+`events` (immediate) alongside event writes.
	// Nil-safe — a nil publisher no-ops (realtime disabled, tests).
	rt *realtime.Publisher

	// mwCache caches per-check maintenance-window definitions with a TTL.
	// Strong-reference map (not internal/utils/cache, whose weak.Pointer
	// values would be GC'd almost immediately here). Keyed by checkUID.
	mwCache   map[string]mwCacheEntry
	mwCacheMu sync.RWMutex
}

// NewService creates a new incident service. rt may be nil (realtime
// disabled): every publish through it is a nil-safe no-op.
func NewService(dbService db.Service, jobsSvc jobsvc.Service, clk clock.Clock, rt *realtime.Publisher) *Service {
	return &Service{
		db:      dbService,
		jobsSvc: jobsSvc,
		clock:   clk,
		rt:      rt,
		mwCache: make(map[string]mwCacheEntry),
	}
}

// isCheckInActiveMaintenance reports whether the check is inside an active
// maintenance window right now. Window definitions are cached per check for
// maintenanceWindowCacheTTL; on a hit within TTL we evaluate models.IsActiveAt
// against the cached slice (so the clock advancing can flip a window active
// mid-TTL without a re-fetch), and on a miss we re-fetch, cache and evaluate.
func (s *Service) isCheckInActiveMaintenance(ctx context.Context, checkUID string) (bool, error) {
	now := s.clock.Now()

	s.mwCacheMu.RLock()
	entry, ok := s.mwCache[checkUID]
	s.mwCacheMu.RUnlock()

	if !ok || now.Sub(entry.fetchedAt) >= maintenanceWindowCacheTTL {
		windows, err := s.db.ListMaintenanceWindowsForCheck(ctx, checkUID)
		if err != nil {
			return false, err
		}

		entry = mwCacheEntry{windows: windows, fetchedAt: now}

		s.mwCacheMu.Lock()
		s.mwCache[checkUID] = entry
		s.mwCacheMu.Unlock()
	}

	for _, window := range entry.windows {
		if models.IsActiveAt(window, now) {
			return true, nil
		}
	}

	return false, nil
}

// ProcessCheckResult processes a check result and manages incidents.
// This is the main entry point called after each check execution.
func (s *Service) ProcessCheckResult(ctx context.Context, check *models.Check, result *models.Result) error {
	// Live hint: a result has been persisted for this org (this runs after
	// every save path — executor, remote worker, heartbeat, email check).
	// Coalesced: the publisher bounds bus traffic to ~1 hint/org/sec.
	s.rt.Publish(ctx, check.OrganizationUID, check.UID, realtime.KindResults)

	if result.Status == nil {
		return nil // Skip results without status
	}

	// Skip incident processing if the check is in an active maintenance window
	inMaintenance, mwErr := s.isCheckInActiveMaintenance(ctx, check.UID)
	if mwErr != nil {
		slog.WarnContext(ctx, "Failed to check maintenance window status",
			"checkUID", check.UID, "error", mwErr)
	}

	if inMaintenance {
		slog.InfoContext(ctx, "Skipping incident processing: check is in maintenance window",
			"checkUID", check.UID)

		return nil
	}

	resultStatus := models.ResultStatus(*result.Status)

	// Determine if this is a success, failure, or warning.
	isSuccess := resultStatus == models.ResultStatusUp
	isFailure := resultStatus == models.ResultStatusDown ||
		resultStatus == models.ResultStatusTimeout ||
		resultStatus == models.ResultStatusError
	// Warning is "up, but something to report": display-only and incident-
	// neutral (modeled on CheckStatusValidating). It updates the visible
	// status and the up<->warning streak edges, but never arms the incident
	// clocks and never opens/resolves an incident.
	isWarning := resultStatus == models.ResultStatusWarning

	if !isSuccess && !isFailure && !isWarning {
		return nil // Skip initial or unknown statuses
	}

	// Look up any active incident first — we need it to decide between
	// `down` (incident open or threshold crossed) and `validating` (failures
	// observed, threshold not crossed yet, no incident open).
	activeIncident, lookupErr := s.db.FindActiveIncidentByCheckUID(ctx, check.UID)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return fmt.Errorf("failed to find active incident: %w", lookupErr)
	}

	now := s.clock.Now()
	newStatus, newStreak, statusChangedAt := deriveCheckStatus(
		check, isSuccess, isFailure, isWarning, activeIncident, now,
	)
	statusChanged := check.Status != newStatus

	// Warning is clock-neutral: it never arms or clears the confirmation /
	// recovery clocks, so a pre-incident failure streak or an open incident's
	// recovery window survives a warning untouched.
	clocks := models.IncidentClockUpdate{}
	if !isWarning {
		clocks = deriveIncidentClocks(check, isSuccess, isFailure, activeIncident, now)
	}

	// Update check status and incident clocks in one atomic UPDATE. Writing
	// both together means a mid-write crash can no longer leave status updated
	// but clocks stale.
	if err := s.db.UpdateCheckStatusAndClocks(
		ctx, check.UID, newStatus, newStreak, statusChangedAt, clocks,
	); err != nil {
		return fmt.Errorf("failed to update check status and clocks: %w", err)
	}

	s.publishStatusHint(ctx, check.OrganizationUID, check.UID, statusChanged)

	// Update local check object for incident logic
	check.Status = newStatus
	check.StatusStreak = newStreak
	if statusChangedAt != nil {
		check.StatusChangedAt = statusChangedAt
	}
	applyClocks(check, clocks, now)

	// Warning never enters the incident state machine: the visible status and
	// streak have been written above; there is nothing to open or resolve.
	if isWarning {
		return nil
	}

	return s.routeCheckResultWithIncident(ctx, check, result, isFailure, activeIncident)
}

// publishStatusHint emits the immediate `checks` live hint when the visible
// check status flipped — dashboards must show a transition within ~2s, so it
// bypasses the coalescing window. No-op when the status is unchanged.
func (s *Service) publishStatusHint(ctx context.Context, orgUID, checkUID string, statusChanged bool) {
	if !statusChanged {
		return
	}
	s.rt.PublishImmediate(ctx, orgUID, checkUID, realtime.KindChecks)
}

// deriveIncidentClocks computes the FirstFailureAt and
// FirstSuccessSinceFailureAt transitions for this result. The flap-reset
// rule is encoded by clearing one when the other is set. The returned
// models.IncidentClockUpdate is tri-state per field: a non-nil pointer sets
// the column, a true Clear* flag sets it to NULL, and the zero value leaves
// the column untouched.
func deriveIncidentClocks(
	check *models.Check, isSuccess, isFailure bool, activeIncident *models.Incident, now time.Time,
) models.IncidentClockUpdate {
	out := models.IncidentClockUpdate{}
	switch {
	case isFailure && activeIncident == nil:
		// First failure (or continuing pre-incident streak): arm the
		// confirmation clock the first time we see a failure on an up check.
		if check.FirstFailureAt == nil {
			t := now
			out.FirstFailureAt = &t
		}
		// This failure may open (or reopen) an incident right after this write,
		// so the recovery clock must not carry over from the *previous*
		// incident: a stale value would let the new incident auto-resolve on
		// its very first success, ignoring the recovery period entirely. The
		// clock is symmetric with FirstFailureAt — each is cleared by the
		// opposite signal.
		if check.FirstSuccessSinceFailureAt != nil {
			out.ClearFirstSuccessSinceFailureAt = true
		}
	case isFailure && activeIncident != nil:
		// Failure during the recovery window resets the recovery clock.
		if check.FirstSuccessSinceFailureAt != nil {
			out.ClearFirstSuccessSinceFailureAt = true
		}
	case isSuccess && activeIncident == nil:
		// Success when no incident is open: clear any pre-incident validating
		// clock so the next failure starts fresh.
		if check.FirstFailureAt != nil {
			out.ClearFirstFailureAt = true
		}
	case isSuccess && activeIncident != nil:
		// First success during an active incident arms the recovery clock.
		//
		// Re-arming a *stale* clock (one predating the incident's onset) rather
		// than only arming an unset one is load-bearing, and pairs with the
		// read-side guard in recoveryElapsed. An incident already open when this
		// code ships carries a clock the clear-on-open above never ran for; the
		// read-side guard would reject that clock forever, and a nil-only check
		// here would never replace it — wedging the incident open until a
		// failure happened to land and clear it. Re-arming makes such rows
		// self-heal on their next success, which is why no backfill migration is
		// needed.
		if staleOrUnarmedRecoveryClock(check, activeIncident) {
			t := now
			out.FirstSuccessSinceFailureAt = &t
		}
	}

	return out
}

// staleOrUnarmedRecoveryClock reports whether the check's recovery clock needs
// to be (re-)armed for this incident: either it was never armed, or it carries
// a value older than the incident's current onset and therefore belongs to a
// previous incident.
func staleOrUnarmedRecoveryClock(check *models.Check, incident *models.Incident) bool {
	if check.FirstSuccessSinceFailureAt == nil {
		return true
	}

	return check.FirstSuccessSinceFailureAt.Before(incidentClockFloor(incident))
}

// incidentClockFloor returns the earliest time a recovery clock may legitimately
// carry for this incident — its current onset: LastReopenedAt when the incident
// has been reopened, StartedAt otherwise. A nil incident yields the zero time,
// so every comparison against the floor is vacuously satisfied and the
// no-incident paths keep their previous behavior.
func incidentClockFloor(incident *models.Incident) time.Time {
	if incident == nil {
		return time.Time{}
	}
	if incident.LastReopenedAt != nil && incident.LastReopenedAt.After(incident.StartedAt) {
		return *incident.LastReopenedAt
	}

	return incident.StartedAt
}

// applyClocks mirrors deriveIncidentClocks's tri-state into the in-memory
// Check, so handleFailure/handleSuccess can read the just-written values.
func applyClocks(check *models.Check, clocks models.IncidentClockUpdate, _ time.Time) {
	if clocks.FirstFailureAt != nil {
		t := *clocks.FirstFailureAt
		check.FirstFailureAt = &t
	} else if clocks.ClearFirstFailureAt {
		check.FirstFailureAt = nil
	}
	if clocks.FirstSuccessSinceFailureAt != nil {
		t := *clocks.FirstSuccessSinceFailureAt
		check.FirstSuccessSinceFailureAt = &t
	} else if clocks.ClearFirstSuccessSinceFailureAt {
		check.FirstSuccessSinceFailureAt = nil
	}
}

// deriveCheckStatus computes the next (status, streak, statusChangedAt) for
// a check given the previous state, this result's success/failure flags, the
// active incident (if any), and the current wall-clock time. Pure function
// to keep ProcessCheckResult under the cyclomatic-complexity cap.
//
// The validating <-> down transitions are treated as sub-states of the same
// failure lifecycle: streak keeps growing across the boundary and
// statusChangedAt is not bumped. Only the up<->failing edge bumps it.
func deriveCheckStatus(
	check *models.Check, isSuccess, isFailure, isWarning bool, activeIncident *models.Incident, now time.Time,
) (models.CheckStatus, int, *time.Time) {
	prevWasFailure := check.Status == models.CheckStatusDown ||
		check.Status == models.CheckStatusValidating

	newStreak, statusChangedAt := deriveStreakAndChange(check, isSuccess, isFailure, isWarning, prevWasFailure, now)

	newStatus := pickStatus(check, isSuccess, isFailure, isWarning, activeIncident, newStreak, now)

	// validating <-> down do not bump statusChangedAt: both are sub-states
	// of "the check is failing right now". Warning is not a failure sub-state,
	// so any edge into or out of warning bumps statusChangedAt normally.
	newIsFailure := newStatus == models.CheckStatusDown ||
		newStatus == models.CheckStatusValidating
	if statusChangedAt == nil && check.Status != newStatus &&
		(!prevWasFailure || !newIsFailure) {
		statusChangedAt = &now
	}

	return newStatus, newStreak, statusChangedAt
}

// deriveStreakAndChange computes the next streak count and whether
// statusChangedAt should bump for the up<->failing (and up<->warning) edge.
// A continuing same-state run (up->up, warning->warning, or failing->failing)
// grows the streak; any other transition resets it to 1 and bumps the change
// timestamp.
func deriveStreakAndChange(
	check *models.Check, isSuccess, isFailure, isWarning, prevWasFailure bool, now time.Time,
) (int, *time.Time) {
	if (isSuccess && check.Status == models.CheckStatusUp) ||
		(isWarning && check.Status == models.CheckStatusWarning) ||
		(isFailure && prevWasFailure) {
		return check.StatusStreak + 1, nil
	}
	t := now

	return 1, &t
}

// pickStatus chooses the externally-visible status for this result. The
// confirmation-elapsed branch falls back to `now` for the very first
// failure (FirstFailureAt isn't persisted yet at this point in
// ProcessCheckResult), which makes the "open immediately"
// (ConfirmationPeriodSeconds == 0) path flip straight to down.
func pickStatus(
	check *models.Check, isSuccess, isFailure, isWarning bool, activeIncident *models.Incident,
	newStreak int, now time.Time,
) models.CheckStatus {
	if isSuccess {
		return models.CheckStatusUp
	}
	// Warning reflects the latest live signal regardless of incident state: it
	// counts as up and never resolves an incident, but the visible status
	// shows what the checker just reported ("up, but something to report").
	if isWarning {
		return models.CheckStatusWarning
	}
	if activeIncident != nil {
		return models.CheckStatusDown
	}
	if isFailure && confirmationElapsedDerive(check, newStreak, now) {
		return models.CheckStatusDown
	}

	return models.CheckStatusValidating
}

// confirmationElapsedDerive is the in-derive variant of confirmationElapsed:
// it accepts an in-flight newStreak so a not-yet-persisted FirstFailureAt
// can still be reasoned about as "the failure starts right now".
func confirmationElapsedDerive(check *models.Check, newStreak int, now time.Time) bool {
	var firstFailure time.Time
	switch {
	case check.FirstFailureAt != nil:
		firstFailure = *check.FirstFailureAt
	case newStreak == 1:
		firstFailure = now
	default:
		firstFailure = now
	}
	confirmation := time.Duration(check.ConfirmationPeriodSeconds) * time.Second

	return !now.Before(firstFailure.Add(confirmation))
}

// routeCheckResultWithIncident dispatches to the per-check or group state
// machine using a pre-fetched active incident (or nil). Extracted from
// ProcessCheckResult to keep cyclomatic complexity manageable; the
// pre-fetched incident lets the validating-state derivation upstream and the
// state-machine downstream agree on what the active incident is for this
// result.
func (s *Service) routeCheckResultWithIncident(
	ctx context.Context, check *models.Check, result *models.Result, isFailure bool,
	incident *models.Incident,
) error {
	// Route uses the active incident's CheckGroupUID first; falls back to the
	// check's group when there's no active incident yet.
	isGroup := (incident != nil && incident.CheckGroupUID != nil) ||
		(incident == nil && check.CheckGroupUID != nil)

	if isFailure {
		if isGroup {
			return s.handleGroupFailure(ctx, check, result, incident)
		}

		return s.handleFailure(ctx, check, result, incident)
	}

	if isGroup {
		return s.handleGroupSuccess(ctx, check, result, incident)
	}

	return s.handleSuccess(ctx, check, result, incident)
}

// handleFailure handles a failed check result.
func (s *Service) handleFailure(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	if incident == nil && !confirmationElapsed(check, s.clock.Now()) {
		// Confirmation window hasn't elapsed; stay validating.
		return nil
	}

	if incident == nil {
		// Threshold met - try to reopen a recently resolved incident before creating a new one
		return s.createOrReopenIncident(ctx, check, result)
	}

	// Update existing incident
	newFailureCount := incident.FailureCount + 1
	update := models.IncidentUpdate{
		FailureCount: &newFailureCount,
	}

	// Check if we should escalate (once per incident)
	if incident.EscalatedAt == nil && newFailureCount >= check.EscalationThreshold {
		now := s.clock.Now()
		update.EscalatedAt = &now
		// Emit escalation event
		if err := s.emitEvent(ctx, check.OrganizationUID, models.EventTypeIncidentEscalated, incident, models.JSONMap{
			keyCheckUID:            check.UID,
			keyCheckSlug:           check.Slug,
			keyCheckName:           check.Name,
			keyFailureCount:        newFailureCount,
			keyEscalationThreshold: check.EscalationThreshold,
		}); err != nil {
			return fmt.Errorf("failed to emit escalation event: %w", err)
		}
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to update incident: %w", err)
	}

	return nil
}

// handleSuccess handles a successful check result.
func (s *Service) handleSuccess(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	if incident == nil {
		// No active incident - nothing to do
		return nil
	}

	if recoveryElapsed(check, incident, s.clock.Now()) {
		return s.resolveIncident(ctx, check, result, incident)
	}

	// Recovery window hasn't elapsed - incident remains active.
	return nil
}

// confirmationElapsed reports whether the configured ConfirmationPeriod
// has passed since the check first started failing. A 0-second period
// means "open immediately on first failure".
func confirmationElapsed(check *models.Check, now time.Time) bool {
	if check.FirstFailureAt == nil {
		return false
	}
	period := time.Duration(check.ConfirmationPeriodSeconds) * time.Second

	return !now.Before(check.FirstFailureAt.Add(period))
}

// recoveryElapsed reports whether the *effective* RecoveryPeriod has passed
// since the first success arrived during the active incident. The effective
// period is flap-aware (see effectiveRecoveryPeriod): a flapping check must
// stay stable progressively longer before auto-resolving. A 0-second base
// period means "resolve immediately on first success".
//
// The clock is scoped to `incident`: a FirstSuccessSinceFailureAt older than
// the incident's onset (see incidentClockFloor) belongs to a previous incident
// and can never resolve this one. That makes the invariant structural rather
// than dependent on every transition remembering to clear the clock.
func recoveryElapsed(check *models.Check, incident *models.Incident, now time.Time) bool {
	if check.FirstSuccessSinceFailureAt == nil {
		return false
	}
	if check.FirstSuccessSinceFailureAt.Before(incidentClockFloor(incident)) {
		return false
	}

	return !now.Before(check.FirstSuccessSinceFailureAt.Add(effectiveRecoveryPeriod(check)))
}

// recoveryHardCeiling bounds the effective recovery period in wall-clock time,
// mirroring the existing reopen-cooldown clamp (maxCooldown). Even a long
// backoff or a large multiplier can never push the required stability beyond
// this.
const recoveryHardCeiling = 30 * time.Minute

// effectiveRecoveryPeriod returns the stability required before an incident
// auto-resolves, given how much the check has been flapping:
//
//	effective = min( R · F^flapCount , R · MaxRecoveryMultiplier , HARD_CEILING )
//
// where R = RecoveryPeriodSeconds and F = FlapBackoffFactor. It short-circuits
// to a plain R (today's constant behavior) when the flapping feature is off
// for this check — F<=1, FlappingWindowSeconds==0, or no flaps accumulated yet
// — so existing checks never regress.
func effectiveRecoveryPeriod(check *models.Check) time.Duration {
	base := time.Duration(check.RecoveryPeriodSeconds) * time.Second

	if check.FlapBackoffFactor <= 1 || check.FlappingWindowSeconds == 0 || check.FlapCount <= 0 {
		return base
	}

	// Cap multiplier: required recovery never exceeds R × MaxRecoveryMultiplier.
	capMult := check.MaxRecoveryMultiplier
	if capMult < 1 {
		capMult = 1
	}

	// Compute F^flapCount in integer space, short-circuiting once it reaches or
	// exceeds the cap so a large flapCount can't overflow.
	multiplier := 1
	for range check.FlapCount {
		multiplier *= check.FlapBackoffFactor
		if multiplier >= capMult {
			multiplier = capMult

			break
		}
	}

	effective := base * time.Duration(multiplier)
	if effective > recoveryHardCeiling {
		effective = recoveryHardCeiling
	}

	return effective
}

// bumpFlap updates the check's rolling flap counter for an outage onset at
// `now`. If this is the first-ever outage, or the previous outage is older
// than the flapping window, the counter resets to 0 (this outage starts a
// fresh window). Otherwise it increments — the outage lands inside the active
// window and counts as a flap. LastOutageAt is always advanced to `now`.
//
// Mutates the in-memory check; the caller persists flap_count / last_outage_at.
func bumpFlap(check *models.Check, now time.Time) {
	window := time.Duration(check.FlappingWindowSeconds) * time.Second

	if check.LastOutageAt == nil || window == 0 || now.Sub(*check.LastOutageAt) > window {
		check.FlapCount = 0
	} else {
		check.FlapCount++
	}

	t := now
	check.LastOutageAt = &t
}

// recordFlap bumps the in-memory flap state for an outage onset and persists
// flap_count / last_outage_at to the check row. Best-effort: a persistence
// failure is logged but never blocks incident creation/reopen — the in-memory
// value still drives this result's effective recovery threshold. Called only
// on the rare incident open/reopen, so the extra write is off the per-result
// hot path (spec 2026-06-30-07).
func (s *Service) recordFlap(ctx context.Context, check *models.Check) {
	bumpFlap(check, s.clock.Now())

	if check.LastOutageAt == nil {
		return
	}

	if err := s.db.UpdateCheckFlapState(ctx, check.UID, check.FlapCount, *check.LastOutageAt); err != nil {
		slog.WarnContext(ctx, "Failed to persist flap state",
			"checkUID", check.UID, "error", err)
	}
}

// createIncident creates a new incident.
func (s *Service) createIncident(ctx context.Context, check *models.Check, result *models.Result) error {
	// Record this outage onset against the rolling flapping window before the
	// incident is created, so the new incident's effective recovery threshold
	// already reflects the (possibly escalated) flap level.
	s.recordFlap(ctx, check)

	// Generate title
	title := s.generateIncidentTitle(check)

	incident := models.NewIncident(check.OrganizationUID, check.UID, result.PeriodStart, title)
	incident.Details = failureDetails(result)

	// Roll up under a hard parent if one is open within the correlation window.
	s.applyRollup(ctx, check, incident)

	if err := s.db.CreateIncident(ctx, incident); err != nil {
		return fmt.Errorf("failed to create incident: %w", err)
	}

	// Emit incident created event
	if err := s.emitEvent(ctx, check.OrganizationUID, models.EventTypeIncidentCreated, incident, models.JSONMap{
		keyCheckUID:  check.UID,
		keyCheckSlug: check.Slug,
		keyCheckName: check.Name,
		keyStartedAt: result.PeriodStart,
		keyResultUID: result.UID,
	}); err != nil {
		return fmt.Errorf("failed to emit incident created event: %w", err)
	}

	return nil
}

// resolveIncident resolves an active incident.
func (s *Service) resolveIncident(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	resolvedState := models.IncidentStateResolved
	resolvedAt := result.PeriodStart

	update := models.IncidentUpdate{
		State:      &resolvedState,
		ResolvedAt: &resolvedAt,
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to resolve incident: %w", err)
	}

	// Calculate duration
	durationSeconds := int64(resolvedAt.Sub(incident.StartedAt).Seconds())

	// Emit incident resolved event
	if err := s.emitEvent(ctx, check.OrganizationUID, models.EventTypeIncidentResolved, incident, models.JSONMap{
		keyCheckUID:        check.UID,
		keyCheckSlug:       check.Slug,
		keyCheckName:       check.Name,
		keyResolvedAt:      resolvedAt,
		keyDurationSeconds: durationSeconds,
		keyTotalFailures:   incident.FailureCount,
	}); err != nil {
		return fmt.Errorf("failed to emit incident resolved event: %w", err)
	}

	// Re-evaluate any rolled-up children: those still down need to page now.
	if err := s.reEvaluateRollupChildren(ctx, incident); err != nil {
		slog.WarnContext(ctx, "Failed to re-evaluate rollup children",
			"parentIncidentUid", incident.UID, "error", err)
	}

	return nil
}

// createOrReopenIncident tries to reopen a recently resolved incident, or creates a new one.
func (s *Service) createOrReopenIncident(
	ctx context.Context, check *models.Check, result *models.Result,
) error {
	reopened, err := s.tryReopenIncident(ctx, check, result)
	if err != nil {
		return err
	}
	if reopened {
		return nil
	}

	return s.createIncident(ctx, check, result)
}

const defaultCooldownMultiplier = 5

// calculateCooldown returns the cooldown duration for incident reopening based on check period.
func calculateCooldown(check *models.Check) time.Duration {
	multiplier := defaultCooldownMultiplier
	if check.ReopenCooldownMultiplier != nil {
		multiplier = *check.ReopenCooldownMultiplier
	}

	if multiplier == 0 {
		return 0 // Reopening disabled
	}

	period := time.Duration(check.Period)
	cooldown := time.Duration(multiplier) * period

	const minCooldown = 2 * time.Minute
	const maxCooldown = 30 * time.Minute

	if cooldown < minCooldown {
		cooldown = minCooldown
	}
	if cooldown > maxCooldown {
		cooldown = maxCooldown
	}

	return cooldown
}

// effectiveRecoveryPeriodSeconds reports the *adaptive* recovery threshold (in
// seconds) currently in force for this check, so the reopen-event payload's
// `effective_recovery_threshold` carries the true number downstream
// notifications should show. With flapping off (or no flaps accumulated) this
// equals the configured RecoveryPeriodSeconds; with flapping active it reflects
// the per-flap backoff and cap. See spec 2026-06-30-07.
func effectiveRecoveryPeriodSeconds(check *models.Check) int {
	return int(effectiveRecoveryPeriod(check) / time.Second)
}

// tryReopenIncident looks for a recently resolved incident and reopens it if appropriate.
// Returns true if an incident was reopened.
func (s *Service) tryReopenIncident(
	ctx context.Context, check *models.Check, result *models.Result,
) (bool, error) {
	cooldown := calculateCooldown(check)
	if cooldown == 0 {
		return false, nil // Reopening disabled for this check
	}
	since := s.clock.Now().Add(-cooldown)

	incident, err := s.db.FindRecentlyResolvedIncidentByCheckUID(ctx, check.UID, since)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("failed to find recently resolved incident: %w", err)
	}

	// Guard: don't reopen manually resolved incidents (has acknowledged_by)
	if incident.AcknowledgedBy != nil {
		return false, nil
	}

	// We intentionally do not gate reopening on check.UpdatedAt vs ResolvedAt.
	// UpdatedAt is bumped by every result write (status, streak and the incident
	// clocks all persist through UpdateCheckStatusAndClocks), so it churns on each
	// probe and is always after ResolvedAt — using it here blocked every
	// legitimate fast relapse. Suppressing a reopen after a genuine config change
	// would need a dedicated config-modified timestamp, which does not exist yet;
	// within the short reopen cooldown the reattach is the right behavior.

	return true, s.reopenIncident(ctx, check, result, incident)
}

// reopenIncident reopens a previously resolved incident.
func (s *Service) reopenIncident(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	now := s.clock.Now()

	// A reopen is an outage onset too — and a fast relapse is the strongest
	// flap signal — so it counts against the flapping window. Bump before
	// emitting the reopened event so keyEffectiveRecoveryThreshold reflects the
	// escalated level.
	s.recordFlap(ctx, check)

	activeState := models.IncidentStateActive
	newRelapseCount := incident.RelapseCount + 1
	newFailureCount := incident.FailureCount + 1
	newDetails := lastFailureDetails(incident.Details, result)

	update := models.IncidentUpdate{
		State:               &activeState,
		ClearResolvedAt:     true,
		RelapseCount:        &newRelapseCount,
		LastReopenedAt:      &now,
		FailureCount:        &newFailureCount,
		Details:             &newDetails,
		ClearAcknowledgedAt: true,
		ClearAcknowledgedBy: true,
	}

	// Re-evaluate rollup on reopen — a hard parent could have started failing
	// in the cooldown window between resolve and reopen.
	tmp := *incident
	tmp.StartedAt = result.PeriodStart
	tmp.PagingSuppressed = false
	tmp.CausedByIncidentUID = nil
	s.applyRollup(ctx, check, &tmp)

	suppressed := tmp.PagingSuppressed
	update.PagingSuppressed = &suppressed

	if tmp.CausedByIncidentUID != nil {
		update.CausedByIncidentUID = tmp.CausedByIncidentUID
	} else {
		update.ClearCausedByIncidentUID = true
	}

	incident.PagingSuppressed = suppressed
	incident.CausedByIncidentUID = tmp.CausedByIncidentUID
	incident.Details = newDetails

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to reopen incident: %w", err)
	}

	// Pass check fields to the incident for notification payload
	incident.RelapseCount = newRelapseCount

	if err := s.emitEvent(ctx, check.OrganizationUID, models.EventTypeIncidentReopened, incident, models.JSONMap{
		keyCheckUID:                   check.UID,
		keyCheckSlug:                  check.Slug,
		keyCheckName:                  check.Name,
		keyRelapseCount:               newRelapseCount,
		keyResultUID:                  result.UID,
		keyEffectiveRecoveryThreshold: effectiveRecoveryPeriodSeconds(check),
	}); err != nil {
		return fmt.Errorf("failed to emit incident reopened event: %w", err)
	}

	return nil
}

// formatGroupTitle returns "<group> — N/M checks down". Used for both create
// and rebuild — callers pass the current failing/total counts.
func formatGroupTitle(group *models.CheckGroup, failing, total int) string {
	name := "Group"
	if group != nil {
		name = group.Name
	}

	return fmt.Sprintf("%s — %d/%d checks down", name, failing, total)
}

// countEnabledGroupMembers returns the number of enabled checks in the group.
// Used to compute the M in the "N/M" title.
func (s *Service) countEnabledGroupMembers(ctx context.Context, orgUID, groupUID string) int {
	filter := &models.ListChecksFilter{
		CheckGroupUID: &groupUID,
	}

	checks, _, err := s.db.ListChecks(ctx, orgUID, filter)
	if err != nil {
		slog.WarnContext(ctx, "Failed to list group members for title",
			"groupUID", groupUID, "error", err)

		return 0
	}

	return len(checks)
}

// handleGroupFailure handles a failed result for a check that belongs to a group.
func (s *Service) handleGroupFailure(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	if incident == nil && !confirmationElapsed(check, s.clock.Now()) {
		return nil
	}

	if incident == nil {
		return s.createOrReopenGroupIncident(ctx, check, result)
	}

	// Group incident is active — update or insert this check's member row.
	return s.updateGroupMemberOnFailure(ctx, check, result, incident)
}

// updateGroupMemberOnFailure handles a check failure when the group incident exists.
func (s *Service) updateGroupMemberOnFailure(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	member, err := s.db.GetIncidentMemberCheck(ctx, incident.UID, check.UID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to load incident member: %w", err)
	}

	now := s.clock.Now()

	if member == nil {
		// New member joining an active incident.
		newMember := &models.IncidentMemberCheck{
			IncidentUID:      incident.UID,
			CheckUID:         check.UID,
			JoinedAt:         now,
			FirstFailureAt:   result.PeriodStart,
			LastFailureAt:    result.PeriodStart,
			FailureCount:     1,
			CurrentlyFailing: true,
		}
		if err := s.db.UpsertIncidentMemberCheck(ctx, newMember); err != nil {
			return fmt.Errorf("failed to insert new member: %w", err)
		}
	} else {
		// Bump the member's failure count, mark currently_failing, advance the
		// last_failure_at timestamp. Same path for "still failing" and "relapse".
		newCount := member.FailureCount + 1
		failing := true
		mUpdate := &models.IncidentMemberUpdate{
			LastFailureAt:    &result.PeriodStart,
			FailureCount:     &newCount,
			CurrentlyFailing: &failing,
		}
		if err := s.db.UpdateIncidentMemberCheck(ctx, incident.UID, check.UID, mUpdate); err != nil {
			return fmt.Errorf("failed to update member: %w", err)
		}
	}

	// Bump the incident's aggregate failure count and re-evaluate escalation.
	newFailureCount := incident.FailureCount + 1
	update := &models.IncidentUpdate{FailureCount: &newFailureCount}

	if incident.EscalatedAt == nil {
		// Per-spec: escalation fires the first time any individual member crosses
		// its own escalation threshold.
		var memberFailureCount int
		if member != nil {
			memberFailureCount = member.FailureCount + 1
		} else {
			memberFailureCount = 1
		}

		if memberFailureCount >= check.EscalationThreshold {
			update.EscalatedAt = &now
		}
	}

	// Rebuild title with the latest failing/total counts.
	failing, total := s.groupCounts(ctx, incident, check)
	if failing > 0 && total > 0 {
		group, _ := s.db.GetCheckGroup(ctx, check.OrganizationUID, *incident.CheckGroupUID)
		title := formatGroupTitle(group, failing, total)
		update.Title = &title
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, update); err != nil {
		return fmt.Errorf("failed to update group incident: %w", err)
	}

	if update.EscalatedAt != nil {
		if err := s.emitEvent(
			ctx, check.OrganizationUID, models.EventTypeIncidentEscalated, incident, models.JSONMap{
				keyCheckUID:            check.UID,
				keyCheckSlug:           check.Slug,
				keyCheckName:           check.Name,
				keyFailureCount:        newFailureCount,
				keyEscalationThreshold: check.EscalationThreshold,
			}); err != nil {
			return fmt.Errorf("failed to emit group escalation event: %w", err)
		}
	}

	return nil
}

// groupCounts returns (failing, total) for an active group incident. `total`
// is the count of currently-enabled checks in the group at evaluation time.
func (s *Service) groupCounts(
	ctx context.Context, incident *models.Incident, check *models.Check,
) (int, int) {
	if incident == nil || incident.CheckGroupUID == nil {
		return 0, 0
	}

	failing, err := s.db.CountFailingIncidentMembers(ctx, incident.UID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to count failing members",
			"incidentUID", incident.UID, "error", err)
	}

	total := s.countEnabledGroupMembers(ctx, check.OrganizationUID, *incident.CheckGroupUID)

	return failing, total
}

// createOrReopenGroupIncident tries to reopen a recently-resolved group
// incident, otherwise creates a new one with this check as the trigger.
func (s *Service) createOrReopenGroupIncident(
	ctx context.Context, check *models.Check, result *models.Result,
) error {
	if check.CheckGroupUID == nil {
		return nil
	}

	cooldown := calculateCooldown(check)
	if cooldown > 0 {
		since := s.clock.Now().Add(-cooldown)

		incident, err := s.db.FindRecentlyResolvedIncidentByGroupUID(ctx, *check.CheckGroupUID, since)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to find recently resolved group incident: %w", err)
		}

		if incident != nil && incident.AcknowledgedBy == nil {
			return s.reopenGroupIncident(ctx, check, result, incident)
		}
	}

	return s.createGroupIncident(ctx, check, result)
}

// createGroupIncident inserts a new group incident with this check as trigger
// and inserts the first member row.
func (s *Service) createGroupIncident(
	ctx context.Context, check *models.Check, result *models.Result,
) error {
	if check.CheckGroupUID == nil {
		return nil
	}

	group, _ := s.db.GetCheckGroup(ctx, check.OrganizationUID, *check.CheckGroupUID)
	totalMembers := s.countEnabledGroupMembers(ctx, check.OrganizationUID, *check.CheckGroupUID)
	if totalMembers == 0 {
		totalMembers = 1
	}

	title := formatGroupTitle(group, 1, totalMembers)
	incident := models.NewIncident(check.OrganizationUID, check.UID, result.PeriodStart, title)
	incident.CheckGroupUID = check.CheckGroupUID
	incident.Details = failureDetails(result)

	if err := s.db.CreateIncident(ctx, incident); err != nil {
		// Race: another concurrent failure beat us to it. Re-fetch the existing
		// active group incident and attach this check as a member.
		if isUniqueConstraintError(err) {
			existing, lookupErr := s.db.FindActiveIncidentByGroupUID(ctx, *check.CheckGroupUID)
			if lookupErr != nil {
				return fmt.Errorf("group incident race: lookup after unique violation: %w", lookupErr)
			}

			slog.WarnContext(ctx, "group incident race resolved, joined existing",
				"incidentUid", existing.UID, "checkUid", check.UID)

			return s.updateGroupMemberOnFailure(ctx, check, result, existing)
		}

		return fmt.Errorf("failed to create group incident: %w", err)
	}

	member := &models.IncidentMemberCheck{
		IncidentUID:      incident.UID,
		CheckUID:         check.UID,
		JoinedAt:         s.clock.Now(),
		FirstFailureAt:   result.PeriodStart,
		LastFailureAt:    result.PeriodStart,
		FailureCount:     1,
		CurrentlyFailing: true,
	}
	if err := s.db.UpsertIncidentMemberCheck(ctx, member); err != nil {
		return fmt.Errorf("failed to insert trigger member: %w", err)
	}

	if err := s.emitEvent(
		ctx, check.OrganizationUID, models.EventTypeIncidentCreated, incident, models.JSONMap{
			keyCheckUID:  check.UID,
			keyCheckSlug: check.Slug,
			keyCheckName: check.Name,
			keyStartedAt: result.PeriodStart,
			keyResultUID: result.UID,
		}); err != nil {
		return fmt.Errorf("failed to emit group incident created event: %w", err)
	}

	return nil
}

// reopenGroupIncident reopens a previously-resolved group incident, marking
// this check as a (possibly re-joining) member.
func (s *Service) reopenGroupIncident(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	now := s.clock.Now()
	activeState := models.IncidentStateActive
	newRelapseCount := incident.RelapseCount + 1
	newFailureCount := incident.FailureCount + 1
	newDetails := lastFailureDetails(incident.Details, result)

	update := models.IncidentUpdate{
		State:               &activeState,
		ClearResolvedAt:     true,
		RelapseCount:        &newRelapseCount,
		LastReopenedAt:      &now,
		FailureCount:        &newFailureCount,
		Details:             &newDetails,
		ClearAcknowledgedAt: true,
		ClearAcknowledgedBy: true,
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to reopen group incident: %w", err)
	}

	incident.Details = newDetails

	// Reset / insert this check's member row to "currently failing".
	failing := true
	one := 1
	mUpdate := &models.IncidentMemberUpdate{
		LastFailureAt:    &result.PeriodStart,
		FailureCount:     &one,
		CurrentlyFailing: &failing,
	}
	if err := s.db.UpdateIncidentMemberCheck(ctx, incident.UID, check.UID, mUpdate); err != nil {
		// If no row exists yet (new member on reopen), insert one.
		member := &models.IncidentMemberCheck{
			IncidentUID:      incident.UID,
			CheckUID:         check.UID,
			JoinedAt:         now,
			FirstFailureAt:   result.PeriodStart,
			LastFailureAt:    result.PeriodStart,
			FailureCount:     1,
			CurrentlyFailing: true,
		}
		if upErr := s.db.UpsertIncidentMemberCheck(ctx, member); upErr != nil {
			return fmt.Errorf("failed to upsert reopen member: %w", upErr)
		}
	}

	incident.RelapseCount = newRelapseCount

	if err := s.emitEvent(
		ctx, check.OrganizationUID, models.EventTypeIncidentReopened, incident, models.JSONMap{
			keyCheckUID:                   check.UID,
			keyCheckSlug:                  check.Slug,
			keyCheckName:                  check.Name,
			keyRelapseCount:               newRelapseCount,
			keyResultUID:                  result.UID,
			keyEffectiveRecoveryThreshold: effectiveRecoveryPeriodSeconds(check),
		}); err != nil {
		return fmt.Errorf("failed to emit group reopened event: %w", err)
	}

	return nil
}

// handleGroupSuccess handles a successful result for a check whose group has
// an active incident.
func (s *Service) handleGroupSuccess(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	if incident == nil {
		return nil
	}

	member, err := s.db.GetIncidentMemberCheck(ctx, incident.UID, check.UID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		return fmt.Errorf("failed to load member on success: %w", err)
	}

	if !member.CurrentlyFailing {
		return nil
	}

	if !recoveryElapsed(check, incident, s.clock.Now()) {
		return nil
	}

	// Mark this member recovered.
	failing := false
	recoveredAt := result.PeriodStart
	mUpdate := &models.IncidentMemberUpdate{
		LastRecoveryAt:   &recoveredAt,
		CurrentlyFailing: &failing,
	}
	if updErr := s.db.UpdateIncidentMemberCheck(ctx, incident.UID, check.UID, mUpdate); updErr != nil {
		return fmt.Errorf("failed to mark member recovered: %w", updErr)
	}

	failingCount, err := s.db.CountFailingIncidentMembers(ctx, incident.UID)
	if err != nil {
		return fmt.Errorf("failed to count failing members: %w", err)
	}

	if failingCount == 0 {
		return s.resolveGroupIncident(ctx, check, result, incident)
	}

	// Some members still failing — rebuild title.
	total := s.countEnabledGroupMembers(ctx, check.OrganizationUID, *incident.CheckGroupUID)
	if total == 0 {
		total = failingCount
	}

	group, _ := s.db.GetCheckGroup(ctx, check.OrganizationUID, *incident.CheckGroupUID)
	title := formatGroupTitle(group, failingCount, total)

	if err := s.db.UpdateIncident(ctx, incident.UID, &models.IncidentUpdate{Title: &title}); err != nil {
		return fmt.Errorf("failed to rebuild group title: %w", err)
	}

	return nil
}

// resolveGroupIncident resolves a group incident when its last failing member recovers.
func (s *Service) resolveGroupIncident(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	resolvedState := models.IncidentStateResolved
	resolvedAt := result.PeriodStart

	update := models.IncidentUpdate{
		State:      &resolvedState,
		ResolvedAt: &resolvedAt,
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to resolve group incident: %w", err)
	}

	durationSeconds := int64(resolvedAt.Sub(incident.StartedAt).Seconds())

	if err := s.emitEvent(
		ctx, check.OrganizationUID, models.EventTypeIncidentResolved, incident, models.JSONMap{
			keyCheckUID:        check.UID,
			keyCheckSlug:       check.Slug,
			keyCheckName:       check.Name,
			keyResolvedAt:      resolvedAt,
			keyDurationSeconds: durationSeconds,
			keyTotalFailures:   incident.FailureCount,
		}); err != nil {
		return fmt.Errorf("failed to emit group resolved event: %w", err)
	}

	return nil
}

// generateIncidentTitle generates a title for an incident.
func (s *Service) generateIncidentTitle(check *models.Check) string {
	if check.Slug != nil && *check.Slug != "" {
		return *check.Slug + " is down"
	}
	if check.Name != nil && *check.Name != "" {
		return *check.Name + " is down"
	}
	return "Check is down"
}

// emitEvent creates an event for the incident lifecycle. When the payload
// carries a non-empty `actor_uid` string, the recorded event is attributed
// to that user instead of the default system actor — used by the manual
// resolve / ack / snooze paths so the audit trail keeps the operator UID.
func (s *Service) emitEvent(
	ctx context.Context, orgUID string, eventType models.EventType, incident *models.Incident, payload models.JSONMap,
) error {
	actorType := models.ActorTypeSystem
	var actorUID *string
	if payload != nil {
		if uid, ok := payload[payloadKeyActorUID].(string); ok && uid != "" {
			actorType = models.ActorTypeUser
			actorUID = &uid
		}
	}

	event := models.NewEvent(orgUID, eventType, actorType)
	event.IncidentUID = &incident.UID
	event.CheckUID = &incident.CheckUID
	event.ActorUID = actorUID
	event.Payload = payload

	if err := s.db.CreateEvent(ctx, event); err != nil {
		return err
	}

	// Live hint: incident lifecycle and its timeline event are visible state —
	// immediate so an opening incident reaches watching dashboards instantly.
	s.rt.PublishImmediate(ctx, orgUID, incident.CheckUID, realtime.KindIncidents, realtime.KindEvents)

	// Queue notifications for incident lifecycle events
	switch eventType {
	case models.EventTypeIncidentCreated, models.EventTypeIncidentResolved, models.EventTypeIncidentEscalated,
		models.EventTypeIncidentReopened:
		if incident.PagingSuppressed {
			// Rolled-up child: record the lifecycle event (already done above
			// via CreateEvent) but do not page. Paging is suppressed for the
			// child's entire lifecycle — opened, escalated, and resolved.
			// Timeline observers see closure through the event row / dashboard,
			// not a paging channel. A child that is still down when its parent
			// resolves is un-suppressed first by reEvaluateChild (paging_suppressed
			// flips to false), so it pages normally on its own later resolution.
			return nil
		}

		// People who were PAGED for this incident only ever hear from the
		// escalation machinery, which stops the moment the incident is handled.
		// Without this they are paged and then never told it ended. Enqueued
		// before the group branch on purpose: a group incident pages under its
		// own UID, so the notice job works identically for it.
		if eventType == models.EventTypeIncidentResolved {
			s.queueResolutionNotice(ctx, orgUID, incident.UID)
		}

		if incident.CheckGroupUID != nil {
			s.queueGroupNotifications(ctx, orgUID, incident.UID, eventType)

			return nil
		}

		checkUID, _ := payload["check_uid"].(string)
		if checkUID == "" {
			slog.WarnContext(ctx, "Missing check_uid in event payload", "eventType", eventType)

			return nil
		}
		s.queueNotifications(ctx, orgUID, checkUID, incident.UID, eventType)

		// Escalation policy fan-out: only on initial open. Resolved /
		// reopened don't start a new paging cycle (resolved is final;
		// reopened is a relapse and the original cycle's pending steps
		// were either fired or canceled).
		if eventType == models.EventTypeIncidentCreated {
			s.scheduleEscalationPolicy(ctx, orgUID, checkUID, incident)
		}
	case models.EventTypeCheckCreated, models.EventTypeCheckUpdated,
		models.EventTypeCheckDeleted,
		models.EventTypeIncidentAcknowledged, models.EventTypeIncidentUnacknowledged,
		models.EventTypeIncidentSnoozed, models.EventTypeIncidentUnsnoozed,
		models.EventTypeIncidentEscalationFailed,
		models.EventTypeStatusUpdateCreated, models.EventTypeStatusUpdateUpdated,
		models.EventTypeStatusUpdateDeleted,
		models.EventTypeOrgActivationSignupCompleted,
		models.EventTypeOrgActivationFirstCheckCreated,
		models.EventTypeOrgActivationFirstResultReceived,
		models.EventTypeOrgActivationFirstNotificationConfigured,
		models.EventTypeOrgActivationFirstIncidentPaged:
		// No notifications for these event types — operator actions, soft
		// escalation failures, status updates, or activation milestones.
	case models.EventTypeIncidentComment:
		// Comments DO notify — but never through here. addCommentByOrgUID
		// writes its own event row and calls queueCommentNotifications
		// directly, because the fan-out needs the comment body and the
		// connection it originated from, neither of which emitEvent carries.
		// Listed explicitly so this switch cannot be read as "comments are
		// silent", which is exactly the bug this case used to encode.
	}

	return nil
}

// queueResolutionNotice enqueues the job that closes the loop with the person
// contacts (Telegram today) paged for this incident.
//
// Same contract as queueNotifications: a failure to enqueue is logged and never
// fails the resolution. An incident that resolved but whose notice job could not
// be created is a missed message; an incident that failed to resolve is an
// outage that never ends.
func (s *Service) queueResolutionNotice(ctx context.Context, orgUID, incidentUID string) {
	config, err := json.Marshal(jobtypes.IncidentResolutionNoticeJobConfig{
		OrganizationUID: orgUID,
		IncidentUID:     incidentUID,
	})
	if err != nil {
		slog.WarnContext(ctx, "Failed to marshal incident resolution notice config",
			"incidentUid", incidentUID, "error", err)

		return
	}

	if _, err := s.jobsSvc.CreateJob(
		ctx, orgUID, string(jobdef.JobTypeIncidentResolutionNotice), config, nil,
	); err != nil {
		slog.WarnContext(ctx, "Failed to create incident resolution notice job",
			"incidentUid", incidentUID, "error", err)
	}
}

// queueGroupNotifications fans out a single notification per (connection,
// event-type), where the connection set is the union of every currently-
// failing member's connections. Recovered members do not bring their
// channels into mid-incident events, but they DO contribute to the
// resolved event so every channel that fired on creation also hears about
// the recovery.
func (s *Service) queueGroupNotifications(
	ctx context.Context, orgUID, incidentUID string, eventType models.EventType,
) {
	members, err := s.db.ListIncidentMemberChecks(ctx, incidentUID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to list group members for notification dedup",
			"incidentUid", incidentUID, "error", err)

		return
	}

	seen := make(map[string]bool)

	for _, member := range members {
		if !member.CurrentlyFailing && eventType != models.EventTypeIncidentResolved {
			continue
		}

		conns, err := s.db.ListChannelsForCheck(ctx, member.CheckUID)
		if err != nil {
			slog.WarnContext(ctx, "Failed to load connections for group member",
				"checkUid", member.CheckUID, "error", err)

			continue
		}

		for _, conn := range conns {
			if !conn.Enabled || seen[conn.UID] {
				continue
			}

			seen[conn.UID] = true
			s.enqueueNotificationJob(ctx, orgUID, conn.UID, string(conn.Type), incidentUID, eventType)
		}
	}
}

// enqueueNotificationJob marshals the config and creates a single job row.
// Shared between per-check and group fan-out paths.
func (s *Service) enqueueNotificationJob(
	ctx context.Context, orgUID, connectionUID, channelType, incidentUID string, eventType models.EventType,
) {
	config, err := json.Marshal(jobtypes.NotificationJobConfig{
		ConnectionUID: connectionUID,
		IncidentUID:   incidentUID,
		EventType:     string(eventType),
	})
	if err != nil {
		slog.WarnContext(ctx, "Failed to marshal notification config",
			"connectionUid", connectionUID,
			"incidentUid", incidentUID,
			"error", err,
		)

		return
	}

	job, err := s.jobsSvc.CreateJob(ctx, orgUID, string(jobdef.JobTypeNotification), config, nil)
	if err != nil {
		slog.WarnContext(ctx, "Failed to create notification job",
			"connectionUid", connectionUID,
			"incidentUid", incidentUID,
			"error", err,
		)

		return
	}

	// Audit: record a pending row so we can later track delivery outcome.
	if auditErr := s.db.CreateIncidentNotification(ctx, models.NewIncidentNotificationForJob(
		orgUID, incidentUID, string(eventType),
		models.IncidentNotificationSourceCheckConnection,
		connectionUID, job.UID, channelType,
		nil, nil,
	)); auditErr != nil {
		slog.WarnContext(ctx, "failed to create notification audit row", "error", auditErr)
	}
}

// queueNotifications queues notification jobs for an incident event.
func (s *Service) queueNotifications(
	ctx context.Context, orgUID, checkUID, incidentUID string, eventType models.EventType,
) {
	// Get connections for this check
	connections, err := s.db.ListChannelsForCheck(ctx, checkUID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to get connections for check", "checkUid", checkUID, "error", err)
		return // Don't fail incident processing for notification errors
	}

	for _, conn := range connections {
		if !conn.Enabled {
			continue
		}

		s.enqueueNotificationJob(ctx, orgUID, conn.UID, string(conn.Type), incidentUID, eventType)
	}
}

// scheduleEscalationPolicy resolves the policy that applies to this
// check (check.escalation_policy_uid wins, then check_group's, then
// nil) and schedules cycle 0 of its steps. Best-effort: failures are
// logged but never block incident creation. The cancel sweep keyed on
// `incidentUid` in job config will drop these jobs on ack/snooze/resolve
// with no extra wiring.
func (s *Service) scheduleEscalationPolicy(
	ctx context.Context, orgUID, checkUID string, incident *models.Incident,
) {
	check, err := s.db.GetCheck(ctx, orgUID, checkUID)
	if err != nil {
		slog.WarnContext(ctx, "escalation: failed to load check", "checkUid", checkUID, "error", err)

		return
	}

	policyUID := resolveEscalationPolicyUID(ctx, s.db, check)
	if policyUID == "" {
		return
	}

	policy, err := s.db.GetEscalationPolicy(ctx, orgUID, policyUID)
	if err != nil {
		slog.WarnContext(ctx, "escalation: failed to load policy",
			"policyUid", policyUID, "error", err)

		return
	}

	steps, err := s.db.ListEscalationPolicySteps(ctx, policyUID)
	if err != nil {
		slog.WarnContext(ctx, "escalation: failed to load steps",
			"policyUid", policyUID, "error", err)

		return
	}

	if len(steps) == 0 {
		return
	}

	if scheduleErr := jobtypes.ScheduleEscalationCycle(
		ctx, s.jobsSvc, incident, policy, steps, incident.StartedAt, 0, slog.Default(),
	); scheduleErr != nil {
		slog.WarnContext(ctx, "escalation: failed to schedule cycle 0",
			"policyUid", policyUID, "error", scheduleErr)
	}
}

// resolveEscalationPolicyUID picks the effective policy for a check
// (check → group → org default → none). The precedence chain itself lives in
// jobtypes.ResolveEscalationPolicyUID so the Slack on-call mention resolver
// names exactly the humans this scheduling path would page — two copies of the
// chain could drift, and a mention that disagrees with the actual page is
// worse than no mention. This wrapper is kept so the package's white-box tests
// and call sites read unchanged.
func resolveEscalationPolicyUID(ctx context.Context, dbSvc db.Service, check *models.Check) string {
	return jobtypes.ResolveEscalationPolicyUID(ctx, dbSvc, check)
}

// ListIncidentsOptions contains options for listing incidents.
type ListIncidentsOptions struct {
	CheckUIDs      []string
	CheckGroupUID  string
	MemberCheckUID string
	States         []string // "active", "resolved"
	Since          *time.Time
	Until          *time.Time
	Cursor         string
	Size           int
	WithCheck      bool // Include check details in response
	HideSuppressed bool // Hide rolled-up (paging-suppressed) incidents
	CausedByUID    string
}

// CheckResponse represents check details embedded in incident responses.
type CheckResponse struct {
	Slug   *string        `json:"slug,omitempty"`
	Type   string         `json:"type"`
	Config models.JSONMap `json:"config,omitempty"`
}

// IncidentResponse represents an incident in API responses.
type IncidentResponse struct {
	UID string `json:"uid"`
	// Number is the short per-org reference rendered as `#42`. Every human-facing
	// surface (dashboard, Slack, Telegram) addresses the incident by this.
	Number              int64                    `json:"number"`
	CheckUID            string                   `json:"checkUid"`
	CheckSlug           *string                  `json:"checkSlug,omitempty"`
	CheckName           *string                  `json:"checkName,omitempty"`
	State               string                   `json:"state"`
	StartedAt           time.Time                `json:"startedAt"`
	ResolvedAt          *time.Time               `json:"resolvedAt,omitempty"`
	ResolvedBy          *string                  `json:"resolvedBy,omitempty"`
	ResolutionType      *string                  `json:"resolutionType,omitempty"`
	EscalatedAt         *time.Time               `json:"escalatedAt,omitempty"`
	AcknowledgedAt      *time.Time               `json:"acknowledgedAt,omitempty"`
	AcknowledgedBy      *string                  `json:"acknowledgedBy,omitempty"`
	SnoozedUntil        *time.Time               `json:"snoozedUntil,omitempty"`
	SnoozedBy           *string                  `json:"snoozedBy,omitempty"`
	SnoozeReason        *string                  `json:"snoozeReason,omitempty"`
	FailureCount        int                      `json:"failureCount"`
	RelapseCount        int                      `json:"relapseCount"`
	LastReopenedAt      *time.Time               `json:"lastReopenedAt,omitempty"`
	Title               *string                  `json:"title,omitempty"`
	Description         *string                  `json:"description,omitempty"`
	Check               *CheckResponse           `json:"check,omitempty"`
	CheckGroupUID       *string                  `json:"checkGroupUid,omitempty"`
	CheckGroupSlug      *string                  `json:"checkGroupSlug,omitempty"`
	Members             []IncidentMemberResponse `json:"members,omitempty"`
	CausedByIncidentUID *string                  `json:"causedByIncidentUid,omitempty"`
	PagingSuppressed    bool                     `json:"pagingSuppressed,omitempty"`
	// Details carries the first-failure / latest-relapse snapshot recorded at
	// incident open/reopen (failure_reason, first_result, and — after a
	// relapse — last_failure). See failureDetails / lastFailureDetails.
	Details models.JSONMap `json:"details,omitempty"`
}

// IncidentMemberResponse represents a single member of a group incident.
type IncidentMemberResponse struct {
	CheckUID         string     `json:"checkUid"`
	CheckSlug        *string    `json:"checkSlug,omitempty"`
	CheckName        *string    `json:"checkName,omitempty"`
	JoinedAt         time.Time  `json:"joinedAt"`
	FirstFailureAt   time.Time  `json:"firstFailureAt"`
	LastFailureAt    time.Time  `json:"lastFailureAt"`
	LastRecoveryAt   *time.Time `json:"lastRecoveryAt,omitempty"`
	FailureCount     int        `json:"failureCount"`
	CurrentlyFailing bool       `json:"currentlyFailing"`
}

// ListIncidentsResponse represents the response for listing incidents.
type ListIncidentsResponse struct {
	Data       []IncidentResponse `json:"data"`
	Pagination PaginationResponse `json:"pagination"`
}

// PaginationResponse represents pagination info.
type PaginationResponse struct {
	Total  int64  `json:"total"`
	Cursor string `json:"cursor,omitempty"`
	Size   int    `json:"size"`
}

// stateToString converts an incident state to a string.
func stateToString(state models.IncidentState) string {
	switch state {
	case models.IncidentStateActive:
		return "active"
	case models.IncidentStateResolved:
		return "resolved"
	default:
		return "unknown"
	}
}

// incidentToResponse converts a model incident to an API response.
func incidentToResponse(inc *models.Incident) IncidentResponse {
	return IncidentResponse{
		UID:                 inc.UID,
		Number:              inc.Number,
		CheckUID:            inc.CheckUID,
		State:               stateToString(inc.State),
		StartedAt:           inc.StartedAt,
		ResolvedAt:          inc.ResolvedAt,
		ResolvedBy:          inc.ResolvedBy,
		ResolutionType:      inc.ResolutionType,
		EscalatedAt:         inc.EscalatedAt,
		AcknowledgedAt:      inc.AcknowledgedAt,
		AcknowledgedBy:      inc.AcknowledgedBy,
		SnoozedUntil:        inc.SnoozedUntil,
		SnoozedBy:           inc.SnoozedBy,
		SnoozeReason:        inc.SnoozeReason,
		FailureCount:        inc.FailureCount,
		RelapseCount:        inc.RelapseCount,
		LastReopenedAt:      inc.LastReopenedAt,
		Title:               inc.Title,
		Description:         inc.Description,
		CheckGroupUID:       inc.CheckGroupUID,
		CausedByIncidentUID: inc.CausedByIncidentUID,
		PagingSuppressed:    inc.PagingSuppressed,
		Details:             inc.Details,
	}
}

// memberToResponse converts a model member row to an API response, optionally
// embedding the resolved check name/slug.
func memberToResponse(member *models.IncidentMemberCheck, check *models.Check) IncidentMemberResponse {
	resp := IncidentMemberResponse{
		CheckUID:         member.CheckUID,
		JoinedAt:         member.JoinedAt,
		FirstFailureAt:   member.FirstFailureAt,
		LastFailureAt:    member.LastFailureAt,
		LastRecoveryAt:   member.LastRecoveryAt,
		FailureCount:     member.FailureCount,
		CurrentlyFailing: member.CurrentlyFailing,
	}

	if check != nil {
		resp.CheckSlug = check.Slug
		resp.CheckName = check.Name
	}

	return resp
}

// loadIncidentMembers populates the Members slice on the response from the
// incident_member_checks rows. Resolves each member's check for slug/name.
func (s *Service) loadIncidentMembers(
	ctx context.Context, orgUID string, inc *models.Incident, resp *IncidentResponse,
) {
	if inc.CheckGroupUID == nil {
		return
	}

	members, err := s.db.ListIncidentMemberChecks(ctx, inc.UID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to list incident members for response",
			"incidentUID", inc.UID, "error", err)

		return
	}

	resp.Members = make([]IncidentMemberResponse, 0, len(members))
	for _, member := range members {
		check, _ := s.db.GetCheck(ctx, orgUID, member.CheckUID)
		resp.Members = append(resp.Members, memberToResponse(member, check))
	}

	if group, err := s.db.GetCheckGroup(ctx, orgUID, *inc.CheckGroupUID); err == nil && group != nil {
		resp.CheckGroupSlug = &group.Slug
	}
}

// checkToResponse converts a model check to an embedded check response.
func checkToResponse(check *models.Check) *CheckResponse {
	return &CheckResponse{
		Slug:   check.Slug,
		Type:   check.Type,
		Config: check.Config,
	}
}

// buildCheckMap builds a map of check UIDs to check models for embedding in responses.
func (s *Service) buildCheckMap(
	ctx context.Context, orgUID string, incidents []*models.Incident, withCheck bool,
) map[string]*models.Check {
	checkMap := make(map[string]*models.Check)
	if !withCheck || len(incidents) == 0 {
		return checkMap
	}

	for _, inc := range incidents {
		if _, exists := checkMap[inc.CheckUID]; exists {
			continue
		}
		check, err := s.db.GetCheck(ctx, orgUID, inc.CheckUID)
		if err == nil {
			checkMap[inc.CheckUID] = check
		}
	}

	return checkMap
}

// stringToState converts a string to an incident state.
func stringToState(s string) (models.IncidentState, bool) {
	switch s {
	case "active":
		return models.IncidentStateActive, true
	case "resolved":
		return models.IncidentStateResolved, true
	default:
		return 0, false
	}
}

// buildListIncidentsFilter builds the DB filter from already-resolved
// options (opts.CheckUIDs has already been resolved to UIDs by ListIncidents
// by this point). Split out so ListIncidents stays under the funlen limit.
func buildListIncidentsFilter(orgUID string, opts *ListIncidentsOptions) *models.ListIncidentsFilter {
	filter := &models.ListIncidentsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       opts.CheckUIDs,
		CheckGroupUID:   opts.CheckGroupUID,
		MemberCheckUID:  opts.MemberCheckUID,
		Since:           opts.Since,
		Until:           opts.Until,
		HideSuppressed:  opts.HideSuppressed,
		CausedByUID:     opts.CausedByUID,
		Limit:           opts.Size + 1, // Fetch one extra to determine hasMore
	}

	// Convert state strings to state values. "acked" / "snoozed" are derived
	// states (active + acknowledged_at | snoozed_until) — they imply
	// state=active so the SQL stays consistent (an acked-and-resolved
	// incident shouldn't appear under "acked").
	for _, stateStr := range opts.States {
		switch stateStr {
		case "acked":
			filter.AckedOnly = true
			filter.States = append(filter.States, models.IncidentStateActive)
		case "snoozed":
			filter.SnoozedOnly = true
			filter.States = append(filter.States, models.IncidentStateActive)
		default:
			if state, ok := stringToState(stateStr); ok {
				filter.States = append(filter.States, state)
			}
		}
	}

	return filter
}

// ListIncidents lists incidents for an organization.
func (s *Service) ListIncidents(
	ctx context.Context, orgSlug string, opts *ListIncidentsOptions,
) (*ListIncidentsResponse, error) {
	// Get organization
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Resolve the checkUid filter (uid or slug) before it reaches the DB
	// layer, whose `check_uid IN (?)` filter only applies when the resolved
	// slice is non-empty (models.ListIncidentsFilter.CheckUIDs). If the
	// caller asked for specific checks and NONE of them resolved, that empty
	// slice would otherwise be indistinguishable from "no filter requested"
	// and silently return every incident in the org — so short-circuit to an
	// explicit empty page instead, matching the "unknown identifier -> no
	// match" contract (no DB round trip needed).
	if len(opts.CheckUIDs) > 0 {
		resolvedUIDs := s.resolveCheckIdentifiers(ctx, org.UID, opts.CheckUIDs)
		if len(resolvedUIDs) == 0 {
			return &ListIncidentsResponse{
				Data:       []IncidentResponse{},
				Pagination: PaginationResponse{Size: opts.Size, Total: 0},
			}, nil
		}

		opts.CheckUIDs = resolvedUIDs
	}

	filter := buildListIncidentsFilter(org.UID, opts)

	// TODO: Parse cursor

	incidents, total, err := s.db.ListIncidents(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list incidents: %w", err)
	}

	// Determine if there are more results
	hasMore := len(incidents) > opts.Size
	if hasMore {
		incidents = incidents[:opts.Size]
	}

	// Build check map if WithCheck is requested
	checkMap := s.buildCheckMap(ctx, org.UID, incidents, opts.WithCheck)

	// Build response
	response := &ListIncidentsResponse{
		Data: make([]IncidentResponse, 0, len(incidents)),
		Pagination: PaginationResponse{
			Size:  opts.Size,
			Total: total,
		},
	}

	for _, inc := range incidents {
		incResponse := incidentToResponse(inc)

		// Add check details if requested
		if check, exists := checkMap[inc.CheckUID]; exists {
			incResponse.Check = checkToResponse(check)
			incResponse.CheckSlug = check.Slug
			incResponse.CheckName = check.Name
		}

		// Populate group members for group incidents.
		s.loadIncidentMembers(ctx, org.UID, inc, &incResponse)

		response.Data = append(response.Data, incResponse)
	}

	// Set cursor if there are more results
	if hasMore && len(incidents) > 0 {
		lastIncident := incidents[len(incidents)-1]
		response.Pagination.Cursor = lastIncident.UID
	}

	return response, nil
}

// resolveCheckIdentifiers resolves a mix of check UIDs and slugs to UIDs, the
// same way results.Service.resolveCheckIdentifiers does for `/results`. The
// `check_uid` column is a Postgres uuid, so passing a raw slug straight into
// an `IN (...)` filter fails with "invalid input syntax for type uuid"
// (issue #127) — resolving through GetCheckByUidOrSlug first lets `/incidents`
// accept a slug just like every other check-scoped endpoint. Identifiers that
// don't match any check are silently dropped, matching `/results`: an unknown
// identifier simply yields no match instead of a DB error.
func (s *Service) resolveCheckIdentifiers(ctx context.Context, orgUID string, identifiers []string) []string {
	if len(identifiers) == 0 {
		return identifiers
	}

	uids := make([]string, 0, len(identifiers))

	for _, id := range identifiers {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		check, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, id)
		if err == nil && check != nil {
			uids = append(uids, check.UID)
		}
		// Silently ignore identifiers that don't match any check.
	}

	return uids
}

// GetIncidentOptions contains options for getting a single incident.
type GetIncidentOptions struct {
	WithCheck bool // Include check details in response
}

// GetIncident gets a single incident by UID.
func (s *Service) GetIncident(
	ctx context.Context, orgSlug, incidentUID string, opts *GetIncidentOptions,
) (*IncidentResponse, error) {
	// Get organization
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	incident, err := s.db.GetIncident(ctx, org.UID, incidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, fmt.Errorf("failed to get incident: %w", err)
	}

	response := incidentToResponse(incident)

	// Always fetch check to populate name and slug
	check, err := s.db.GetCheck(ctx, org.UID, incident.CheckUID)
	if err == nil {
		response.CheckSlug = check.Slug
		response.CheckName = check.Name
		if opts != nil && opts.WithCheck {
			response.Check = checkToResponse(check)
		}
	}

	// Populate group members for group incidents.
	s.loadIncidentMembers(ctx, org.UID, incident, &response)

	return &response, nil
}

// GetIncidentByUID gets an incident by UID, requiring organization UID.
func (s *Service) GetIncidentByUID(ctx context.Context, orgUID, incidentUID string) (*models.Incident, error) {
	incident, err := s.db.GetIncident(ctx, orgUID, incidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, fmt.Errorf("failed to get incident: %w", err)
	}
	return incident, nil
}

// tryEmailAck performs an email-channel ack and returns whether it
// succeeded. Folds the (incident, error) result the magic-link handler
// doesn't need into a single bool so the caller can keep its return type
// simple (nil error after rendering an HTML error page would otherwise trip
// the nilerr lint).
func (s *Service) tryEmailAck(
	ctx context.Context, orgSlug, incidentUID, ackedBy, recipientEmail string,
) bool {
	_, err := s.AcknowledgeIncident(ctx, orgSlug, &AcknowledgeIncidentRequest{
		IncidentUID:         incidentUID,
		AcknowledgedBy:      ackedBy,
		AcknowledgedByEmail: recipientEmail,
		Note:                "",
		Via:                 "email",
	})

	return err == nil
}

// lookupUserUIDByEmail returns the User UID for the given email, or "" if no
// user is found. Used by the magic-link ack path where the recipient may or
// may not be a known platform user.
func (s *Service) lookupUserUIDByEmail(ctx context.Context, email string) string {
	if email == "" {
		return ""
	}

	user, err := s.db.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		return ""
	}

	return user.UID
}

// AcknowledgeIncidentRequest contains the data needed to acknowledge an incident.
type AcknowledgeIncidentRequest struct {
	IncidentUID    string
	AcknowledgedBy string // User UID or identifier
	// AcknowledgedByEmail is the recipient email for magic-link acks where
	// AcknowledgedBy may be empty (recipient is not a known user). Goes into
	// the event payload so the audit trail is complete.
	AcknowledgedByEmail string
	SlackUserID         string // Slack user ID if acknowledged via Slack
	SlackUsername       string // Slack username for display
	// PhoneNumber is the E.164 number of the caller for phone (DTMF) acks. Goes
	// into the event payload so the audit trail names the acker even when the
	// number belongs to no known platform user.
	PhoneNumber string
	// TelegramActor is the "via Telegram (<first name>)" attribution string.
	// Telegram acks are credited to the SolidPing user the chat is LINKED to,
	// but in a group the person who pressed the button may be somebody else
	// entirely — this records who actually did it, which the user UID alone
	// cannot express.
	TelegramActor string
	Note          string // Optional free-text note
	Via           string // "slack", "web", "email", "phone", etc.
}

// AcknowledgeIncident marks an incident as acknowledged. Accepts the org
// slug (as the HTTP layer always has) and resolves it to a UID internally.
func (s *Service) AcknowledgeIncident(
	ctx context.Context, orgSlug string, req *AcknowledgeIncidentRequest,
) (*models.Incident, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	return s.acknowledgeIncidentByOrgUID(ctx, org.UID, req)
}

// acknowledgeIncidentByOrgUID is the org-UID variant used by internal callers
// (Slack handler, sweeper) that already hold a UID — they skip the slug
// lookup that the public method performs.
func (s *Service) acknowledgeIncidentByOrgUID(
	ctx context.Context, orgUID string, req *AcknowledgeIncidentRequest,
) (*models.Incident, error) {
	// Get the incident
	incident, err := s.db.GetIncident(ctx, orgUID, req.IncidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, fmt.Errorf("failed to get incident: %w", err)
	}

	// Check if already acknowledged
	if incident.AcknowledgedAt != nil {
		slog.InfoContext(ctx, "Incident already acknowledged",
			"incident_uid", incident.UID,
			"acknowledged_at", *incident.AcknowledgedAt,
		)
		return incident, nil // Return existing incident without error
	}

	// Update the incident
	now := s.clock.Now()
	update := models.IncidentUpdate{
		AcknowledgedAt: &now,
	}
	if req.AcknowledgedBy != "" {
		update.AcknowledgedBy = &req.AcknowledgedBy
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return nil, fmt.Errorf("failed to update incident: %w", err)
	}

	// Update local copy
	incident.AcknowledgedAt = &now
	incident.AcknowledgedBy = update.AcknowledgedBy

	// Create acknowledgment event
	event := models.NewEvent(orgUID, models.EventTypeIncidentAcknowledged, models.ActorTypeUser)
	event.IncidentUID = &incident.UID
	event.CheckUID = &incident.CheckUID
	event.Payload = models.JSONMap{
		payloadKeyVia:    req.Via,
		"slack_user_id":  req.SlackUserID,
		"slack_username": req.SlackUsername,
		"note":           req.Note,
		keyCheckUID:      incident.CheckUID,
	}
	// Best-effort: the check may have been deleted since the incident opened;
	// don't fail the acknowledgment over a missing slug/name.
	if check, chkErr := s.db.GetCheck(ctx, orgUID, incident.CheckUID); chkErr == nil && check != nil {
		event.Payload[keyCheckSlug] = check.Slug
		event.Payload[keyCheckName] = check.Name
	}
	// Magic-link acks come in with the recipient email even when the
	// recipient is not a known platform user — record it on the payload so
	// the audit trail names the acker even when actor_uid is NULL.
	if req.AcknowledgedByEmail != "" {
		event.Payload["acknowledged_by_email"] = req.AcknowledgedByEmail
	}
	// Phone (DTMF) acks record the caller's number, mirroring the email
	// attribution above.
	if req.PhoneNumber != "" {
		event.Payload["acknowledged_by_phone"] = req.PhoneNumber
	}
	// Telegram acks: the credited user is the chat's linked account, so without
	// this the timeline could not tell "the on-call person acked from their DM"
	// apart from "a colleague pressed the button in the team group".
	if req.TelegramActor != "" {
		event.Payload["acknowledged_by_telegram"] = req.TelegramActor
	}
	if req.AcknowledgedBy != "" {
		event.ActorUID = &req.AcknowledgedBy
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		slog.WarnContext(ctx, "Failed to create acknowledgment event",
			"incident_uid", incident.UID,
			"error", err,
		)
		// Don't fail the acknowledgment for event creation errors
	}

	s.rt.PublishImmediate(ctx, orgUID, incident.CheckUID, realtime.KindIncidents, realtime.KindEvents)

	s.cancelPendingNotifications(ctx, incident.UID, nil)

	slog.InfoContext(ctx, "Incident acknowledged",
		"incident_uid", incident.UID,
		"via", req.Via,
		"slack_user_id", req.SlackUserID,
	)

	return incident, nil
}

// cancelPendingNotifications soft-deletes future notification jobs for an
// incident. Logs but never returns the error — cancellation is best-effort
// and must not fail the user-visible action that triggered it.
func (s *Service) cancelPendingNotifications(ctx context.Context, incidentUID string, notBefore *time.Time) {
	canceled, err := s.jobsSvc.CancelPendingForIncident(ctx, incidentUID, notBefore)
	if err != nil {
		slog.WarnContext(ctx, "Failed to cancel pending notifications",
			"incident_uid", incidentUID,
			"error", err,
		)

		return
	}

	if canceled > 0 {
		slog.InfoContext(ctx, "Canceled pending notification jobs",
			"incident_uid", incidentUID,
			"count", canceled,
		)
	}
}

// GetCheckByUID gets a check by UID within an organization.
func (s *Service) GetCheckByUID(ctx context.Context, orgUID, checkUID string) (*models.Check, error) {
	return s.db.GetCheck(ctx, orgUID, checkUID)
}

// AcknowledgeIncidentFromSlack marks an incident as acknowledged via Slack.
// This method is used by the Slack integration to acknowledge incidents.
func (s *Service) AcknowledgeIncidentFromSlack(
	ctx context.Context, orgUID, incidentUID, slackUserID, slackUsername string,
) (*models.Incident, error) {
	return s.acknowledgeIncidentByOrgUID(ctx, orgUID, &AcknowledgeIncidentRequest{
		IncidentUID:   incidentUID,
		SlackUserID:   slackUserID,
		SlackUsername: slackUsername,
		Via:           "slack",
	})
}

// AcknowledgeIncidentFromPhone marks an incident acknowledged via a phone
// (DTMF) callback. The caller's number is recorded on the event payload. Like
// the Slack/email variants it is idempotent and cancels pending escalation jobs.
func (s *Service) AcknowledgeIncidentFromPhone(
	ctx context.Context, orgUID, incidentUID, phone string,
) (*models.Incident, error) {
	return s.acknowledgeIncidentByOrgUID(ctx, orgUID, &AcknowledgeIncidentRequest{
		IncidentUID: incidentUID,
		PhoneNumber: phone,
		Via:         "phone",
	})
}

// AcknowledgeIncidentFromTelegram marks an incident acknowledged from a
// Telegram chat — the inline Acknowledge button or a typed /ack.
//
// userUID is the SolidPing account the chat is linked to (it may be empty when
// the linkage is gone), and actor is the "via Telegram (<first name>)" label of
// whoever actually pressed the button. Like every other channel variant it is
// idempotent and cancels pending escalation jobs.
func (s *Service) AcknowledgeIncidentFromTelegram(
	ctx context.Context, orgUID, incidentUID, userUID, actor string,
) (*models.Incident, error) {
	return s.acknowledgeIncidentByOrgUID(ctx, orgUID, &AcknowledgeIncidentRequest{
		IncidentUID:    incidentUID,
		AcknowledgedBy: userUID,
		TelegramActor:  actor,
		Via:            "telegram",
	})
}

// AddCommentRequest carries the data needed to append a comment to an
// incident's timeline. Source-specific fields are only read for that source
// (ActorUID for web, the Slack* fields for slack).
type AddCommentRequest struct {
	IncidentUID string
	Text        string
	Source      string // CommentSourceWeb | CommentSourceSlack | CommentSourceTelegram
	// ActorUID is the SolidPing user's UID. Set for the web source and for the
	// telegram source (a verified linked contact always maps to a user).
	ActorUID string
	// Slack attribution (slack source) — the author usually has no SolidPing
	// user, so these go into the payload and ActorUID stays empty.
	SlackUserID   string
	SlackUserName string
	SlackTeamID   string
	SlackTs       string
	// Telegram attribution (telegram source). ActorUID carries the identity;
	// these two are display/provenance detail for the timeline.
	TelegramChatID string
	TelegramActor  string
}

// AddComment appends a user-authored comment to an incident's timeline as an
// append-only `incident.comment` event. Accepts the org slug (as the HTTP
// layer always has) and resolves it to a UID internally.
func (s *Service) AddComment(
	ctx context.Context, orgSlug string, req *AddCommentRequest,
) (*models.Event, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	return s.addCommentByOrgUID(ctx, org.UID, req)
}

// AddCommentFromSlack appends a comment ingested from a Slack thread reply.
// Internal callers already hold the org UID (resolved via the reverse thread
// lookup), so this skips the slug lookup AddComment performs. Satisfies the
// slack package's IncidentService interface.
func (s *Service) AddCommentFromSlack(
	ctx context.Context, orgUID, incidentUID, text, slackUserID, slackUserName, slackTeamID, slackTs string,
) (*models.Event, error) {
	return s.addCommentByOrgUID(ctx, orgUID, &AddCommentRequest{
		IncidentUID:   incidentUID,
		Text:          text,
		Source:        CommentSourceSlack,
		SlackUserID:   slackUserID,
		SlackUserName: slackUserName,
		SlackTeamID:   slackTeamID,
		SlackTs:       slackTs,
	})
}

// AddCommentFromTelegram appends a comment posted with the Telegram bot's
// /comment command. Unlike Slack, the author is a VERIFIED linked contact, so
// the comment is attributed to their SolidPing user (actorUID) exactly as a
// dashboard comment would be — the Telegram identity is provenance detail, not
// the identity itself. Satisfies the telegramcb package's Commenter interface.
func (s *Service) AddCommentFromTelegram(
	ctx context.Context, orgUID, incidentUID, text, userUID, actor, chatID string,
) (*models.Event, error) {
	return s.addCommentByOrgUID(ctx, orgUID, &AddCommentRequest{
		IncidentUID:    incidentUID,
		Text:           text,
		Source:         CommentSourceTelegram,
		ActorUID:       userUID,
		TelegramActor:  actor,
		TelegramChatID: chatID,
	})
}

// addCommentByOrgUID is the shared core for both comment sources. It validates
// the body, confirms the incident exists in the org, writes the append-only
// event, and publishes a live `events` hint so watching dashboards refresh.
func (s *Service) addCommentByOrgUID(
	ctx context.Context, orgUID string, req *AddCommentRequest,
) (*models.Event, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return nil, ErrCommentEmpty
	}
	if len(text) > MaxCommentLength {
		return nil, ErrCommentTooLong
	}

	incident, err := s.db.GetIncident(ctx, orgUID, req.IncidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}

		return nil, fmt.Errorf("failed to get incident: %w", err)
	}

	event := models.NewEvent(orgUID, models.EventTypeIncidentComment, models.ActorTypeUser)
	event.IncidentUID = &incident.UID
	event.CheckUID = &incident.CheckUID

	payload := models.JSONMap{
		payloadKeyCommentText:   text,
		payloadKeyCommentSource: req.Source,
		keyCheckUID:             incident.CheckUID,
	}

	switch req.Source {
	case CommentSourceWeb:
		if req.ActorUID != "" {
			event.ActorUID = &req.ActorUID
		}
	case CommentSourceSlack:
		payload[payloadKeySlackUserID] = req.SlackUserID
		payload[payloadKeySlackUserName] = req.SlackUserName
		payload[payloadKeySlackTeamID] = req.SlackTeamID
		payload[payloadKeySlackTs] = req.SlackTs
	case CommentSourceTelegram:
		if req.ActorUID != "" {
			event.ActorUID = &req.ActorUID
		}
		payload[payloadKeyTelegramChat] = req.TelegramChatID
		payload[payloadKeyVia] = CommentSourceTelegram
	}

	// Best-effort check identity for rendering, mirroring the ack path — the
	// check may have been deleted since the incident opened.
	if check, chkErr := s.db.GetCheck(ctx, orgUID, incident.CheckUID); chkErr == nil && check != nil {
		payload[keyCheckSlug] = check.Slug
		payload[keyCheckName] = check.Name
	}

	author := s.commentAuthorName(ctx, req)
	if author != "" {
		payload[payloadKeyCommentAuthor] = author
	}

	event.Payload = payload

	if err := s.db.CreateEvent(ctx, event); err != nil {
		return nil, fmt.Errorf("failed to create comment event: %w", err)
	}

	// Live hint: a new timeline event is visible state — immediate so the
	// comment reaches watching dashboards without a manual refresh.
	s.rt.PublishImmediate(ctx, orgUID, incident.CheckUID, realtime.KindEvents)

	// Fan the comment out to the channels handling this incident. Best-effort,
	// exactly like the lifecycle events: a comment that was recorded but not
	// broadcast is a missed message; a comment that failed to record is lost
	// operator knowledge, and the caller has already been told it succeeded.
	s.queueCommentNotifications(ctx, orgUID, incident, req, text, author)

	return event, nil
}

// commentAuthorName resolves the human label a channel message attributes the
// comment to. Slack carries its own display name, Telegram carries the actor
// label the bot saw, and a dashboard comment is looked up from the user row.
// Returns "" when nothing better than "someone" is known — senders render a
// neutral fallback rather than an empty byline.
func (s *Service) commentAuthorName(ctx context.Context, req *AddCommentRequest) string {
	switch req.Source {
	case CommentSourceSlack:
		return strings.TrimSpace(req.SlackUserName)
	case CommentSourceTelegram:
		if actor := strings.TrimSpace(req.TelegramActor); actor != "" {
			return actor
		}
	}

	if req.ActorUID == "" {
		return ""
	}

	user, err := s.db.GetUser(ctx, req.ActorUID)
	if err != nil || user == nil {
		return ""
	}

	if name := strings.TrimSpace(user.Name); name != "" {
		return name
	}

	return user.Email
}

// queueCommentNotifications fans an incident comment out to the channels
// attached to the incident's check (or, for a group incident, to the union of
// its currently-failing members' channels — the same connection set the
// lifecycle events use).
//
// Two filters apply on top of that set, and both live somewhere reusable
// rather than inline here:
//
//   - notifications.AcceptsEventType — the registry's per-sender opt-out. SMS
//     and voice do not receive comments.
//   - isCommentEchoOrigin — the connection the comment CAME FROM is skipped,
//     or the bot repeats the author's own words back into the thread they just
//     typed them in. (Distinct from the bot_id ingest guard, which stops the
//     re-posted message from being re-ingested; both are needed.)
//
// v1 scope: check-attached channels only. People paged through an escalation
// policy are not forwarded comments — see wiki/features/incident-comments.md.
func (s *Service) queueCommentNotifications(
	ctx context.Context, orgUID string, incident *models.Incident,
	req *AddCommentRequest, text, author string,
) {
	comment := &notifications.CommentInfo{
		Text:       text,
		AuthorName: author,
		Source:     req.Source,
	}

	for _, conn := range s.commentFanoutConnections(ctx, incident) {
		if !conn.Enabled {
			continue
		}

		if !notifications.AcceptsEventType(conn.Type, string(models.EventTypeIncidentComment)) {
			continue
		}

		if isCommentEchoOrigin(conn, req) {
			slog.DebugContext(ctx, "Skipping comment fan-out to its own origin connection",
				"connectionUid", conn.UID, "incidentUid", incident.UID)

			continue
		}

		s.enqueueCommentNotificationJob(ctx, orgUID, conn, incident.UID, comment)
	}
}

// commentFanoutConnections resolves the connection set a comment reaches,
// mirroring queueNotifications / queueGroupNotifications so a comment lands
// exactly where the incident's own alerts landed.
func (s *Service) commentFanoutConnections(
	ctx context.Context, incident *models.Incident,
) []*models.Integration {
	if incident.CheckGroupUID == nil {
		conns, err := s.db.ListChannelsForCheck(ctx, incident.CheckUID)
		if err != nil {
			slog.WarnContext(ctx, "Failed to load connections for comment fan-out",
				"checkUid", incident.CheckUID, "error", err)

			return nil
		}

		return conns
	}

	members, err := s.db.ListIncidentMemberChecks(ctx, incident.UID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to list group members for comment fan-out",
			"incidentUid", incident.UID, "error", err)

		return nil
	}

	seen := make(map[string]bool)
	out := make([]*models.Integration, 0, len(members))

	for _, member := range members {
		conns, err := s.db.ListChannelsForCheck(ctx, member.CheckUID)
		if err != nil {
			slog.WarnContext(ctx, "Failed to load connections for group member comment fan-out",
				"checkUid", member.CheckUID, "error", err)

			continue
		}

		for _, conn := range conns {
			if seen[conn.UID] {
				continue
			}

			seen[conn.UID] = true

			out = append(out, conn)
		}
	}

	return out
}

// isCommentEchoOrigin reports whether this connection is the one the comment
// was typed in.
//
// Matched at WORKSPACE level for Slack, not per connection row: the incident's
// Slack thread is stored once per incident, so every Slack connection in that
// workspace would post into the very thread the author is reading.
func isCommentEchoOrigin(conn *models.Integration, req *AddCommentRequest) bool {
	if req.Source != CommentSourceSlack || conn.Type != models.ConnectionTypeSlack {
		return false
	}

	if req.SlackTeamID == "" {
		return false
	}

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	if err != nil || settings == nil {
		return false
	}

	return settings.TeamID == req.SlackTeamID
}

// enqueueCommentNotificationJob is enqueueNotificationJob with the comment
// body attached. Kept separate rather than widening the lifecycle helper's
// signature, so the lifecycle path stays exactly as it was.
func (s *Service) enqueueCommentNotificationJob(
	ctx context.Context, orgUID string, conn *models.Integration,
	incidentUID string, comment *notifications.CommentInfo,
) {
	eventType := string(models.EventTypeIncidentComment)

	config, err := json.Marshal(jobtypes.NotificationJobConfig{
		ConnectionUID: conn.UID,
		IncidentUID:   incidentUID,
		EventType:     eventType,
		Comment:       comment,
	})
	if err != nil {
		slog.WarnContext(ctx, "Failed to marshal comment notification config",
			"connectionUid", conn.UID, "incidentUid", incidentUID, "error", err)

		return
	}

	job, err := s.jobsSvc.CreateJob(ctx, orgUID, string(jobdef.JobTypeNotification), config, nil)
	if err != nil {
		slog.WarnContext(ctx, "Failed to create comment notification job",
			"connectionUid", conn.UID, "incidentUid", incidentUID, "error", err)

		return
	}

	if auditErr := s.db.CreateIncidentNotification(ctx, models.NewIncidentNotificationForJob(
		orgUID, incidentUID, eventType,
		models.IncidentNotificationSourceCheckConnection,
		conn.UID, job.UID, string(conn.Type),
		nil, nil,
	)); auditErr != nil {
		slog.WarnContext(ctx, "failed to create comment notification audit row", "error", auditErr)
	}
}

// UnacknowledgeIncident clears the acknowledgment on an incident. Use case:
// ack'd by mistake, want escalation to resume. Accepts the org slug.
func (s *Service) UnacknowledgeIncident(
	ctx context.Context, orgSlug, incidentUID, actorUID, via string,
) (*models.Incident, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	return s.unacknowledgeIncidentByOrgUID(ctx, org.UID, incidentUID, actorUID, via)
}

func (s *Service) unacknowledgeIncidentByOrgUID(
	ctx context.Context, orgUID, incidentUID, actorUID, via string,
) (*models.Incident, error) {
	incident, err := s.db.GetIncident(ctx, orgUID, incidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}

		return nil, fmt.Errorf("failed to get incident: %w", err)
	}

	if incident.AcknowledgedAt == nil {
		return incident, nil
	}

	update := models.IncidentUpdate{
		ClearAcknowledgedAt: true,
		ClearAcknowledgedBy: true,
	}
	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return nil, fmt.Errorf("failed to update incident: %w", err)
	}

	incident.AcknowledgedAt = nil
	incident.AcknowledgedBy = nil

	event := models.NewEvent(orgUID, models.EventTypeIncidentUnacknowledged, models.ActorTypeUser)
	event.IncidentUID = &incident.UID
	event.CheckUID = &incident.CheckUID
	event.Payload = models.JSONMap{payloadKeyVia: via, keyCheckUID: incident.CheckUID}
	// Best-effort: the check may have been deleted since the incident opened;
	// don't fail the unacknowledge over a missing slug/name.
	if check, chkErr := s.db.GetCheck(ctx, orgUID, incident.CheckUID); chkErr == nil && check != nil {
		event.Payload[keyCheckSlug] = check.Slug
		event.Payload[keyCheckName] = check.Name
	}

	if actorUID != "" {
		event.ActorUID = &actorUID
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		slog.WarnContext(ctx, "Failed to create unack event",
			"incident_uid", incident.UID, "error", err)
	}

	s.rt.PublishImmediate(ctx, orgUID, incident.CheckUID, realtime.KindIncidents, realtime.KindEvents)

	return incident, nil
}

// SnoozeIncidentRequest carries the validated parameters for SnoozeIncident.
// Either Until or Duration must be set; the handler is responsible for
// translating the API form to one of these.
type SnoozeIncidentRequest struct {
	IncidentUID string
	ActorUID    string // user UID, empty for system
	Until       *time.Time
	Duration    *time.Duration
	Reason      string
	Via         string // "web" | "slack" | "email"
}

// SnoozeIncident silences an incident until a future time. Snooze implies
// acknowledgment — silencing an unack'd incident is the worst-of-both-worlds
// state, so the ack is set if missing. Accepts the org slug.
func (s *Service) SnoozeIncident(
	ctx context.Context, orgSlug string, req *SnoozeIncidentRequest,
) (*models.Incident, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	return s.snoozeIncidentByOrgUID(ctx, org.UID, req)
}

func (s *Service) snoozeIncidentByOrgUID(
	ctx context.Context, orgUID string, req *SnoozeIncidentRequest,
) (*models.Incident, error) {
	until, err := s.resolveSnoozeUntil(req)
	if err != nil {
		return nil, err
	}

	incident, err := s.db.GetIncident(ctx, orgUID, req.IncidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}

		return nil, fmt.Errorf("failed to get incident: %w", err)
	}

	now := s.clock.Now()
	update := models.IncidentUpdate{
		SnoozedUntil: &until,
	}
	if req.ActorUID != "" {
		update.SnoozedBy = &req.ActorUID
	}
	if req.Reason != "" {
		update.SnoozeReason = &req.Reason
	}
	if incident.AcknowledgedAt == nil {
		update.AcknowledgedAt = &now
		if req.ActorUID != "" {
			update.AcknowledgedBy = &req.ActorUID
		}
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return nil, fmt.Errorf("failed to update incident: %w", err)
	}

	incident.SnoozedUntil = &until
	incident.SnoozedBy = update.SnoozedBy
	incident.SnoozeReason = update.SnoozeReason
	if update.AcknowledgedAt != nil {
		incident.AcknowledgedAt = update.AcknowledgedAt
		incident.AcknowledgedBy = update.AcknowledgedBy
	}

	event := models.NewEvent(orgUID, models.EventTypeIncidentSnoozed, models.ActorTypeUser)
	event.IncidentUID = &incident.UID
	event.CheckUID = &incident.CheckUID
	event.Payload = models.JSONMap{
		"until":       until.Format(time.RFC3339),
		"reason":      req.Reason,
		payloadKeyVia: req.Via,
		keyCheckUID:   incident.CheckUID,
	}
	// Best-effort: the check may have been deleted since the incident opened;
	// don't fail the snooze over a missing slug/name.
	if check, chkErr := s.db.GetCheck(ctx, orgUID, incident.CheckUID); chkErr == nil && check != nil {
		event.Payload[keyCheckSlug] = check.Slug
		event.Payload[keyCheckName] = check.Name
	}

	if req.ActorUID != "" {
		event.ActorUID = &req.ActorUID
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		slog.WarnContext(ctx, "Failed to create snooze event",
			"incident_uid", incident.UID, "error", err)
	}

	s.rt.PublishImmediate(ctx, orgUID, incident.CheckUID, realtime.KindIncidents, realtime.KindEvents)

	s.cancelPendingNotifications(ctx, incident.UID, &until)

	return incident, nil
}

// resolveSnoozeUntil normalizes Until / Duration into a single timestamp and
// validates the resulting window. Pulled out so SnoozeIncident stays simple.
func (s *Service) resolveSnoozeUntil(req *SnoozeIncidentRequest) (time.Time, error) {
	now := s.clock.Now()

	var until time.Time

	switch {
	case req.Until != nil:
		until = *req.Until
	case req.Duration != nil:
		until = now.Add(*req.Duration)
	default:
		return time.Time{}, ErrSnoozeMissingDur
	}

	if !until.After(now) {
		return time.Time{}, ErrSnoozeUntilInPast
	}

	if until.Sub(now) > MaxSnoozeDuration {
		return time.Time{}, ErrSnoozeTooLong
	}

	return until, nil
}

// UnsnoozeIncident clears the snooze on an incident. via is "manual" or
// "auto" (the auto sweeper uses "auto"). Accepts the org slug.
func (s *Service) UnsnoozeIncident(
	ctx context.Context, orgSlug, incidentUID, actorUID, via string,
) (*models.Incident, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	return s.unsnoozeIncidentByOrgUID(ctx, org.UID, incidentUID, actorUID, via)
}

func (s *Service) unsnoozeIncidentByOrgUID(
	ctx context.Context, orgUID, incidentUID, actorUID, via string,
) (*models.Incident, error) {
	incident, err := s.db.GetIncident(ctx, orgUID, incidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}

		return nil, fmt.Errorf("failed to get incident: %w", err)
	}

	if incident.SnoozedUntil == nil {
		return incident, nil
	}

	update := models.IncidentUpdate{
		ClearSnoozedUntil: true,
		ClearSnoozedBy:    true,
		ClearSnoozeReason: true,
	}
	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return nil, fmt.Errorf("failed to update incident: %w", err)
	}

	incident.SnoozedUntil = nil
	incident.SnoozedBy = nil
	incident.SnoozeReason = nil

	event := models.NewEvent(orgUID, models.EventTypeIncidentUnsnoozed, models.ActorTypeUser)
	if via == "auto" {
		event.ActorType = models.ActorTypeSystem
	}

	event.IncidentUID = &incident.UID
	event.CheckUID = &incident.CheckUID
	event.Payload = models.JSONMap{payloadKeyVia: via, keyCheckUID: incident.CheckUID}
	// Best-effort: the check may have been deleted since the incident opened;
	// don't fail the unsnooze over a missing slug/name.
	if check, chkErr := s.db.GetCheck(ctx, orgUID, incident.CheckUID); chkErr == nil && check != nil {
		event.Payload[keyCheckSlug] = check.Slug
		event.Payload[keyCheckName] = check.Name
	}

	if actorUID != "" {
		event.ActorUID = &actorUID
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		slog.WarnContext(ctx, "Failed to create unsnooze event",
			"incident_uid", incident.UID, "error", err)
	}

	s.rt.PublishImmediate(ctx, orgUID, incident.CheckUID, realtime.KindIncidents, realtime.KindEvents)

	return incident, nil
}

// ResolveIncidentRequest carries the parameters for ResolveIncident.
type ResolveIncidentRequest struct {
	IncidentUID string
	ActorUID    string
	Note        string
	Via         string // "web" | "slack" | "email"
}

// ResolveIncident closes an incident manually. Idempotent: returns the
// existing incident if already resolved. If a new failure rolls in after a
// manual resolve, the existing incident-creation logic opens a fresh
// incident — manual resolutions are never reopened. Accepts the org slug.
func (s *Service) ResolveIncident(
	ctx context.Context, orgSlug string, req *ResolveIncidentRequest,
) (*models.Incident, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	return s.resolveIncidentByOrgUID(ctx, org.UID, req)
}

func (s *Service) resolveIncidentByOrgUID(
	ctx context.Context, orgUID string, req *ResolveIncidentRequest,
) (*models.Incident, error) {
	incident, err := s.db.GetIncident(ctx, orgUID, req.IncidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}

		return nil, fmt.Errorf("failed to get incident: %w", err)
	}

	if incident.State == models.IncidentStateResolved {
		return incident, nil
	}

	now := s.clock.Now()
	state := models.IncidentStateResolved
	resolutionType := models.ResolutionTypeManual
	update := models.IncidentUpdate{
		State:          &state,
		ResolvedAt:     &now,
		ResolutionType: &resolutionType,
	}
	if req.ActorUID != "" {
		update.ResolvedBy = &req.ActorUID
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return nil, fmt.Errorf("failed to update incident: %w", err)
	}

	incident.State = state
	incident.ResolvedAt = &now
	incident.ResolutionType = &resolutionType
	incident.ResolvedBy = update.ResolvedBy

	// Cancel pending escalation steps that haven't fired yet. Must happen
	// BEFORE emitEvent — the cancel sweep matches every pending job by
	// incidentUid, so doing it after would also drop the resolved
	// notifications we're about to queue.
	s.cancelPendingNotifications(ctx, incident.UID, nil)

	// Route through emitEvent so the channel fan-out and group-incident dedup
	// shared with auto-resolve also runs here. Without this, manual resolves
	// silently skipped notifying every channel that fired on incident open.
	payload := models.JSONMap{
		payloadKeyVia:     req.Via,
		"note":            req.Note,
		"resolution_type": resolutionType,
		keyCheckUID:       incident.CheckUID,
	}
	// Best-effort: the check may have been deleted since the incident opened;
	// don't fail the resolve over a missing slug/name.
	if check, chkErr := s.db.GetCheck(ctx, orgUID, incident.CheckUID); chkErr == nil && check != nil {
		payload[keyCheckSlug] = check.Slug
		payload[keyCheckName] = check.Name
	}
	if req.ActorUID != "" {
		payload[payloadKeyActorUID] = req.ActorUID
	}

	if err := s.emitEvent(ctx, orgUID, models.EventTypeIncidentResolved, incident, payload); err != nil {
		slog.WarnContext(ctx, "Failed to emit resolve event",
			"incident_uid", incident.UID, "error", err)
	}

	return incident, nil
}

// SweepUnsnooze clears expired snoozes on active incidents. Intended to be
// called by a periodic background job. Returns the number of incidents
// unsnoozed and any error from the listing query (per-incident update
// errors are logged and skipped).
func (s *Service) SweepUnsnooze(ctx context.Context) (int, error) {
	incidents, err := s.db.ListExpiredSnoozedIncidents(ctx, s.clock.Now())
	if err != nil {
		return 0, fmt.Errorf("failed to list expired snoozes: %w", err)
	}

	count := 0

	for _, inc := range incidents {
		if _, err := s.unsnoozeIncidentByOrgUID(ctx, inc.OrganizationUID, inc.UID, "", "auto"); err != nil {
			slog.WarnContext(ctx, "Failed to auto-unsnooze incident",
				"incident_uid", inc.UID, "error", err)

			continue
		}

		count++
	}

	return count, nil
}
