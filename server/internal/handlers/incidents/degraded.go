package incidents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Event-payload / details keys carried by degraded incidents (spec
// 2026-09-22-03). They ride the same `incident.created` / `incident.resolved`
// events a check outage does, which is the point: timeline, ack magic links,
// resolution notice and every channel sender keep working with no new plumbing.
const (
	keyDegradedFailures       = "degraded_failures"
	keyDegradedFailureWindow  = "degraded_failures_window"
	keyDegradedFailureSlots   = "degraded_failure_slots"
	keyDegradedFailureCount   = "degraded_failure_count"
	keyDegradedFailureFired   = "degraded_failures_fired"
	keyDegradedSlowCount      = "degraded_slow_count"
	keyDegradedSlow           = "degraded_slow"
	keyDegradedSlowWindow     = "degraded_slow_window"
	keyDegradedSlowSlots      = "degraded_slow_slots"
	keyDegradedSlowFired      = "degraded_slow_fired"
	keyDegradedSlowThreshold  = "slow_threshold_ms"
	keyDegradedAvailabilityPc = "degraded_availability_pct"
	keyDegradedResolveWindow  = "degraded_resolve_window"
	keyDegradedWindowFrom     = "degraded_window_from"
	keyDegradedWindowTo       = "degraded_window_to"
	keyEscalatedToIncidentUID = "escalated_to_incident_uid"
)

// DegradedSnapshot is what the evaluator knew at the instant it decided. Every
// field lands in the incident's `details` and, from there, in the notification
// bodies: a message that says "degraded" without saying how many probes out of
// how many, and whether the target is up right now, sends the reader back to the
// dashboard before they can decide anything — which is exactly what a
// notification is supposed to avoid.
type DegradedSnapshot struct {
	// Failures / FailuresWindow / FailureSlots are M, N and how many countable
	// probes the window actually held.
	Failures       int
	FailuresWindow int
	FailureSlots   int
	FailureMatches int
	FailuresFired  bool

	Slow          int
	SlowWindow    int
	SlowSlots     int
	SlowMatches   int
	SlowFired     bool
	SlowThreshold int

	// AvailabilityPct is the availability over the failure window, the number the
	// notification leads with. Nil when the failure rule was not evaluated.
	AvailabilityPct *float64

	// ResolveWindow is how many consecutive clean countable probes will close
	// this incident. Recorded at open so resolution cannot silently change rule
	// while the incident is live.
	ResolveWindow int

	// WindowFrom / WindowTo bound the probes the numbers describe, so a
	// notification can deep-link into the episode (`graphFrom` / `graphTo`).
	WindowFrom time.Time
	WindowTo   time.Time

	// CurrentlyUp is the check's live status at decision time. "Currently up" is
	// the phrase that stops a degraded notification reading like an outage.
	CurrentlyUp bool
}

// Details renders the snapshot as the incident's `details` JSON.
func (s *DegradedSnapshot) Details() models.JSONMap {
	out := models.JSONMap{
		keyDegradedFailures:      s.Failures,
		keyDegradedFailureWindow: s.FailuresWindow,
		keyDegradedFailureSlots:  s.FailureSlots,
		keyDegradedFailureCount:  s.FailureMatches,
		keyDegradedFailureFired:  s.FailuresFired,
		keyDegradedSlow:          s.Slow,
		keyDegradedSlowWindow:    s.SlowWindow,
		keyDegradedSlowSlots:     s.SlowSlots,
		keyDegradedSlowCount:     s.SlowMatches,
		keyDegradedSlowFired:     s.SlowFired,
		keyDegradedSlowThreshold: s.SlowThreshold,
		keyDegradedResolveWindow: s.ResolveWindow,
		keyDegradedCurrentlyUp:   s.CurrentlyUp,
	}

	if s.AvailabilityPct != nil {
		out[keyDegradedAvailabilityPc] = *s.AvailabilityPct
	}

	if !s.WindowFrom.IsZero() {
		out[keyDegradedWindowFrom] = s.WindowFrom
	}

	if !s.WindowTo.IsZero() {
		out[keyDegradedWindowTo] = s.WindowTo
	}

	return out
}

// keyDegradedCurrentlyUp lives apart from the block above only because the
// notifications package reads it by the same literal and the pairing is easier
// to see here.
const keyDegradedCurrentlyUp = "degraded_currently_up"

// OpenDegradedIncidentRequest is what the degraded evaluator hands over.
type OpenDegradedIncidentRequest struct {
	Check     *models.Check
	StartedAt time.Time
	Title     string
	Snapshot  *DegradedSnapshot
}

// OpenDegradedIncident opens a degraded incident through the normal incident
// machinery.
//
// THREE things it deliberately does NOT do, each one a resolved decision rather
// than an omission:
//
//   - No rollup / dependency cascade, in either direction. A degraded ancestor
//     suppressing a descendant's real outage is a bad trade, and a real outage
//     rolled up under "3 of the last 6 probes were slow" is worse. `applyRollup`
//     is simply never called, and the cascade's own query
//     (FindActiveIncidentsForChecksInWindow) already filters kind='check', so a
//     degraded incident cannot be picked up as a parent either.
//   - No escalation policy. queueLifecycleNotifications skips the escalation
//     branch for this kind: the wording has to read differently from an outage,
//     and paging on-call anyway would undo that. Channel fan-out still happens —
//     notify-only, not silent.
//   - No flap bump. FlapCount counts OUTAGES; letting intermittence inflate it
//     would lengthen the recovery period of the next real incident on evidence
//     that has nothing to do with outage flapping.
//
// Status-page publication IS attempted, and the page-level `publish_degraded`
// opt-in (false everywhere by default) is what decides — see
// incidentpublications.eligiblePages.
func (s *Service) OpenDegradedIncident(
	ctx context.Context, req *OpenDegradedIncidentRequest,
) (*models.Incident, error) {
	if req == nil || req.Check == nil || req.Snapshot == nil {
		return nil, ErrIncidentNotFound
	}

	incident := models.NewIncident(req.Check.OrganizationUID, req.Check.UID, req.StartedAt, req.Title)
	incident.Kind = models.IncidentKindDegraded
	incident.Details = req.Snapshot.Details()

	if err := s.db.CreateIncident(ctx, incident); err != nil {
		// uq_active_degraded_incident is the real dedup guarantee: two evaluator
		// replicas on the same minute both read "no open incident" and both
		// insert. The loser re-fetches the winner's row rather than notifying
		// twice.
		if db.IsUniqueViolation(err) {
			existing, findErr := s.FindActiveDegradedIncident(ctx, req.Check.UID)
			if findErr == nil && existing != nil {
				return existing, nil
			}
		}

		return nil, fmt.Errorf("failed to create degraded incident: %w", err)
	}

	payload := models.JSONMap{
		keyCheckUID:  req.Check.UID,
		keyCheckSlug: req.Check.Slug,
		keyCheckName: req.Check.Name,
		keyStartedAt: req.StartedAt,
	}
	for key, value := range incident.Details {
		payload[key] = value
	}

	if err := s.emitEvent(
		ctx, req.Check.OrganizationUID, models.EventTypeIncidentCreated, incident, payload,
	); err != nil {
		return nil, fmt.Errorf("failed to emit degraded incident created event: %w", err)
	}

	s.publishOpened(ctx, incident)

	return incident, nil
}

// UpdateDegradedIncident refreshes an open degraded incident's numbers in place.
//
// At most one open degraded incident per check, so a worsening episode must
// update rather than notify again. ResolveWindow is carried forward from the
// open, never overwritten: the rule that will close this incident was fixed when
// it opened.
func (s *Service) UpdateDegradedIncident(
	ctx context.Context, incident *models.Incident, snapshot *DegradedSnapshot,
) error {
	details := snapshot.Details()

	if window, ok := degradedResolveWindow(incident); ok {
		details[keyDegradedResolveWindow] = window
	}

	update := models.IncidentUpdate{Details: &details}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to update degraded incident: %w", err)
	}

	incident.Details = details

	return nil
}

