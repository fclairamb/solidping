package incidents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Event-payload keys carried by burn incidents. They ride in the same
// `incident.created` / `incident.resolved` events check outages use, which is
// the point: the timeline, the ack magic links, the resolution notice and every
// channel sender keep working with no new plumbing, and a burn alert is legible
// to anything that already understands an incident.
const (
	keySLOUID              = "slo_uid"
	keySLOName             = "slo_name"
	keySLOSlug             = "slo_slug"
	keySLOAlertPolicyUID   = "slo_alert_policy_uid"
	keySLOAlertPolicyKind  = "slo_alert_policy_kind"
	keyBurnSeverity        = "severity"
	keyBurnThreshold       = "burn_threshold"
	keyBurnRateLong        = "burn_rate_long"
	keyBurnRateShort       = "burn_rate_short"
	keyBurnPeak            = "burn_peak"
	keyBurnLongWindowSecs  = "long_window_seconds"
	keyBurnShortWindowSecs = "short_window_seconds"
	keyBudgetRemainingSecs = "budget_remaining_seconds"
	keyProjectedExhaustion = "projected_exhaustion_at"
	keyTargetPct           = "target_pct"
)

// BurnSnapshot is what the evaluator knows at the instant it decides. Every
// field lands in the incident's `details` and, from there, in the notification
// bodies — a page that says "your budget is burning" without saying how fast,
// how much is left and when it runs out is a page that forces the reader back
// to the dashboard before they can act.
type BurnSnapshot struct {
	SLOUID    string
	SLOName   string
	SLOSlug   string
	TargetPct float64

	PolicyUID          string
	PolicyKind         string
	Severity           string
	Threshold          float64
	LongWindowSeconds  int
	ShortWindowSeconds int

	LongBurnRate  float64
	ShortBurnRate float64

	BudgetRemainingSeconds int64
	ProjectedExhaustionAt  *time.Time
}

// Details renders the snapshot as the incident's `details` JSON.
func (s *BurnSnapshot) Details() models.JSONMap {
	out := models.JSONMap{
		keySLOUID:              s.SLOUID,
		keySLOName:             s.SLOName,
		keySLOSlug:             s.SLOSlug,
		keyTargetPct:           s.TargetPct,
		keySLOAlertPolicyUID:   s.PolicyUID,
		keySLOAlertPolicyKind:  s.PolicyKind,
		keyBurnSeverity:        s.Severity,
		keyBurnThreshold:       s.Threshold,
		keyBurnLongWindowSecs:  s.LongWindowSeconds,
		keyBurnShortWindowSecs: s.ShortWindowSeconds,
		keyBurnRateLong:        s.LongBurnRate,
		keyBurnRateShort:       s.ShortBurnRate,
		keyBurnPeak:            s.LongBurnRate,
		keyBudgetRemainingSecs: s.BudgetRemainingSeconds,
	}

	if s.ProjectedExhaustionAt != nil {
		out[keyProjectedExhaustion] = *s.ProjectedExhaustionAt
	}

	return out
}

// OpenSLOBurnIncidentRequest is what the burn evaluator hands over.
//
// AnchorCheck is the incident's routing anchor: `incidents.check_uid` is NOT
// NULL and channel resolution plus escalation-policy resolution both key on a
// check, so a burn incident borrows one. For a check-scoped SLO that is the
// SLO's own check; for a group-scoped SLO it is a deterministic member. The
// incident's `check_group_uid` stays NULL on purpose — setting it would collide
// with `uq_active_group_incident` and would route through the group fan-out,
// which deliberately skips escalation policies.
type OpenSLOBurnIncidentRequest struct {
	OrganizationUID string
	AnchorCheck     *models.Check
	StartedAt       time.Time
	Title           string
	Snapshot        *BurnSnapshot
}

