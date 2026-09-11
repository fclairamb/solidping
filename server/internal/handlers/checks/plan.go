package checks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// createPlan is everything planCreateCheck resolved on the way to deciding a
// create request is valid. CreateCheck consumes it instead of recomputing any
// of it, which is what keeps "what the dry run checks" and "what the write
// path checks" the same set by construction rather than by discipline.
type createPlan struct {
	checker checkerdef.Checker
	spec    *checkerdef.CheckSpec
	// period is the RAW period: zero when the request proposes none. Several
	// gates (the demo floor, the SMTP send interval) treat zero as "not
	// proposed" and skip, so substituting the default here would silently
	// tighten them.
	period           time.Duration
	regions          []string
	effective        map[string]any
	userProvidedSlug bool
}

// planCreateCheck runs every request-level rule the create path enforces and
// writes NOTHING. It is called by CreateCheck itself and, for a would-create
// item, by the import dry run.
//
// Before spec 2026-09-10-01 the dry run returned as soon as it had decided
// created-vs-updated: a 47-check document that could not possibly be written
// dry-ran to `{"created": 47, "errors": []}`. A dry run that skips the
// validation the real path performs is worse than no dry run, so the
// validation now lives in exactly one function that both callers run.
//
//nolint:cyclop // one linear gate per rule; splitting it would hide the order
func (s *Service) planCreateCheck(
	ctx context.Context, org *models.Organization, req CreateCheckRequest,
) (*createPlan, error) {
	// `internal` is never writable from a request (spec 2026-08-27-01): it is
	// what exempts a check from the quota below, so accepting it here would
	// hand every caller a quota bypass.
	if findings := requestFieldFindings(requestFieldValues{Internal: req.Internal}); len(findings) > 0 {
		return nil, findings[0].Err
	}

	// Enforce the MaxChecks quota before doing any work. Nothing reaching this
	// path can be internal (rejected above), so the quota always applies —
	// server-created internal checks are written through db.CreateCheck and
	// never pass here.
	if s.entitlements != nil {
		if quotaErr := s.entitlements.CheckCreateAllowed(ctx, org.UID); quotaErr != nil {
			return nil, quotaErr
		}
	}

	// Label keys and values, against the one canonical rule — checked here so
	// a key the database would refuse can never reach a write (spec
	// 2026-09-10-01).
	if labelErr := models.ValidateLabels(req.Labels); labelErr != nil {
		return nil, labelErr
	}

	checker, ok := registry.GetChecker(checkerdef.CheckType(req.Type))
	if !ok {
		return nil, ErrInvalidCheckType
	}

	period, err := planPeriod(req.Type, req.Period)
	if err != nil {
		return nil, err
	}

	// The EFFECTIVE period — what the row will carry — is only used by the
	// regionSpread bound, which is how CreateCheck has always checked it
	// (against check.Period, i.e. the default when the request omits one).
	effectivePeriod := period
	if effectivePeriod == 0 {
		effectivePeriod = defaultCheckPeriod
	}

	userProvidedSlug := req.Slug != ""
	if userProvidedSlug {
		if slugErr := validateSlug(req.Slug); slugErr != nil {
			return nil, slugErr
		}
	}

	// Resolve regions BEFORE any config work: credential sealing (spec
	// 2026-07-16-02) keys off the check's private regions, and the tunnel
	// region rules (spec 2026-07-18-07) are validated against the resolved
	// set, not the raw request.
	resolvedRegions, err := s.regions.ResolveRegionsForCheck(ctx, req.Regions, org.UID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve regions: %w", err)
	}

	// Demo-session payload rules (spec 2026-09-06-02). Deliberately AFTER
	// ResolveRegionsForCheck so the region rule is applied to the resolved,
	// about-to-be-stored set rather than to the raw request.
	if demoErr := assertDemoCheckShape(ctx, req.Type, period, resolvedRegions); demoErr != nil {
		return nil, demoErr
	}

	// Normalize the config into its canonical stored shape (e.g. HTTP's
	// username/password → basicAuth fold) before validating it, so the rules
	// below see exactly what will be persisted.
	effective := req.Config

	if req.Config != nil {
		normalized, normErr := normalizeCheckConfig(req.Type, req.Config)
		if normErr != nil {
			return nil, normErr
		}

		effective = normalized
	}

	// The shared config validators — the uniform timeout cap, the
	// address-family rule, the tunnel reference rules and the SMTP send-mode
	// rules. Run from the same list the dry-run validate endpoint reads.
	if cfgErr := s.firstConfigValidationError(
		ctx, org.UID, req.Type, effective, resolvedRegions,
	); cfgErr != nil {
		return nil, cfgErr
	}

	// Send-mode SMTP checks need a period floor so the paired inbox can't be
	// flooded (spec 2026-08-19-04).
	if intervalErr := validateSMTPSendInterval(req.Type, effective, period); intervalErr != nil {
		return nil, intervalErr
	}

	// The checker's own Validate — note it MUTATES the spec (name/slug
	// autogeneration from the URL, heartbeat/email token minting), which is
	// exactly why the resulting spec is carried in the plan rather than
	// recomputed by the caller.
	spec := &checkerdef.CheckSpec{
		Name:   req.Name,
		Slug:   req.Slug,
		Period: period,
		Config: req.Config,
	}

	if err := checker.Validate(spec); err != nil { //nolint:govet // scoped shadow
		return nil, err
	}

	// The remaining request-level guards — regionSpread's bound, the
	// tracerouteOnFailure enum, the flapping knobs' floors and the incident
	// periods' bound — through the same shared list ValidateCheck uses (spec
	// 2026-08-28-14), against the EFFECTIVE period.
	if findings := requestFieldFindings(requestFieldValues{
		RegionSpreadPeriod:        effectivePeriod,
		RegionSpread:              req.RegionSpread,
		ConfirmationPeriodSeconds: req.ConfirmationPeriodSeconds,
		RecoveryPeriodSeconds:     req.RecoveryPeriodSeconds,
		TracerouteOnFailure:       req.TracerouteOnFailure,
		FlappingWindowSeconds:     req.FlappingWindowSeconds,
		FlapBackoffFactor:         req.FlapBackoffFactor,
		MaxRecoveryMultiplier:     req.MaxRecoveryMultiplier,
	}); len(findings) > 0 {
		return nil, findings[0].Err
	}

	return &createPlan{
		checker:          checker,
		spec:             spec,
		period:           period,
		regions:          resolvedRegions,
		effective:        effective,
		userProvidedSlug: userProvidedSlug,
	}, nil
}

