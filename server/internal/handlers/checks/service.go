package checks

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/activation"
	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkheartbeat"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sloghook"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// ValidateCheckRequest is the request body for the validate check endpoint.
//
// Slug and DependsOn are optional. When DependsOn is supplied, validation
// resolves parent slugs against the org's existing checks and runs the same
// cycle/self/cross-org/kind validators that PUT-by-slug runs — without
// writing anything. The frontend uses this for inline cycle errors on the
// create form before the user clicks save.
type ValidateCheckRequest struct {
	Type   string         `json:"type"`
	Slug   string         `json:"slug,omitempty"`
	Config map[string]any `json:"config"`
	// Regions is the check's selected region set. Supplied so the tunnel region
	// rules (spec 2026-07-18-07) can be validated live — a `tunnelCheckUid`
	// reference is legal or not depending on which regions the check runs in.
	Regions   []string             `json:"regions,omitempty"`
	DependsOn []ExportedDependency `json:"dependsOn,omitempty"`
}

// ValidateCheckResponse is the response body for the validate check endpoint.
type ValidateCheckResponse struct {
	Valid  bool                        `json:"valid"`
	Fields []base.ValidationErrorField `json:"fields,omitempty"`
	// Warnings are advisory, per-field notes that do NOT make the config
	// invalid — `valid` stays true and the write path accepts it. Today the
	// only source is the IPv6-in-a-region-that-reports-no-IPv6 hint (spec
	// 2026-08-15-11), which is deliberately never a rejection: the advertised
	// capability lags reality and the run-time probe is the authority.
	Warnings []base.ValidationErrorField `json:"warnings,omitempty"`
}

// eventPayloadCheckUIDKey is the JSON key used in check.* event payloads
// for the check UID. Centralized so producers and consumers (notably the
// express runner) cannot drift out of sync.
const eventPayloadCheckUIDKey = "check_uid"

// dependsOnFieldName is the JSON/validation field name for the dependency
// payload — extracted to a constant because it appears in multiple per-row
// validation messages.
const dependsOnFieldName = "dependsOn"

// configFieldName is the JSON/validation field name for the config payload.
const configFieldName = "config"

// slugRegex validates slug format: lowercase letter, then 2-99 lowercase letters/digits/hyphens.
// Total length: 3-100 characters.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,99}$`)

// slugInvalidCharsRegex matches characters that are not valid in a slug.
var slugInvalidCharsRegex = regexp.MustCompile(`[^a-z0-9-]`)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrCheckNotFound is returned when a check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrInvalidCheckType is returned when an unsupported check type is provided.
	ErrInvalidCheckType = errors.New("invalid check type")
	// ErrNotHeartbeatCheck is returned when a heartbeat-only operation (e.g.
	// rotate-token) is attempted on a check of a different type.
	ErrNotHeartbeatCheck = errors.New("check is not a heartbeat type")
	// ErrNoAgentsToSealTo is returned when a check targeting ONLY private
	// region(s) carries secrets but no active agent exists to seal them to
	// (spec 2026-07-16-02, Decision 4). The write is refused rather than
	// falling back to a server-decryptable copy: "targeting private region(s)
	// only → seal-only, no org-DEK copy" is an unconditional invariant, and a
	// check whose secrets cannot be sealed cannot run on an agent anyway (the
	// agent would receive public config only and merge nothing).
	ErrNoAgentsToSealTo = errors.New("no active agent to seal credentials to")
	// ErrSlugConflict is returned when a slug already exists.
	ErrSlugConflict = errors.New("slug already exists")
	// ErrSlugGenerationFailed is returned when a unique slug cannot be generated.
	ErrSlugGenerationFailed = errors.New("could not generate unique slug after 99 attempts")
	// ErrInvalidSlugFormat is returned when a slug has an invalid format (e.g., looks like a UUID).
	ErrInvalidSlugFormat = errors.New("invalid slug format")
	// ErrInvalidCursor is returned when the cursor parameter is malformed.
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrUnsupportedExportVersion is returned when the export document has an unsupported version.
	ErrUnsupportedExportVersion = errors.New("unsupported export version")
	// ErrEmptyChecksArray is returned when the import document has no checks.
	ErrEmptyChecksArray = errors.New("checks array must not be empty")

	// Dependency-related sentinel errors used by the import / PUT-by-slug
	// dependency apply paths. Each is wrapped with %w plus context (the
	// offending parent slug, etc.) so callers can both errors.Is for the
	// shape and read the message for diagnostics.
	errDepEmptyParentSlug   = errors.New("dependsOn: empty parentSlug")
	errDepUnknownParent     = errors.New("dependsOn: unknown parent slug")
	errDepCrossOrg          = errors.New("dependsOn: cross-org parent not allowed")
	errDepSelfEdge          = errors.New("dependsOn: self-edge not allowed")
	errDepInvalidKind       = errors.New("dependsOn: invalid kind")
	errDepDuplicateParent   = errors.New("dependsOn: duplicate parent")
	errDepCycleDetected     = errors.New("dependsOn: cycle detected")
	errDepCycleSimpleEdge   = errors.New("cycle detected")
	errDepInvalidImportKind = errors.New("invalid kind")
	errDepImportSelfEdge    = errors.New("self-edge not allowed")
)

// isUUID checks if a string is a valid UUID.
func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// errIncidentPeriodOutOfRange is returned when ConfirmationPeriodSeconds
// or RecoveryPeriodSeconds is outside [0, 86400]. Cap is one day so
// typos can't accidentally suspend alerting indefinitely.
var errIncidentPeriodOutOfRange = errors.New("must be between 0 and 86400 seconds (one day)")

// MaxIncidentPeriodSeconds is the inclusive upper bound on
// ConfirmationPeriodSeconds / RecoveryPeriodSeconds. Exported so callers
// outside this package (the third-party importers) can clamp foreign values
// into range instead of failing the whole check on import.
const MaxIncidentPeriodSeconds = 86400

// validateIncidentPeriod range-checks a confirmation/recovery period in
// seconds. 0 is allowed and means "fire immediately on the first signal".
func validateIncidentPeriod(seconds int) error {
	if seconds < 0 || seconds > MaxIncidentPeriodSeconds {
		return errIncidentPeriodOutOfRange
	}

	return nil
}

// errRegionSpreadOutOfRange is returned when regionSpread is negative or is
// not strictly less than the check's period (spec 2026-07-20-05). Absent/null
// regionSpread reverts to the period/region_count default and is never
// validated here.
var errRegionSpreadOutOfRange = errors.New("regionSpread must be at least 0 and less than the check period")

// validateRegionSpread enforces 0 <= spread < period. period is the check's
// effective period (the new value on a period edit, else the stored one).
func validateRegionSpread(spread, period time.Duration) error {
	if spread < 0 || spread >= period {
		return errRegionSpreadOutOfRange
	}

	return nil
}

// applyRegionSpreadUpdate maps a PATCH's regionSpread string onto a CheckUpdate
// (spec 2026-07-20-05): an explicit empty string clears the override back to
// the period/region_count default; a value is parsed and validated against the
// effective period before being set.
func applyRegionSpreadUpdate(update *models.CheckUpdate, raw string, effectivePeriod time.Duration) error {
	if raw == "" {
		update.ClearRegionSpread = true

		return nil
	}

	var spread timeutils.Duration
	if err := spread.Scan(raw); err != nil {
		return err
	}

	if err := validateRegionSpread(time.Duration(spread), effectivePeriod); err != nil {
		return err
	}

	update.RegionSpread = &spread

	return nil
}

// periodBoundError reports a check period outside its type's allowed bounds
// (spec 2026-07-01-04 D1). It is a distinct error type (not a sentinel)
// because the message embeds the per-type bound ("period for browser checks
// must be at least 60s") and must surface verbatim as a 400 VALIDATION_ERROR.
type periodBoundError struct {
	CheckType string
	Bound     time.Duration
	TooLong   bool
}

func (e *periodBoundError) Error() string {
	direction := "at least"
	if e.TooLong {
		direction = "at most"
	}

	return fmt.Sprintf("period for %s checks must be %s %s", e.CheckType, direction, formatPeriodBound(e.Bound))
}

// formatPeriodBound renders a period bound compactly, the way users write
// periods: whole hours as "6h", whole minutes at or above ten minutes as
// "15m", and everything else in seconds ("60s", "30s", "10s") so the common
// floors read exactly as the spec states them.
func formatPeriodBound(bound time.Duration) string {
	switch {
	case bound >= time.Hour && bound%time.Hour == 0:
		return fmt.Sprintf("%dh", bound/time.Hour)
	case bound >= 10*time.Minute && bound%time.Minute == 0:
		return fmt.Sprintf("%dm", bound/time.Minute)
	default:
		return fmt.Sprintf("%ds", bound/time.Second)
	}
}

// validatePeriodForType enforces the server-side period bounds for a check
// type (spec 2026-07-01-04 D1): min = the type's MinPeriod (else the global
// 10s floor), max = the type's MaxPeriod (else none). A zero period means
// "not provided" (the default applies) and always passes. Internal checks
// (worker self-stats etc.) and the synthetic `sleep` type are exempt — sleep
// is the load-harness dial (spec 2026-07-01-01) and must stay free to express
// pathological mixes. Existing rows are grandfathered: this runs only on
// create/update writes, never via migration.
func validatePeriodForType(checkType string, period time.Duration, internal bool) error {
	if period == 0 || internal || checkerdef.CheckType(checkType) == checkerdef.CheckTypeSleep {
		return nil
	}

	minPeriod := checkerdef.GlobalMinPeriod

	var maxPeriod time.Duration

	if meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType)); meta != nil {
		if meta.MinPeriod > 0 {
			minPeriod = meta.MinPeriod
		}

		maxPeriod = meta.MaxPeriod
	}

	if period < minPeriod {
		return &periodBoundError{CheckType: checkType, Bound: minPeriod}
	}

	if maxPeriod > 0 && period > maxPeriod {
		return &periodBoundError{CheckType: checkType, Bound: maxPeriod, TooLong: true}
	}

	return nil
}

// Flapping (adaptive recovery) validation errors — spec 2026-06-30-07.
var (
	errFlappingWindowNegative  = errors.New("flappingWindowSeconds must be >= 0 (0 = off)")
	errFlapBackoffTooSmall     = errors.New("flapBackoffFactor must be >= 1 (1 = off)")
	errMaxRecoveryMultTooSmall = errors.New("maxRecoveryMultiplier must be >= 1")
)

// validateFlappingFields range-checks the optional flapping knobs on a
// create/update request. nil fields are skipped (left at their stored value /
// default). Out-of-range values surface as VALIDATION_ERROR upstream.
func validateFlappingFields(windowSeconds, backoffFactor, maxRecoveryMult *int) error {
	if windowSeconds != nil && *windowSeconds < 0 {
		return errFlappingWindowNegative
	}
	if backoffFactor != nil && *backoffFactor < 1 {
		return errFlapBackoffTooSmall
	}
	if maxRecoveryMult != nil && *maxRecoveryMult < 1 {
		return errMaxRecoveryMultTooSmall
	}

	return nil
}

// validateSlug validates that a slug has a valid format.
// Valid slugs: start with lowercase letter, followed by 2-99 lowercase letters, digits, or hyphens.
// Total length: 3-100 characters. Must not look like a UUID.
func validateSlug(slug string) error {
	if slug == "" {
		return nil // Empty slug is allowed (will be auto-generated)
	}

	if isUUID(slug) {
		return ErrInvalidSlugFormat
	}

	if !slugRegex.MatchString(slug) {
		return ErrInvalidSlugFormat
	}

	return nil
}

// sanitizeSlug cleans a slug by removing invalid characters, ensuring it starts with a letter,
// and truncating to the max slug length (50 chars). This is used for auto-generated slugs.
func sanitizeSlug(slug string) string {
	const maxSlugLen = 50

	// Convert to lowercase
	slug = strings.ToLower(slug)

	// Replace invalid characters with hyphens
	slug = slugInvalidCharsRegex.ReplaceAllString(slug, "-")

	// Remove consecutive hyphens
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}

	// Trim leading/trailing hyphens
	slug = strings.Trim(slug, "-")

	// Ensure slug starts with a letter (prepend 'x' if it starts with a digit or is empty)
	if len(slug) == 0 || (slug[0] >= '0' && slug[0] <= '9') {
		slug = "x" + slug
	}

	// Truncate to max length and trim any trailing hyphen from truncation
	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}

	return slug
}

// validateAndCheckSlugConflict validates a slug and checks for conflicts in the database.
func (s *Service) validateAndCheckSlugConflict(
	ctx context.Context, orgUID, newSlug string, currentSlug *string,
) error {
	if errValidate := validateSlug(newSlug); errValidate != nil {
		return errValidate
	}

	// Check for slug conflict (only if slug is different from current)
	if currentSlug != nil && *currentSlug == newSlug {
		return nil // Same slug, no conflict
	}

	existingCheck, errCheck := s.db.GetCheckByUidOrSlug(ctx, orgUID, newSlug)
	if errCheck != nil && !errors.Is(errCheck, sql.ErrNoRows) {
		return errCheck
	}
	if existingCheck != nil {
		return ErrSlugConflict
	}

	return nil
}

// Service provides business logic for check management.
type Service struct {
	db            db.Service
	eventNotifier notifier.EventNotifier
	regions       *regions.Service
	// creds is always non-nil — its .Enabled() reports whether a master
	// key is configured. When disabled, write paths fall back to plaintext.
	creds credentials.Service
	// entitlements enforces the per-org MaxChecks cap at creation. May be
	// nil in tests / paths that don't construct it; guards no-op when nil.
	entitlements *entcore.Service
	// checkStats caches the per-org aggregate counters served by
	// GetCheckStats (spec 2026-08-02-06). Zero value is a usable empty cache.
	checkStats checkStatsCache
	// checkStatsTTL overrides defaultCheckStatsTTL. Zero = use the default;
	// only tests set it.
	checkStatsTTL time.Duration
}

// NewService creates a new checks service. entSvc enforces the MaxChecks
// quota at creation; pass nil to disable enforcement (e.g. in unit tests
// that don't exercise quotas).
func NewService(
	dbService db.Service,
	eventNotifier notifier.EventNotifier,
	creds credentials.Service,
	entSvc *entcore.Service,
) *Service {
	return &Service{
		db:            dbService,
		eventNotifier: eventNotifier,
		regions:       regions.NewService(dbService),
		creds:         creds,
		entitlements:  entSvc,
	}
}

// ValidateCheck validates a check configuration without persisting it. orgSlug
// is required when req.DependsOn is non-empty so parent slugs can be resolved
// against the org's checks; for plain config validation it can be empty.
func (s *Service) ValidateCheck(
	ctx context.Context, orgSlug string, req *ValidateCheckRequest,
) (ValidateCheckResponse, error) {
	checkType := checkerdef.CheckType(req.Type)

	checker, ok := registry.GetChecker(checkType)
	if !ok {
		return ValidateCheckResponse{
			Valid: false,
			Fields: []base.ValidationErrorField{
				{Name: fieldType, Message: "unsupported check type"},
			},
		}, nil
	}

	spec := &checkerdef.CheckSpec{
		Config: req.Config,
	}

	if cfgErr := checker.Validate(spec); cfgErr != nil {
		// Mirror legacy behavior — surface the first config error.
		return s.formatValidateError(cfgErr), nil
	}

	// Uniform per-check timeout cap (spec 2026-07-11-05) so live validation
	// agrees with the create/update paths for every check type.
	if timeoutErr := validateConfigTimeout(req.Config); timeoutErr != nil {
		return s.formatValidateError(timeoutErr), nil
	}

	// Uniform per-check address-family rule (spec 2026-08-09-02), same
	// reasoning: the form must see the same verdict the write path will give.
	if versionErr := validateIPVersionConfig(req.Type, req.Config); versionErr != nil {
		return s.formatValidateError(versionErr), nil
	}

	// Tunnel reference rules (existence, type, fingerprint, chaining, and the
	// region rules of spec 2026-07-18-07) so the form's selector can show the
	// error inline before the user hits save. Needs the org to resolve the
	// referenced SSH check; skip silently when the org can't be resolved (the
	// create/update path re-validates anyway).
	if _, tunneled := checkerdef.TunnelCheckUIDFrom(req.Config); tunneled {
		if org := s.lookupOrgForValidate(ctx, orgSlug); org != nil {
			if tunnelErr := s.validateTunnelConfig(
				ctx, org.UID, req.Type, req.Config, req.Regions,
			); tunnelErr != nil {
				return s.formatValidateError(tunnelErr), nil
			}
		}
	}

	if depFields, depErr := s.validateDependsOn(ctx, orgSlug, req.Slug, req.DependsOn); depErr != nil {
		return ValidateCheckResponse{}, depErr
	} else if len(depFields) > 0 {
		return ValidateCheckResponse{Valid: false, Fields: depFields}, nil
	}

	// Advisory only, and evaluated LAST so it can never mask a real error: the
	// config is valid, we just have reason to think one of its regions cannot
	// currently do IPv6 (or run a browser).
	var warnings []base.ValidationErrorField
	if org := s.lookupOrgForValidate(ctx, orgSlug); org != nil {
		warnings = s.regionCapabilityWarnings(ctx, org.UID, req.Type, req.Config, req.Regions)
	}

	return ValidateCheckResponse{Valid: true, Warnings: warnings}, nil
}

// validateDependsOn runs the same edge validators (parent existence, self,
// cross-org, kind, cycle) that the PUT-by-slug path uses, but without
// touching the DB beyond reads. Returns per-field validation errors when
// caller-supplied input is bad; returns a server error only on DB failures.
func (s *Service) validateDependsOn(
	ctx context.Context, orgSlug, childSlug string, deps []ExportedDependency,
) ([]base.ValidationErrorField, error) {
	if len(deps) == 0 {
		return nil, nil
	}

	if orgSlug == "" {
		return []base.ValidationErrorField{
			{Name: dependsOnFieldName, Message: "org context required to validate dependsOn"},
		}, nil
	}

	// Translate a DB miss on org lookup into a user-facing field error
	// rather than surfacing a 500 — caller is asking us to validate the
	// payload, not to commit.
	org := s.lookupOrgForValidate(ctx, orgSlug)
	if org == nil {
		return []base.ValidationErrorField{
			{Name: dependsOnFieldName, Message: "organization not found"},
		}, nil
	}

	childUID, childExists := s.lookupChildForValidate(ctx, org.UID, childSlug)

	resolvedParents, resolvedDescs, fields := s.resolveDeclaredDeps(ctx, org.UID, childUID, childExists, deps)

	if len(fields) > 0 {
		return fields, nil
	}

	return s.cycleCheckProposedDeps(ctx, org.UID, childUID, childExists, resolvedParents, resolvedDescs)
}

