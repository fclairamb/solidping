package checks

import (
	"context"
	"fmt"
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
)

// fieldPeriod is the JSON/validation field name for the check period.
const fieldPeriod = "period"

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

	depFields, depErr := s.validateDependsOn(ctx, orgSlug, req.Slug, req.DependsOn)
	if depErr != nil {
		return ValidateCheckResponse{}, depErr
	}

	for _, field := range depFields {
		findings.addError(field.Name, CodeInvalidDependsOn, field.Message)
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

	projected, err := s.entitlements.ProjectChecksPerMinute(ctx, orgUID, entcore.CheckRateProposal{
		ExcludeCheckUID: req.ExcludeCheckUID,
		Type:            req.Type,
		Period:          period,
		Regions:         req.Regions,
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
		return fmt.Sprintf("%d", int64(rate))
	}

	return fmt.Sprintf("%.1f", rate)
}
