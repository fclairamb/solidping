package checks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// fieldName is the JSON/validation field name for the check name.
const fieldName = "name"

// msgNameRequired is the client-facing wording for a blank name. Capitalized
// because it is rendered verbatim as a field message in the dashboard, next to
// the other field messages in handler.go.
const msgNameRequired = "Name is required and cannot be blank"

// errCheckNameRequired is returned by create and update when the resulting
// check name would be empty or whitespace-only (spec 2026-09-11-02).
//
// Why the API ever accepted one: the validation treated an empty string as
// "present". The consequences only showed up two systems later — the exporter
// omits an empty string, and both ValidateDocument and the import path require
// `name`, so the instance produced a config-as-code document it would itself
// refuse to consume. One org's export failed its validator with `missing
// required key 'name'` and the offending check had to be excluded from the
// tracked file.
var errCheckNameRequired = errors.New("name is required and cannot be blank")

// validateCheckName enforces "required, min length 1 AFTER trimming".
//
// Trimming is the point: `" "` is exactly as unusable as `""` — it renders as
// a blank row in the dashboard and exports as a name nobody can search for —
// and accepting it would leave the same hole one space wide. The stored value
// is NOT trimmed here: this validates, it does not rewrite what the caller
// asked for.
func validateCheckName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errCheckNameRequired
	}

	return nil
}

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

// defaultCheckPeriod is the flat fallback defaultPeriodForType uses for a
// check type that declares no DefaultPeriod of its own (http, tcp, icmp, …).
// It happens to equal models.NewCheck's own constant, but that is no longer
// load-bearing anywhere below NewCheck itself: every other reader resolves
// through defaultPeriodForType, which is type-aware. See NewCheck's comment
// for why NewCheck keeps this flat value directly instead of calling the
// resolver.
const defaultCheckPeriod = time.Minute

// defaultPeriodForType resolves the period a check of this type gets when a
// create/import/validate request supplies none (spec 2026-09-11-07). It is
// the ONE place that resolution happens — CreateCheck, planCreateCheck's
// effective period (for the regionSpread bound) and
// validateRequestFieldFindings (POST /checks/validate, same bound) all call
// this, so the three cannot answer differently about the same no-period
// request the way they used to when CreateCheck alone fell through to
// models.NewCheck's flat 1-minute constant regardless of type.
//
// Resolution: the type's own MinPeriod/DefaultPeriod (checkerdef metadata),
// clamped UP to MinPeriod if a meta ever declared a DefaultPeriod below its
// own floor — see TestCheckTypeMetaDefaultPeriodNeverBelowMinPeriod in
// checkerdef, which pins that no meta does today so the clamp here is a
// by-construction guarantee, not a rescue for a known-bad value. Falls back to
// defaultCheckPeriod for a type with no DefaultPeriod at all (0 = "use the
// global default").
func defaultPeriodForType(checkType string) time.Duration {
	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType))
	if meta == nil || meta.DefaultPeriod == 0 {
		return defaultCheckPeriod
	}

	if meta.MinPeriod > 0 && meta.DefaultPeriod < meta.MinPeriod {
		return meta.MinPeriod
	}

	return meta.DefaultPeriod
}

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
func requestFieldFindings(values requestFieldValues) []requestFieldFinding {
	var findings []requestFieldFinding

	// `internal` is what exempts a check from the MaxChecks quota (spec
	// 2026-08-27-01) — nothing else is worth reporting once it's present.
	if values.Internal != nil {
		findings = append(findings, requestFieldFinding{
			Name: fieldInternal, Code: CodeInternalNotWritable,
			Message: msgInternalNotWritable, Err: ErrInternalFieldNotWritable,
		})
	}

	findings = appendRegionSpreadFinding(findings, values)
	findings = appendTracerouteFinding(findings, values)
	findings = appendFlappingFindings(findings, values)
	findings = appendIncidentPeriodFindings(findings, values)

	return findings
}