// lookupOrgForValidate returns the org by slug, or nil if not found. Folds
// the (org, error) result into a single nilable pointer so the validate
// surface can translate "missing org" into a user-facing field error
// without tripping the nilerr lint.
func (s *Service) lookupOrgForValidate(ctx context.Context, orgSlug string) *models.Organization {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil
	}

	return org
}

// lookupChildForValidate resolves the child check by slug for the validate
// path. Missing is fine — for create flows the slug isn't in the DB yet.
// Returns (uid, true) when found, ("", false) otherwise.
func (s *Service) lookupChildForValidate(
	ctx context.Context, orgUID, childSlug string,
) (string, bool) {
	if childSlug == "" {
		return "", false
	}

	existing, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, childSlug)
	if err != nil || existing == nil {
		return "", false
	}

	return existing.UID, true
}

// resolveDeclaredDeps walks the declared deps and per-row reports validation
// errors (parent existence, self, cross-org, kind, duplicate). Returns the
// resolved parent map, per-parent description map, and any per-row field
// errors collected along the way.
func (s *Service) resolveDeclaredDeps(
	ctx context.Context, orgUID, childUID string, childExists bool, deps []ExportedDependency,
) (
	map[string]models.CheckDependencyKind,
	map[string]*string,
	[]base.ValidationErrorField,
) {
	resolvedParents := make(map[string]models.CheckDependencyKind, len(deps))
	resolvedDescs := make(map[string]*string, len(deps))
	fields := make([]base.ValidationErrorField, 0)

	for i := range deps {
		dep := &deps[i]
		fieldName := fmt.Sprintf("dependsOn[%d]", i)

		if dep.ParentSlug == "" {
			fields = append(fields, base.ValidationErrorField{Name: fieldName, Message: "parentSlug is required"})

			continue
		}

		parent, getErr := s.db.GetCheckByUidOrSlug(ctx, orgUID, dep.ParentSlug)
		if getErr != nil || parent == nil {
			fields = append(fields, base.ValidationErrorField{
				Name:    fieldName,
				Message: "unknown parent slug: " + dep.ParentSlug,
			})

			continue
		}

		if parent.OrganizationUID != orgUID {
			fields = append(fields, base.ValidationErrorField{Name: fieldName, Message: "cross-org parent not allowed"})

			continue
		}

		if childExists && parent.UID == childUID {
			fields = append(fields, base.ValidationErrorField{Name: fieldName, Message: "self-edge not allowed"})

			continue
		}

		kind := models.CheckDependencyKind(dep.Kind)
		if !kind.IsValid() {
			fields = append(fields, base.ValidationErrorField{Name: fieldName, Message: "invalid kind: " + dep.Kind})

			continue
		}

		if _, dup := resolvedParents[parent.UID]; dup {
			fields = append(fields, base.ValidationErrorField{Name: fieldName, Message: "duplicate parent: " + dep.ParentSlug})

			continue
		}

		resolvedParents[parent.UID] = kind

		if dep.Description != "" {
			desc := dep.Description
			resolvedDescs[parent.UID] = &desc
		}
	}

	return resolvedParents, resolvedDescs, fields
}

// cycleCheckProposedDeps runs the cycle DFS against the proposed post-state.
// childExists==false means a create flow where there are no existing edges
// to swap out; we simulate as if the child has only the declared parents.
func (s *Service) cycleCheckProposedDeps(
	ctx context.Context, orgUID, childUID string, childExists bool,
	resolvedParents map[string]models.CheckDependencyKind,
	_ map[string]*string,
) ([]base.ValidationErrorField, error) {
	if !childExists {
		if msg := s.cycleMsg(ctx, orgUID, childUID, nil, resolvedParents); msg != "" {
			return []base.ValidationErrorField{{Name: dependsOnFieldName, Message: msg}}, nil
		}

		return nil, nil
	}

	existing, listErr := s.db.ListCheckDependencyParents(ctx, childUID)
	if listErr != nil {
		return nil, fmt.Errorf("list parents: %w", listErr)
	}

	existingByParent := make(map[string]*models.CheckDependency, len(existing))
	for _, edge := range existing {
		existingByParent[edge.ParentCheckUID] = edge
	}

	if msg := s.cycleMsg(ctx, orgUID, childUID, existingByParent, resolvedParents); msg != "" {
		return []base.ValidationErrorField{{Name: dependsOnFieldName, Message: msg}}, nil
	}

	return nil, nil
}

// cycleMsg returns the cycle-detection error message as a plain string, or
// empty when the proposed graph is acyclic. Folds the error away so the
// validate surface can convert it to a field-level message without nilerr.
func (s *Service) cycleMsg(
	ctx context.Context, orgUID, childUID string,
	existingByParent map[string]*models.CheckDependency,
	resolvedParents map[string]models.CheckDependencyKind,
) string {
	if err := s.assertProposedNoCycle(ctx, orgUID, childUID, existingByParent, resolvedParents); err != nil {
		return err.Error()
	}

	return ""
}

// formatValidateError converts a checker.Validate error into a
// ValidateCheckResponse with one field-level entry. Unknown error shapes
// fall back to a single generic message under the "config" field rather
// than propagating a server-side error.
func (s *Service) formatValidateError(err error) ValidateCheckResponse {
	resp := ValidateCheckResponse{Valid: false}

	if configErr := checkerdef.IsConfigError(err); configErr != nil {
		resp.Fields = []base.ValidationErrorField{
			{Name: configErr.Parameter, Message: configErr.Message},
		}

		return resp
	}

	resp.Fields = []base.ValidationErrorField{{Name: configFieldName, Message: err.Error()}}

	return resp
}

// CheckResponse represents a check in API responses.
type CheckResponse struct {
	UID           string         `json:"uid"`
	Name          *string        `json:"name,omitempty"`
	Slug          *string        `json:"slug,omitempty"`
	Description   *string        `json:"description,omitempty"`
	CheckGroupUID *string        `json:"checkGroupUid,omitempty"`
	Type          *string        `json:"type,omitempty"`
	Config        map[string]any `json:"config,omitempty"`
	// TargetHost is derived at read time from the public Config — see
	// checkerdef.ExtractTargetHost. Not stored; omitted (never sent as
	// explicit null) when the check's config carries no host/url/target
	// field (e.g. heartbeat/email).
	TargetHost *string `json:"targetHost,omitempty"`
	// ConfigPrivateKeys lists the keys whose values are encrypted at rest
	// for this check. The dashboard uses it to render placeholder hints
	// (●●●●●●●●) for fields it can't display. Non-secret by construction
	// — these are key names, not values.
	ConfigPrivateKeys []string `json:"configPrivateKeys,omitempty"`
	// NeedsReseal (detail responses only) flags a private-region check whose
	// sealed credential blob no longer matches the region's active agent set
	// (agent enrolled or revoked since the write), or that could not be sealed
	// at write time. Fix: re-save the check's credentials. Nil when the check
	// targets no private region.
	NeedsReseal *bool `json:"needsReseal,omitempty"`
	// Warnings are advisory notes attached to a successful create/update — the
	// check WAS written and will run. Today the only one is "you pinned IPv6 in
	// a region whose live workers report no IPv6 egress" (spec 2026-08-15-11).
	// Never populated on read paths, and never a reason to reject a write.
	Warnings []base.ValidationErrorField `json:"warnings,omitempty"`
	Regions  []string                    `json:"regions,omitempty"`
	Enabled  *bool                       `json:"enabled,omitempty"`
	Internal *bool                       `json:"internal,omitempty"`
	Period   *string                     `json:"period,omitempty"`
	// RegionSpread is the resolved inter-region scheduling offset override
	// (spec 2026-07-20-05), a duration string (HH:MM:SS). Omitted when the
	// check uses the default period/region_count spread.
	RegionSpread *string           `json:"regionSpread,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	// Status is the synthesized check-level status: "up", "down",
	// "validating" (failure observed but threshold not crossed), "created",
	// or "degraded". Distinct from LastResult.Status, which echoes the raw
	// result row.
	Status           string                    `json:"status,omitempty"`
	LastResult       *LastResultResponse       `json:"lastResult,omitempty"`
	LastStatusChange *LastStatusChangeResponse `json:"lastStatusChange,omitempty"`
	CreatedAt        *time.Time                `json:"createdAt,omitempty"`

	// Adaptive resolution / flapping settings.
	ReopenCooldownMultiplier *int `json:"reopenCooldownMultiplier,omitempty"`
	FlappingWindowSeconds    int  `json:"flappingWindowSeconds"`
	FlapBackoffFactor        int  `json:"flapBackoffFactor"`
	MaxRecoveryMultiplier    int  `json:"maxRecoveryMultiplier"`

	// Wall-clock incident-tracking periods (seconds), per spec 2026-05-08-02.
	// 0 means "open / resolve immediately on first signal".
	ConfirmationPeriodSeconds int `json:"confirmationPeriodSeconds"`
	RecoveryPeriodSeconds     int `json:"recoveryPeriodSeconds"`

	// EscalationPolicyUID points to the escalation policy that fires when
	// an incident on this check opens. Empty/nil = no policy on the check
	// (the group's policy may still apply at run time).
	EscalationPolicyUID *string `json:"escalationPolicyUid,omitempty"`

	// Scheduling is the read-only scheduling telemetry block (spec
	// 2026-07-01-04 D3). Detail responses only (GET by uid/slug) — list
	// responses never carry it — and omitted until the check's first run
	// produces a cost signal.
	Scheduling *CheckSchedulingResponse `json:"scheduling,omitempty"`
}

// CheckSchedulingResponse surfaces the scheduler's per-check telemetry on the
// detail response (spec 2026-07-01-04 D3), derived from the check's
// per-region check_jobs rows (max across regions).
type CheckSchedulingResponse struct {
	// CostEwmaMs is the smoothed execution cost in milliseconds.
	CostEwmaMs float64 `json:"costEwmaMs"`
	// DelayEwmaMs is the smoothed start lateness in milliseconds (pure
	// telemetry; never steers the claim order).
	DelayEwmaMs float64 `json:"delayEwmaMs"`
	// DutyCyclePct is round(100 × cost_ewma_ms / period_ms): the share of a
	// runner slot this check permanently occupies. 100 means the check takes
	// as long to run as its period — a full-time slot hog.
	DutyCyclePct int `json:"dutyCyclePct"`
}

// CheckStatus represents the current status of a check.
type CheckStatus struct {
	LastResult  *string    `json:"lastResult,omitempty"`
	LastChecked *time.Time `json:"lastChecked,omitempty"`
	Uptime24h   *float64   `json:"uptime24h,omitempty"`
}

// LastResultResponse represents the last execution result for a check.
type LastResultResponse struct {
	UID        string         `json:"uid"`
	Status     string         `json:"status"`
	Timestamp  time.Time      `json:"timestamp"`
	DurationMs *float32       `json:"durationMs,omitempty"`
	Output     map[string]any `json:"output,omitempty"`
	Metrics    map[string]any `json:"metrics,omitempty"`
}

// LastStatusChangeResponse represents when and to what status a check last changed.
type LastStatusChangeResponse struct {
	Time   time.Time `json:"time"`
	Status string    `json:"status"`
}

// lastStatusChangeOf builds the `lastStatusChange` embed from the check row
// itself: checks.status_changed_at plus the derived checks.status the UI
// already shows next to it, maintained on the incident path by
// deriveCheckStatus/UpdateCheckStatusAndClocks.
//
// It deliberately does NOT re-derive transitions from `results` any more
// (spec 2026-08-09-07). That query scanned the organization's whole raw
// history, and — because the oldest surviving raw row of a partition always
// looked like a transition — it reported the raw-retention horizon (a
// timestamp that slid forward on every compaction run) for any check that had
// not genuinely changed status inside the retention window.
//
// Contract when checks.status_changed_at is NULL (a check that has never
// recorded a transition): the field is omitted entirely — no fallback to
// created_at, no fallback to the old query. That matches what clients already
// saw for a check with no history.
func lastStatusChangeOf(check *models.Check) *LastStatusChangeResponse {
	if check == nil || check.StatusChangedAt == nil {
		return nil
	}

	return &LastStatusChangeResponse{
		Time: *check.StatusChangedAt,
		// Uppercase to keep the wire value byte-identical in shape to the
		// former result-status serialization ("UP"/"DOWN"/…), which is what
		// the CLI, the Slack/Teams commands and the OpenAPI enum consume.
		Status: strings.ToUpper(check.Status.String()),
	}
}

// ListChecksOptions contains options for listing checks.
type ListChecksOptions struct {
	IncludeLastResult       bool
	IncludeLastStatusChange bool
	Labels                  map[string]string
	CheckGroupUID           *string
	Query                   string
	// Types optionally restricts the listing to given check types (`?type=ssh`).
	// The dashboard's tunnel selector needs "the org's ssh checks" and there is
	// no other primitive for it; comma-separated multi-value, per the API's
	// singular-name convention.
	Types    []string
	Internal *string
	Statuses []models.CheckStatus
	Cursor   string
	Limit    int
	// Sort opts into an alternate ordering. "group" = group sort_order asc,
	// ungrouped last, then created_at DESC / uid DESC within a bucket.
	// "targetHost" = targetHost ascending, none-of-host/url/target last, then
	// name ascending. Empty = the default created_at DESC ordering. The
	// handler validates the value.
	Sort string
}

// PaginationResponse contains pagination metadata.
type PaginationResponse struct {
	Total  int64  `json:"total"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

// ListChecksResponse is the response for listing checks.
type ListChecksResponse struct {
	Data       []CheckResponse    `json:"data"`
	Pagination PaginationResponse `json:"pagination"`
}

// GetCheckOptions contains optional parameters for getting a single check.
type GetCheckOptions struct {
	IncludeLastResult       bool
	IncludeLastStatusChange bool
}

// ListChecks retrieves checks for an organization with pagination and filtering.
//
//nolint:cyclop,gocognit,funlen,gocritic // Complex due to optional field handling and label filtering
func (s *Service) ListChecks(ctx context.Context, orgSlug string, opts ListChecksOptions) (*ListChecksResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	sortByGroup := opts.Sort == "group"
	sortByTargetHost := opts.Sort == "targetHost"

	// Build filter
	filter := &models.ListChecksFilter{
		Labels:           opts.Labels,
		CheckGroupUID:    opts.CheckGroupUID,
		Query:            opts.Query,
		Types:            opts.Types,
		Internal:         opts.Internal,
		Statuses:         opts.Statuses,
		Limit:            opts.Limit,
		SortByGroup:      sortByGroup,
		SortByTargetHost: sortByTargetHost,
	}

	// Parse cursor. sort=group and sort=targetHost each carry their own
	// composite cursor; the default ordering keeps the two-part cursor.
	if opts.Cursor != "" {
		if cursorErr := s.applyListCursor(filter, opts.Cursor, sortByGroup, sortByTargetHost); cursorErr != nil {
			return nil, cursorErr
		}
	}

	// Get checks for the organization
	checks, total, err := s.db.ListChecks(sloghook.WithCallsite(ctx, "checks.list"), org.UID, filter)
	if err != nil {
		return nil, err
	}

	// Detect "has more" and trim
	hasMore := filter.Limit > 0 && len(checks) > filter.Limit
	if hasMore {
		checks = checks[:filter.Limit]
	}

	// Convert to response format
	responses := make([]CheckResponse, len(checks))
	for i, check := range checks {
		responses[i] = s.convertCheckToResponse(check)
	}

	// Fetch labels for all checks
	if len(checks) > 0 {
		checkUIDs := make([]string, len(checks))
		for i, check := range checks {
			checkUIDs[i] = check.UID
		}

		labelsMap, err := s.db.GetLabelsForChecks(ctx, checkUIDs)
		if err != nil {
			return nil, err
		}

		// Attach labels to responses
		for i := range responses {
			if labels, ok := labelsMap[checks[i].UID]; ok && len(labels) > 0 {
				labelMap := make(map[string]string)
				for _, label := range labels {
					labelMap[label.Key] = label.Value
				}
				responses[i].Labels = labelMap
			}
		}
	}

	// If last result is requested, fetch them in a single query
	if opts.IncludeLastResult && len(checks) > 0 {
		checkUIDs := make([]string, len(checks))
		for i, check := range checks {
			checkUIDs[i] = check.UID
		}

		lastResults, err := s.db.GetLastResultForChecks(ctx, org.UID, checkUIDs)
		if err != nil {
			return nil, err
		}

		// Attach last results to responses — slim shape (no output/metrics):
		// list consumers only read status/durationMs (spec 2026-07-06-01 Part B).
		for i := range responses {
			if result, ok := lastResults[checks[i].UID]; ok {
				responses[i].LastResult = s.convertResultToLastResultResponseSlim(result)
			}
		}
	}

	// Last status change comes straight off the check rows already in hand —
	// no extra query at all (spec 2026-08-09-07). IncludeLastStatusChange is
	// now purely response-shaping.
	if opts.IncludeLastStatusChange {
		for i := range responses {
			responses[i].LastStatusChange = lastStatusChangeOf(checks[i])
		}
	}

	// Build next cursor. sort=group emits the composite cursor carrying the
	// last row's effective group sort key (scanned into GroupSortKey by the
	// store) so the next page resumes at the right group boundary. sort=
	// targetHost similarly carries the scanned TargetHostSortKey plus the
	// tiebreaker name.
	var nextCursor string
	if hasMore && len(checks) > 0 {
		lastCheck := checks[len(checks)-1]
		switch {
		case sortByGroup:
			nextCursor = s.encodeGroupCursor(lastCheck.GroupSortKey, lastCheck.CreatedAt, lastCheck.UID)
		case sortByTargetHost:
			name := ""
			if lastCheck.Name != nil {
				name = *lastCheck.Name
			}
			nextCursor = s.encodeTargetHostCursor(lastCheck.TargetHostSortKey, name, lastCheck.UID)
		default:
			nextCursor = s.encodeCursor(lastCheck.CreatedAt, lastCheck.UID)
		}
	}

	limit := opts.Limit
	if limit == 0 {
		limit = len(responses)
	}

	return &ListChecksResponse{
		Data: responses,
		Pagination: PaginationResponse{
			Total:  total,
			Cursor: nextCursor,
			Limit:  limit,
		},
	}, nil
}

// applyListCursor decodes the pagination cursor and populates the filter's
// cursor fields. sort=group expects the composite (group key, created_at, uid)
// cursor; sort=targetHost expects the composite (target host key, name, uid)
// cursor; the default ordering expects the two-part (created_at, uid) cursor.
func (s *Service) applyListCursor(
	filter *models.ListChecksFilter, cursor string, sortByGroup, sortByTargetHost bool,
) error {
	if sortByGroup {
		groupKey, ts, uid, err := s.decodeGroupCursor(cursor)
		if err != nil {
			return ErrInvalidCursor
		}
		filter.CursorGroupSortKey = &groupKey
		filter.CursorCreatedAt = &ts
		filter.CursorUID = &uid

		return nil
	}

	if sortByTargetHost {
		hostKey, name, uid, err := s.decodeTargetHostCursor(cursor)
		if err != nil {
			return ErrInvalidCursor
		}
		filter.CursorTargetHostKey = &hostKey
		filter.CursorTargetHostName = &name
		filter.CursorUID = &uid

		return nil
	}

	ts, uid, err := s.decodeCursor(cursor)
	if err != nil {
		return ErrInvalidCursor
	}
	filter.CursorCreatedAt = &ts
	filter.CursorUID = &uid

	return nil
}

func (s *Service) encodeCursor(createdAt time.Time, uid string) string {
	cursorStr := fmt.Sprintf("%s|%s", createdAt.Format(time.RFC3339Nano), uid)
	return base64.URLEncoding.EncodeToString([]byte(cursorStr))
}

func (s *Service) decodeCursor(cursor string) (time.Time, string, error) {
	decoded, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}

	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return time.Time{}, "", ErrInvalidCursor
	}

	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}

	return ts, parts[1], nil
}

