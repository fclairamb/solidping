package checks

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// Machine codes carried by validate findings (spec 2026-08-26-05). They are
// the stable half of a finding: messages are prose and get reworded, codes are
// what a client may branch on.
const (
	// CodeUnsupportedType is reported when the check type has no checker.
	CodeUnsupportedType = "UNSUPPORTED_TYPE"
	// CodeInvalidConfig covers every config-level rejection: the checker's own
	// Validate, the uniform timeout cap, the address-family rule, the tunnel
	// reference rules and the SMTP send-mode rules.
	CodeInvalidConfig = "INVALID_CONFIG"
	// CodeInvalidPeriod is an unparseable period, or one outside the type's
	// min/max bounds.
	CodeInvalidPeriod = "INVALID_PERIOD"
	// CodeInvalidSlug is a slug that does not match the slug format.
	CodeInvalidSlug = "INVALID_SLUG"
	// CodeSlugTaken is a slug already used by another live check of the org.
	// Advisory by nature — the value can be taken between this answer and the
	// save, which is why creation keeps its own 409.
	CodeSlugTaken = "SLUG_TAKEN"
	// CodeInvalidDependsOn covers the dependsOn edge rules.
	CodeInvalidDependsOn = "INVALID_DEPENDS_ON"
	// CodeOrgRateOverLimit is the org-rate projection warning. It is also the
	// frontend's pointer to the check scheduling page: a client that sees this
	// code renders the "review scheduling" link, without parsing the message.
	CodeOrgRateOverLimit = "ORG_RATE_OVER_LIMIT"
	// CodeInternalNotWritable is the machine code for the internal-field
	// refusal (spec 2026-08-27-01 / 2026-08-28-14).
	CodeInternalNotWritable = "INTERNAL_NOT_WRITABLE"
	// CodeInvalidRegionSpread covers a malformed or out-of-bound regionSpread.
	CodeInvalidRegionSpread = "INVALID_REGION_SPREAD"
	// CodeInvalidTracerouteOnFailure is an unrecognized tracerouteOnFailure value.
	CodeInvalidTracerouteOnFailure = "INVALID_TRACEROUTE_POLICY"
	// CodeInvalidFlappingField covers the three flapping knobs' floors.
	CodeInvalidFlappingField = "INVALID_FLAPPING_FIELD"
	// CodeInvalidIncidentPeriod covers confirmationPeriodSeconds /
	// recoveryPeriodSeconds falling outside [0, MaxIncidentPeriodSeconds].
	CodeInvalidIncidentPeriod = "INVALID_INCIDENT_PERIOD"
)

// fieldPeriod is the JSON/validation field name for the check period.
const fieldPeriod = "period"

// Field names for the request-level guards shared by CreateCheck and
// ValidateCheck (spec 2026-08-28-14) — mirror the JSON tags of both request
// structs exactly, since these are the same field on either shape.
const (
	fieldRegionSpread              = "regionSpread"
	fieldConfirmationPeriodSeconds = "confirmationPeriodSeconds"
	fieldRecoveryPeriodSeconds     = "recoveryPeriodSeconds"
	fieldTracerouteOnFailure       = "tracerouteOnFailure"
	fieldFlappingWindowSeconds     = "flappingWindowSeconds"
	fieldFlapBackoffFactor         = "flapBackoffFactor"
	fieldMaxRecoveryMultiplier     = "maxRecoveryMultiplier"
)

// defaultCheckPeriod mirrors models.NewCheck's default Period. CreateCheck
// resolves the check's effective period to this before validating
// regionSpread whenever the request doesn't propose one; ValidateCheck must
// resolve the same fallback or a bare regionSpread (no period proposed) would
// be checked against 0 and rejected as a false positive.
const defaultCheckPeriod = time.Minute

