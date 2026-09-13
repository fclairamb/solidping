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

// DryRunCaveat names one validation an import dry run provably cannot perform
// without writing. The spec behind this (2026-09-10-01) is explicit that such
// a gap must be DOCUMENTED IN THE RESPONSE rather than silently omitted — a
// dry run whose unstated limits the caller has to guess at is the failure mode
// the whole change exists to remove.
type DryRunCaveat string

const (
	// DryRunCaveatSlugRace is unconditional: the plan reads the org's current
	// slugs, and a concurrent writer can claim one in the gap before the real
	// import.
	DryRunCaveatSlugRace DryRunCaveat = "a concurrent create can claim a slug between this dry run and the real " +
		"import, turning a planned create into a conflict"

	// DryRunCaveatSecretMerge is reported when the document would update a
	// check that already holds encrypted or region-sealed config: the real
	// update validates the MERGE of the document's config with the stored
	// secrets (mergePatchConfig), which a dry run cannot reproduce without
	// decrypting a row it must not touch. The plan validates the document's
	// config on its own, so a rule that depends on a stored secret field is
	// checked differently here.
	DryRunCaveatSecretMerge DryRunCaveat = "one or more checks this document would update hold encrypted or " +
		"region-sealed config; their configs were validated as written rather than merged with the stored " +
		"secrets, so a rule that depends on a secret field is not fully reproduced here"
)

// caveatSet collects the caveats a dry run accumulated, de-duplicated and in a
// stable order (first-seen), so two runs of the same document report the same
// list.
type caveatSet struct {
	seen  map[DryRunCaveat]struct{}
	order []DryRunCaveat
}

func newCaveatSet() *caveatSet {
	return &caveatSet{seen: make(map[DryRunCaveat]struct{}, 2)}
}

func (c *caveatSet) add(caveat DryRunCaveat) {
	if c == nil {
		return
	}

	if _, ok := c.seen[caveat]; ok {
		return
	}

	c.seen[caveat] = struct{}{}
	c.order = append(c.order, caveat)
}