// AutoResolveDegradedIncident closes a degraded incident whose condition has
// been false for the governing number of consecutive clean probes.
func (s *Service) AutoResolveDegradedIncident(
	ctx context.Context, incident *models.Incident, resolvedAt time.Time, snapshot *DegradedSnapshot,
) error {
	details := snapshot.Details()

	return s.closeDegradedIncident(ctx, incident, resolvedAt, models.ResolutionTypeAuto, details)
}

// EscalateDegradedIncident closes a degraded incident because a REAL outage just
// opened on the same check, and points the outage back at it.
//
// No in-place promotion: the degraded row resolves with
// `resolution_type = "escalated"` and the outage carries
// `caused_by_incident_uid`, so nothing keyed on `kind` ever has to cope with a
// kind changing mid-life. When the outage resolves the evaluator simply runs
// again; if the check is still intermittent a NEW degraded incident opens.
//
// `paging_suppressed` is deliberately NOT set on the outage. That flag means
// "this incident is rolled up under a parent and must not page", and an outage
// preceded by a degraded episode must page exactly as loudly as any other. The
// caused_by pointer here is provenance, not suppression — which is also why the
// pointer is only written when the rollup logic has not already claimed that
// column for a real parent: a genuine hard-dependency parent outranks this
// annotation, and overwriting it would silently detach the child from the rollup
// that IS suppressing it.
func (s *Service) EscalateDegradedIncident(
	ctx context.Context, degradedIncident, outage *models.Incident, at time.Time,
) error {
	if degradedIncident == nil || outage == nil {
		return nil
	}

	details := models.JSONMap{}
	for key, value := range degradedIncident.Details {
		details[key] = value
	}

	details[keyEscalatedToIncidentUID] = outage.UID

	if err := s.closeDegradedIncident(
		ctx, degradedIncident, at, models.ResolutionTypeEscalated, details,
	); err != nil {
		return err
	}

	if outage.CausedByIncidentUID != nil {
		// Already rolled up under a real parent — leave it alone, see the doc
		// comment.
		return nil
	}

	if err := s.db.UpdateIncident(ctx, outage.UID, &models.IncidentUpdate{
		CausedByIncidentUID: &degradedIncident.UID,
	}); err != nil {
		return fmt.Errorf("failed to record degraded provenance on outage: %w", err)
	}

	outage.CausedByIncidentUID = &degradedIncident.UID

	return nil
}