// requestFieldValues is the request-level field set both CreateCheck and
// ValidateCheck check for exactly the same rules (spec 2026-08-28-14). Every
// rule here is decidable from the request alone — no DB lookup, no write —
// which is what makes sharing one function safe: a caller learns from
// validate exactly what create will refuse.
//
// Fields intentionally NOT here (checkGroupUid, escalationPolicyUid, name,
// description, labels, reopenCooldownMultiplier) have no create-time rule
// that rejects any value — see the spec's Decisions section.
type requestFieldValues struct {
	// Internal is checked first, mirroring CreateCheck's own gate (spec
	// 2026-08-27-01): any non-nil value is refused outright.
	Internal *bool

	// RegionSpreadPeriod is the period regionSpread is measured against —
	// the request's own proposed period when given, else defaultCheckPeriod,
	// exactly as CreateCheck resolves check.Period before validating
	// regionSpread today.
	RegionSpreadPeriod time.Duration
	RegionSpread       *string

	ConfirmationPeriodSeconds *int
	RecoveryPeriodSeconds     *int
	TracerouteOnFailure       *string
	FlappingWindowSeconds     *int
	FlapBackoffFactor         *int
	MaxRecoveryMultiplier     *int
}

// requestFieldFinding is one request-level guard's outcome: enough to build
// either a validate response field (Name/Code/Message) or, for the first one
// in the list, CreateCheck's typed error (Err) — so mapping back never has to
// re-derive it from the message.
type requestFieldFinding struct {
	Name    string
	Code    string
	Message string
	Err     error
}

// requestFieldFindings runs, in the fixed order CreateCheck has always
// checked them in, every request-level (non-config) guard the write paths
// enforce. ValidateCheck turns every finding into a blocking field; CreateCheck
// takes only the first and returns its Err — same error values as before this
// spec, so the write paths' error shape is unchanged.
func requestFieldFindings(v requestFieldValues) []requestFieldFinding {
	var findings []requestFieldFinding

	// `internal` is what exempts a check from the MaxChecks quota (spec
	// 2026-08-27-01) — nothing else is worth reporting once it's present.
	if v.Internal != nil {
		findings = append(findings, requestFieldFinding{
			Name: fieldInternal, Code: CodeInternalNotWritable,
			Message: msgInternalNotWritable, Err: ErrInternalFieldNotWritable,
		})
	}

	if v.RegionSpread != nil && *v.RegionSpread != "" {
		var spread timeutils.Duration
		if err := spread.Scan(*v.RegionSpread); err != nil {
			findings = append(findings, requestFieldFinding{
				Name: fieldRegionSpread, Code: CodeInvalidRegionSpread, Message: err.Error(), Err: err,
			})
		} else if err := validateRegionSpread(time.Duration(spread), v.RegionSpreadPeriod); err != nil {
			findings = append(findings, requestFieldFinding{
				Name: fieldRegionSpread, Code: CodeInvalidRegionSpread, Message: err.Error(), Err: err,
			})
		}
	}

	if v.TracerouteOnFailure != nil {
		if _, ok := parseTraceroutePolicy(*v.TracerouteOnFailure); !ok {
			findings = append(findings, requestFieldFinding{
				Name: fieldTracerouteOnFailure, Code: CodeInvalidTracerouteOnFailure,
				Message: errInvalidTraceroutePolicy.Error(), Err: errInvalidTraceroutePolicy,
			})
		}
	}

	if v.FlappingWindowSeconds != nil && *v.FlappingWindowSeconds < 0 {
		findings = append(findings, requestFieldFinding{
			Name: fieldFlappingWindowSeconds, Code: CodeInvalidFlappingField,
			Message: errFlappingWindowNegative.Error(), Err: errFlappingWindowNegative,
		})
	}
	if v.FlapBackoffFactor != nil && *v.FlapBackoffFactor < 1 {
		findings = append(findings, requestFieldFinding{
			Name: fieldFlapBackoffFactor, Code: CodeInvalidFlappingField,
			Message: errFlapBackoffTooSmall.Error(), Err: errFlapBackoffTooSmall,
		})
	}
	if v.MaxRecoveryMultiplier != nil && *v.MaxRecoveryMultiplier < 1 {
		findings = append(findings, requestFieldFinding{
			Name: fieldMaxRecoveryMultiplier, Code: CodeInvalidFlappingField,
			Message: errMaxRecoveryMultTooSmall.Error(), Err: errMaxRecoveryMultTooSmall,
		})
	}

	if v.ConfirmationPeriodSeconds != nil {
		if err := validateIncidentPeriod(*v.ConfirmationPeriodSeconds); err != nil {
			findings = append(findings, requestFieldFinding{
				Name: fieldConfirmationPeriodSeconds, Code: CodeInvalidIncidentPeriod, Message: err.Error(),
				Err: fmt.Errorf("%s: %w", fieldConfirmationPeriodSeconds, err),
			})
		}
	}
	if v.RecoveryPeriodSeconds != nil {
		if err := validateIncidentPeriod(*v.RecoveryPeriodSeconds); err != nil {
			findings = append(findings, requestFieldFinding{
				Name: fieldRecoveryPeriodSeconds, Code: CodeInvalidIncidentPeriod, Message: err.Error(),
				Err: fmt.Errorf("%s: %w", fieldRecoveryPeriodSeconds, err),
			})
		}
	}

	return findings
}