// OpenSLOBurnIncident opens a burn-rate incident through the normal incident
// machinery.
//
// It is the whole reason burn alerting is not a parallel alerting path: by
// landing in `emitEvent` with an `incident.created`, the alert inherits channel
// fan-out, escalation policies, severity-gated routing at step-fire time,
// ack/snooze/manual-resolve, the resolution notice and the incident timeline,
// none of which this feature had to reimplement.
//
// Status-page publication is deliberately NOT triggered: a burn rate is an
// internal operations signal about an error budget, not a customer-facing
// outage. The outage that caused it publishes on its own.
func (s *Service) OpenSLOBurnIncident(
	ctx context.Context, req *OpenSLOBurnIncidentRequest,
) (*models.Incident, error) {
	if req == nil || req.AnchorCheck == nil {
		return nil, ErrIncidentNotFound
	}

	incident := models.NewIncident(req.OrganizationUID, req.AnchorCheck.UID, req.StartedAt, req.Title)
	incident.Kind = models.IncidentKindSLOBurn
	incident.SLOUID = &req.Snapshot.SLOUID
	incident.SLOAlertPolicyUID = &req.Snapshot.PolicyUID
	incident.Details = req.Snapshot.Details()

	if err := s.db.CreateIncident(ctx, incident); err != nil {
		// The partial unique index uq_active_slo_burn_incident is the real
		// dedup guarantee: two evaluator replicas on the same minute both read
		// "no open incident" and both insert. The loser re-fetches the winner's
		// row rather than double-paging.
		if db.IsUniqueViolation(err) {
			existing, findErr := s.db.FindActiveBurnIncident(ctx, req.Snapshot.SLOUID, req.Snapshot.PolicyUID)
			if findErr == nil {
				return existing, nil
			}
		}

		return nil, fmt.Errorf("failed to create burn incident: %w", err)
	}

	payload := models.JSONMap{
		keyCheckUID:  req.AnchorCheck.UID,
		keyCheckSlug: req.AnchorCheck.Slug,
		keyCheckName: req.AnchorCheck.Name,
		keyStartedAt: req.StartedAt,
	}
	for key, value := range req.Snapshot.Details() {
		payload[key] = value
	}

	if err := s.emitEvent(
		ctx, req.OrganizationUID, models.EventTypeIncidentCreated, incident, payload,
	); err != nil {
		return nil, fmt.Errorf("failed to emit burn incident created event: %w", err)
	}

	return incident, nil
}

// UpdateSLOBurnIncident refreshes an open burn incident in place.
//
// Dedup means at most ONE open incident per (SLO, policy); a burn that gets
// worse must therefore update rather than page again. `burn_peak` is carried
// forward as the worst long-window rate seen during the incident, because "it
// hit 40x at 3am" is the number the post-mortem wants and the current rate has
// long since forgotten it.
func (s *Service) UpdateSLOBurnIncident(
	ctx context.Context, incident *models.Incident, snapshot *BurnSnapshot,
) error {
	details := snapshot.Details()

	if peak, ok := burnPeak(incident); ok && peak > snapshot.LongBurnRate {
		details[keyBurnPeak] = peak
	}

	update := models.IncidentUpdate{Details: &details}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to update burn incident: %w", err)
	}

	incident.Details = details

	return nil
}

// AutoResolveSLOBurnIncident closes a burn incident that has cooled off.
//
// Routed through emitEvent for the same reason the open is: whoever was paged
// has to be told it ended, and the resolution notice / channel fan-out that
// does that is the incident machinery's, not this feature's.
func (s *Service) AutoResolveSLOBurnIncident(
	ctx context.Context, incident *models.Incident, resolvedAt time.Time, snapshot *BurnSnapshot,
) error {
	state := models.IncidentStateResolved
	resolutionType := models.ResolutionTypeAuto
	details := snapshot.Details()

	if peak, ok := burnPeak(incident); ok && peak > snapshot.LongBurnRate {
		details[keyBurnPeak] = peak
	}

	update := models.IncidentUpdate{
		State:          &state,
		ResolvedAt:     &resolvedAt,
		ResolutionType: &resolutionType,
		Details:        &details,
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to resolve burn incident: %w", err)
	}

	// Same ordering rule the manual resolve path documents: cancel pending
	// escalation steps BEFORE emitting, or the sweep (which matches every
	// pending job by incidentUid) also drops the resolved notifications this
	// emit is about to queue.
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
		return fmt.Errorf("failed to emit burn incident resolved event: %w", err)
	}

	return nil
}

// FindActiveBurnIncident returns the open burn incident for a (SLO, policy)
// pair, or nil when there is none. Exported so the evaluator and the alerting
// API can both ask without importing the db package's error conventions.
func (s *Service) FindActiveBurnIncident(
	ctx context.Context, sloUID, policyUID string,
) (*models.Incident, error) {
	incident, err := s.db.FindActiveBurnIncident(ctx, sloUID, policyUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // "no open incident" is a normal answer, not a failure
		}

		return nil, fmt.Errorf("find active burn incident: %w", err)
	}

	return incident, nil
}

// burnPeak reads the worst long-window burn rate recorded so far.
func burnPeak(incident *models.Incident) (float64, bool) {
	if incident == nil || incident.Details == nil {
		return 0, false
	}

	switch value := incident.Details[keyBurnPeak].(type) {
	case float64:
		return value, true
	case int64:
		return float64(value), true
	case int:
		return float64(value), true
	default:
		return 0, false
	}
}