// appendRegionSpreadFinding checks regionSpread's 0 <= spread < period bound
// (spec 2026-07-20-05) — split out of requestFieldFindings to keep its
// cyclomatic complexity down.
func appendRegionSpreadFinding(findings []requestFieldFinding, values requestFieldValues) []requestFieldFinding {
	if values.RegionSpread == nil || *values.RegionSpread == "" {
		return findings
	}

	var spread timeutils.Duration
	if err := spread.Scan(*values.RegionSpread); err != nil {
		return append(findings, requestFieldFinding{
			Name: fieldRegionSpread, Code: CodeInvalidRegionSpread, Message: err.Error(), Err: err,
		})
	}

	if err := validateRegionSpread(time.Duration(spread), values.RegionSpreadPeriod); err != nil {
		return append(findings, requestFieldFinding{
			Name: fieldRegionSpread, Code: CodeInvalidRegionSpread, Message: err.Error(), Err: err,
		})
	}

	return findings
}

// appendTracerouteFinding checks the tracerouteOnFailure enum (spec
// 2026-08-21-10).
func appendTracerouteFinding(findings []requestFieldFinding, values requestFieldValues) []requestFieldFinding {
	if values.TracerouteOnFailure == nil {
		return findings
	}

	if _, ok := parseTraceroutePolicy(*values.TracerouteOnFailure); !ok {
		findings = append(findings, requestFieldFinding{
			Name: fieldTracerouteOnFailure, Code: CodeInvalidTracerouteOnFailure,
			Message: errInvalidTraceroutePolicy.Error(), Err: errInvalidTraceroutePolicy,
		})
	}

	return findings
}

// appendFlappingFindings checks the three adaptive-recovery knobs' floors
// (spec 2026-06-30-07).
func appendFlappingFindings(findings []requestFieldFinding, values requestFieldValues) []requestFieldFinding {
	if values.FlappingWindowSeconds != nil && *values.FlappingWindowSeconds < 0 {
		findings = append(findings, requestFieldFinding{
			Name: fieldFlappingWindowSeconds, Code: CodeInvalidFlappingField,
			Message: errFlappingWindowNegative.Error(), Err: errFlappingWindowNegative,
		})
	}
	if values.FlapBackoffFactor != nil && *values.FlapBackoffFactor < 1 {
		findings = append(findings, requestFieldFinding{
			Name: fieldFlapBackoffFactor, Code: CodeInvalidFlappingField,
			Message: errFlapBackoffTooSmall.Error(), Err: errFlapBackoffTooSmall,
		})
	}
	if values.MaxRecoveryMultiplier != nil && *values.MaxRecoveryMultiplier < 1 {
		findings = append(findings, requestFieldFinding{
			Name: fieldMaxRecoveryMultiplier, Code: CodeInvalidFlappingField,
			Message: errMaxRecoveryMultTooSmall.Error(), Err: errMaxRecoveryMultTooSmall,
		})
	}

	return findings
}

// appendIncidentPeriodFindings checks confirmationPeriodSeconds and
// recoveryPeriodSeconds against [0, MaxIncidentPeriodSeconds] (spec
// 2026-05-08-02).
func appendIncidentPeriodFindings(findings []requestFieldFinding, values requestFieldValues) []requestFieldFinding {
	if values.ConfirmationPeriodSeconds != nil {
		if err := validateIncidentPeriod(*values.ConfirmationPeriodSeconds); err != nil {
			findings = append(findings, requestFieldFinding{
				Name: fieldConfirmationPeriodSeconds, Code: CodeInvalidIncidentPeriod, Message: err.Error(),
				Err: fmt.Errorf("%s: %w", fieldConfirmationPeriodSeconds, err),
			})
		}
	}
	if values.RecoveryPeriodSeconds != nil {
		if err := validateIncidentPeriod(*values.RecoveryPeriodSeconds); err != nil {
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
		regionSpreadPeriod = defaultPeriodForType(req.Type)
	}

	fieldFindings := requestFieldFindings(requestFieldValues{
		Internal:                  req.Internal,
		RegionSpreadPeriod:        regionSpreadPeriod,
		RegionSpread:              req.RegionSpread,
		ConfirmationPeriodSeconds: req.ConfirmationPeriodSeconds,
		RecoveryPeriodSeconds:     req.RecoveryPeriodSeconds,
		TracerouteOnFailure:       req.TracerouteOnFailure,
		FlappingWindowSeconds:     req.FlappingWindowSeconds,
		FlapBackoffFactor:         req.FlapBackoffFactor,
		MaxRecoveryMultiplier:     req.MaxRecoveryMultiplier,
	})
	for i := range fieldFindings {
		findings.addError(fieldFindings[i].Name, fieldFindings[i].Code, fieldFindings[i].Message)
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