// encodeGroupCursor encodes the composite sort=group cursor: the effective
// group sort key, then the created_at/uid pair, pipe-delimited. Distinct from
// the two-part default cursor so decodeGroupCursor can reject a default cursor
// fed to a group listing (and vice versa).
func (s *Service) encodeGroupCursor(groupSortKey int64, createdAt time.Time, uid string) string {
	cursorStr := fmt.Sprintf("%d|%s|%s", groupSortKey, createdAt.Format(time.RFC3339Nano), uid)
	return base64.URLEncoding.EncodeToString([]byte(cursorStr))
}

func (s *Service) decodeGroupCursor(cursor string) (int64, time.Time, string, error) {
	decoded, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, time.Time{}, "", err
	}

	parts := strings.SplitN(string(decoded), "|", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return 0, time.Time{}, "", ErrInvalidCursor
	}

	groupSortKey, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, time.Time{}, "", ErrInvalidCursor
	}

	ts, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return 0, time.Time{}, "", err
	}

	return groupSortKey, ts, parts[2], nil
}

// targetHostCursorPayload is the composite sort=targetHost cursor: the
// scanned TargetHostSortKey, then name, then uid as the final tiebreaker.
// JSON-encoded (rather than pipe-delimited like the other cursors) because
// both the sort key (derived from free-form config) and name are
// user-controlled text that could itself contain "|".
type targetHostCursorPayload struct {
	Host string `json:"h"`
	Name string `json:"n"`
	UID  string `json:"u"`
}

// encodeTargetHostCursor encodes the composite sort=targetHost cursor.
// targetHostCursorPayload is a plain string struct, so Marshal cannot fail in
// practice; the error is still checked (never ignored) to satisfy errchkjson
// and because an encode failure should yield an empty (invalid, rejected on
// decode) cursor rather than a corrupt one.
func (s *Service) encodeTargetHostCursor(hostKey, name, uid string) string {
	payload, err := json.Marshal(targetHostCursorPayload{Host: hostKey, Name: name, UID: uid})
	if err != nil {
		return ""
	}

	return base64.URLEncoding.EncodeToString(payload)
}

func (s *Service) decodeTargetHostCursor(cursor string) (string, string, string, error) {
	decoded, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", "", err
	}

	var payload targetHostCursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return "", "", "", ErrInvalidCursor
	}

	if payload.UID == "" {
		return "", "", "", ErrInvalidCursor
	}

	return payload.Host, payload.Name, payload.UID, nil
}

// CreateCheckRequest represents a request to create a new check.
type CreateCheckRequest struct {
	Name          string            `json:"name"`
	Slug          string            `json:"slug"`
	Description   string            `json:"description"`
	CheckGroupUID *string           `json:"checkGroupUid"`
	Type          string            `json:"type"`
	Config        map[string]any    `json:"config"`
	Regions       []string          `json:"regions"`
	Enabled       *bool             `json:"enabled"`
	Internal      *bool             `json:"internal,omitempty"`
	Period        *string           `json:"period"`
	Labels        map[string]string `json:"labels"`

	// RegionSpread is the optional inter-region scheduling offset (spec
	// 2026-07-20-05), a duration string like the period. Absent/empty = the
	// default of period/region_count; "0s" fires all regions together. Must
	// satisfy 0 <= regionSpread < period.
	RegionSpread *string `json:"regionSpread,omitempty"`

	// Wall-clock incident-tracking periods (seconds), per spec
	// 2026-05-08-02. 0 means "open / resolve immediately on first signal".
	ConfirmationPeriodSeconds *int `json:"confirmationPeriodSeconds,omitempty"`
	RecoveryPeriodSeconds     *int `json:"recoveryPeriodSeconds,omitempty"`

	// Adaptive resolution / flapping settings.
	ReopenCooldownMultiplier *int `json:"reopenCooldownMultiplier,omitempty"`
	FlappingWindowSeconds    *int `json:"flappingWindowSeconds,omitempty"`
	FlapBackoffFactor        *int `json:"flapBackoffFactor,omitempty"`
	MaxRecoveryMultiplier    *int `json:"maxRecoveryMultiplier,omitempty"`

	// EscalationPolicyUID points to the escalation policy that fires when
	// an incident on this check opens.
	EscalationPolicyUID *string `json:"escalationPolicyUid,omitempty"`
}

// CreateCheck creates a new check for an organization.
//
//nolint:cyclop,funlen,gocritic,gocognit // Complex due to validation and field handling
func (s *Service) CreateCheck(ctx context.Context, orgSlug string, req CreateCheckRequest) (CheckResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, ErrOrganizationNotFound
	}

	// Enforce the MaxChecks quota before doing any work. Internal /
	// system-created checks (discovery hosts, heartbeats) are exempt.
	isInternal := req.Internal != nil && *req.Internal
	if !isInternal && s.entitlements != nil {
		if quotaErr := s.entitlements.CheckCreateAllowed(ctx, org.UID); quotaErr != nil {
			return CheckResponse{}, quotaErr
		}
	}

	// Get the checker to validate the configuration
	checker, ok := registry.GetChecker(checkerdef.CheckType(req.Type))
	if !ok {
		return CheckResponse{}, ErrInvalidCheckType
	}

	// Parse the period if provided
	var period time.Duration
	if req.Period != nil && *req.Period != "" {
		var duration timeutils.Duration
		if scanErr := duration.Scan(*req.Period); scanErr != nil {
			return CheckResponse{}, scanErr
		}
		period = time.Duration(duration)
	}

	// Enforce per-type period bounds (spec 2026-07-01-04 D1). Internal
	// checks and the synthetic sleep type are exempt; an absent period
	// falls back to the default and needs no validation.
	if periodErr := validatePeriodForType(req.Type, period, isInternal); periodErr != nil {
		return CheckResponse{}, periodErr
	}

	// Track if slug was user-provided
	userProvidedSlug := req.Slug != ""

	// Validate slug format if provided by user
	if userProvidedSlug {
		if slugErr := validateSlug(req.Slug); slugErr != nil {
			return CheckResponse{}, slugErr
		}
	}

	// Uniform per-check timeout cap (spec 2026-07-11-05): applies to every
	// check type regardless of the checker's own validation.
	if req.Config != nil {
		if timeoutErr := validateConfigTimeout(req.Config); timeoutErr != nil {
			return CheckResponse{}, timeoutErr
		}

		// Uniform per-check address-family rule (spec 2026-08-09-02): rejects an
		// unknown value, the option on a type that cannot honor it, and the
		// tunnel × ipVersion combination.
		if versionErr := validateIPVersionConfig(req.Type, req.Config); versionErr != nil {
			return CheckResponse{}, versionErr
		}

		// Send-mode SMTP reference validation (spec 2026-08-19-04): the
		// delivery_check_uid reference, org/type, and the instance's
		// email_inbox requirement.
		if smtpErr := s.validateSMTPDeliveryConfig(ctx, org.UID, req.Type, req.Config); smtpErr != nil {
			return CheckResponse{}, smtpErr
		}
	}

	// Send-mode SMTP checks need a period floor so the paired inbox can't be
	// flooded (spec 2026-08-19-04).
	if intervalErr := validateSMTPSendInterval(req.Type, req.Config, period); intervalErr != nil {
		return CheckResponse{}, intervalErr
	}

	// Create CheckSpec for validation
	spec := &checkerdef.CheckSpec{
		Name:   req.Name,
		Slug:   req.Slug,
		Period: period,
		Config: req.Config,
	}

	// Validate the spec - this may modify Name and Slug
	if err := checker.Validate(spec); err != nil { //nolint:govet // Intentional shadowing for scoped error
		return CheckResponse{}, err
	}

	// Handle slug conflicts
	finalSlug, err := s.ensureUniqueSlug(ctx, org.UID, spec.Slug, userProvidedSlug)
	if err != nil {
		return CheckResponse{}, err
	}

	// Create check with unique slug
	check := models.NewCheck(org.UID, finalSlug, req.Type)

	// Set check group
	if req.CheckGroupUID != nil && *req.CheckGroupUID != "" {
		check.CheckGroupUID = req.CheckGroupUID
	}

	// Set name from validated spec
	if spec.Name != "" {
		check.Name = &spec.Name
	}

	// Set description
	if req.Description != "" {
		check.Description = &req.Description
	}

	// Resolve regions BEFORE the config split: credential sealing (spec
	// 2026-07-16-02) keys off the check's private regions, so the resolved
	// region set must be on the model when applyEncryption runs.
	resolvedRegions, err := s.regions.ResolveRegionsForCheck(ctx, req.Regions, org.UID)
	if err != nil {
		return CheckResponse{}, fmt.Errorf("failed to resolve regions: %w", err)
	}
	check.Regions = resolvedRegions

	// Set config — normalize it into its canonical stored shape (e.g. HTTP's
	// username/password → basicAuth fold), then split secrets out and encrypt
	// them under the org DEK (and/or seal them to the private region's agents)
	// before persisting. Plaintext fallback when no master key.
	if req.Config != nil {
		effective, normErr := normalizeCheckConfig(req.Type, req.Config)
		if normErr != nil {
			return CheckResponse{}, normErr
		}

		// A `tunnelCheckUid` reference is validated against the referenced SSH
		// check (existence, type, fingerprint, no chaining) — the checker's own
		// Validate cannot do this: it never touches the database.
		if tunnelErr := s.validateTunnelConfig(ctx, org.UID, req.Type, effective, check.Regions); tunnelErr != nil {
			return CheckResponse{}, tunnelErr
		}

		if encErr := s.applyEncryption(ctx, check, effective); encErr != nil {
			return CheckResponse{}, encErr
		}
	}

	// Set enabled (default is true from NewCheck)
	if req.Enabled != nil {
		check.Enabled = *req.Enabled
	}

	// Set internal flag
	if req.Internal != nil {
		check.Internal = *req.Internal
	}

	// Set period (default is 1 minute from NewCheck)
	if req.Period != nil && *req.Period != "" {
		var duration timeutils.Duration
		if err := duration.Scan(*req.Period); err != nil { //nolint:govet
			return CheckResponse{}, err
		}
		check.Period = duration
	}

	// Set the optional inter-region spread override (spec 2026-07-20-05),
	// validated against the effective period. Empty string = default.
	if req.RegionSpread != nil && *req.RegionSpread != "" {
		var spread timeutils.Duration
		if err := spread.Scan(*req.RegionSpread); err != nil { //nolint:govet
			return CheckResponse{}, err
		}
		if vErr := validateRegionSpread(time.Duration(spread), time.Duration(check.Period)); vErr != nil {
			return CheckResponse{}, vErr
		}
		check.RegionSpread = &spread
	}

	// Set adaptive resolution / flapping settings.
	check.ReopenCooldownMultiplier = req.ReopenCooldownMultiplier

	if vErr := validateFlappingFields(
		req.FlappingWindowSeconds, req.FlapBackoffFactor, req.MaxRecoveryMultiplier,
	); vErr != nil {
		return CheckResponse{}, vErr
	}
	if req.FlappingWindowSeconds != nil {
		check.FlappingWindowSeconds = *req.FlappingWindowSeconds
	}
	if req.FlapBackoffFactor != nil {
		check.FlapBackoffFactor = *req.FlapBackoffFactor
	}
	if req.MaxRecoveryMultiplier != nil {
		check.MaxRecoveryMultiplier = *req.MaxRecoveryMultiplier
	}

	if req.ConfirmationPeriodSeconds != nil {
		if vErr := validateIncidentPeriod(*req.ConfirmationPeriodSeconds); vErr != nil {
			return CheckResponse{}, fmt.Errorf("confirmationPeriodSeconds: %w", vErr)
		}
		check.ConfirmationPeriodSeconds = *req.ConfirmationPeriodSeconds
	}
	if req.RecoveryPeriodSeconds != nil {
		if vErr := validateIncidentPeriod(*req.RecoveryPeriodSeconds); vErr != nil {
			return CheckResponse{}, fmt.Errorf("recoveryPeriodSeconds: %w", vErr)
		}
		check.RecoveryPeriodSeconds = *req.RecoveryPeriodSeconds
	}

	if req.EscalationPolicyUID != nil && *req.EscalationPolicyUID != "" {
		check.EscalationPolicyUID = req.EscalationPolicyUID
	}

	// Create check in DB. The slug resolved above is an optimistic guess: a
	// concurrent create in this org can claim it in the gap before the insert,
	// so the insert re-resolves and retries rather than surfacing the raw
	// unique-index violation.
	if errCreate := s.insertCheckResolvingSlugRace(
		ctx, org.UID, spec.Slug, userProvidedSlug, check,
	); errCreate != nil {
		return CheckResponse{}, errCreate
	}

	// Handle labels if provided
	if len(req.Labels) > 0 {
		labelUIDs := make([]string, 0, len(req.Labels))
		for key, value := range req.Labels {
			label, err := s.db.GetOrCreateLabel(ctx, org.UID, key, value) //nolint:govet
			if err != nil {
				return CheckResponse{}, fmt.Errorf("failed to create label: %w", err)
			}
			labelUIDs = append(labelUIDs, label.UID)
		}
		//nolint:govet // Intentional shadowing for scoped error
		if err := s.db.SetCheckLabels(ctx, check.UID, labelUIDs); err != nil {
			return CheckResponse{}, fmt.Errorf("failed to set check labels: %w", err)
		}
	}

	// Auto-attach default connections
	defaultConnections, err := s.db.ListDefaultChannels(ctx, org.UID)
	if err == nil {
		for _, conn := range defaultConnections {
			checkConn := models.NewCheckConnection(check.UID, conn.UID, org.UID)
			_ = s.db.CreateCheckConnection(ctx, checkConn)
		}
	}

	// Emit check.created event
	if err := s.emitEvent(ctx, org.UID, models.EventTypeCheckCreated, check); err != nil {
		slog.WarnContext(ctx, "failed to emit check.created event", "error", err)
		// Don't fail check creation for event emission errors
	}

	// Activation funnel: idempotent — only fires for the org's first check.
	activation.Emit(ctx, s.db, org.UID,
		models.EventTypeOrgActivationFirstCheckCreated,
		activation.SourceAPI, "")

	// Fetch the check with labels for response
	response := s.convertCheckToResponse(check)
	if len(req.Labels) > 0 {
		response.Labels = req.Labels
	}

	// The check is already written at this point — a region-capability mismatch
	// (IPv6, headless Chrome) is reported, never enforced.
	response.Warnings = s.regionCapabilityWarnings(ctx, org.UID, check.Type, check.Config, check.Regions)

	return response, nil
}

// GetCheck retrieves a single check by UID or slug.
func (s *Service) GetCheck(
	ctx context.Context, orgSlug, identifier string, opts GetCheckOptions,
) (CheckResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, ErrOrganizationNotFound
	}

	// Resolve check by UID or slug (auto-detected)
	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return CheckResponse{}, ErrCheckNotFound
	}

	// Convert to response
	response := s.convertCheckToResponse(check)

	// Attach the read-only scheduling telemetry (spec 2026-07-01-04 D3).
	// Detail-only by design: a single indexed lookup by check_uid here, no
	// join fan-out on list responses.
	if schedErr := s.attachSchedulingInfo(ctx, check, &response); schedErr != nil {
		return CheckResponse{}, schedErr
	}

	// Needs-re-seal flag for private-region checks (spec 2026-07-16-02).
	// Detail-only: it costs an agent lookup per private region.
	response.NeedsReseal = s.computeNeedsReseal(ctx, check)

	// Fetch and attach labels
	labels, err := s.db.GetLabelsForCheck(ctx, check.UID)
	if err != nil {
		return CheckResponse{}, fmt.Errorf("failed to get labels: %w", err)
	}
	if len(labels) > 0 {
		labelMap := make(map[string]string)
		for _, label := range labels {
			labelMap[label.Key] = label.Value
		}
		response.Labels = labelMap
	}

	// Fetch last result if requested
	if opts.IncludeLastResult {
		lastResults, err := s.db.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
		if err != nil {
			return CheckResponse{}, fmt.Errorf("failed to get last result: %w", err)
		}
		if result, ok := lastResults[check.UID]; ok {
			response.LastResult = s.convertResultToLastResultResponse(result)
		}
	}

	// Last status change is carried by the check row itself — no query
	// (spec 2026-08-09-07), same contract as the list path.
	if opts.IncludeLastStatusChange {
		response.LastStatusChange = lastStatusChangeOf(check)
	}

	return response, nil
}

// UpdateCheckRequest represents a request to update a check.
type UpdateCheckRequest struct {
	Name          *string            `json:"name,omitempty"`
	Slug          *string            `json:"slug,omitempty"`
	Description   *string            `json:"description,omitempty"`
	CheckGroupUID *string            `json:"checkGroupUid"`
	Config        *map[string]any    `json:"config,omitempty"`
	Regions       *[]string          `json:"regions,omitempty"`
	Enabled       *bool              `json:"enabled,omitempty"`
	Internal      *bool              `json:"internal,omitempty"`
	Period        *string            `json:"period,omitempty"`
	Labels        *map[string]string `json:"labels,omitempty"`

	// RegionSpread is the optional inter-region scheduling offset override
	// (spec 2026-07-20-05). A duration string sets it; an explicit empty
	// string "" clears it back to the period/region_count default; absent
	// (nil) leaves it unchanged.
	RegionSpread *string `json:"regionSpread,omitempty"`

	// EscalationPolicyUID points to the escalation policy that fires when
	// an incident on this check opens. nil = no policy on the check
	// itself (the group's policy may still apply); empty string = clear.
	EscalationPolicyUID *string `json:"escalationPolicyUid,omitempty"`

	// Wall-clock incident-tracking periods (seconds), per spec
	// 2026-05-08-02. 0 means "open / resolve immediately on first signal".
	ConfirmationPeriodSeconds *int `json:"confirmationPeriodSeconds,omitempty"`
	RecoveryPeriodSeconds     *int `json:"recoveryPeriodSeconds,omitempty"`

	// Adaptive resolution / flapping settings.
	ReopenCooldownMultiplier *int `json:"reopenCooldownMultiplier,omitempty"`
	FlappingWindowSeconds    *int `json:"flappingWindowSeconds,omitempty"`
	FlapBackoffFactor        *int `json:"flapBackoffFactor,omitempty"`
	MaxRecoveryMultiplier    *int `json:"maxRecoveryMultiplier,omitempty"`
}