// planPeriod parses an optional period string and enforces the per-type bounds
// (spec 2026-07-01-04 D1), returning the RAW period — zero when the request
// proposes none, which is what the bounds check itself treats as "exempt".
func planPeriod(checkType string, raw *string) (time.Duration, error) {
	period := time.Duration(0)

	if raw != nil && *raw != "" {
		var duration timeutils.Duration
		if scanErr := duration.Scan(*raw); scanErr != nil {
			return 0, scanErr
		}

		period = time.Duration(duration)
	}

	// Internal checks and the synthetic sleep type are exempt; an absent
	// period falls back to the default and needs no validation. Nothing
	// planned here is internal (spec 2026-08-27-01), hence the constant.
	if periodErr := validatePeriodForType(checkType, period, false); periodErr != nil {
		return 0, periodErr
	}

	return period, nil
}

// planUpdateCheck runs the request-level rules the update path enforces
// against an EXISTING check, writing nothing. It is the would-update half of
// the import dry run.
//
// It is deliberately not a full mirror of UpdateCheck: PATCH semantics mean
// several of that function's gates only exist relative to what is being
// patched, and reproducing the config merge would mean decrypting and
// re-encrypting a row a dry run must not touch. What it does cover is
// everything an import document can actually get wrong — label keys and
// values, the period bounds, region resolution, the shared config validators
// and the checker's own Validate on the incoming config, and the request-field
// guards. The gap is named in the response rather than hidden: see
// DryRunCaveat.
func (s *Service) planUpdateCheck(
	ctx context.Context, org *models.Organization, existing *models.Check, req *UpsertCheckRequest,
) error {
	if req.Internal != nil {
		return ErrInternalFieldNotWritable
	}

	if demoErr := assertDemoMayWriteCheck(ctx, existing); demoErr != nil {
		return demoErr
	}

	if labelErr := models.ValidateLabels(req.Labels); labelErr != nil {
		return labelErr
	}

	period := time.Duration(existing.Period)

	if req.Period != nil && *req.Period != "" {
		var duration timeutils.Duration
		if scanErr := duration.Scan(*req.Period); scanErr != nil {
			return scanErr
		}

		period = time.Duration(duration)

		if periodErr := validatePeriodForType(existing.Type, period, existing.Internal); periodErr != nil {
			return periodErr
		}
	}

	regionsForCheck := existing.Regions

	if len(req.Regions) > 0 {
		resolved, regErr := s.regions.ResolveRegionsForCheck(ctx, req.Regions, org.UID)
		if regErr != nil {
			return fmt.Errorf("failed to resolve regions: %w", regErr)
		}

		regionsForCheck = resolved
	}

	if req.Config != nil {
		normalized, normErr := normalizeCheckConfig(existing.Type, req.Config)
		if normErr != nil {
			return normErr
		}

		if cfgErr := s.validatePatchedConfig(
			existing.Type, normalized, existing.ConfigSealed != nil && existing.ConfigPrivate == nil,
			existing.ConfigPrivateKeys,
		); cfgErr != nil {
			return cfgErr
		}

		if cfgErr := s.firstConfigValidationError(
			ctx, org.UID, existing.Type, normalized, regionsForCheck,
		); cfgErr != nil {
			return cfgErr
		}

		if intervalErr := validateSMTPSendInterval(existing.Type, normalized, period); intervalErr != nil {
			return intervalErr
		}
	}

	if findings := requestFieldFindings(requestFieldValues{
		RegionSpreadPeriod:        period,
		ConfirmationPeriodSeconds: req.ConfirmationPeriodSeconds,
		RecoveryPeriodSeconds:     req.RecoveryPeriodSeconds,
		TracerouteOnFailure:       req.TracerouteOnFailure,
		FlappingWindowSeconds:     req.FlappingWindowSeconds,
		FlapBackoffFactor:         req.FlapBackoffFactor,
		MaxRecoveryMultiplier:     req.MaxRecoveryMultiplier,
	}); len(findings) > 0 {
		return findings[0].Err
	}

	return nil
}