// validateFindings accumulates one validate pass.
//
// Blocking findings land in fields, advisory ones in warnings — the split the
// wire has always had. Every entry is severity- and code-tagged, so a client
// that merges the two lists can still tell them apart.
type validateFindings struct {
	fields   []base.ValidationErrorField
	warnings []base.ValidationErrorField
}

func (f *validateFindings) addError(name, code, message string) {
	f.fields = append(f.fields, base.ValidationErrorField{
		Name: name, Message: message, Severity: base.SeverityError, Code: code,
	})
}

// addErrorFrom turns a validator error into a field finding, preferring the
// parameter name a *ConfigError names over the caller's fallback.
func (f *validateFindings) addErrorFrom(err error, fallbackName, code string) {
	name := fallbackName
	if configErr := checkerdef.IsConfigError(err); configErr != nil && configErr.Parameter != "" {
		name = configErr.Parameter
	}

	f.addError(name, code, err.Error())
}

func (f *validateFindings) addWarning(name, code, message string) {
	f.warnings = append(f.warnings, base.ValidationErrorField{
		Name: name, Message: message, Severity: base.SeverityWarning, Code: code,
	})
}

func (f *validateFindings) response() ValidateCheckResponse {
	return ValidateCheckResponse{
		Valid:    len(f.fields) == 0,
		Fields:   f.fields,
		Warnings: f.warnings,
	}
}

// configValidationErrors runs, in one pass, every config-level rule that the
// dry-run validate endpoint and the real create/update paths must agree on.
//
// It exists so those two can never drift: the write paths take the FIRST error
// (their contract is a single 400), the validate endpoint turns EVERY one into
// a field finding, but both read the same list from the same function. A rule
// added here is enforced and previewed at once, or not at all.
//
// orgUID may be empty on the validate path when the org could not be resolved;
// the two DB-backed validators are then skipped rather than guessed at (the
// write path re-runs them with a real org anyway).
func (s *Service) configValidationErrors(
	ctx context.Context, orgUID, checkType string, effective map[string]any, checkRegions []string,
) []error {
	var errs []error

	if effective == nil {
		return nil
	}

	// Uniform per-check timeout cap (spec 2026-07-11-05).
	if err := validateConfigTimeout(effective); err != nil {
		errs = append(errs, err)
	}

	// Uniform per-check address-family rule (spec 2026-08-09-02).
	if err := validateIPVersionConfig(checkType, effective); err != nil {
		errs = append(errs, err)
	}

	if orgUID == "" {
		return errs
	}

	// Tunnel reference rules (existence, type, fingerprint, chaining, regions —
	// spec 2026-07-18-07).
	if err := s.validateTunnelConfig(ctx, orgUID, checkType, effective, checkRegions); err != nil {
		errs = append(errs, err)
	}

	// Send-mode SMTP reference validation (spec 2026-08-19-04).
	if err := s.validateSMTPDeliveryConfig(ctx, orgUID, checkType, effective); err != nil {
		errs = append(errs, err)
	}

	return errs
}

// firstConfigValidationError is the write paths' view of
// configValidationErrors: one error, or nil.
func (s *Service) firstConfigValidationError(
	ctx context.Context, orgUID, checkType string, effective map[string]any, checkRegions []string,
) error {
	if errs := s.configValidationErrors(ctx, orgUID, checkType, effective, checkRegions); len(errs) > 0 {
		return errs[0]
	}

	return nil
}