// UpsertCheckRequest represents a request to create or update a check by slug.
//
// DependsOn is a pointer-typed slice so the handler can distinguish three
// states: absent (nil — leave existing edges alone), explicit empty
// (`*req.DependsOn == nil` after decode is impossible; an explicit `[]` decodes
// to a non-nil zero-length slice — wipe all edges for this check), and
// non-empty (set the edges to exactly this list).
type UpsertCheckRequest struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	CheckGroupUID *string           `json:"checkGroupUid"`
	Type          string            `json:"type"`
	Config        map[string]any    `json:"config"`
	Regions       []string          `json:"regions,omitempty"`
	Enabled       *bool             `json:"enabled"`
	Internal      *bool             `json:"internal,omitempty"`
	Period        *string           `json:"period"`
	Labels        map[string]string `json:"labels"`

	// Wall-clock incident-tracking periods (seconds), per spec
	// 2026-05-08-02. 0 means "open / resolve immediately on first signal".
	ConfirmationPeriodSeconds *int `json:"confirmationPeriodSeconds,omitempty"`
	RecoveryPeriodSeconds     *int `json:"recoveryPeriodSeconds,omitempty"`

	// Adaptive resolution / flapping settings. nil leaves the value untouched
	// (create → system default; update → unchanged).
	ReopenCooldownMultiplier *int `json:"reopenCooldownMultiplier,omitempty"`
	FlappingWindowSeconds    *int `json:"flappingWindowSeconds,omitempty"`
	FlapBackoffFactor        *int `json:"flapBackoffFactor,omitempty"`
	MaxRecoveryMultiplier    *int `json:"maxRecoveryMultiplier,omitempty"`

	DependsOn *[]ExportedDependency `json:"dependsOn,omitempty"`
}

// UpdateCheck updates an existing check by UID or slug.
//
//nolint:cyclop,funlen,gocognit // Complex due to multiple validation and field update paths
func (s *Service) UpdateCheck(
	ctx context.Context, orgSlug, identifier string, req *UpdateCheckRequest,
) (CheckResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, ErrOrganizationNotFound
	}

	// Resolve check by UID or slug (auto-detected)
	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return CheckResponse{}, ErrCheckNotFound
	}

	// Validate slug if provided
	if req.Slug != nil && *req.Slug != "" {
		if errSlug := s.validateAndCheckSlugConflict(ctx, org.UID, *req.Slug, check.Slug); errSlug != nil {
			return CheckResponse{}, errSlug
		}
	}

	// Build update object
	update := models.CheckUpdate{}
	if req.CheckGroupUID != nil {
		update.CheckGroupUID = req.CheckGroupUID
	}
	if req.Name != nil {
		update.Name = req.Name
	}
	if req.Slug != nil {
		update.Slug = req.Slug
	}
	if req.Description != nil {
		update.Description = req.Description
	}
	if req.EscalationPolicyUID != nil {
		if *req.EscalationPolicyUID == "" {
			update.ClearEscalationPolicyUID = true
		} else {
			update.EscalationPolicyUID = req.EscalationPolicyUID
		}
	}
	// Resolve a region patch BEFORE the config handling so credential sealing
	// sees the regions the check will have after this PATCH.
	if req.Regions != nil {
		resolvedRegions, regErr := s.regions.ResolveRegionsForCheck(ctx, *req.Regions, org.UID)
		if regErr != nil {
			return CheckResponse{}, fmt.Errorf("failed to resolve regions: %w", regErr)
		}
		check.Regions = resolvedRegions
		update.Regions = &resolvedRegions
	}
	if req.Config != nil {
		if cfgErr := s.applyConfigUpdate(ctx, check, *req.Config, &update); cfgErr != nil {
			return CheckResponse{}, cfgErr
		}
	} else if req.Regions != nil {
		// A regions-only PATCH still has to re-validate a tunnel reference: the
		// dependent's private regions must stay covered by the SSH check (spec
		// 2026-07-18-07, decisions 1–2). When req.Config is set, applyConfigUpdate
		// already ran this against the new regions; here we run it against the
		// unchanged stored config (its tunnelCheckUid is on the public side).
		if tunnelErr := s.validateTunnelConfig(
			ctx, check.OrganizationUID, check.Type, check.Config, check.Regions,
		); tunnelErr != nil {
			return CheckResponse{}, tunnelErr
		}
	}
	// Guard the SSH check's own side: narrowing its regions (or making it
	// sealed-only) while other checks tunnel through it must not silently strand
	// them. Runs against the post-update state (regions resolved above; sealing
	// applied by applyConfigUpdate when config changed).
	if req.Regions != nil || req.Config != nil {
		if coverErr := s.assertTunnelRegionsStillCover(ctx, check); coverErr != nil {
			return CheckResponse{}, coverErr
		}
	}
	if req.Enabled != nil {
		update.Enabled = req.Enabled
	}
	if req.Internal != nil {
		update.Internal = req.Internal
	}
	if req.Period != nil {
		var duration timeutils.Duration
		if errScan := duration.Scan(*req.Period); errScan != nil {
			return CheckResponse{}, errScan
		}
		// Enforce per-type period bounds on the new value (spec
		// 2026-07-01-04 D1) — existing rows are grandfathered until the
		// next write to the period. The effective internal flag accounts
		// for the same PATCH toggling it; the type cannot change on PATCH.
		internal := check.Internal
		if req.Internal != nil {
			internal = *req.Internal
		}
		if periodErr := validatePeriodForType(check.Type, time.Duration(duration), internal); periodErr != nil {
			return CheckResponse{}, periodErr
		}
		update.Period = &duration
	}
	// Optional inter-region spread override (spec 2026-07-20-05).
	if req.RegionSpread != nil {
		effectivePeriod := time.Duration(check.Period)
		if update.Period != nil {
			effectivePeriod = time.Duration(*update.Period)
		}
		if applyErr := applyRegionSpreadUpdate(&update, *req.RegionSpread, effectivePeriod); applyErr != nil {
			return CheckResponse{}, applyErr
		}
	}
	if req.ReopenCooldownMultiplier != nil {
		update.ReopenCooldownMultiplier = req.ReopenCooldownMultiplier
	}
	if vErr := validateFlappingFields(
		req.FlappingWindowSeconds, req.FlapBackoffFactor, req.MaxRecoveryMultiplier,
	); vErr != nil {
		return CheckResponse{}, vErr
	}
	if req.FlappingWindowSeconds != nil {
		update.FlappingWindowSeconds = req.FlappingWindowSeconds
	}
	if req.FlapBackoffFactor != nil {
		update.FlapBackoffFactor = req.FlapBackoffFactor
	}
	if req.MaxRecoveryMultiplier != nil {
		update.MaxRecoveryMultiplier = req.MaxRecoveryMultiplier
	}
	if req.ConfirmationPeriodSeconds != nil {
		if vErr := validateIncidentPeriod(*req.ConfirmationPeriodSeconds); vErr != nil {
			return CheckResponse{}, fmt.Errorf("confirmationPeriodSeconds: %w", vErr)
		}
		update.ConfirmationPeriodSeconds = req.ConfirmationPeriodSeconds
	}
	if req.RecoveryPeriodSeconds != nil {
		if vErr := validateIncidentPeriod(*req.RecoveryPeriodSeconds); vErr != nil {
			return CheckResponse{}, fmt.Errorf("recoveryPeriodSeconds: %w", vErr)
		}
		update.RecoveryPeriodSeconds = req.RecoveryPeriodSeconds
	}

	// Send-mode SMTP checks need a period floor so the paired inbox can't be
	// flooded (spec 2026-08-19-04). Checked here, after both config and period
	// are finalized, so a PATCH that flips send_email on AND shortens the
	// period in the same request is caught regardless of field order.
	if req.Config != nil || req.Period != nil {
		effectivePeriod := time.Duration(check.Period)
		if update.Period != nil {
			effectivePeriod = time.Duration(*update.Period)
		}

		if intervalErr := validateSMTPSendInterval(check.Type, check.Config, effectivePeriod); intervalErr != nil {
			return CheckResponse{}, intervalErr
		}
	}

	// Update check in DB. A rename carries the same optimistic-slug gap as
	// creation — validateAndCheckSlugConflict above only proves the slug was
	// free when it looked — but a rename slug is always user-provided, so the
	// loser reports the conflict it would have reported synchronously instead
	// of being silently renamed to something else.
	if errUpdate := s.db.UpdateCheck(ctx, check.UID, &update); errUpdate != nil {
		if update.Slug != nil && db.IsCheckSlugCollision(errUpdate) {
			return CheckResponse{}, ErrSlugConflict
		}

		return CheckResponse{}, errUpdate
	}

	// Reconcile check jobs if regions, period, spread, enabled, or config
	// changed (a regionSpread-only edit re-levels the per-region phases).
	if req.Regions != nil || req.Period != nil || req.RegionSpread != nil ||
		req.Enabled != nil || req.Config != nil {
		updatedCheck, fetchErr := s.db.GetCheck(ctx, org.UID, check.UID)
		if fetchErr != nil {
			return CheckResponse{}, fetchErr
		}

		// A region_spread-only edit changes the phase stagger but nothing on the
		// job row, so force a re-level in that case (period / region changes are
		// already detected from the row itself).
		relevel := req.RegionSpread != nil
		if reconcileErr := s.reconcileCheckJobs(ctx, updatedCheck, relevel); reconcileErr != nil {
			return CheckResponse{}, fmt.Errorf("failed to reconcile check jobs: %w", reconcileErr)
		}
	}

	// Handle labels if provided (nil means no change, empty map means clear all)
	if req.Labels != nil {
		labelUIDs := make([]string, 0, len(*req.Labels))
		for key, value := range *req.Labels {
			label, labelErr := s.db.GetOrCreateLabel(ctx, org.UID, key, value)
			if labelErr != nil {
				return CheckResponse{}, fmt.Errorf("failed to create label: %w", labelErr)
			}
			labelUIDs = append(labelUIDs, label.UID)
		}
		if setLabelsErr := s.db.SetCheckLabels(ctx, check.UID, labelUIDs); setLabelsErr != nil {
			return CheckResponse{}, fmt.Errorf("failed to set check labels: %w", setLabelsErr)
		}
	}

	// Fetch updated check
	updatedCheck, err := s.db.GetCheck(ctx, org.UID, check.UID)
	if err != nil {
		return CheckResponse{}, err
	}

	// Convert to response
	response := s.convertCheckToResponse(updatedCheck)

	// Same advisory as on create: the update is already persisted, this only
	// tells the user what the region last reported about IPv6 / headless Chrome.
	response.Warnings = s.regionCapabilityWarnings(
		ctx, org.UID, updatedCheck.Type, updatedCheck.Config, updatedCheck.Regions,
	)

	// Fetch and attach labels
	labels, err := s.db.GetLabelsForCheck(ctx, check.UID)
	if err != nil {
		return CheckResponse{}, fmt.Errorf("failed to get labels: %w", err)
	}
	if len(labels) > 0 {
		labelMap := make(map[string]string)
		for _, label := range labels {
			labelMap[label.Key] = label.Value
		}
		response.Labels = labelMap
	}

	return response, nil
}

// RotateHeartbeatToken mints a fresh heartbeat ping token for a heartbeat
// check and persists it, invalidating every previously issued ping URL
// immediately (no grace period: heartbeat pings are frequent and the operator
// updates the sender right after rotating, unlike webhook signing secrets
// which keep a 24h grace window for slower-moving receivers). The token is
// public-by-design (see checkheartbeat/secret_fields.go) so this is a plain
// config-column write, not a secret-envelope rotation.
func (s *Service) RotateHeartbeatToken(ctx context.Context, orgSlug, identifier string) (CheckResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, ErrOrganizationNotFound
	}

	// Resolve check by UID or slug (auto-detected)
	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return CheckResponse{}, ErrCheckNotFound
	}

	if checkerdef.CheckType(check.Type) != checkerdef.CheckTypeHeartbeat {
		return CheckResponse{}, ErrNotHeartbeatCheck
	}

	newToken, err := checkheartbeat.GenerateToken()
	if err != nil {
		return CheckResponse{}, fmt.Errorf("failed to generate heartbeat token: %w", err)
	}

	newConfig := models.JSONMap{}
	for k, v := range check.Config {
		newConfig[k] = v
	}
	newConfig["token"] = newToken

	if err := s.db.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Config: &newConfig}); err != nil {
		return CheckResponse{}, fmt.Errorf("failed to update check: %w", err)
	}

	return s.GetCheck(ctx, orgSlug, check.UID, GetCheckOptions{})
}

// UpsertCheck creates or updates a check by slug (idempotent operation).
func (s *Service) UpsertCheck(
	ctx context.Context, orgSlug, slug string, req *UpsertCheckRequest,
) (CheckResponse, bool, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, false, ErrOrganizationNotFound
	}

	// Try to get existing check by slug
	existingCheck, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, slug)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CheckResponse{}, false, fmt.Errorf("failed to query check: %w", err)
	}

	if existingCheck != nil {
		// Check exists - update it
		updateReq := UpdateCheckRequest{
			Name:                      &req.Name,
			Description:               &req.Description,
			CheckGroupUID:             req.CheckGroupUID,
			Config:                    &req.Config,
			Enabled:                   req.Enabled,
			Internal:                  req.Internal,
			Period:                    req.Period,
			Labels:                    &req.Labels,
			ConfirmationPeriodSeconds: req.ConfirmationPeriodSeconds,
			RecoveryPeriodSeconds:     req.RecoveryPeriodSeconds,
			ReopenCooldownMultiplier:  req.ReopenCooldownMultiplier,
			FlappingWindowSeconds:     req.FlappingWindowSeconds,
			FlapBackoffFactor:         req.FlapBackoffFactor,
			MaxRecoveryMultiplier:     req.MaxRecoveryMultiplier,
		}
		if len(req.Regions) > 0 {
			updateReq.Regions = &req.Regions
		}

		updatedCheck, updateErr := s.UpdateCheck(ctx, orgSlug, slug, &updateReq)
		if updateErr != nil {
			return updatedCheck, false, updateErr
		}

		if depErr := s.applyUpsertDeps(ctx, org.UID, existingCheck.UID, req.DependsOn); depErr != nil {
			return updatedCheck, false, depErr
		}

		return updatedCheck, false, nil
	}

	// Check doesn't exist - create it
	createReq := CreateCheckRequest{
		Name:                      req.Name,
		Slug:                      slug,
		Description:               req.Description,
		CheckGroupUID:             req.CheckGroupUID,
		Type:                      req.Type,
		Config:                    req.Config,
		Regions:                   req.Regions,
		Enabled:                   req.Enabled,
		Internal:                  req.Internal,
		Period:                    req.Period,
		Labels:                    req.Labels,
		ConfirmationPeriodSeconds: req.ConfirmationPeriodSeconds,
		RecoveryPeriodSeconds:     req.RecoveryPeriodSeconds,
		ReopenCooldownMultiplier:  req.ReopenCooldownMultiplier,
		FlappingWindowSeconds:     req.FlappingWindowSeconds,
		FlapBackoffFactor:         req.FlapBackoffFactor,
		MaxRecoveryMultiplier:     req.MaxRecoveryMultiplier,
	}

	check, err := s.CreateCheck(ctx, orgSlug, createReq)
	if err != nil {
		return check, true, err
	}

	if depErr := s.applyUpsertDeps(ctx, org.UID, check.UID, req.DependsOn); depErr != nil {
		return check, true, depErr
	}

	return check, true, nil
}

// applyUpsertDeps applies the destructive PUT-by-slug dependency sync. nil
// pointer means "deps absent — leave alone." Non-nil (including zero-length)
// means "set deps to exactly this list." Validates cycle/self/cross-org/kind
// before any write — if any entry fails, no edges are mutated.
//
// Atomicity caveat: this runs after the check upsert outside any wrapping
// transaction; a failed dep apply leaves the check itself updated. The spec
// names this as a known limitation pending a transaction wrapper around the
// whole upsert path.
//
//nolint:cyclop,funlen,gocognit // resolve, validate-all, apply diff
func (s *Service) applyUpsertDeps(
	ctx context.Context, orgUID, childUID string, declared *[]ExportedDependency,
) error {
	if declared == nil {
		return nil
	}

	desired := *declared

	resolvedParents := make(map[string]models.CheckDependencyKind, len(desired))
	resolvedDescs := make(map[string]*string, len(desired))

	for i := range desired {
		dep := &desired[i]
		if dep.ParentSlug == "" {
			return errDepEmptyParentSlug
		}

		parent, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, dep.ParentSlug)
		if err != nil || parent == nil {
			return fmt.Errorf("%w: %s", errDepUnknownParent, dep.ParentSlug)
		}

		if parent.OrganizationUID != orgUID {
			return fmt.Errorf("%w: %s", errDepCrossOrg, dep.ParentSlug)
		}

		if parent.UID == childUID {
			return errDepSelfEdge
		}

		kind := models.CheckDependencyKind(dep.Kind)
		if !kind.IsValid() {
			return fmt.Errorf("%w: %s", errDepInvalidKind, dep.Kind)
		}

		if _, dup := resolvedParents[parent.UID]; dup {
			return fmt.Errorf("%w: %s", errDepDuplicateParent, dep.ParentSlug)
		}

		resolvedParents[parent.UID] = kind

		if dep.Description != "" {
			desc := dep.Description
			resolvedDescs[parent.UID] = &desc
		}
	}

	existing, err := s.db.ListCheckDependencyParents(ctx, childUID)
	if err != nil {
		return fmt.Errorf("list existing parents: %w", err)
	}

	existingByParent := make(map[string]*models.CheckDependency, len(existing))
	for _, edge := range existing {
		existingByParent[edge.ParentCheckUID] = edge
	}

	// Cycle check: simulate the proposed graph (current org graph, minus this
	// child's existing edges, plus the desired edges) and run a DFS. We do
	// this before any mutation so failures keep the DB untouched.
	if cycleErr := s.assertProposedNoCycle(ctx, orgUID, childUID, existingByParent, resolvedParents); cycleErr != nil {
		return cycleErr
	}

	for parentUID, edge := range existingByParent {
		if _, keep := resolvedParents[parentUID]; !keep {
			if delErr := s.db.DeleteCheckDependency(ctx, edge.UID); delErr != nil {
				return fmt.Errorf("delete edge: %w", delErr)
			}
		}
	}

	for parentUID, kind := range resolvedParents {
		desc := resolvedDescs[parentUID]
		existing, ok := existingByParent[parentUID]
		if !ok {
			newEdge := models.NewCheckDependency(orgUID, parentUID, childUID, kind, desc)
			if cErr := s.db.CreateCheckDependency(ctx, newEdge); cErr != nil {
				return fmt.Errorf("create edge: %w", cErr)
			}

			continue
		}

		if uErr := s.maybeUpdateEdge(ctx, existing, kind, desc); uErr != nil {
			return uErr
		}
	}

	return nil
}