// closeDegradedIncident is the shared resolve path for both auto and escalated
// closures.
func (s *Service) closeDegradedIncident(
	ctx context.Context, incident *models.Incident, resolvedAt time.Time,
	resolutionType string, details models.JSONMap,
) error {
	state := models.IncidentStateResolved

	update := models.IncidentUpdate{
		State:          &state,
		ResolvedAt:     &resolvedAt,
		ResolutionType: &resolutionType,
		Details:        &details,
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to resolve degraded incident: %w", err)
	}

	// Same ordering rule the manual resolve path documents: cancel pending
	// notifications BEFORE emitting, or the sweep (which matches every pending
	// job by incidentUid) also drops the resolved notifications this emit is
	// about to queue.
	s.cancelPendingNotifications(ctx, incident.UID, nil)

	incident.State = state
	incident.ResolvedAt = &resolvedAt
	incident.ResolutionType = &resolutionType
	incident.Details = details

	payload := models.JSONMap{
		keyCheckUID:        incident.CheckUID,
		keyResolvedAt:      resolvedAt,
		keyDurationSeconds: int64(resolvedAt.Sub(incident.StartedAt).Seconds()),
		"resolution_type":  resolutionType,
	}

	if check, err := s.db.GetCheck(ctx, incident.OrganizationUID, incident.CheckUID); err == nil && check != nil {
		payload[keyCheckSlug] = check.Slug
		payload[keyCheckName] = check.Name
	}

	for key, value := range details {
		payload[key] = value
	}

	if err := s.emitEvent(
		ctx, incident.OrganizationUID, models.EventTypeIncidentResolved, incident, payload,
	); err != nil {
		return fmt.Errorf("failed to emit degraded incident resolved event: %w", err)
	}

	s.publishResolved(ctx, incident)

	return nil
}

// FindActiveDegradedIncident returns the open degraded incident for a check, or
// nil when there is none. Exported so the evaluator and the API can both ask
// without importing the db package's error conventions.
func (s *Service) FindActiveDegradedIncident(
	ctx context.Context, checkUID string,
) (*models.Incident, error) {
	incident, err := s.db.FindActiveDegradedIncident(ctx, checkUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // "no open incident" is a normal answer, not a failure
		}

		return nil, fmt.Errorf("find active degraded incident: %w", err)
	}

	return incident, nil
}

// resolveDegradedForOutage is the hook the check state machine calls when a real
// outage opens: it closes any open degraded incident on that check as
// `escalated`. Best-effort and logged — a degraded incident that outlived its
// escalation is a stale amber row, while an outage that failed to open is an
// outage nobody is paged for.
func (s *Service) resolveDegradedForOutage(ctx context.Context, outage *models.Incident) {
	if outage == nil || outage.Kind != models.IncidentKindCheck {
		return
	}

	open, err := s.FindActiveDegradedIncident(ctx, outage.CheckUID)
	if err != nil {
		slog.WarnContext(ctx, "degraded: failed to look up open degraded incident",
			"checkUid", outage.CheckUID, "error", err)

		return
	}

	if open == nil {
		return
	}

	if err := s.EscalateDegradedIncident(ctx, open, outage, s.clock.Now()); err != nil {
		slog.WarnContext(ctx, "degraded: failed to escalate degraded incident into outage",
			"degradedIncidentUid", open.UID, "incidentUid", outage.UID, "error", err)
	}
}

// degradedResolveWindow reads the governing resolution window recorded on an open
// degraded incident.
func degradedResolveWindow(incident *models.Incident) (int, bool) {
	if incident == nil || incident.Details == nil {
		return 0, false
	}

	switch value := incident.Details[keyDegradedResolveWindow].(type) {
	case float64:
		return int(value), true
	case int64:
		return int(value), true
	case int:
		return value, true
	default:
		return 0, false
	}
}