// PlanUpsert validates an upsert request exactly the way the write path will,
// without writing anything, and reports whether the item would be created.
func (s *Service) PlanUpsert(
	ctx context.Context, org *models.Organization, slug string, req *UpsertCheckRequest,
) (created bool, err error) {
	// Same lookup UpsertCheck performs. A missing row is not an error here
	// (both backends answer sql.ErrNoRows), only a real query failure is.
	existing, lookupErr := s.db.GetCheckByUidOrSlug(ctx, org.UID, slug)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return false, fmt.Errorf("failed to query check: %w", lookupErr)
	}

	if existing != nil {
		return false, s.planUpdateCheck(ctx, org, existing, req)
	}

	if req.Internal != nil {
		return true, ErrInternalFieldNotWritable
	}

	createReq := upsertToCreateRequest(slug, req)

	_, planErr := s.planCreateCheck(ctx, org, createReq)

	return true, planErr
}

// upsertToCreateRequest is the ONE place an UpsertCheckRequest becomes a
// CreateCheckRequest. UpsertCheck and the dry-run planner both use it, so the
// plan is built from exactly the request the write path would build.
func upsertToCreateRequest(slug string, req *UpsertCheckRequest) CreateCheckRequest {
	return CreateCheckRequest{
		Name:          req.Name,
		Slug:          slug,
		Description:   req.Description,
		CheckGroupUID: req.CheckGroupUID,
		Type:          req.Type,
		Config:        req.Config,
		Regions:       req.Regions,
		Enabled:       req.Enabled,
		// Internal is deliberately NOT forwarded (spec 2026-08-27-01).
		Period:                    req.Period,
		Labels:                    req.Labels,
		ConfirmationPeriodSeconds: req.ConfirmationPeriodSeconds,
		RecoveryPeriodSeconds:     req.RecoveryPeriodSeconds,
		TracerouteOnFailure:       req.TracerouteOnFailure,
		ReopenCooldownMultiplier:  req.ReopenCooldownMultiplier,
		FlappingWindowSeconds:     req.FlappingWindowSeconds,
		FlapBackoffFactor:         req.FlapBackoffFactor,
		MaxRecoveryMultiplier:     req.MaxRecoveryMultiplier,
	}
}