// maybeUpdateEdge issues an UpdateCheckDependency only if the kind or
// description differs from `existing`. Centralized so applyUpsertDeps doesn't
// nest the diff-vs-noop branch.
func (s *Service) maybeUpdateEdge(
	ctx context.Context, existing *models.CheckDependency,
	kind models.CheckDependencyKind, desc *string,
) error {
	update := models.CheckDependencyUpdate{}
	needs := false

	if existing.Kind != kind {
		update.Kind = &kind
		needs = true
	}

	if !sameDesc(existing.Description, desc) {
		if desc == nil {
			update.ClearDescription = true
		} else {
			update.Description = desc
		}

		needs = true
	}

	if !needs {
		return nil
	}

	if err := s.db.UpdateCheckDependency(ctx, existing.UID, &update); err != nil {
		return fmt.Errorf("update edge: %w", err)
	}

	return nil
}

// assertProposedNoCycle runs the cycle DFS on the post-state of a destructive
// dep sync (existing graph minus child's current edges, plus desired edges).
func (s *Service) assertProposedNoCycle(
	ctx context.Context, orgUID, childUID string,
	existingByParent map[string]*models.CheckDependency,
	resolvedParents map[string]models.CheckDependencyKind,
) error {
	const depthCap = 32

	allDeps, err := s.db.ListCheckDependenciesByOrg(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("list deps for cycle check: %w", err)
	}

	adjacency := make(map[string]map[string]struct{}, len(allDeps))
	addEdge := func(parent, child string) {
		if _, ok := adjacency[parent]; !ok {
			adjacency[parent] = make(map[string]struct{})
		}
		adjacency[parent][child] = struct{}{}
	}
	removeEdge := func(parent, child string) {
		if children, ok := adjacency[parent]; ok {
			delete(children, child)
		}
	}

	for _, dep := range allDeps {
		addEdge(dep.ParentCheckUID, dep.ChildCheckUID)
	}

	for parentUID := range existingByParent {
		removeEdge(parentUID, childUID)
	}

	for parentUID := range resolvedParents {
		addEdge(parentUID, childUID)
	}

	for parentUID := range resolvedParents {
		if existing, ok := existingByParent[parentUID]; ok && existing.Kind == resolvedParents[parentUID] {
			continue
		}

		stack := []string{childUID}
		visited := map[string]struct{}{childUID: {}}

		for depth := 0; depth < depthCap && len(stack) > 0; depth++ {
			next := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			for child := range adjacency[next] {
				if child == parentUID {
					return fmt.Errorf("%w via parent %s", errDepCycleDetected, parentUID)
				}

				if _, seen := visited[child]; seen {
					continue
				}

				visited[child] = struct{}{}
				stack = append(stack, child)
			}
		}
	}

	return nil
}

// DeleteCheck deletes a check by UID or slug (soft delete).
func (s *Service) DeleteCheck(ctx context.Context, orgSlug, identifier string) error {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	// Resolve check by UID or slug (auto-detected)
	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return ErrCheckNotFound
	}

	// Refuse to delete a bastion other checks tunnel through — they would all
	// start failing on their next execution with no explanation.
	if tunnelErr := s.assertNotUsedAsTunnel(ctx, check); tunnelErr != nil {
		return tunnelErr
	}

	// Count active incidents for the event payload
	activeIncidentCount, err := s.db.CountActiveIncidentsByCheckUID(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("failed to check active incidents: %w", err)
	}

	// Resolve any active incidents before deleting
	if activeIncidentCount > 0 {
		if resolveErr := s.resolveActiveIncidentsForDelete(ctx, org.UID, check.UID); resolveErr != nil {
			return resolveErr
		}
	}

	// Delete all check jobs for this check
	existingJobs, err := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("failed to list check jobs: %w", err)
	}
	for _, job := range existingJobs {
		if delErr := s.db.DeleteCheckJob(ctx, job.UID); delErr != nil {
			return fmt.Errorf("failed to delete check job: %w", delErr)
		}
	}

	// Delete dependency edges referencing this check (either side), so they
	// don't outlive the check and later resolve to an empty check ref (#129).
	if err := s.db.DeleteCheckDependenciesForCheck(ctx, check.UID); err != nil {
		return fmt.Errorf("failed to delete check dependencies: %w", err)
	}

	// Delete check
	if err := s.db.DeleteCheck(ctx, check.UID); err != nil {
		return fmt.Errorf("failed to delete check: %w", err)
	}

	// Emit check.deleted event (actor-attributed — see emitEvent).
	event := audit.NewEvent(ctx, org.UID, models.EventTypeCheckDeleted,
		audit.Target{Type: "check", UID: check.UID, Name: checkDisplayName(check)},
		models.JSONMap{
			eventPayloadCheckUIDKey:  check.UID,
			"check_slug":             check.Slug,
			"check_name":             check.Name,
			"check_type":             check.Type,
			"active_incidents_count": activeIncidentCount,
		})
	event.CheckUID = &check.UID

	if err := s.db.CreateEvent(ctx, event); err != nil {
		slog.WarnContext(ctx, "failed to emit check.deleted event", "error", err)
	}

	return nil
}

// resolveActiveIncidentsForDelete resolves every active incident on a check
// about to be deleted, so no incident outlives the check it points at.
func (s *Service) resolveActiveIncidentsForDelete(ctx context.Context, orgUID, checkUID string) error {
	incidents, _, err := s.db.ListIncidents(ctx, &models.ListIncidentsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       []string{checkUID},
		States:          []models.IncidentState{models.IncidentStateActive},
	})
	if err != nil {
		return fmt.Errorf("failed to list active incidents: %w", err)
	}

	now := time.Now()
	resolvedState := models.IncidentStateResolved

	for _, incident := range incidents {
		if updateErr := s.db.UpdateIncident(ctx, incident.UID, &models.IncidentUpdate{
			State:      &resolvedState,
			ResolvedAt: &now,
		}); updateErr != nil {
			return fmt.Errorf("failed to resolve incident %s: %w", incident.UID, updateErr)
		}
	}

	return nil
}

// ensureUniqueSlug ensures the slug is unique within the organization.
// If userProvided is false and slug exists, it appends a number to make it unique.
// If userProvided is true and slug exists, it returns ErrSlugConflict.
func (s *Service) ensureUniqueSlug(ctx context.Context, orgUID, slug string, userProvided bool) (string, error) {
	const maxSlugLength = 50
	const minSlugLength = 3

	// Sanitize auto-generated slugs to remove invalid characters
	if !userProvided {
		slug = sanitizeSlug(slug)
	}

	// Truncate slug if too long (reserve space for suffix like "-99")
	if len(slug) > maxSlugLength && !userProvided {
		// Auto-generated slug is too long, truncate it
		// Reserve 3 characters for potential "-XX" suffix
		slug = slug[:maxSlugLength-3]
	}

	// Check if slug already exists
	existingCheck, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, slug)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// Real error occurred during lookup (not just "not found")
		return "", err
	}

	// Slug exists
	if existingCheck != nil { //nolint:nestif // Slug conflict resolution logic
		if userProvided {
			// User explicitly provided this slug, return conflict error
			return "", ErrSlugConflict
		}

		// Slug was auto-generated, find a unique one by appending numbers
		baseSlug := slug
		for i := 2; i <= 99; i++ { // Try up to 99 (fits in 2 chars)
			suffix := fmt.Sprintf("-%d", i)
			// Ensure base + suffix doesn't exceed max length
			maxBaseLen := maxSlugLength - len(suffix)
			if len(baseSlug) > maxBaseLen {
				baseSlug = baseSlug[:maxBaseLen]
			}

			candidateSlug := baseSlug + suffix

			// Ensure we're above minimum length
			if len(candidateSlug) < minSlugLength {
				continue
			}

			existing, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, candidateSlug)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				// Real error occurred during lookup (not just "not found")
				return "", err
			}
			if existing == nil {
				// Slug is available
				return candidateSlug, nil
			}
		}

		// Couldn't find unique slug after 99 attempts
		return "", ErrSlugGenerationFailed
	}

	return slug, nil
}

// checkSlugStallAttempts bounds how many CONSECUTIVE inserts the slug loop may
// make WITHOUT MAKING PROGRESS before it gives up and surfaces the violation.
//
// It counts stalls, not retries, and the distinction is the whole point. Losing
// the slug race is not a failure: it means a competing creation committed, so
// the re-read that follows returns a DIFFERENT slug and the next insert aims
// somewhere new. N simultaneous creations for one target make the unluckiest
// one lose up to N-1 times, and N is set by the caller — a scripted import, a
// burst of dashboard tabs, parallel E2E workers — not by this loop. Charging
// those legitimate losses to a small constant is what made a 24-way burst fail
// outright while a 2-way one looked perfectly healthy; the same lesson is
// written up on IncidentNumberAttempts in internal/db/incidentnumber.go.
//
// A stall is the genuinely pathological case: the re-read handed back the very
// slug that just failed, so retrying it can only fail identically. Termination
// does not rest on this constant alone — ensureUniqueSlug itself gives up after
// the "-99" suffix — but a bounded stall keeps a spinning loop from holding a
// request open while it re-reads the same answer forever.
const checkSlugStallAttempts = 8

// insertCheckResolvingSlugRace inserts a check, re-resolving an auto-generated
// slug and retrying whenever a concurrent creation committed the same one first.
//
// ensureUniqueSlug is a SELECT-then-INSERT with a gap in the middle: between the
// two, another request in the same organization can take the slug it just found
// free. checks_slug_idx — not the SELECT — is what actually guarantees
// uniqueness, so the read is only an optimistic guess and losing the race costs
// one retry rather than a duplicate.
//
// This is not a rare interleaving. An auto-generated slug derives from the
// check's target, not from its name, so two checks pointed at the same URL get
// the same base slug by construction — two dashboard tabs, a scripted import,
// or two parallel E2E workers creating checks for one host collide on the first
// try. Before this loop the loser surfaced as a bare 23505 from the driver,
// which the handler could only render as a 500 "Internal server error".
//
// A user-provided slug is never re-resolved: silently storing something other
// than what was asked for would be a surprise. Its collision becomes
// ErrSlugConflict — the same 400 the pre-check returns when it sees the row —
// so the outcome no longer depends on which side of the gap the conflict landed.
//
// The whole insert (check + check_jobs + initial result) runs in one
// transaction, so a losing attempt leaves nothing behind to clean up before the
// next one.
func (s *Service) insertCheckResolvingSlugRace(
	ctx context.Context, orgUID, baseSlug string, userProvidedSlug bool, check *models.Check,
) error {
	return insertResolvingSlugRace(ctx, check, userProvidedSlug,
		func(ctx context.Context) error {
			return s.db.CreateCheck(ctx, check)
		},
		func(ctx context.Context) (string, error) {
			return s.ensureUniqueSlug(ctx, orgUID, baseSlug, false)
		},
	)
}

// insertResolvingSlugRace is the loop itself, with the database reached only
// through the two closures its caller supplies: insert writes the check as it
// currently stands, resolve re-reads a free slug for it.
//
// Split out this way for the same reason db.CreateIncidentWithNumber is — the
// interesting behavior is entirely in the interleaving, and whether two
// goroutines actually collide is up to the scheduler. With the I/O injected,
// every branch (retry, stall, give up, refuse to retry) is pinned by a
// deterministic test instead of by a lucky run.
func insertResolvingSlugRace(
	ctx context.Context,
	check *models.Check,
	userProvidedSlug bool,
	insert func(context.Context) error,
	resolve func(context.Context) (string, error),
) error {
	stalls := 0

	for {
		err := insert(ctx)
		if err == nil || !db.IsCheckSlugCollision(err) {
			return err
		}

		if userProvidedSlug {
			return ErrSlugConflict
		}

		lost := ""
		if check.Slug != nil {
			lost = *check.Slug
		}

		nextSlug, resolveErr := resolve(ctx)
		if resolveErr != nil {
			return resolveErr
		}

		if nextSlug == lost {
			// No progress: the re-read handed back the slug that just failed,
			// so the next insert can only fail the same way. Bounded, then
			// surfaced as the violation it is.
			stalls++
			if stalls >= checkSlugStallAttempts {
				return err
			}
		} else {
			// A competing creation committed and we aimed elsewhere. That is
			// contention to ride out, not a livelock — it must not be charged
			// to the stall budget.
			stalls = 0
		}

		check.Slug = &nextSlug
	}
}

// ReconcileStaleJobSchedules re-reconciles every enabled check that has at
// least one check_job whose period no longer matches the check's period, and
// returns the number of checks reconciled (spec 2026-07-20-05). It exists for
// the one-shot startup pass: multi-region checks written before the
// per-region-full-period change carry a stale basePeriod×region_count job
// period and would keep it until the check is next edited. reconcileCheckJobs
// recomputes period, scheduled_at, and effective_scheduled_at for those jobs.
// Idempotent — a check whose jobs already match is never returned by the
// query, and a second run finds nothing to fix.
func (s *Service) ReconcileStaleJobSchedules(ctx context.Context) (int, error) {
	stale, err := s.db.ListChecksWithStaleJobPeriods(ctx)
	if err != nil {
		return 0, fmt.Errorf("list checks with stale job periods: %w", err)
	}

	for _, check := range stale {
		// relevel=false: the stale jobs are detected by their mismatched period,
		// which already forces the phase recompute; correct jobs are untouched.
		if reconcileErr := s.reconcileCheckJobs(ctx, check, false); reconcileErr != nil {
			return 0, fmt.Errorf("reconcile check %s: %w", check.UID, reconcileErr)
		}
	}

	return len(stale), nil
}

// reconcileCheckJobs ensures the check_jobs match the check's current regions
// and period. relevel forces every surviving job's phase to be recomputed even
// when nothing on the row changed — needed for a region_spread-only edit, whose
// effect (the phase stagger) is an input to the phase formula but is not stored
// on the job row, so it cannot be detected by comparing job columns.
//
//nolint:cyclop,gocognit,nestif,funlen // Reconciliation handles multiple update paths
func (s *Service) reconcileCheckJobs(ctx context.Context, check *models.Check, relevel bool) error {
	existingJobs, err := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("failed to list check jobs: %w", err)
	}

	// Denormalize the org's plan weight onto every (re)materialized job so the
	// cost-aware scheduler can reserve capacity and credit paid orgs without a
	// per-claim entitlements lookup (spec 2026-06-30-09). Refreshed here on every
	// reconcile, so a plan change propagates to the jobs the next time the check
	// is touched. Free (weight 0) when entitlements are disabled.
	planWeight := entcore.PlanWeightFree
	if s.entitlements != nil {
		planWeight = s.entitlements.PlanWeight(ctx, check.OrganizationUID)
	}

	// If check is disabled, delete all jobs
	if !check.Enabled {
		for _, job := range existingJobs {
			if err := s.db.DeleteCheckJob(ctx, job.UID); err != nil {
				return fmt.Errorf("failed to delete check job: %w", err)
			}
		}

		return nil
	}

	// Sort a copy of the target regions so index i is stable and matches
	// scheduling.RegionIndex's own sort (spec 2026-07-05-08 D2) — both must
	// agree on the same region ordering for the phase formula to level
	// correctly. RegionIndex re-sorting this already-sorted slice internally
	// is a cheap no-op, not a mismatch.
	targetRegions := make([]string, len(check.Regions))
	copy(targetRegions, check.Regions)
	sort.Strings(targetRegions)

	basePeriod := time.Duration(check.Period)
	n := len(targetRegions)

	// Inter-region offset ("spread"), resolved once from the same inputs the
	// worker's release path uses (spec 2026-07-20-05) so the phase is
	// reproducible across processes: default period/n, or the check's
	// region_spread override. Every region's job now runs at the FULL base
	// period (no split) — the spread only staggers their phases.
	spread := scheduling.RegionSpread(basePeriod, n, check.RegionSpreadDuration())

	// No regions: ensure exactly one job without a region. Still jitters the
	// single job (NextAligned with region=nil) so identical checks don't
	// herd on creation (D1's "single/no-region jobs get jitter-only phase").
	if n == 0 {
		// Delete all existing jobs and create one without region
		for _, job := range existingJobs {
			if err := s.db.DeleteCheckJob(ctx, job.UID); err != nil {
				return fmt.Errorf("failed to delete check job: %w", err)
			}
		}

		scheduledAt := scheduling.NextAligned(time.Now(), basePeriod, basePeriod, check.UID, nil, nil, 0)
		job := models.NewCheckJob(check.OrganizationUID, check.UID, check.Period)
		job.Type = check.Type
		job.Config = check.Config
		job.ConfigPrivate = check.ConfigPrivate
		job.ConfigPrivateKeys = check.ConfigPrivateKeys
		job.ConfigSealed = check.ConfigSealed
		job.Encrypted = check.ConfigPrivate != nil
		job.ScheduledAt = &scheduledAt
		job.EffectiveScheduledAt = &scheduledAt
		job.PlanWeight = planWeight

		return s.db.CreateCheckJob(ctx, job)
	}

	// Build map of existing regions
	existingByRegion := make(map[string]*models.CheckJob)
	for _, job := range existingJobs {
		if job.Region != nil {
			existingByRegion[*job.Region] = job
		}
	}

	targetSet := make(map[string]bool, n)
	for _, r := range targetRegions {
		targetSet[r] = true
	}

	// Region-set change (add/remove of a sibling region) shifts every
	// survivor's index even when nothing else about the job changed, so it
	// must also force re-leveling (D2). Compare the existing job region set
	// to the target set; a mismatch in either direction (removed or added)
	// counts as changed.
	regionSetChanged := len(existingByRegion) != n
	if !regionSetChanged {
		for existingRegion := range existingByRegion {
			if !targetSet[existingRegion] {
				regionSetChanged = true

				break
			}
		}
	}

	// Delete jobs for removed regions (and any null-region jobs)
	for _, job := range existingJobs {
		if job.Region == nil || !targetSet[*job.Region] {
			if err := s.db.DeleteCheckJob(ctx, job.UID); err != nil {
				return fmt.Errorf("failed to delete check job: %w", err)
			}
		}
	}

	// Create jobs for new regions, update period for existing. Every region's
	// job runs at the full base period (spec 2026-07-20-05 — no more split
	// period); the spread staggers their phases.
	for _, region := range targetRegions {
		if existing, ok := existingByRegion[region]; ok {
			// Update period, config, type, and plan weight if changed, or if
			// the region set changed (an add/remove elsewhere shifts this
			// job's own index even though nothing on this row directly
			// changed) — D2.
			needsUpdate := relevel ||
				existing.Period != check.Period ||
				existing.Type != check.Type ||
				existing.PlanWeight != planWeight ||
				!configEqual(existing.Config, check.Config) ||
				!optStrEqual(existing.ConfigPrivate, check.ConfigPrivate) ||
				!optStrEqual(existing.ConfigSealed, check.ConfigSealed) ||
				regionSetChanged

			if needsUpdate {
				regionCopy := region
				scheduledAt := scheduling.NextAligned(
					time.Now(), basePeriod, basePeriod, check.UID, &regionCopy, targetRegions, spread,
				)

				if _, err := s.db.DB().NewUpdate().
					Model((*models.CheckJob)(nil)).
					Set("period = ?", check.Period).
					Set("type = ?", check.Type).
					Set("config = ?", check.Config).
					// Keep the job's secret columns in step with the check so an
					// updated credential (v1 envelope or region-sealed blob)
					// reaches the dispatch row without waiting for a region-set
					// change (spec 2026-07-16-02).
					Set("config_private = ?", check.ConfigPrivate).
					Set("config_private_keys = ?", check.ConfigPrivateKeys).
					Set("config_sealed = ?", check.ConfigSealed).
					Set("encrypted = ?", check.ConfigPrivate != nil).
					Set("plan_weight = ?", planWeight).
					Set("scheduled_at = ?", scheduledAt).
					Set("effective_scheduled_at = ?", scheduledAt).
					Set("updated_at = ?", time.Now()).
					Where("uid = ?", existing.UID).
					Exec(ctx); err != nil {
					return fmt.Errorf("failed to update check job: %w", err)
				}
			}
		} else {
			// Create new job, phase-aligned per D1 (replaces the old one-shot
			// time.Now().Add(basePeriod * i) stagger, which was never
			// maintained after creation — F1).
			regionCopy := region
			scheduledAt := scheduling.NextAligned(
				time.Now(), basePeriod, basePeriod, check.UID, &regionCopy, targetRegions, spread,
			)

			job := models.NewCheckJob(check.OrganizationUID, check.UID, check.Period)
			job.Type = check.Type
			job.Config = check.Config
			job.ConfigPrivate = check.ConfigPrivate
			job.ConfigPrivateKeys = check.ConfigPrivateKeys
			job.ConfigSealed = check.ConfigSealed
			job.Encrypted = check.ConfigPrivate != nil
			job.Region = &regionCopy
			job.ScheduledAt = &scheduledAt
			job.EffectiveScheduledAt = &scheduledAt
			job.PlanWeight = planWeight

			if err := s.db.CreateCheckJob(ctx, job); err != nil {
				return fmt.Errorf("failed to create check job: %w", err)
			}
		}
	}

	return nil
}

