package sloalerts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// PolicyResponse is one burn-rate alert policy as returned by the API.
//
// It deliberately mixes CONFIGURATION (windows, threshold, severity) with LIVE
// STATE (current burn rates, whether the window is conclusive, whether it is
// firing right now). The dashboard's Alerting section is useless without both:
// "fires above 14.4x" answers nothing unless the reader can also see that the
// last hour ran at 31x.
type PolicyResponse struct {
	UID                string  `json:"uid"`
	SLOUID             string  `json:"sloUid"`
	Kind               string  `json:"kind"`
	Enabled            bool    `json:"enabled"`
	LongWindowSeconds  int     `json:"longWindowSeconds"`
	ShortWindowSeconds int     `json:"shortWindowSeconds"`
	Threshold          float64 `json:"threshold"`
	Severity           string  `json:"severity"`
	MinSamples         int     `json:"minSamples"`

	LastEvaluatedAt *time.Time `json:"lastEvaluatedAt"`

	// LongBurnRate / ShortBurnRate are recomputed for this request rather than
	// read back from the policy row, so the number on screen is current even
	// when the evaluator is a minute behind. Null means the window carries no
	// countable probe.
	LongBurnRate  *float64 `json:"longBurnRate"`
	ShortBurnRate *float64 `json:"shortBurnRate"`
	// LongSamples / ShortSamples plus the Conclusive flags are what make a
	// silent policy explainable: "not firing" and "not enough data to say" are
	// different answers and the UI must be able to tell them apart.
	LongSamples      int  `json:"longSamples"`
	ShortSamples     int  `json:"shortSamples"`
	LongConclusive   bool `json:"longConclusive"`
	ShortConclusive  bool `json:"shortConclusive"`
	OverThresholdNow bool `json:"overThresholdNow"`

	Firing         bool       `json:"firing"`
	IncidentUID    *string    `json:"incidentUid"`
	IncidentNumber *int64     `json:"incidentNumber"`
	FiringSince    *time.Time `json:"firingSince"`
	// ResolvingSince is the hysteresis anchor: when set, both windows have been
	// below threshold since that instant and the incident auto-resolves once it
	// has held for a full short window.
	ResolvingSince *time.Time `json:"resolvingSince"`
}

// UpdatePolicyRequest is the PATCH body. Nil means "leave alone".
type UpdatePolicyRequest struct {
	Enabled            *bool    `json:"enabled,omitempty"`
	LongWindowSeconds  *int     `json:"longWindowSeconds,omitempty"`
	ShortWindowSeconds *int     `json:"shortWindowSeconds,omitempty"`
	Threshold          *float64 `json:"threshold,omitempty"`
	Severity           *string  `json:"severity,omitempty"`
	MinSamples         *int     `json:"minSamples,omitempty"`
}

// ListPolicies returns both built-in policies for an SLO, with their live burn
// rates and fire state. Missing policy rows are materialized on the way through,
// so an SLO created before this feature existed answers exactly like a new one.
func (s *Service) ListPolicies(
	ctx context.Context, orgSlug, sloIdent string, now time.Time,
) ([]PolicyResponse, error) {
	org, row, err := s.resolveSLO(ctx, orgSlug, sloIdent)
	if err != nil {
		return nil, err
	}

	policies, err := s.slos.EnsureAlertPolicies(ctx, org.UID, row.UID)
	if err != nil {
		return nil, err
	}

	out := make([]PolicyResponse, 0, len(policies))

	for _, policy := range policies {
		resp, buildErr := s.describe(ctx, row, policy, now)
		if buildErr != nil {
			return nil, buildErr
		}

		out = append(out, resp)
	}

	return out, nil
}

// GetPolicy returns one policy with its live state.
func (s *Service) GetPolicy(
	ctx context.Context, orgSlug, sloIdent, policyUID string, now time.Time,
) (PolicyResponse, error) {
	row, policy, err := s.resolvePolicy(ctx, orgSlug, sloIdent, policyUID)
	if err != nil {
		return PolicyResponse{}, err
	}

	return s.describe(ctx, row, policy, now)
}

// UpdatePolicy applies a partial update to a built-in policy.
//
// `kind` is not writable: there are exactly two built-ins and they are what the
// product means by "fast burn" and "slow burn". Everything an operator actually
// needs to tune — the windows, the multiple, the severity, the sample floor —
// is.
func (s *Service) UpdatePolicy(
	ctx context.Context, orgSlug, sloIdent, policyUID string, req UpdatePolicyRequest, now time.Time,
) (PolicyResponse, error) {
	row, policy, err := s.resolvePolicy(ctx, orgSlug, sloIdent, policyUID)
	if err != nil {
		return PolicyResponse{}, err
	}

	if validationErr := validateUpdate(policy, req); validationErr != nil {
		return PolicyResponse{}, validationErr
	}

	update := models.SLOAlertPolicyUpdate{
		Enabled:            req.Enabled,
		LongWindowSeconds:  req.LongWindowSeconds,
		ShortWindowSeconds: req.ShortWindowSeconds,
		Threshold:          req.Threshold,
		Severity:           req.Severity,
		MinSamples:         req.MinSamples,
	}

	// Retuning the thresholds invalidates the hysteresis clock: it was measured
	// against the old numbers, and carrying it forward could auto-resolve an
	// incident on evidence gathered under a rule that no longer applies.
	if req.Threshold != nil || req.LongWindowSeconds != nil || req.ShortWindowSeconds != nil {
		update.ClearBelowThresholdSince = true
	}

	if err := s.db.UpdateSLOAlertPolicy(ctx, policy.UID, update); err != nil {
		return PolicyResponse{}, fmt.Errorf("update alert policy: %w", err)
	}

	updated, err := s.db.GetSLOAlertPolicy(ctx, row.OrganizationUID, policy.UID)
	if err != nil {
		return PolicyResponse{}, fmt.Errorf("reload alert policy: %w", err)
	}

	return s.describe(ctx, row, updated, now)
}