// ValidateCheck validates a check configuration without persisting it.
//
// It reports EVERY finding it can compute, not just the first (spec
// 2026-08-26-05): a form that fixes one field at a time, learning of the next
// problem only after another round trip, is the thing this replaces. Findings
// carry a severity — only an `error` makes `valid` false, a `warning` never
// blocks — and a machine code.
//
// orgSlug is required for everything that needs the org's other rows (dependsOn
// parents, slug uniqueness, the rate projection, tunnel and SMTP references);
// with no resolvable org those checks are skipped rather than guessed at, and
// the create/update path re-validates regardless.
func (s *Service) ValidateCheck(
	ctx context.Context, orgSlug string, req *ValidateCheckRequest,
) (ValidateCheckResponse, error) {
	findings := &validateFindings{}

	checker, ok := registry.GetChecker(checkerdef.CheckType(req.Type))
	if !ok {
		findings.addError(fieldType, CodeUnsupportedType, "unsupported check type")

		return findings.response(), nil
	}

	orgUID := ""
	if org := s.lookupOrgForValidate(ctx, orgSlug); org != nil {
		orgUID = org.UID
	}

	effective := s.validateConfigFindings(ctx, orgUID, req, checker, findings)
	period := s.validatePeriodFindings(req, effective, findings)
	s.validateSlugFindings(ctx, orgUID, req, findings)
	validateRequestFieldFindings(req, period, findings)

	depFields, depErr := s.validateDependsOn(ctx, orgSlug, req.Slug, req.DependsOn)
	if depErr != nil {
		return ValidateCheckResponse{}, depErr
	}

	for i := range depFields {
		findings.addError(depFields[i].Name, CodeInvalidDependsOn, depFields[i].Message)
	}

	// Advisory only, and evaluated LAST so it can never mask a real error.
	if orgUID != "" {
		findings.warnings = append(
			findings.warnings,
			s.regionCapabilityWarnings(ctx, orgUID, req.Type, effective, req.Regions)...,
		)

		s.orgRateWarning(ctx, orgUID, req, period, findings)
	}

	return findings.response(), nil
}

// validateConfigFindings runs the checker's own Validate, normalizes the
// config the way the write paths do, then runs the shared config validators.
// Returns the config the rest of the pass should reason about — normalized
// when normalization succeeded, the raw request config otherwise, so one bad
// rule never costs the caller every other finding.
func (s *Service) validateConfigFindings(
	ctx context.Context, orgUID string, req *ValidateCheckRequest,
	checker checkerdef.Checker, findings *validateFindings,
) map[string]any {
	effective := req.Config

	if cfgErr := checker.Validate(&checkerdef.CheckSpec{Config: req.Config}); cfgErr != nil {
		findings.addErrorFrom(cfgErr, configFieldName, CodeInvalidConfig)
	}

	if req.Config != nil {
		normalized, normErr := normalizeCheckConfig(req.Type, req.Config)
		if normErr != nil {
			findings.addErrorFrom(normErr, configFieldName, CodeInvalidConfig)
		} else {
			effective = normalized
		}
	}

	for _, err := range s.configValidationErrors(ctx, orgUID, req.Type, effective, req.Regions) {
		findings.addErrorFrom(err, configFieldName, CodeInvalidConfig)
	}

	return effective
}

// validatePeriodFindings parses the proposed period and holds it to the same
// bounds the write paths enforce. Returns the parsed period (0 when absent or
// unparseable) for the rate projection to reason about.
func (s *Service) validatePeriodFindings(
	req *ValidateCheckRequest, effective map[string]any, findings *validateFindings,
) time.Duration {
	if req.Period == "" {
		return 0
	}

	var scanned timeutils.Duration
	if err := scanned.Scan(req.Period); err != nil {
		findings.addError(fieldPeriod, CodeInvalidPeriod, fmt.Sprintf("invalid period %q", req.Period))

		return 0
	}

	period := time.Duration(scanned)

	// Nothing validated here can be internal — the flag is not writable
	// (spec 2026-08-27-01) — hence the constant false.
	if err := validatePeriodForType(req.Type, period, false); err != nil {
		findings.addErrorFrom(err, fieldPeriod, CodeInvalidPeriod)
	}

	if err := validateSMTPSendInterval(req.Type, effective, period); err != nil {
		findings.addErrorFrom(err, fieldPeriod, CodeInvalidPeriod)
	}

	return period
}

