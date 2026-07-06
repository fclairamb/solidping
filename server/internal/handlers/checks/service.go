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
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/activation"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
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
	Type      string               `json:"type"`
	Slug      string               `json:"slug,omitempty"`
	Config    map[string]any       `json:"config"`
	DependsOn []ExportedDependency `json:"dependsOn,omitempty"`
}

// ValidateCheckResponse is the response body for the validate check endpoint.
type ValidateCheckResponse struct {
	Valid  bool                        `json:"valid"`
	Fields []base.ValidationErrorField `json:"fields,omitempty"`
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

// slugRegex validates slug format: lowercase letter, then 2-19 lowercase letters/digits/hyphens.
// Total length: 3-20 characters.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,49}$`)

// slugInvalidCharsRegex matches characters that are not valid in a slug.
var slugInvalidCharsRegex = regexp.MustCompile(`[^a-z0-9-]`)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrCheckNotFound is returned when a check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrInvalidCheckType is returned when an unsupported check type is provided.
	ErrInvalidCheckType = errors.New("invalid check type")
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

// validateIncidentPeriod range-checks a confirmation/recovery period in
// seconds. 0 is allowed and means "fire immediately on the first signal".
func validateIncidentPeriod(seconds int) error {
	if seconds < 0 || seconds > 86400 {
		return errIncidentPeriodOutOfRange
	}

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
// Valid slugs: start with lowercase letter, followed by 2-49 lowercase letters, digits, or hyphens.
// Total length: 3-50 characters. Must not look like a UUID.
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
	ctx context.Context, orgSlug string, req ValidateCheckRequest,
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

	if depFields, depErr := s.validateDependsOn(ctx, orgSlug, req.Slug, req.DependsOn); depErr != nil {
		return ValidateCheckResponse{}, depErr
	} else if len(depFields) > 0 {
		return ValidateCheckResponse{Valid: false, Fields: depFields}, nil
	}

	return ValidateCheckResponse{Valid: true}, nil
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
	// ConfigPrivateKeys lists the keys whose values are encrypted at rest
	// for this check. The dashboard uses it to render placeholder hints
	// (●●●●●●●●) for fields it can't display. Non-secret by construction
	// — these are key names, not values.
	ConfigPrivateKeys []string          `json:"configPrivateKeys,omitempty"`
	Regions           []string          `json:"regions,omitempty"`
	Enabled           *bool             `json:"enabled,omitempty"`
	Internal          *bool             `json:"internal,omitempty"`
	Period            *string           `json:"period,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
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

// ListChecksOptions contains options for listing checks.
type ListChecksOptions struct {
	IncludeLastResult       bool
	IncludeLastStatusChange bool
	Labels                  map[string]string
	CheckGroupUID           *string
	Query                   string
	Internal                *string
	Statuses                []models.CheckStatus
	Cursor                  string
	Limit                   int
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

	// Build filter
	filter := &models.ListChecksFilter{
		Labels:        opts.Labels,
		CheckGroupUID: opts.CheckGroupUID,
		Query:         opts.Query,
		Internal:      opts.Internal,
		Statuses:      opts.Statuses,
		Limit:         opts.Limit,
	}

	// Parse cursor
	if opts.Cursor != "" {
		ts, uid, errCursor := s.decodeCursor(opts.Cursor)
		if errCursor != nil {
			return nil, ErrInvalidCursor
		}
		filter.CursorCreatedAt = &ts
		filter.CursorUID = &uid
	}

	// Get checks for the organization
	checks, total, err := s.db.ListChecks(ctx, org.UID, filter)
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

		// Attach last results to responses
		for i := range responses {
			if result, ok := lastResults[checks[i].UID]; ok {
				responses[i].LastResult = s.convertResultToLastResultResponse(result)
			}
		}
	}

	// If last status change is requested, fetch them in a single query
	if opts.IncludeLastStatusChange && len(checks) > 0 {
		checkUIDs := make([]string, len(checks))
		for i, check := range checks {
			checkUIDs[i] = check.UID
		}

		lastStatusChanges, err := s.db.GetLastStatusChangeForChecks(ctx, checkUIDs)
		if err != nil {
			return nil, err
		}

		// Attach last status change data to responses
		for i := range responses {
			if change, ok := lastStatusChanges[checks[i].UID]; ok {
				responses[i].LastStatusChange = &LastStatusChangeResponse{
					Time:   change.Time,
					Status: change.Status,
				}
			}
		}
	}

	// Build next cursor
	var nextCursor string
	if hasMore && len(checks) > 0 {
		lastCheck := checks[len(checks)-1]
		nextCursor = s.encodeCursor(lastCheck.CreatedAt, lastCheck.UID)
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

	// If slug was not user-provided, generate a slug based on the checker type and detected slug
	if !userProvidedSlug {
		spec.Slug = string(checker.Type()) + "-" + spec.Slug
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

	// Set config — split secrets out and encrypt them under the org DEK
	// before persisting. Plaintext fallback when no master key.
	if req.Config != nil {
		if encErr := s.applyEncryption(ctx, check, req.Config); encErr != nil {
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

	// Resolve regions for the check
	resolvedRegions, err := s.regions.ResolveRegionsForCheck(ctx, req.Regions, org.UID)
	if err != nil {
		return CheckResponse{}, fmt.Errorf("failed to resolve regions: %w", err)
	}
	check.Regions = resolvedRegions

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

	// Create check in DB
	if err := s.db.CreateCheck(ctx, check); err != nil { //nolint:govet
		return CheckResponse{}, err
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

	// Fetch last status change if requested
	if opts.IncludeLastStatusChange {
		lastStatusChanges, err := s.db.GetLastStatusChangeForChecks(ctx, []string{check.UID})
		if err != nil {
			return CheckResponse{}, fmt.Errorf("failed to get last status change: %w", err)
		}
		if change, ok := lastStatusChanges[check.UID]; ok {
			response.LastStatusChange = &LastStatusChangeResponse{
				Time:   change.Time,
				Status: change.Status,
			}
		}
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
	Enabled       *bool             `json:"enabled"`
	Internal      *bool             `json:"internal,omitempty"`
	Period        *string           `json:"period"`
	Labels        map[string]string `json:"labels"`

	// Wall-clock incident-tracking periods (seconds), per spec
	// 2026-05-08-02. 0 means "open / resolve immediately on first signal".
	ConfirmationPeriodSeconds *int `json:"confirmationPeriodSeconds,omitempty"`
	RecoveryPeriodSeconds     *int `json:"recoveryPeriodSeconds,omitempty"`

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
	if req.Config != nil {
		// PATCH-merge rule: read the existing effective config (decrypting
		// the private side) and apply the spec's preserve-absent-secrets
		// behavior. A naive replace here would silently wipe encrypted
		// secrets the user can't see in their dashboard.
		merged, mergeErr := s.applyConfigPatch(ctx, check, *req.Config)
		if mergeErr != nil {
			return CheckResponse{}, mergeErr
		}
		if encErr := s.applyEncryption(ctx, check, merged); encErr != nil {
			return CheckResponse{}, encErr
		}
		configMap := check.Config
		update.Config = &configMap
		update.ConfigPrivate = check.ConfigPrivate
		update.ConfigPrivateKeys = check.ConfigPrivateKeys
		if check.ConfigPrivate == nil {
			update.ClearConfigPrivate = true
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
	if req.Regions != nil {
		resolvedRegions, regErr := s.regions.ResolveRegionsForCheck(ctx, *req.Regions, org.UID)
		if regErr != nil {
			return CheckResponse{}, fmt.Errorf("failed to resolve regions: %w", regErr)
		}
		update.Regions = &resolvedRegions
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

	// Update check in DB
	if errUpdate := s.db.UpdateCheck(ctx, check.UID, &update); errUpdate != nil {
		return CheckResponse{}, errUpdate
	}

	// Reconcile check jobs if regions, period, enabled, or config changed
	if req.Regions != nil || req.Period != nil || req.Enabled != nil || req.Config != nil {
		updatedCheck, fetchErr := s.db.GetCheck(ctx, org.UID, check.UID)
		if fetchErr != nil {
			return CheckResponse{}, fetchErr
		}

		if reconcileErr := s.reconcileCheckJobs(ctx, updatedCheck); reconcileErr != nil {
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
			Name:          &req.Name,
			Description:   &req.Description,
			CheckGroupUID: req.CheckGroupUID,
			Config:        &req.Config,
			Enabled:       req.Enabled,
			Internal:      req.Internal,
			Period:        req.Period,
			Labels:        &req.Labels,
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
		Name:          req.Name,
		Slug:          slug,
		Description:   req.Description,
		CheckGroupUID: req.CheckGroupUID,
		Type:          req.Type,
		Config:        req.Config,
		Enabled:       req.Enabled,
		Internal:      req.Internal,
		Period:        req.Period,
		Labels:        req.Labels,
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

	// Count active incidents for the event payload
	activeIncidentCount, err := s.db.CountActiveIncidentsByCheckUID(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("failed to check active incidents: %w", err)
	}

	// Resolve any active incidents before deleting
	if activeIncidentCount > 0 {
		incidents, listErr := s.db.ListIncidents(ctx, &models.ListIncidentsFilter{
			OrganizationUID: org.UID,
			CheckUIDs:       []string{check.UID},
			States:          []models.IncidentState{models.IncidentStateActive},
		})
		if listErr != nil {
			return fmt.Errorf("failed to list active incidents: %w", listErr)
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

	// Delete check
	if err := s.db.DeleteCheck(ctx, check.UID); err != nil {
		return fmt.Errorf("failed to delete check: %w", err)
	}

	// Emit check.deleted event
	event := models.NewEvent(org.UID, models.EventTypeCheckDeleted, models.ActorTypeUser)
	event.CheckUID = &check.UID
	event.Payload = models.JSONMap{
		eventPayloadCheckUIDKey:  check.UID,
		"check_slug":             check.Slug,
		"check_name":             check.Name,
		"check_type":             check.Type,
		"active_incidents_count": activeIncidentCount,
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		slog.WarnContext(ctx, "failed to emit check.deleted event", "error", err)
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

// reconcileCheckJobs ensures the check_jobs match the check's current regions and period.
//
//nolint:cyclop,gocognit,nestif,funlen // Reconciliation handles multiple update paths
func (s *Service) reconcileCheckJobs(ctx context.Context, check *models.Check) error {
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

		scheduledAt := scheduling.NextAligned(time.Now(), basePeriod, basePeriod, check.UID, nil, nil)
		job := models.NewCheckJob(check.OrganizationUID, check.UID, check.Period)
		job.Type = check.Type
		job.Config = check.Config
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

	// Create jobs for new regions, update period for existing
	splitPeriod := timeutils.Duration(basePeriod * time.Duration(n))

	for _, region := range targetRegions {
		if existing, ok := existingByRegion[region]; ok {
			// Update period, config, type, and plan weight if changed, or if
			// the region set changed (an add/remove elsewhere shifts this
			// job's own index even though nothing on this row directly
			// changed) — D2.
			needsUpdate := existing.Period != splitPeriod ||
				existing.Type != check.Type ||
				existing.PlanWeight != planWeight ||
				!configEqual(existing.Config, check.Config) ||
				regionSetChanged

			if needsUpdate {
				regionCopy := region
				scheduledAt := scheduling.NextAligned(
					time.Now(), basePeriod, time.Duration(splitPeriod), check.UID, &regionCopy, targetRegions,
				)

				if _, err := s.db.DB().NewUpdate().
					Model((*models.CheckJob)(nil)).
					Set("period = ?", splitPeriod).
					Set("type = ?", check.Type).
					Set("config = ?", check.Config).
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
				time.Now(), basePeriod, time.Duration(splitPeriod), check.UID, &regionCopy, targetRegions,
			)

			job := models.NewCheckJob(check.OrganizationUID, check.UID, splitPeriod)
			job.Type = check.Type
			job.Config = check.Config
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

	var privateKeys []string
	if check.ConfigPrivateKeys != nil {
		_ = json.Unmarshal([]byte(*check.ConfigPrivateKeys), &privateKeys)
	}

	return CheckResponse{
		UID:                       check.UID,
		Name:                      check.Name,
		Slug:                      check.Slug,
		Description:               check.Description,
		CheckGroupUID:             check.CheckGroupUID,
		Type:                      &check.Type,
		Config:                    check.Config,
		ConfigPrivateKeys:         privateKeys,
		Regions:                   check.Regions,
		Enabled:                   &check.Enabled,
		Internal:                  &check.Internal,
		Period:                    &periodStr,
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

// convertResultToLastResultResponse converts a Result model to LastResultResponse.
func (s *Service) convertResultToLastResultResponse(result *models.Result) *LastResultResponse {
	if result == nil {
		return nil
	}

	// Convert status int to string
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
		}
	}

	return &LastResultResponse{
		UID:        result.UID,
		Status:     statusStr,
		Timestamp:  result.PeriodStart,
		DurationMs: result.Duration,
		Output:     result.Output,
		Metrics:    result.Metrics,
	}
}

// emitEvent creates an event for the check lifecycle.
func (s *Service) emitEvent(
	ctx context.Context,
	orgUID string,
	eventType models.EventType,
	check *models.Check,
) error {
	event := models.NewEvent(orgUID, eventType, models.ActorTypeUser)
	event.CheckUID = &check.UID
	event.Payload = models.JSONMap{
		eventPayloadCheckUIDKey: check.UID,
		"check_slug":            check.Slug,
		"check_name":            check.Name,
		"check_type":            check.Type,
	}

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

// ExportDocument represents the top-level JSON export format.
type ExportDocument struct {
	Version      int           `json:"version"`
	ExportedAt   string        `json:"exportedAt"`
	Organization string        `json:"organization"`
	Checks       []ExportCheck `json:"checks"`
	// SecretsStripped is true to flag that secret-bearing keys (passwords,
	// tokens, private keys, ...) were removed from every check's Config.
	// Importers must accept the stripped shape and the operator will
	// re-enter secrets after import. Underscore prefix is intentional —
	// it marks the field as a meta hint distinct from check-config keys.
	SecretsStripped bool `json:"_secretsStripped"` //nolint:tagliatelle // intentional underscore-prefixed meta field
}

// ExportCheck represents a single check in the export format.
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
	ConfirmationPeriodSeconds int                  `json:"confirmationPeriodSeconds,omitempty"`
	EscalationThreshold       int                  `json:"escalationThreshold,omitempty"`
	RecoveryPeriodSeconds     int                  `json:"recoveryPeriodSeconds,omitempty"`
	ReopenCooldownMultiplier  *int                 `json:"reopenCooldownMultiplier,omitempty"`
	FlappingWindowSeconds     int                  `json:"flappingWindowSeconds,omitempty"`
	FlapBackoffFactor         int                  `json:"flapBackoffFactor,omitempty"`
	MaxRecoveryMultiplier     int                  `json:"maxRecoveryMultiplier,omitempty"`
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
			ConfirmationPeriodSeconds: check.ConfirmationPeriodSeconds,
			EscalationThreshold:       check.EscalationThreshold,
			RecoveryPeriodSeconds:     check.RecoveryPeriodSeconds,
			ReopenCooldownMultiplier:  check.ReopenCooldownMultiplier,
			FlappingWindowSeconds:     check.FlappingWindowSeconds,
			FlapBackoffFactor:         check.FlapBackoffFactor,
			MaxRecoveryMultiplier:     check.MaxRecoveryMultiplier,
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

	return &ExportDocument{
		Version:         1,
		ExportedAt:      time.Now().UTC().Format(time.RFC3339),
		Organization:    orgSlug,
		Checks:          exportChecks,
		SecretsStripped: true,
	}, nil
}

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

// ImportChecks imports checks from an export document.
func (s *Service) ImportChecks(
	ctx context.Context, orgSlug string, doc *ExportDocument, dryRun bool,
) (*ImportResult, error) {
	if doc.Version != 1 {
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

	// Build upsert request
	upsertReq := UpsertCheckRequest{
		Name:          exportedCheck.Name,
		Description:   exportedCheck.Description,
		CheckGroupUID: checkGroupUID,
		Type:          exportedCheck.Type,
		Config:        exportedCheck.Config,
		Enabled:       &exportedCheck.Enabled,
		Internal:      &exportedCheck.Internal,
		Labels:        exportedCheck.Labels,
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

	finalSlug, err := s.cloneResolveSlug(ctx, org.UID, source, req)
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

	if createErr := s.db.CreateCheck(ctx, clone); createErr != nil {
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

func (s *Service) cloneResolveSlug(
	ctx context.Context, orgUID string, source *models.Check, req *CloneCheckRequest,
) (string, error) {
	sourceSlug := ""
	if source.Slug != nil {
		sourceSlug = *source.Slug
	}

	var (
		targetSlug       string
		userProvidedSlug bool
	)

	if req != nil && req.Slug != nil && *req.Slug != "" {
		if err := validateSlug(*req.Slug); err != nil {
			return "", err
		}

		targetSlug = *req.Slug
		userProvidedSlug = true
	} else {
		base := sourceSlug
		if base == "" {
			base = source.Type
		}

		targetSlug = base + "-copy"
	}

	return s.ensureUniqueSlug(ctx, orgUID, targetSlug, userProvidedSlug)
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

// applyEncryption splits the effective config into public/private using the
// checker's declared SecretFields, encrypts the private side under the org
// DEK, and writes the resulting columns onto the check.
//
// When encryption is disabled at the server (no master key) the full config
// stays plaintext on `Config` and `ConfigPrivate*` are NULL. That fallback
// is intentional — the spec calls it out — and is logged once at startup.
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

	if !s.creds.Enabled() || len(private) == 0 {
		check.ConfigPrivate = nil
		check.ConfigPrivateKeys = nil
		// Plaintext fallback: when no master key is configured the secrets
		// must still be persisted so the check actually works. Put them
		// back on Config and document the gap.
		if !s.creds.Enabled() {
			for k, v := range private {
				check.Config[k] = v
			}
		}
		return nil
	}

	envelope, err := s.creds.EncryptForOrg(ctx, check.OrganizationUID, private)
	if err != nil {
		return fmt.Errorf("encrypt check config: %w", err)
	}

	check.ConfigPrivate = &envelope
	keysJSON, err := json.Marshal(sortedKeys(private))
	if err != nil {
		return fmt.Errorf("marshal config private keys: %w", err)
	}
	keysStr := string(keysJSON)
	check.ConfigPrivateKeys = &keysStr

	return nil
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

	if !s.creds.Enabled() {
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