func (c *caveatSet) list() []DryRunCaveat {
	if c == nil {
		return nil
	}

	return c.order
}

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
//nolint:cyclop,funlen,gocritic // one linear gate per rule; splitting it would hide the order
func (s *Service) planCreateCheck(
	ctx context.Context, org *models.Organization, req CreateCheckRequest, pendingCreates int,
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
		// pendingCreates is non-zero only for a dry run, which has decided on
		// creations it has not written: without it a 100-check document would
		// dry-run clean against a cap of 1 and then fail on item 2 for real.
		if quotaErr := s.entitlements.CheckCreateAllowedWithPending(
			ctx, org.UID, pendingCreates,
		); quotaErr != nil {
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

	period, err := planPeriod(req.Type, req.Period, req.Config)
	if err != nil {
		return nil, err
	}

	// The EFFECTIVE period — what the row will carry — is only used by the
	// regionSpread bound, which is how CreateCheck has always checked it
	// (against check.Period, i.e. the default when the request omits one).
	// defaultPeriodForType is the same type-aware resolution CreateCheck uses
	// to fill in check.Period itself (spec 2026-09-11-07), so this can never
	// disagree with what actually gets stored.
	effectivePeriod := period
	if effectivePeriod == 0 {
		effectivePeriod = defaultPeriodForType(req.Type)
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

	// Reconstruct the export-redacted fields the request omitted but that are
	// derivable from what it did carry (spec 2026-09-11-02) — today, a
	// send-mode SMTP check's `delivery_to` from its `delivery_check_uid`.
	// Runs BEFORE every validator and before checker.Validate, which requires
	// delivery_to on a send-mode check: importing a stripped export must not
	// depend on the operator re-typing a value the document deliberately omits.
	// Applied to the spec's config too, since that is the map checker.Validate
	// sees.
	derivedConfig := s.deriveRedactedFields(ctx, org.UID, req.Type, effective)
	effective = withInjectedConfig(effective, derivedConfig)

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
		Config: withInjectedConfig(req.Config, derivedConfig),
	}

	if validateErr := checker.Validate(spec); validateErr != nil {
		return nil, validateErr
	}

	// A name the caller actually SUPPLIED must survive trimming (spec
	// 2026-09-11-02). An absent one (which this request shape cannot tell
	// apart from `""`) is not an error: checker.Validate has just derived one
	// for most types ("SMTP: mail.acme.com", "Domain: acme.com", …) and
	// CreateCheck falls back to the slug for the types that derive none —
	// which is the same answer the backfill migration gives existing rows, and
	// what checkDisplayName has always rendered anyway.
	//
	// What must never happen again is a check whose STORED name is blank: the
	// exporter omits an empty string, and both ValidateDocument and the import
	// path require `name`, so such a check made the server produce a document
	// the server itself refuses to consume.
	if req.Name != "" {
		if nameErr := validateCheckName(req.Name); nameErr != nil {
			return nil, nameErr
		}
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
func planPeriod(checkType string, raw *string, configMap map[string]any) (time.Duration, error) {
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
	if periodErr := validatePeriodForType(
		checkType, period, false, parsedConfigForType(checkType, configMap),
	); periodErr != nil {
		return 0, periodErr
	}

	return period, nil
}

// plannedUpdatePeriod resolves the period this update would store — the
// existing one when the document proposes none — and holds a proposed one to
// the same bounds the write path enforces.
func plannedUpdatePeriod(existing *models.Check, req *UpsertCheckRequest) (time.Duration, error) {
	if req.Period == nil || *req.Period == "" {
		return time.Duration(existing.Period), nil
	}

	var duration timeutils.Duration
	if scanErr := duration.Scan(*req.Period); scanErr != nil {
		return 0, scanErr
	}

	period := time.Duration(duration)

	// A document that also rewrites the config is held to the NEW script's
	// floor; one that only moves the period, to the stored script's.
	configForPeriod := existing.Config
	if req.Config != nil {
		configForPeriod = req.Config
	}

	if periodErr := validatePeriodForType(
		existing.Type, period, existing.Internal,
		parsedConfigForType(existing.Type, configForPeriod),
	); periodErr != nil {
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
// guards.
//
// The one gap is named in the response, never hidden: when the existing check
// holds encrypted or region-sealed config, the real update validates the MERGE
// of the document's config with the stored secrets, and this records
// DryRunCaveatSecretMerge on the caveat collector so the dry-run response says
// so.
func (s *Service) planUpdateCheck(
	ctx context.Context,
	org *models.Organization,
	existing *models.Check,
	req *UpsertCheckRequest,
	caveats *caveatSet,
) error {
	if req.Internal != nil {
		return ErrInternalFieldNotWritable
	}

	if demoErr := assertDemoMayWriteCheck(ctx, existing); demoErr != nil {
		return demoErr
	}

	// UpsertCheck always forwards the document's name to UpdateCheck, so a
	// blank one is refused there too — checked here so a dry run says so
	// instead of a real run being the first to mention it (spec 2026-09-11-02).
	if nameErr := validateCheckName(req.Name); nameErr != nil {
		return nameErr
	}

	if labelErr := models.ValidateLabels(req.Labels); labelErr != nil {
		return labelErr
	}

	period, periodErr := plannedUpdatePeriod(existing, req)
	if periodErr != nil {
		return periodErr
	}

	regionsForCheck := existing.Regions

	if len(req.Regions) > 0 {
		resolved, regErr := s.regions.ResolveRegionsForCheck(ctx, req.Regions, org.UID)
		if regErr != nil {
			return fmt.Errorf("failed to resolve regions: %w", regErr)
		}

		regionsForCheck = resolved
	}

	if req.Config != nil && checkHoldsSecretConfig(existing) {
		caveats.add(DryRunCaveatSecretMerge)
	}

	if cfgErr := s.planUpdateConfig(ctx, org, existing, req.Config, regionsForCheck, period); cfgErr != nil {
		return cfgErr
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

// checkHoldsSecretConfig reports whether a stored check carries config the
// server splits out and encrypts (or seals to a private region's agents). Only
// for those does the real update's merge differ from validating the document's
// config as written — for a plaintext row the two are the same input, and
// claiming a caveat would be noise.
func checkHoldsSecretConfig(check *models.Check) bool {
	return check.ConfigPrivate != nil || check.ConfigSealed != nil ||
		(check.ConfigPrivateKeys != nil && *check.ConfigPrivateKeys != "" && *check.ConfigPrivateKeys != "[]")
}

// planUpdateConfig runs the config-level rules the update path enforces
// against an incoming config, writing nothing. A nil config is a no-op: the
// document is not changing it.
func (s *Service) planUpdateConfig(
	ctx context.Context,
	org *models.Organization,
	existing *models.Check,
	config map[string]any,
	regionsForCheck []string,
	period time.Duration,
) error {
	if config == nil {
		return nil
	}

	normalized, normErr := normalizeCheckConfig(existing.Type, config)
	if normErr != nil {
		return normErr
	}

	// Mirror what the real update does to the incoming config BEFORE it
	// validates it (spec 2026-09-11-02): an export-redacted field the document
	// omits is preserved from the stored check, or derived from what the
	// document did carry. Without this the dry run would reject the very
	// document the exporter produces — a `secrets: stripped` export omits the
	// email ingest token and the SMTP delivery_to by design.
	//
	// On a COPY: `normalized` can be the caller's own map when the type needs
	// no normalization, and a planner must not write into the document it is
	// checking.
	planned := make(map[string]any, len(normalized)+1)
	for key, value := range normalized {
		planned[key] = value
	}

	preserveAbsentRedactedFields(existing, planned)
	planned = withInjectedConfig(planned, s.deriveRedactedFields(ctx, org.UID, existing.Type, planned))

	// The stored secrets, as PLACEHOLDERS. The real update validates the merge
	// of the document with the encrypted column; a dry run must not open that
	// column, but it can reproduce which keys the merge would produce — which
	// is the difference between "sftp: password or private_key is required"
	// on a document that is exactly right, and a clean dry run of the org's
	// own export (spec 2026-09-11-04).
	//
	// A key the document sets EXPLICITLY is left alone, including an explicit
	// empty value: that clears the secret, and the resulting "required" error
	// is a real one the operator has to see.
	//
	// What this does not reproduce is any rule that depends on a secret's
	// VALUE — which is precisely what DryRunCaveatSecretMerge still says.
	injectSecretPlaceholders(existing.Type, planned, parseConfigPrivateKeys(existing.ConfigPrivateKeys))

	if cfgErr := s.validatePatchedConfig(
		existing.Type, planned, existing.ConfigSealed != nil && existing.ConfigPrivate == nil,
		existing.ConfigPrivateKeys,
	); cfgErr != nil {
		return cfgErr
	}

	if cfgErr := s.firstConfigValidationError(
		ctx, org.UID, existing.Type, planned, regionsForCheck,
	); cfgErr != nil {
		return cfgErr
	}

	return validateSMTPSendInterval(existing.Type, planned, period)
}

// PlanUpsert validates an upsert request exactly the way the write path will,
// without writing anything, and reports whether the item would be created.
// pendingCreates is how many creations the caller has already planned in this
// batch but not written — see planCreateCheck's quota gate. caveats collects
// what this plan could not fully reproduce.
func (s *Service) PlanUpsert(
	ctx context.Context,
	org *models.Organization,
	slug string,
	req *UpsertCheckRequest,
	pendingCreates int,
	caveats *caveatSet,
) (bool, error) {
	// Same lookup UpsertCheck performs. A missing row is not an error here
	// (both backends answer sql.ErrNoRows), only a real query failure is.
	existing, lookupErr := s.db.GetCheckByUidOrSlug(ctx, org.UID, slug)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return false, fmt.Errorf("failed to query check: %w", lookupErr)
	}

	if existing != nil {
		return false, s.planUpdateCheck(ctx, org, existing, req, caveats)
	}

	if req.Internal != nil {
		return true, ErrInternalFieldNotWritable
	}

	createReq := upsertToCreateRequest(slug, req)

	_, planErr := s.planCreateCheck(ctx, org, createReq, pendingCreates)

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