// validateSlugFindings reports a malformed slug, and a slug already taken by
// another LIVE check of the org — the collision that used to surface only as a
// 409 on submit (spec 2026-08-26-05).
//
// req.ExcludeCheckUID is the check being edited: its own slug must not be
// reported against itself. A soft-deleted check releases its slug (the unique
// index and the lookup both skip deleted rows), so reusing one never collides.
func (s *Service) validateSlugFindings(
	ctx context.Context, orgUID string, req *ValidateCheckRequest, findings *validateFindings,
) {
	if req.Slug == "" {
		return
	}

	if err := validateSlug(req.Slug); err != nil {
		findings.addError(fieldSlug, CodeInvalidSlug, err.Error())

		return
	}

	if orgUID == "" {
		return
	}

	existing, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, req.Slug)
	if err != nil || existing == nil {
		return
	}

	if existing.UID == req.ExcludeCheckUID {
		return
	}

	findings.addError(fieldSlug, CodeSlugTaken, msgSlugConflictOrg)
}

// validateRequestFieldFindings runs the request-level guards CreateCheck
// enforces (spec 2026-08-28-14) — internal, regionSpread's bound, the
// tracerouteOnFailure enum, the flapping knobs' floors, and the incident
// periods' bound — reporting EVERY finding as blocking, unlike CreateCheck
// which stops at the first. period is the proposed period parsed by
// validatePeriodFindings (0 when none was proposed).
func validateRequestFieldFindings(req *ValidateCheckRequest, period time.Duration, findings *validateFindings) {
	regionSpreadPeriod := period
	if period == 0 {
		regionSpreadPeriod = defaultCheckPeriod
	}

	for _, f := range requestFieldFindings(requestFieldValues{
		Internal:                  req.Internal,
		RegionSpreadPeriod:        regionSpreadPeriod,
		RegionSpread:              req.RegionSpread,
		ConfirmationPeriodSeconds: req.ConfirmationPeriodSeconds,
		RecoveryPeriodSeconds:     req.RecoveryPeriodSeconds,
		TracerouteOnFailure:       req.TracerouteOnFailure,
		FlappingWindowSeconds:     req.FlappingWindowSeconds,
		FlapBackoffFactor:         req.FlapBackoffFactor,
		MaxRecoveryMultiplier:     req.MaxRecoveryMultiplier,
	}) {
		findings.addError(f.Name, f.Code, f.Message)
	}
}

// orgRateWarning projects the org's checks-per-minute demand with this check's
// proposed period/regions substituted (or added, for a create) and warns when
// the result would exceed the resolved MaxChecksPerMinute.
//
// A WARNING, never an error: going over the cap does not make the config
// invalid, it makes some executions get skipped. Blocking the save would be
// worse than the problem — the user would be unable to fix an over-limit org
// by editing the very checks that put it there.
//
// Passive types (heartbeat, email) are exempt: they return before the token
// gate and so draw no execution budget at all.
func (s *Service) orgRateWarning(
	ctx context.Context, orgUID string, req *ValidateCheckRequest,
	period time.Duration, findings *validateFindings,
) {
	if s.entitlements == nil || period <= 0 || checkerdef.CheckType(req.Type).IsPassive() {
		return
	}

	enabled := req.Enabled == nil || *req.Enabled
	if !enabled {
		return
	}

	// Resolve the region set the same way the write path does: an empty
	// selection means "the org's defaults", which is frequently more than one
	// region — projecting the raw request would under-count exactly the case
	// (a fresh check, no regions touched) the warning exists for.
	proposedRegions := req.Regions
	if resolved, resolveErr := s.regions.ResolveRegionsForCheck(ctx, req.Regions, orgUID); resolveErr == nil {
		proposedRegions = resolved
	}

	projected, err := s.entitlements.ProjectChecksPerMinute(ctx, orgUID, entcore.CheckRateProposal{
		ExcludeCheckUID: req.ExcludeCheckUID,
		Type:            req.Type,
		Period:          period,
		Regions:         proposedRegions,
		Enabled:         true,
	})
	if err != nil || !projected.Over() {
		return
	}

	findings.addWarning(fieldPeriod, CodeOrgRateOverLimit, fmt.Sprintf(
		"this schedule would put the organization at %s checks/minute, "+
			"over its limit of %d — executions beyond the limit are skipped",
		formatRate(projected.Demand), *projected.Limit,
	))
}

// formatRate renders a per-minute rate the way a human reads it: whole numbers
// bare, fractions to one decimal.
func formatRate(rate float64) string {
	if rate == float64(int64(rate)) {
		return strconv.FormatInt(int64(rate), 10)
	}

	return fmt.Sprintf("%.1f", rate)
}