// validateUpdate rejects a configuration the schema would refuse anyway — a 400
// beats a 500 — plus the window ordering, which is the rule that makes
// multiwindow alerting mean anything.
func validateUpdate(policy *models.SLOAlertPolicy, req UpdatePolicyRequest) error {
	longWindow := policy.LongWindowSeconds
	if req.LongWindowSeconds != nil {
		longWindow = *req.LongWindowSeconds
	}

	shortWindow := policy.ShortWindowSeconds
	if req.ShortWindowSeconds != nil {
		shortWindow = *req.ShortWindowSeconds
	}

	if shortWindow <= 0 || longWindow <= 0 || shortWindow > longWindow || longWindow > maxWindowSeconds {
		return ErrInvalidWindows
	}

	if req.Threshold != nil && *req.Threshold <= 0 {
		return ErrInvalidThreshold
	}

	if req.Severity != nil &&
		*req.Severity != models.SLOAlertSeverityCritical &&
		*req.Severity != models.SLOAlertSeverityWarning {
		return ErrInvalidSeverity
	}

	if req.MinSamples != nil && *req.MinSamples < 1 {
		return ErrInvalidMinSamples
	}

	return nil
}

// describe renders one policy with a freshly measured burn rate and its
// current incident binding.
func (s *Service) describe(
	ctx context.Context, row *models.SLO, policy *models.SLOAlertPolicy, now time.Time,
) (PolicyResponse, error) {
	result, err := s.evaluate(ctx, row, policy, now)
	if err != nil {
		return PolicyResponse{}, err
	}

	resp := PolicyResponse{
		UID:                policy.UID,
		SLOUID:             policy.SLOUID,
		Kind:               policy.Kind,
		Enabled:            policy.Enabled,
		LongWindowSeconds:  policy.LongWindowSeconds,
		ShortWindowSeconds: policy.ShortWindowSeconds,
		Threshold:          policy.Threshold,
		Severity:           policy.Severity,
		MinSamples:         policy.MinSamples,
		LastEvaluatedAt:    policy.LastEvaluatedAt,
		LongBurnRate:       result.Long.BurnRate,
		ShortBurnRate:      result.Short.BurnRate,
		LongSamples:        result.Long.Samples,
		ShortSamples:       result.Short.Samples,
		LongConclusive:     result.Long.Conclusive,
		ShortConclusive:    result.Short.Conclusive,
		OverThresholdNow:   result.firing(policy.Threshold),
		ResolvingSince:     policy.BelowThresholdSince,
	}

	incident, err := s.incidents.FindActiveBurnIncident(ctx, policy.SLOUID, policy.UID)
	if err != nil {
		return PolicyResponse{}, err
	}

	if incident != nil {
		number := incident.Number
		resp.Firing = true
		resp.IncidentUID = &incident.UID
		resp.IncidentNumber = &number
		resp.FiringSince = &incident.StartedAt
	}

	return resp, nil
}

// resolveSLO resolves an org slug plus an SLO UID-or-slug.
func (s *Service) resolveSLO(
	ctx context.Context, orgSlug, ident string,
) (*models.Organization, *models.SLO, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, nil, ErrOrganizationNotFound
	}

	row, err := s.db.GetSLO(ctx, org.UID, ident)
	if err == nil {
		return org, row, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("get slo: %w", err)
	}

	row, err = s.db.GetSLOBySlug(ctx, org.UID, ident)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrSLONotFound
		}

		return nil, nil, fmt.Errorf("get slo by slug: %w", err)
	}

	return org, row, nil
}

// resolvePolicy resolves a policy and asserts it belongs to the named SLO —
// without that check, a UID from another org's SLO would be editable by anyone
// who could guess it.
func (s *Service) resolvePolicy(
	ctx context.Context, orgSlug, sloIdent, policyUID string,
) (*models.SLO, *models.SLOAlertPolicy, error) {
	org, row, err := s.resolveSLO(ctx, orgSlug, sloIdent)
	if err != nil {
		return nil, nil, err
	}

	if _, ensureErr := s.slos.EnsureAlertPolicies(ctx, org.UID, row.UID); ensureErr != nil {
		return nil, nil, ensureErr
	}

	policy, err := s.db.GetSLOAlertPolicy(ctx, org.UID, policyUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrPolicyNotFound
		}

		return nil, nil, fmt.Errorf("get alert policy: %w", err)
	}

	if policy.SLOUID != row.UID {
		return nil, nil, ErrPolicyNotFound
	}

	return row, policy, nil
}