// optStrEqual compares two optional strings by value.
func optStrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}

// configEqual compares two JSONMap configs for equality.
func configEqual(configA, configB models.JSONMap) bool {
	if len(configA) != len(configB) {
		return false
	}

	for key, valA := range configA {
		valB, ok := configB[key]
		if !ok {
			return false
		}

		if fmt.Sprintf("%v", valA) != fmt.Sprintf("%v", valB) {
			return false
		}
	}

	return true
}

// convertCheckToResponse converts a database Check model to a CheckResponse.
// Returns the public config plus the list of encrypted-key names — secret
// values are never echoed back to the client. The dashboard uses
// configPrivateKeys to render placeholder dots.
func (s *Service) convertCheckToResponse(check *models.Check) CheckResponse {
	// Convert Period to database string format (HH:MM:SS)
	periodValue, _ := check.Period.Value()
	periodStr, _ := periodValue.(string)

	// RegionSpread (optional): same HH:MM:SS string form as the period; left
	// nil in the response when the check uses the default period/region_count.
	var regionSpreadStr *string
	if check.RegionSpread != nil {
		spreadValue, _ := check.RegionSpread.Value()
		if s, ok := spreadValue.(string); ok {
			regionSpreadStr = &s
		}
	}

	var privateKeys []string
	if check.ConfigPrivateKeys != nil {
		_ = json.Unmarshal([]byte(*check.ConfigPrivateKeys), &privateKeys)
	}

	// Defense-in-depth: strip any declared-secret key from the outgoing public
	// config and union it into configPrivateKeys, regardless of storage mode.
	// The storage split (applyEncryption) already keeps secrets out of the
	// public column, but this protects not-yet-migrated rows and any future
	// write path that forgets to split — the invariant "a secret value never
	// leaves the API" holds even if the row is malformed. Mirrors
	// stripSecretKeysForExport.
	publicConfig, privateKeys := redactSecretConfig(check, privateKeys)

	return CheckResponse{
		UID:                       check.UID,
		Name:                      check.Name,
		Slug:                      check.Slug,
		Description:               check.Description,
		CheckGroupUID:             check.CheckGroupUID,
		Type:                      &check.Type,
		Config:                    publicConfig,
		TargetHost:                checkerdef.ExtractTargetHost(publicConfig),
		ConfigPrivateKeys:         privateKeys,
		Regions:                   check.Regions,
		Enabled:                   &check.Enabled,
		Internal:                  &check.Internal,
		Period:                    &periodStr,
		RegionSpread:              regionSpreadStr,
		Status:                    check.Status.String(),
		CreatedAt:                 &check.CreatedAt,
		ReopenCooldownMultiplier:  check.ReopenCooldownMultiplier,
		FlappingWindowSeconds:     check.FlappingWindowSeconds,
		FlapBackoffFactor:         check.FlapBackoffFactor,
		MaxRecoveryMultiplier:     check.MaxRecoveryMultiplier,
		ConfirmationPeriodSeconds: check.ConfirmationPeriodSeconds,
		RecoveryPeriodSeconds:     check.RecoveryPeriodSeconds,
		EscalationPolicyUID:       check.EscalationPolicyUID,
	}
}

// attachSchedulingInfo populates response.Scheduling from the check's
// per-region check_jobs rows (spec 2026-07-01-04 D3): the cost/delay EWMAs
// are the max across regions (the worst region is the one occupying a slot),
// and dutyCyclePct = round(100 × cost_ewma_ms / period_ms). The block stays
// absent until the first run produces a cost signal (cost_ewma_ms == 0).
func (s *Service) attachSchedulingInfo(ctx context.Context, check *models.Check, response *CheckResponse) error {
	jobs, err := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("failed to get check jobs: %w", err)
	}

	var costMs, delayMs float64

	for _, job := range jobs {
		costMs = math.Max(costMs, job.CostEWMAMs)
		delayMs = math.Max(delayMs, job.DelayEWMAMs)
	}

	// No cost signal yet (never ran) → the block is omitted.
	if costMs <= 0 {
		return nil
	}

	dutyCyclePct := 0
	if periodMs := time.Duration(check.Period).Milliseconds(); periodMs > 0 {
		dutyCyclePct = int(math.Round(100 * costMs / float64(periodMs)))
	}

	response.Scheduling = &CheckSchedulingResponse{
		CostEwmaMs:   costMs,
		DelayEwmaMs:  delayMs,
		DutyCyclePct: dutyCyclePct,
	}

	return nil
}

// resultStatusString converts a Result's raw status int to its string form,
// shared by both the full (detail) and slim (list) LastResultResponse
// conversions.
func resultStatusString(result *models.Result) string {
	statusStr := "unknown"
	if result.Status != nil {
		switch *result.Status {
		case int(models.ResultStatusUp):
			statusStr = "up"
		case int(models.ResultStatusWarning):
			statusStr = "warning"
		case int(models.ResultStatusDegraded):
			statusStr = "degraded"
		case int(models.ResultStatusDown):
			statusStr = "down"
		case int(models.ResultStatusTimeout):
			statusStr = "timeout"
		case int(models.ResultStatusError):
			statusStr = "error"
		case int(models.ResultStatusCreated):
			statusStr = "created"
		case int(models.ResultStatusAbandoned):
			// Reaper-minted terminal status: the attempt was never reported
			// on. Rendered neutrally, never as a failure (spec 2026-08-18-10).
			statusStr = "abandoned"
		}
	}

	return statusStr
}

// convertResultToLastResultResponse converts a Result model to the full
// LastResultResponse, including Output/Metrics. Detail responses
// (GetCheck) only — the detail page renders Output, Metrics, and the
// SSL-chain card from these fields.
func (s *Service) convertResultToLastResultResponse(result *models.Result) *LastResultResponse {
	if result == nil {
		return nil
	}

	return &LastResultResponse{
		UID:        result.UID,
		Status:     resultStatusString(result),
		Timestamp:  result.PeriodStart,
		DurationMs: result.Duration,
		Output:     result.Output,
		Metrics:    result.Metrics,
	}
}

// convertResultToLastResultResponseSlim converts a Result model to a slim
// LastResultResponse for list responses (ListChecks): {uid, status,
// timestamp, durationMs} only — Output/Metrics are omitted (spec
// 2026-07-06-01 Part B). No list consumer (checks.index.tsx,
// dashboard-page.tsx, status-dashboard.tsx) reads those fields; carrying
// them made list payloads ~48% larger for data that's never rendered.
func (s *Service) convertResultToLastResultResponseSlim(result *models.Result) *LastResultResponse {
	if result == nil {
		return nil
	}

	return &LastResultResponse{
		UID:        result.UID,
		Status:     resultStatusString(result),
		Timestamp:  result.PeriodStart,
		DurationMs: result.Duration,
	}
}

// emitEvent creates an event for the check lifecycle.
func (s *Service) emitEvent(
	ctx context.Context,
	orgUID string,
	eventType models.EventType,
	check *models.Check,
) error {
	// Built through internal/audit so a check event carries the same actor
	// identity (user/token, source IP, user agent) as every other audit event.
	// Before spec 2026-08-21-09 these rows hardcoded actor_type=user and never
	// set actor_uid at all, so "who deleted this check?" had no answer.
	event := audit.NewEvent(ctx, orgUID, eventType,
		audit.Target{Type: "check", UID: check.UID, Name: checkDisplayName(check)},
		models.JSONMap{
			eventPayloadCheckUIDKey: check.UID,
			"check_slug":            check.Slug,
			"check_name":            check.Name,
			"check_type":            check.Type,
		})
	event.CheckUID = &check.UID

	if err := s.db.CreateEvent(ctx, event); err != nil {
		return fmt.Errorf("failed to create event: %w", err)
	}

	// Notify workers to pick up the new check immediately. The express
	// runner subscribes to check.created and pulls the check_uid out of
	// the payload so it can claim the new job without going through the
	// regular fetcher pool.
	if s.eventNotifier != nil {
		payload := "{}"
		if encoded, err := json.Marshal(map[string]string{eventPayloadCheckUIDKey: check.UID}); err == nil {
			payload = string(encoded)
		}

		if err := s.eventNotifier.Notify(ctx, string(eventType), payload); err != nil {
			slog.WarnContext(ctx, "failed to send real-time notification",
				"event_type", eventType,
				"error", err,
			)
			// Don't fail check creation for notification errors
		}
	}

	return nil
}

// Export/Import types

// SecretsMarkerStripped is the value of ExportDocument.Secrets when the export
// had every secret-bearing config key removed. It is a string enum (rather than
// a bool) so a future trusted-transport export can add "included".
const SecretsMarkerStripped = "stripped"

// ExportDocument represents the top-level JSON export format. It is the canonical
// in-memory representation shared by export, import, and apply; the JSON tags
// carry the v1 shape (for backward-compatible v1 decode), while v2 in/out is
// handled by the wire layer in export_v2.go and the custom UnmarshalJSON below.
type ExportDocument struct {
	Version      int    `json:"version"`
	ExportedAt   string `json:"exportedAt"`
	Organization string `json:"organization"`
	// Secrets flags whether secret-bearing keys (passwords, tokens, private
	// keys, ...) were removed from every check's Config. "stripped" means the
	// operator re-enters secrets after import. Serialized as the v2 `secrets`
	// marker; import accepts the legacy `_secretsStripped` bool too (see
	// UnmarshalJSON).
	Secrets string        `json:"secrets,omitempty"`
	Checks  []ExportCheck `json:"checks"`
}

// ExportCheck represents a single check in the export format. The alerting
// fields are pointers so import can distinguish "absent" (resolve to the
// document default, then the system default) from an explicit value.
type ExportCheck struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	// PreviousSlug, when set on an apply manifest, makes a slug rename
	// reconcile in place (rather than delete+create). Ignored by export and
	// import; only the apply reconcile path consults it.
	PreviousSlug              string               `json:"previousSlug,omitempty"`
	Description               string               `json:"description,omitempty"`
	Type                      string               `json:"type"`
	Config                    map[string]any       `json:"config"`
	Regions                   []string             `json:"regions,omitempty"`
	Labels                    map[string]string    `json:"labels,omitempty"`
	Enabled                   bool                 `json:"enabled"`
	Internal                  bool                 `json:"internal,omitempty"`
	Period                    string               `json:"period,omitempty"`
	Group                     string               `json:"group,omitempty"`
	ConfirmationPeriodSeconds *int                 `json:"confirmationPeriodSeconds,omitempty"`
	EscalationThreshold       *int                 `json:"escalationThreshold,omitempty"`
	RecoveryPeriodSeconds     *int                 `json:"recoveryPeriodSeconds,omitempty"`
	ReopenCooldownMultiplier  *int                 `json:"reopenCooldownMultiplier,omitempty"`
	FlappingWindowSeconds     *int                 `json:"flappingWindowSeconds,omitempty"`
	FlapBackoffFactor         *int                 `json:"flapBackoffFactor,omitempty"`
	MaxRecoveryMultiplier     *int                 `json:"maxRecoveryMultiplier,omitempty"`
	DependsOn                 []ExportedDependency `json:"dependsOn,omitempty"`
}

// ExportedDependency mirrors an edge in slug-keyed form. Slug-keyed because
// export documents are portable across instances where UIDs differ.
type ExportedDependency struct {
	ParentSlug  string `json:"parentSlug"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
}

// ImportResult represents the result of an import operation.
type ImportResult struct {
	Created int           `json:"created"`
	Updated int           `json:"updated"`
	Skipped int           `json:"skipped"`
	Errors  []ImportError `json:"errors"`
}

// ImportError represents an error for a specific check during import.
type ImportError struct {
	Index int    `json:"index"`
	Slug  string `json:"slug"`
	Error string `json:"error"`
}

// ExportChecks exports checks for an organization in the portable JSON format.
//
//nolint:cyclop,funlen,gocritic,gocognit // group/label/dep resolution in one pass
func (s *Service) ExportChecks(
	ctx context.Context, orgSlug string, opts ListChecksOptions,
) (*ExportDocument, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Fetch all checks (no pagination limit for export)
	filter := &models.ListChecksFilter{
		Labels:        opts.Labels,
		CheckGroupUID: opts.CheckGroupUID,
		Query:         opts.Query,
	}

	checks, _, err := s.db.ListChecks(ctx, org.UID, filter)
	if err != nil {
		return nil, err
	}

	// Fetch labels for all checks
	checkUIDs := make([]string, len(checks))
	for i, c := range checks {
		checkUIDs[i] = c.UID
	}

	labelsMap, err := s.db.GetLabelsForChecks(ctx, checkUIDs)
	if err != nil {
		return nil, err
	}

	// Fetch check groups for group name resolution
	groups, err := s.db.ListCheckGroups(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	groupMap := make(map[string]string, len(groups))
	for _, g := range groups {
		groupMap[g.UID] = g.Name
	}

	// Fetch all dependencies in this org and group by child UID. Slug-keyed
	// at write time below so the export doc is portable.
	allDeps, err := s.db.ListCheckDependenciesByOrg(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	slugByUID := make(map[string]string, len(checks))
	for _, c := range checks {
		if c.Slug != nil {
			slugByUID[c.UID] = *c.Slug
		}
	}

	depsByChild := make(map[string][]ExportedDependency, len(checks))
	for _, dep := range allDeps {
		parentSlug, ok := slugByUID[dep.ParentCheckUID]
		// Skip edges whose parent isn't in the exported set (filtered out by
		// the caller's labels/group filter, or otherwise unreachable). The
		// dep is left intact in the DB; the export simply can't represent it.
		if !ok || parentSlug == "" {
			continue
		}

		entry := ExportedDependency{ParentSlug: parentSlug, Kind: string(dep.Kind)}
		if dep.Description != nil {
			entry.Description = *dep.Description
		}

		depsByChild[dep.ChildCheckUID] = append(depsByChild[dep.ChildCheckUID], entry)
	}

	for childUID := range depsByChild {
		sort.Slice(depsByChild[childUID], func(i, j int) bool {
			return depsByChild[childUID][i].ParentSlug < depsByChild[childUID][j].ParentSlug
		})
	}

	// Build export checks
	exportChecks := make([]ExportCheck, 0, len(checks))
	for _, check := range checks {
		periodValue, _ := check.Period.Value()
		periodStr, _ := periodValue.(string)

		exported := ExportCheck{
			Type:                      check.Type,
			Config:                    stripSecretKeysForExport(check),
			Regions:                   check.Regions,
			Enabled:                   check.Enabled,
			Internal:                  check.Internal,
			Period:                    periodStr,
			ConfirmationPeriodSeconds: intPtr(check.ConfirmationPeriodSeconds),
			EscalationThreshold:       intPtr(check.EscalationThreshold),
			RecoveryPeriodSeconds:     intPtr(check.RecoveryPeriodSeconds),
			ReopenCooldownMultiplier:  check.ReopenCooldownMultiplier,
			FlappingWindowSeconds:     intPtr(check.FlappingWindowSeconds),
			FlapBackoffFactor:         intPtr(check.FlapBackoffFactor),
			MaxRecoveryMultiplier:     intPtr(check.MaxRecoveryMultiplier),
		}

		if check.Name != nil {
			exported.Name = *check.Name
		}
		if check.Slug != nil {
			exported.Slug = *check.Slug
		}
		if check.Description != nil {
			exported.Description = *check.Description
		}

		// Resolve group name
		if check.CheckGroupUID != nil {
			if name, ok := groupMap[*check.CheckGroupUID]; ok {
				exported.Group = name
			}
		}

		// Attach labels
		if labels, ok := labelsMap[check.UID]; ok && len(labels) > 0 {
			exported.Labels = make(map[string]string, len(labels))
			for _, label := range labels {
				exported.Labels[label.Key] = label.Value
			}
		}

		if deps := depsByChild[check.UID]; len(deps) > 0 {
			exported.DependsOn = deps
		}

		exportChecks = append(exportChecks, exported)
	}

	// Deterministic ordering: group (empty group last), then slug. Keeps
	// git-committed exports diff-clean and groups related checks together.
	sort.SliceStable(exportChecks, func(i, j int) bool {
		groupI, groupJ := exportChecks[i].Group, exportChecks[j].Group
		if groupI != groupJ {
			switch {
			case groupI == "":
				return false
			case groupJ == "":
				return true
			default:
				return groupI < groupJ
			}
		}

		return exportChecks[i].Slug < exportChecks[j].Slug
	})

	return &ExportDocument{
		Version:      ExportVersionV2,
		ExportedAt:   time.Now().UTC().Format(time.RFC3339),
		Organization: orgSlug,
		Checks:       exportChecks,
		Secrets:      SecretsMarkerStripped,
	}, nil
}

// intPtr returns a pointer to v. Used by the exporter to populate the
// pointer-typed ExportCheck alerting fields from concrete model values.
func intPtr(v int) *int { return &v }

// stripSecretKeysForExport returns the check's Config with every key
// declared as secret by the checker (and every key already in
// ConfigPrivateKeys) removed. Exports are portable across instances and
// re-encrypting under a different KEK is out of scope, so the safe
// default is "operator re-enters secrets after import".
func stripSecretKeysForExport(check *models.Check) map[string]any {
	out := make(map[string]any, len(check.Config))
	for k, v := range check.Config {
		out[k] = v
	}

	secretSet := map[string]struct{}{}

	if cfg, ok := registry.ParseConfig(checkerdef.CheckType(check.Type)); ok {
		for _, k := range credentials.SecretFieldsFor(cfg) {
			secretSet[k] = struct{}{}
		}
	}

	if check.ConfigPrivateKeys != nil && *check.ConfigPrivateKeys != "" {
		var privateKeys []string
		if err := json.Unmarshal([]byte(*check.ConfigPrivateKeys), &privateKeys); err == nil {
			for _, k := range privateKeys {
				secretSet[k] = struct{}{}
			}
		}
	}

	for k := range secretSet {
		delete(out, k)
	}

	return out
}

// redactSecretConfig computes the (publicConfig, configPrivateKeys) pair for a
// read response: the check's Config with every declared-secret key (and every
// key already advertised in ConfigPrivateKeys) stripped out, plus the union of
// the already-advertised private keys with any secret key that was actually
// present in the public config.
//
// This is the read-time defense-in-depth for convertCheckToResponse: the
// storage split (applyEncryption) already keeps secrets out of the public
// column in every mode, but this guarantees the "no secret value leaves the
// API" invariant even for a row whose split was never applied — a
// not-yet-migrated plaintext row, or a future write path that forgets to
// split. A copy is returned so the cached model is never mutated.
func redactSecretConfig(check *models.Check, privateKeys []string) (map[string]any, []string) {
	secretSet := map[string]struct{}{}
	if cfg, ok := registry.ParseConfig(checkerdef.CheckType(check.Type)); ok {
		for _, k := range credentials.SecretFieldsFor(cfg) {
			secretSet[k] = struct{}{}
		}
	}

	advertised := make(map[string]struct{}, len(privateKeys))
	for _, k := range privateKeys {
		secretSet[k] = struct{}{}
		advertised[k] = struct{}{}
	}

	public := make(map[string]any, len(check.Config))
	for key, value := range check.Config {
		if _, isSecret := secretSet[key]; isSecret {
			// Present-but-secret: strip it from the public config and make sure
			// it is advertised as a private key (placeholder dot), since the
			// value has to live somewhere the operator can re-enter it.
			advertised[key] = struct{}{}

			continue
		}

		public[key] = value
	}

	if len(advertised) == 0 {
		return public, nil
	}

	keys := make([]string, 0, len(advertised))
	for k := range advertised {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return public, keys
}

// ImportChecks imports checks from an export document.
func (s *Service) ImportChecks(
	ctx context.Context, orgSlug string, doc *ExportDocument, dryRun bool,
) (*ImportResult, error) {
	if !isSupportedExportVersion(doc.Version) {
		return nil, ErrUnsupportedExportVersion
	}

	if len(doc.Checks) == 0 {
		return nil, ErrEmptyChecksArray
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Pre-fetch existing groups for name matching
	existingGroups, err := s.db.ListCheckGroups(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	groupByName := make(map[string]*models.CheckGroup, len(existingGroups))
	for _, g := range existingGroups {
		groupByName[strings.ToLower(g.Name)] = g
	}

	result := &ImportResult{
		Errors: []ImportError{},
	}

	pass1Failed := make(map[string]struct{}, 0)

	for i := range doc.Checks {
		created, importErr := s.importSingleCheck(ctx, org, orgSlug, &doc.Checks[i], i, dryRun, groupByName)
		if importErr != nil {
			result.Errors = append(result.Errors, *importErr)
			pass1Failed[doc.Checks[i].Slug] = struct{}{}

			continue
		}

		if created {
			result.Created++
		} else {
			result.Updated++
		}
	}

	// Pass 2: apply dependsOn after every check has been upserted, so a
	// payload can declare both endpoints of an edge for the first time.
	// Skipped on dry-run since pass 1 only simulated the upserts.
	if !dryRun {
		s.importDependencies(ctx, org.UID, doc.Checks, pass1Failed, result)
	}

	return result, nil
}

// importDependencies applies the additive dep merge for pass 2 of import.
// Skipped silently for any check whose pass-1 upsert failed; otherwise
// resolves slugs, runs validators, and writes/updates edges. Errors land in
// result.Errors with the same shape as pass-1 errors.
func (s *Service) importDependencies(
	ctx context.Context,
	orgUID string,
	checks []ExportCheck,
	pass1Failed map[string]struct{},
	result *ImportResult,
) {
	hasAnyDeps := false
	for i := range checks {
		if len(checks[i].DependsOn) > 0 {
			hasAnyDeps = true

			break
		}
	}

	if !hasAnyDeps {
		return
	}

	currentChecks, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{})
	if err != nil {
		result.Errors = append(result.Errors, ImportError{
			Index: -1, Slug: "", Error: "list checks for dep import: " + err.Error(),
		})

		return
	}

	uidBySlug := make(map[string]string, len(currentChecks))
	for _, c := range currentChecks {
		if c.Slug != nil {
			uidBySlug[*c.Slug] = c.UID
		}
	}

	for i := range checks {
		entry := &checks[i]
		if len(entry.DependsOn) == 0 {
			continue
		}

		if _, failed := pass1Failed[entry.Slug]; failed {
			result.Errors = append(result.Errors, ImportError{
				Index: i, Slug: entry.Slug,
				Error: "skipped dependsOn: pass-1 upsert failed for this check",
			})

			continue
		}

		childUID, ok := uidBySlug[entry.Slug]
		if !ok {
			result.Errors = append(result.Errors, ImportError{
				Index: i, Slug: entry.Slug,
				Error: "dependsOn: cannot resolve own slug after upsert",
			})

			continue
		}

		for j := range entry.DependsOn {
			dep := &entry.DependsOn[j]
			parentUID, ok := uidBySlug[dep.ParentSlug]
			if !ok {
				result.Errors = append(result.Errors, ImportError{
					Index: i, Slug: entry.Slug,
					Error: "dependsOn: unknown parent slug: " + dep.ParentSlug,
				})

				continue
			}

			if applyErr := s.applyImportEdge(ctx, orgUID, parentUID, childUID, dep); applyErr != nil {
				result.Errors = append(result.Errors, ImportError{
					Index: i, Slug: entry.Slug,
					Error: "dependsOn[" + dep.ParentSlug + "]: " + applyErr.Error(),
				})
			}
		}
	}
}

// applyImportEdge writes (or updates) one edge for pass-2 import. Returns the
// validation error if any. Additive: existing edges with same kind+desc are
// no-ops; differing kind/desc triggers update; absent edges are created after
// a cycle check.
func (s *Service) applyImportEdge(
	ctx context.Context, orgUID, parentUID, childUID string, dep *ExportedDependency,
) error {
	if parentUID == childUID {
		return errDepImportSelfEdge
	}

	kind := models.CheckDependencyKind(dep.Kind)
	if !kind.IsValid() {
		return fmt.Errorf("%w: %s", errDepInvalidImportKind, dep.Kind)
	}

	existing, err := s.db.FindCheckDependencyEdge(ctx, parentUID, childUID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("find edge: %w", err)
	}

	var descPtr *string
	if dep.Description != "" {
		desc := dep.Description
		descPtr = &desc
	}

	if existing != nil {
		return s.maybeUpdateEdge(ctx, existing, kind, descPtr)
	}

	if cycleErr := s.assertEdgeNoCycle(ctx, orgUID, parentUID, childUID); cycleErr != nil {
		return cycleErr
	}

	newEdge := models.NewCheckDependency(orgUID, parentUID, childUID, kind, descPtr)

	return s.db.CreateCheckDependency(ctx, newEdge)
}

// sameDesc reports whether two optional descriptions carry the same value.
func sameDesc(left, right *string) bool {
	if left == nil && right == nil {
		return true
	}

	if left == nil || right == nil {
		return false
	}

	return *left == *right
}

// assertEdgeNoCycle is the package-local mirror of
// checkdependencies.Service.assertNoCycle — kept inline to avoid coupling the
// checks handler to the dependency handler. DFS over the existing org graph;
// if `parent` is reachable from `child`, adding (parent → child) closes a
// cycle.
func (s *Service) assertEdgeNoCycle(ctx context.Context, orgUID, parentUID, childUID string) error {
	const depthCap = 32

	deps, err := s.db.ListCheckDependenciesByOrg(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("list deps for cycle check: %w", err)
	}

	adjacency := make(map[string][]string, len(deps))
	for _, dep := range deps {
		adjacency[dep.ParentCheckUID] = append(adjacency[dep.ParentCheckUID], dep.ChildCheckUID)
	}

	stack := []string{childUID}
	visited := map[string]struct{}{childUID: {}}

	for depth := 0; depth < depthCap && len(stack) > 0; depth++ {
		next := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, child := range adjacency[next] {
			if child == parentUID {
				return errDepCycleSimpleEdge
			}

			if _, ok := visited[child]; ok {
				continue
			}

			visited[child] = struct{}{}
			stack = append(stack, child)
		}
	}

	return nil
}

// importSingleCheck handles importing a single check.
// Returns (wasCreated, error). wasCreated is true if a new check was created, false if updated.
func (s *Service) importSingleCheck(
	ctx context.Context,
	org *models.Organization,
	orgSlug string,
	exportedCheck *ExportCheck,
	index int,
	dryRun bool,
	groupByName map[string]*models.CheckGroup,
) (bool, *ImportError) {
	// Validate required fields
	if exportedCheck.Slug == "" {
		return false, &ImportError{Index: index, Slug: exportedCheck.Slug, Error: "slug is required"}
	}
	if exportedCheck.Type == "" {
		return false, &ImportError{Index: index, Slug: exportedCheck.Slug, Error: "type is required"}
	}
	if exportedCheck.Config == nil {
		return false, &ImportError{Index: index, Slug: exportedCheck.Slug, Error: "config is required"}
	}

	// Validate check type
	if _, ok := registry.GetChecker(checkerdef.CheckType(exportedCheck.Type)); !ok {
		return false, &ImportError{
			Index: index, Slug: exportedCheck.Slug, Error: "invalid check type: " + exportedCheck.Type,
		}
	}

	// Check if slug exists to determine created vs updated
	existing, _ := s.db.GetCheckByUidOrSlug(ctx, org.UID, exportedCheck.Slug)
	created := existing == nil

	if dryRun {
		return created, nil
	}

	// Resolve group by name (case-insensitive), auto-create if needed
	var checkGroupUID *string
	if exportedCheck.Group != "" {
		group, ok := groupByName[strings.ToLower(exportedCheck.Group)]
		if !ok {
			// Auto-create group
			groupSlug := sanitizeSlug(strings.ToLower(exportedCheck.Group))
			newGroup := models.NewCheckGroup(org.UID, exportedCheck.Group, groupSlug)
			if createErr := s.db.CreateCheckGroup(ctx, newGroup); createErr != nil {
				return false, &ImportError{
					Index: index, Slug: exportedCheck.Slug,
					Error: "failed to create group: " + createErr.Error(),
				}
			}
			groupByName[strings.ToLower(exportedCheck.Group)] = newGroup
			group = newGroup
		}
		checkGroupUID = &group.UID
	}

	// Build upsert request. The alerting fields are already resolved
	// (check value → document default → absent) by the time the document is
	// decoded, so a nil pointer here means "use the system default".
	upsertReq := UpsertCheckRequest{
		Name:                      exportedCheck.Name,
		Description:               exportedCheck.Description,
		CheckGroupUID:             checkGroupUID,
		Type:                      exportedCheck.Type,
		Config:                    exportedCheck.Config,
		Regions:                   exportedCheck.Regions,
		Enabled:                   &exportedCheck.Enabled,
		Internal:                  &exportedCheck.Internal,
		Labels:                    exportedCheck.Labels,
		ConfirmationPeriodSeconds: exportedCheck.ConfirmationPeriodSeconds,
		RecoveryPeriodSeconds:     exportedCheck.RecoveryPeriodSeconds,
		ReopenCooldownMultiplier:  exportedCheck.ReopenCooldownMultiplier,
		FlappingWindowSeconds:     exportedCheck.FlappingWindowSeconds,
		FlapBackoffFactor:         exportedCheck.FlapBackoffFactor,
		MaxRecoveryMultiplier:     exportedCheck.MaxRecoveryMultiplier,
	}
	if exportedCheck.Period != "" {
		upsertReq.Period = &exportedCheck.Period
	}

	_, _, upsertErr := s.UpsertCheck(ctx, orgSlug, exportedCheck.Slug, &upsertReq)
	if upsertErr != nil {
		return false, &ImportError{Index: index, Slug: exportedCheck.Slug, Error: upsertErr.Error()}
	}

	return created, nil
}

// CloneCheckRequest carries the optional overrides for the clone endpoint.
// All fields are optional — an empty body produces a clone with safe defaults
// (`(copy)` suffix on name, `-copy` suffix on slug, enabled=true).
type CloneCheckRequest struct {
	Name          *string `json:"name,omitempty"`
	Slug          *string `json:"slug,omitempty"`
	Description   *string `json:"description,omitempty"`
	CheckGroupUID *string `json:"checkGroupUid,omitempty"`
	Enabled       *bool   `json:"enabled,omitempty"`
}

// CloneCheck creates a near-identical copy of an existing check, server-side.
// Runtime state (status streak, results, incidents, scheduler row) is not
// copied; status-page and maintenance-window references are not copied
// either. Labels and check_connections (with their per-check setting
// overrides) are re-linked to the new check.
func (s *Service) CloneCheck(
	ctx context.Context, orgSlug, sourceIdentifier string, req *CloneCheckRequest,
) (CheckResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, ErrOrganizationNotFound
	}

	source, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, sourceIdentifier)
	if err != nil || source == nil {
		return CheckResponse{}, ErrCheckNotFound
	}

	finalSlug, baseSlug, userProvidedSlug, err := s.cloneResolveSlug(ctx, org.UID, source, req)
	if err != nil {
		return CheckResponse{}, err
	}

	clone := s.cloneBuildCheck(org.UID, source, req, finalSlug)

	// Clone bypasses CreateCheck (calls s.db.CreateCheck directly), so the
	// MaxChecks quota guard must be applied here too. Internal clones are
	// exempt, mirroring CreateCheck.
	if !clone.Internal && s.entitlements != nil {
		if quotaErr := s.entitlements.CheckCreateAllowed(ctx, org.UID); quotaErr != nil {
			return CheckResponse{}, quotaErr
		}
	}

	// Same optimistic-slug race as CreateCheck: two clones of one check, or a
	// clone racing a create for the same target, resolve to the same slug.
	if createErr := s.insertCheckResolvingSlugRace(
		ctx, org.UID, baseSlug, userProvidedSlug, clone,
	); createErr != nil {
		// %w keeps errors.Is(err, ErrSlugConflict) true, so the handler still
		// maps a lost user-named-slug race to the same 400 as the pre-check.
		return CheckResponse{}, fmt.Errorf("create clone: %w", createErr)
	}

	if labelErr := s.cloneCopyLabels(ctx, source.UID, clone.UID); labelErr != nil {
		slog.WarnContext(ctx, "failed to copy labels for clone",
			"source_uid", source.UID, "clone_uid", clone.UID, "error", labelErr)
	}

	if connErr := s.cloneCopyConnections(ctx, source.UID, clone.UID, org.UID); connErr != nil {
		slog.WarnContext(ctx, "failed to copy check connections for clone",
			"source_uid", source.UID, "clone_uid", clone.UID, "error", connErr)
	}

	if eventErr := s.emitEvent(ctx, org.UID, models.EventTypeCheckCreated, clone); eventErr != nil {
		slog.WarnContext(ctx, "failed to emit check.created event for clone", "error", eventErr)
	}

	response := s.convertCheckToResponse(clone)

	if labels, lerr := s.db.GetLabelsForCheck(ctx, clone.UID); lerr == nil && len(labels) > 0 {
		response.Labels = make(map[string]string, len(labels))
		for _, lbl := range labels {
			response.Labels[lbl.Key] = lbl.Value
		}
	}

	return response, nil
}

// cloneResolveSlug picks the clone's slug. It returns the resolved slug, the
// base it was derived from and whether the caller asked for it by name — the
// last two so the insert can re-resolve after losing the slug race (see
// insertCheckResolvingSlugRace).
func (s *Service) cloneResolveSlug(
	ctx context.Context, orgUID string, source *models.Check, req *CloneCheckRequest,
) (string, string, bool, error) {
	sourceSlug := ""
	if source.Slug != nil {
		sourceSlug = *source.Slug
	}

	var (
		baseSlug         string
		userProvidedSlug bool
	)

	if req != nil && req.Slug != nil && *req.Slug != "" {
		if err := validateSlug(*req.Slug); err != nil {
			return "", "", false, err
		}

		baseSlug = *req.Slug
		userProvidedSlug = true
	} else {
		base := sourceSlug
		if base == "" {
			base = source.Type
		}

		baseSlug = base + "-copy"
	}

	finalSlug, err := s.ensureUniqueSlug(ctx, orgUID, baseSlug, userProvidedSlug)
	if err != nil {
		return "", "", false, err
	}

	return finalSlug, baseSlug, userProvidedSlug, nil
}

func (s *Service) cloneBuildCheck(
	orgUID string, source *models.Check, req *CloneCheckRequest, finalSlug string,
) *models.Check {
	targetName := resolveCloneName(source, req, finalSlug)

	clone := models.NewCheck(orgUID, finalSlug, source.Type)
	clone.Name = &targetName
	clone.Description = resolveCloneDescription(source, req)

	clone.Config = source.Config
	clone.Regions = append([]string(nil), source.Regions...)
	clone.Period = source.Period
	clone.Internal = source.Internal
	clone.ConfirmationPeriodSeconds = source.ConfirmationPeriodSeconds
	clone.EscalationThreshold = source.EscalationThreshold
	clone.RecoveryPeriodSeconds = source.RecoveryPeriodSeconds
	clone.ReopenCooldownMultiplier = source.ReopenCooldownMultiplier
	clone.FlappingWindowSeconds = source.FlappingWindowSeconds
	clone.FlapBackoffFactor = source.FlapBackoffFactor
	clone.MaxRecoveryMultiplier = source.MaxRecoveryMultiplier
	clone.EscalationPolicyUID = source.EscalationPolicyUID
	clone.CheckGroupUID = resolveCloneGroup(source, req)

	// A clone is enabled by default so it starts running immediately, like a
	// normal create. An explicit `enabled` in the request still wins.
	clone.Enabled = true
	if req != nil && req.Enabled != nil {
		clone.Enabled = *req.Enabled
	}

	return clone
}

func resolveCloneName(source *models.Check, req *CloneCheckRequest, fallback string) string {
	if req != nil && req.Name != nil && *req.Name != "" {
		return *req.Name
	}

	if source.Name != nil && *source.Name != "" {
		return *source.Name + " (copy)"
	}

	return fallback
}

func resolveCloneDescription(source *models.Check, req *CloneCheckRequest) *string {
	if req != nil && req.Description != nil {
		copied := *req.Description

		return &copied
	}

	if source.Description != nil {
		copied := *source.Description

		return &copied
	}

	return nil
}

func resolveCloneGroup(source *models.Check, req *CloneCheckRequest) *string {
	if req == nil || req.CheckGroupUID == nil {
		return source.CheckGroupUID
	}

	if *req.CheckGroupUID == "" {
		return nil
	}

	value := *req.CheckGroupUID

	return &value
}

func (s *Service) cloneCopyLabels(ctx context.Context, sourceUID, cloneUID string) error {
	labels, err := s.db.GetLabelsForCheck(ctx, sourceUID)
	if err != nil {
		return fmt.Errorf("load source labels: %w", err)
	}

	if len(labels) == 0 {
		return nil
	}

	uids := make([]string, 0, len(labels))
	for _, lbl := range labels {
		uids = append(uids, lbl.UID)
	}

	if err := s.db.SetCheckLabels(ctx, cloneUID, uids); err != nil {
		return fmt.Errorf("set clone labels: %w", err)
	}

	return nil
}

func (s *Service) cloneCopyConnections(ctx context.Context, sourceUID, cloneUID, orgUID string) error {
	srcConns, err := s.db.ListCheckConnectionsWithSettings(ctx, sourceUID)
	if err != nil {
		return fmt.Errorf("list source check connections: %w", err)
	}

	for _, src := range srcConns {
		newConn := models.NewCheckConnection(cloneUID, src.ConnectionUID, orgUID)

		if src.Settings != nil {
			copied := make(models.JSONMap, len(*src.Settings))
			for key, value := range *src.Settings {
				copied[key] = value
			}

			newConn.Settings = &copied
		}

		if err := s.db.CreateCheckConnection(ctx, newConn); err != nil {
			return fmt.Errorf("create clone check connection: %w", err)
		}
	}

	return nil
}

// normalizeCheckConfig rewrites an effective config map into the canonical
// shape its checker wants stored, via the optional
// checkerdef.ConfigNormalizer interface. Unknown checker types and configs
// that don't implement it pass through untouched.
//
// It MUST be called on the effective (post-merge) config, never on a raw PATCH
// body: normalizing a patch would fold half a credential and then replace the
// stored map with it.
func normalizeCheckConfig(checkType string, effective map[string]any) (map[string]any, error) {
	cfg, ok := registry.ParseConfig(checkerdef.CheckType(checkType))
	if !ok {
		return effective, nil
	}

	return checkerdef.NormalizeConfigFor(cfg, effective)
}

// applyEncryption splits the effective config into public/private using the
// checker's declared SecretFields and writes the resulting columns onto the
// check. Secrets are ALWAYS split out of the public `Config` column — the
// public column must never carry a secret value, in any mode (spec
// 2026-07-18-06).
//
// The private side is stored as: a region-sealed (age) envelope for private
// regions, an AES-GCM envelope when a master key is configured, or a plaintext
// envelope (the documented self-hosted no-key fallback) otherwise. The
// plaintext envelope keeps secrets out of the public column and out of API
// responses even though it does not encrypt them at rest.
func (s *Service) applyEncryption(ctx context.Context, check *models.Check, effective map[string]any) error {
	cfg, ok := registry.ParseConfig(checkerdef.CheckType(check.Type))
	if !ok {
		// Unknown checker — keep the existing behavior: store the map as
		// plaintext and let validation fail elsewhere. Don't silently lose
		// data on the encryption seam.
		check.Config = effective
		check.ConfigPrivate = nil
		check.ConfigPrivateKeys = nil
		return nil
	}

	secrets := credentials.SecretFieldsFor(cfg)
	public, private := credentials.SplitConfig(effective, secrets)
	check.Config = public

	sealedOnly, sealErr := s.applyRegionSealing(ctx, check, private)
	if sealErr != nil {
		return sealErr
	}

	if sealedOnly {
		// Sealed-only: never store a server-decryptable copy. Placeholder
		// key names stay visible to the dashboard.
		check.ConfigPrivate = nil

		return setConfigPrivateKeys(check, private)
	}

	if len(private) == 0 {
		check.ConfigPrivate = nil
		check.ConfigPrivateKeys = nil

		return nil
	}

	// Secrets are ALWAYS split out of the public `Config` column and stored in
	// `config_private`, in every mode — the public column must never carry a
	// secret value (the leak this fixes). When a master key is configured we
	// store an AES-GCM envelope; when it is not, we store a clearly-marked
	// plaintext envelope (the documented V1 self-hosted fallback). Either way
	// `config_private_keys` is set so the dashboard renders placeholder dots.
	var (
		envelope string
		err      error
	)

	if s.creds.Enabled() {
		envelope, err = s.creds.EncryptForOrg(ctx, check.OrganizationUID, private)
		if err != nil {
			return fmt.Errorf("encrypt check config: %w", err)
		}
	} else {
		envelope, err = credentials.SealPlaintext(private)
		if err != nil {
			return fmt.Errorf("seal plaintext check config: %w", err)
		}
	}

	check.ConfigPrivate = &envelope

	return setConfigPrivateKeys(check, private)
}

// applyConfigUpdate handles a PATCHed config: the preserve-absent-secrets
// merge, timeout validation, encryption/sealing, and the sealed-only PATCH
// contract (spec 2026-07-16-02: when the request carries no secret values the
// server cannot re-seal what it cannot read — the previous sealed blob is kept
// AS-IS; secrets provided → a fresh blob replaces it). Fills the update
// struct's config columns.
func (s *Service) applyConfigUpdate(
	ctx context.Context, check *models.Check, patch map[string]any, update *models.CheckUpdate,
) error {
	// PATCH-merge rule: read the existing effective config (decrypting
	// the private side) and apply the spec's preserve-absent-secrets
	// behavior. A naive replace here would silently wipe encrypted
	// secrets the user can't see in their dashboard.
	oldSealed := check.ConfigSealed
	oldPrivateKeys := check.ConfigPrivateKeys

	merged, mergeErr := s.applyConfigPatch(ctx, check, patch)
	if mergeErr != nil {
		return mergeErr
	}

	// Normalize the EFFECTIVE config, after the merge and before encryption —
	// never the raw patch, whose folded output would replace the stored map
	// wholesale and wipe unrelated keys. This is also the only validation the
	// config gets on the PATCH path (UpdateCheck never calls checker.Validate),
	// so its *ConfigError surfaces as a 400.
	merged, normErr := normalizeCheckConfig(check.Type, merged)
	if normErr != nil {
		return normErr
	}

	// Uniform per-check timeout cap (spec 2026-07-11-05) — validated on
	// the merged config so a PATCH cannot smuggle in an over-cap value.
	if timeoutErr := validateConfigTimeout(merged); timeoutErr != nil {
		return timeoutErr
	}

	// Same reasoning for the address family: validated on the merged config so a
	// PATCH cannot smuggle in an invalid value, the option on a type that cannot
	// honor it, or an ipVersion onto an already-tunneled check.
	if versionErr := validateIPVersionConfig(check.Type, merged); versionErr != nil {
		return versionErr
	}

	// Same reasoning for the tunnel reference: validated on the merged config so
	// a PATCH cannot smuggle in a dangling / unverified / chained tunnel.
	if tunnelErr := s.validateTunnelConfig(
		ctx, check.OrganizationUID, check.Type, merged, check.Regions,
	); tunnelErr != nil {
		return tunnelErr
	}

	// Send-mode SMTP reference validation (spec 2026-08-19-04) — validated on
	// the merged config so a PATCH cannot smuggle in a dangling reference or
	// turn on send_email without a valid pairing.
	if smtpErr := s.validateSMTPDeliveryConfig(ctx, check.OrganizationUID, check.Type, merged); smtpErr != nil {
		return smtpErr
	}

	if encErr := s.applyEncryption(ctx, check, merged); encErr != nil {
		return encErr
	}

	if check.ConfigSealed == nil && oldSealed != nil {
		check.ConfigSealed = oldSealed

		if check.ConfigPrivateKeys == nil {
			check.ConfigPrivateKeys = oldPrivateKeys
		}
	}

	configMap := check.Config
	update.Config = &configMap
	update.ConfigPrivate = check.ConfigPrivate
	update.ConfigPrivateKeys = check.ConfigPrivateKeys

	if check.ConfigPrivate == nil {
		update.ClearConfigPrivate = true
	}

	update.ConfigSealed = check.ConfigSealed
	if check.ConfigSealed == nil {
		update.ClearConfigSealed = true
	}

	return nil
}

// applyRegionSealing implements phase 2 of spec 2026-07-16-02. When the check
// targets private region(s) and carries secrets, it seals them to the X25519
// keys of every ACTIVE agent of those regions and stores the blob on
// check.ConfigSealed. Returns true when the check is sealed-ONLY (private
// regions only): the caller must then skip the v1 envelope so the server holds
// no decryptable copy. Mixed private+cloud checks dual-store (sealed blob here
// + v1 envelope in the caller).
//
// Decision 4 is unconditional — a private-only check's secrets are sealed-only
// or the write does not happen. When no active agent exists to seal to, the
// write is REFUSED with ErrNoAgentsToSealTo rather than silently degraded to a
// server-decryptable envelope. There is nothing to lose by refusing: without a
// sealed blob the agent receives public config only and the check could not
// work regardless, so the fallback would buy a broken check AND a
// plaintext-recoverable secret on the server.
func (s *Service) applyRegionSealing(
	ctx context.Context, check *models.Check, private map[string]any,
) (bool, error) {
	check.ConfigSealed = nil
	privateRegions := filterPrivateRegions(check.Regions)
	sealedOnly := len(privateRegions) > 0 && len(privateRegions) == len(check.Regions)

	if len(private) == 0 || len(privateRegions) == 0 {
		return false, nil
	}

	recipients, recErr := s.activeRecipientsForRegions(ctx, check.OrganizationUID, privateRegions)
	if recErr != nil {
		return false, recErr
	}

	if len(recipients) == 0 {
		if sealedOnly {
			return false, fmt.Errorf(
				"%w: enroll an agent in %s before saving credentials for a check that runs only there",
				ErrNoAgentsToSealTo, strings.Join(privateRegions, ", "))
		}

		// Mixed private+cloud: the cloud side still works off the org-DEK
		// envelope, so the write proceeds without a sealed blob. The check is
		// flagged needs-re-seal until an agent enrolls and the credentials are
		// re-saved (or auto re-sealed — the server can decrypt this one).
		slog.WarnContext(ctx,
			"mixed-region check has no active agents to seal to; agents cannot run it until re-sealed",
			"checkUid", check.UID, "regions", privateRegions)

		return false, nil
	}

	sealed, sealErr := credentials.SealForRecipients(recipients, private)
	if sealErr != nil {
		return false, fmt.Errorf("seal check config: %w", sealErr)
	}

	check.ConfigSealed = &sealed

	return sealedOnly, nil
}

// setConfigPrivateKeys records the secret key names (dashboard placeholders).
// A nil map clears the column.
func setConfigPrivateKeys(check *models.Check, private map[string]any) error {
	if len(private) == 0 {
		check.ConfigPrivateKeys = nil

		return nil
	}

	keysJSON, err := json.Marshal(sortedKeys(private))
	if err != nil {
		return fmt.Errorf("marshal config private keys: %w", err)
	}

	keysStr := string(keysJSON)
	check.ConfigPrivateKeys = &keysStr

	return nil
}

// filterPrivateRegions returns the org-private (`@…`) regions of a region set.
func filterPrivateRegions(regionSlugs []string) []string {
	out := make([]string, 0, len(regionSlugs))
	for _, region := range regionSlugs {
		if regions.IsPrivateRegion(region) {
			out = append(out, region)
		}
	}

	return out
}

// computeNeedsReseal reports whether a private-region check's sealed blob is
// out of step with the active agent set (nil when the check targets no private
// region, or on lookup failure — the flag is advisory, never a hard error).
func (s *Service) computeNeedsReseal(ctx context.Context, check *models.Check) *bool {
	privateRegions := filterPrivateRegions(check.Regions)
	if len(privateRegions) == 0 {
		return nil
	}

	hasSecrets := check.ConfigPrivateKeys != nil && *check.ConfigPrivateKeys != "" && *check.ConfigPrivateKeys != "[]"

	if check.ConfigSealed == nil || *check.ConfigSealed == "" {
		if hasSecrets {
			// Secrets exist but were never sealed (e.g. written before any
			// agent enrolled) — agents cannot decrypt them.
			need := true

			return &need
		}

		return nil
	}

	recipients, err := s.activeRecipientsForRegions(ctx, check.OrganizationUID, privateRegions)
	if err != nil {
		return nil
	}

	fingerprints := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		fingerprints = append(fingerprints, credentials.RecipientFingerprint(recipient))
	}

	need, err := credentials.NeedsReseal(*check.ConfigSealed, fingerprints)
	if err != nil {
		return nil
	}

	return &need
}

// activeRecipientsForRegions collects the X25519 recipients (age public keys)
// of every active agent across the given private regions — the recipient set a
// credential blob is sealed to at write time.
func (s *Service) activeRecipientsForRegions(
	ctx context.Context, orgUID string, privateRegions []string,
) ([]string, error) {
	recipients := make([]string, 0, 4)
	seen := make(map[string]struct{})

	for _, region := range privateRegions {
		agents, err := s.db.ListActiveAgentsByRegion(ctx, orgUID, region)
		if err != nil {
			return nil, fmt.Errorf("list active agents for %s: %w", region, err)
		}

		for _, agent := range agents {
			if _, dup := seen[agent.X25519PublicKey]; dup {
				continue
			}

			seen[agent.X25519PublicKey] = struct{}{}
			recipients = append(recipients, agent.X25519PublicKey)
		}
	}

	return recipients, nil
}

// loadDecryptedConfig returns the merged plaintext effective config of a
// check. Used by the PATCH path so the secret-preservation rule has the
// existing values to merge into. Returns the public config unchanged when
// the row has no private payload (legacy or unencrypted checks).
func (s *Service) loadDecryptedConfig(ctx context.Context, check *models.Check) (map[string]any, error) {
	if check.ConfigPrivate == nil || *check.ConfigPrivate == "" {
		// Defensive copy so callers can mutate freely.
		out := make(map[string]any, len(check.Config))
		for k, v := range check.Config {
			out[k] = v
		}
		return out, nil
	}

	// A plaintext envelope opens without a master key; only AES-GCM / sealed
	// envelopes require one. Gate the disabled error on that so a no-key
	// self-hosted PATCH can still read (and thus preserve) its own secrets.
	if credentials.RequiresKey(*check.ConfigPrivate) && !s.creds.Enabled() {
		return nil, fmt.Errorf("decrypt check %s: %w", check.UID, credentials.ErrDisabled)
	}

	private, err := s.creds.DecryptForOrg(ctx, check.OrganizationUID, *check.ConfigPrivate)
	if err != nil {
		return nil, fmt.Errorf("decrypt check %s: %w", check.UID, err)
	}

	out := make(map[string]any, len(check.Config)+len(private))
	for k, v := range check.Config {
		out[k] = v
	}
	for k, v := range private {
		out[k] = v
	}
	return out, nil
}

// mergePatchConfig applies the PATCH-merge rule: existing secret values
// are preserved unless the request explicitly sends them. Sending a key
// with null or empty-string clears it.
func mergePatchConfig(existing, patch map[string]any, secretFields []string) map[string]any {
	merged := make(map[string]any, len(existing)+len(patch))

	// Keep all of existing as the base.
	for k, v := range existing {
		merged[k] = v
	}

	// Layer the patch — but for *non-secret* keys the patch wins fully
	// (replace semantics). For secret keys, an absent key preserves
	// existing; an explicit null/empty clears.
	secretSet := map[string]struct{}{}
	for _, k := range secretFields {
		secretSet[k] = struct{}{}
	}

	for key, val := range patch {
		if _, isSecret := secretSet[key]; isSecret {
			if val == nil || val == "" {
				delete(merged, key)
				continue
			}
		}
		merged[key] = val
	}

	// Non-secret keys absent from patch should also be removed (PATCH
	// semantics on the public side: replace wholesale). But the existing
	// PATCH contract for checks already replaces the whole config map, so
	// we honor that — public keys absent from patch are dropped.
	for k := range merged {
		if _, isSecret := secretSet[k]; isSecret {
			continue
		}
		if _, present := patch[k]; !present {
			delete(merged, k)
		}
	}

	return merged
}

// sortedKeys returns the keys of m in lexicographic order. Stable output
// matters for ConfigPrivateKeys so the dashboard placeholder list doesn't
// flicker between renders.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// applyConfigPatch implements the PATCH-merge rule from the spec: secret
// keys absent from the incoming config are preserved from the existing
// config_private; explicitly-supplied keys (including null/empty)
// overwrite/clear. Non-secret keys follow the existing replace-wholesale
// PATCH semantics.
func (s *Service) applyConfigPatch(
	ctx context.Context, check *models.Check, patch map[string]any,
) (map[string]any, error) {
	existing, err := s.loadDecryptedConfig(ctx, check)
	if err != nil {
		return nil, err
	}

	cfg, ok := registry.ParseConfig(checkerdef.CheckType(check.Type))
	if !ok {
		// Unknown checker — fall back to plain replace.
		return patch, nil
	}

	return mergePatchConfig(existing, patch, credentials.SecretFieldsFor(cfg)), nil
}

// checkDisplayName is the check's name for the audit trail, falling back to
// its slug when the optional name is unset.
func checkDisplayName(check *models.Check) string {
	if check.Name != nil && *check.Name != "" {
		return *check.Name
	}

	if check.Slug != nil {
		return *check.Slug
	}

	return ""
}
