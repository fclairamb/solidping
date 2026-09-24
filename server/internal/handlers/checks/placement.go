package checks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// Automatic region placement (spec 2026-09-25-06, Part A).
//
// A check records its placement intent: `pinned` (regions is the user's
// explicit list and never moves) or `auto` (regions is the CURRENT placement,
// chosen here and re-placed by the region sweep when a placed region goes
// dark). Every write path — create, PATCH, PUT-by-slug, the import dry run and
// the config-as-code diff — resolves placement through this file, so the API,
// the MCP tools and config-as-code cannot disagree.

// Field names of the placement request fields.
const (
	fieldPlacement   = "placement"
	fieldRegionCount = "regionCount"
	fieldRegionPool  = "regionPool"
)

// Machine codes of the placement findings.
const (
	// CodeInvalidPlacement covers every placement request rejection.
	CodeInvalidPlacement = "INVALID_PLACEMENT"
	// CodePlacementRegionCountReduced is the advisory warning attached to a
	// write whose automatic placement runs from fewer regions than asked:
	// fewer eligible regions exist, or the org's checks-per-minute limit only
	// fits fewer runs per period.
	CodePlacementRegionCountReduced = "PLACEMENT_REGION_COUNT_REDUCED"
)

// Placement request errors. Every message leads with the field it is about.
var (
	errInvalidPlacement = errors.New(
		"placement must be \"pinned\" (run exactly from regions) or \"auto\" (the scheduler places the check)")
	errRegionCountOutOfRange = fmt.Errorf("regionCount must be between 1 and %d", regions.MaxAutoRegionCount)
	errRegionPoolPrivate     = errors.New(
		"regionPool may only name cloud regions: private (@) regions are pinned-only — list them in regions " +
			"with placement \"pinned\"")
	errRegionsWithAutoPlacement = errors.New(
		"regions cannot be combined with automatic placement: under placement \"auto\" the scheduler chooses " +
			"the regions (restrict the candidates with regionPool), and an explicit regions list means " +
			"placement \"pinned\"")
	errAutoFieldsOnPinned = errors.New(
		"regionCount and regionPool only apply to placement \"auto\"")
	errRegionPoolUnknown = errors.New("regionPool names a region that is not declared")
	errNoEligibleRegion  = errors.New(
		"regions: no cloud region can run this check automatically (check the regionPool and the regions' " +
			"capabilities), so it cannot be placed")
)

// isPlacementError reports whether err is a placement request rejection — a
// caller mistake, rendered as a 400 VALIDATION_ERROR.
func isPlacementError(err error) bool {
	return errors.Is(err, errInvalidPlacement) ||
		errors.Is(err, errRegionCountOutOfRange) ||
		errors.Is(err, errRegionPoolPrivate) ||
		errors.Is(err, errRegionsWithAutoPlacement) ||
		errors.Is(err, errAutoFieldsOnPinned) ||
		errors.Is(err, errRegionPoolUnknown) ||
		errors.Is(err, errNoEligibleRegion)
}

// placementFieldOf names the request field a placement error is about.
func placementFieldOf(err error) string {
	switch {
	case errors.Is(err, errRegionCountOutOfRange):
		return fieldRegionCount
	case errors.Is(err, errRegionPoolPrivate), errors.Is(err, errRegionPoolUnknown):
		return fieldRegionPool
	case errors.Is(err, errRegionsWithAutoPlacement), errors.Is(err, errNoEligibleRegion):
		return fieldRegions
	default:
		return fieldPlacement
	}
}

// placementRequest is the placement half of a write request, as sent.
type placementRequest struct {
	Placement *string
	// Regions is the explicit region list; RegionsSet reports whether the
	// request named `regions` at all (a PATCH `regions: []` is a request).
	Regions     []string
	RegionsSet  bool
	RegionCount *int
	RegionPool  *[]string
}

// placementOutcome is what the check will store.
type placementOutcome struct {
	placement   string
	regions     []string
	regionCount *int
	regionPool  []string
	warnings    []base.ValidationErrorField
}

// applyTo writes the outcome onto an in-memory check.
func (o *placementOutcome) applyTo(check *models.Check) {
	check.Placement = o.placement
	check.Regions = o.regions
	check.RegionCount = o.regionCount
	check.RegionPool = o.regionPool
}

// applyToUpdate writes the outcome onto a column update.
func (o *placementOutcome) applyToUpdate(update *models.CheckUpdate) {
	regionList := o.regions
	placement := o.placement
	update.Regions = &regionList
	update.Placement = &placement

	if o.regionCount == nil {
		update.ClearRegionCount = true
	} else {
		update.RegionCount = o.regionCount
	}

	if len(o.regionPool) == 0 {
		update.ClearRegionPool = true
	} else {
		pool := o.regionPool
		update.RegionPool = &pool
	}
}

// placementRequestFindings is the placement half of requestFieldFindings:
// every rule decidable from the request alone.
func placementRequestFindings(req *placementRequest) []requestFieldFinding {
	var findings []requestFieldFinding

	add := func(err error) {
		findings = append(findings, requestFieldFinding{
			Name: placementFieldOf(err), Code: CodeInvalidPlacement, Message: err.Error(), Err: err,
		})
	}

	if req.Placement != nil && *req.Placement != models.PlacementPinned && *req.Placement != models.PlacementAuto {
		add(errInvalidPlacement)
	}

	if req.RegionCount != nil && (*req.RegionCount < 1 || *req.RegionCount > regions.MaxAutoRegionCount) {
		add(errRegionCountOutOfRange)
	}

	if req.RegionPool != nil && slices.ContainsFunc(*req.RegionPool, regions.IsPrivateRegion) {
		add(errRegionPoolPrivate)
	}

	autoFields := req.RegionCount != nil || (req.RegionPool != nil && len(*req.RegionPool) > 0)

	switch {
	case req.Placement != nil && *req.Placement == models.PlacementPinned && autoFields:
		add(errAutoFieldsOnPinned)
	case len(req.Regions) > 0 && (autoFields || (req.Placement != nil && *req.Placement == models.PlacementAuto)):
		add(errRegionsWithAutoPlacement)
	}

	return findings
}

// firstPlacementError returns the first static placement rejection, nil when
// the request is well formed.
func firstPlacementError(req *placementRequest) error {
	if findings := placementRequestFindings(req); len(findings) > 0 {
		return findings[0].Err
	}

	return nil
}

// requiredCapabilities is what a region must be able to do to run the check:
// headless Chrome for a browser check, the pinned address family for an
// `ipVersion: ipv4|ipv6` target.
func requiredCapabilities(checkType string, config map[string]any) []string {
	var required []string

	if checkerdef.CheckType(checkType) == checkerdef.CheckTypeBrowser {
		required = append(required, regions.CapabilityBrowser)
	}

	switch version, err := checkerdef.IPVersionFromConfig(config); {
	case err != nil:
	case version == checkerdef.IPVersionIPv6:
		required = append(required, regions.CapabilityIPv6)
	case version == checkerdef.IPVersionIPv4:
		required = append(required, regions.CapabilityIPv4)
	}

	return required
}

// placementEnv is the org-level input of a placement: the candidate order,
// the capability index and the healthy set.
type placementEnv struct {
	candidates   []string
	capabilities map[string]regions.RegionDefinition
	healthy      map[string]bool
}

// loadPlacementEnv reads everything a placement needs for one org.
//
// Healthy means: at least one live worker serves the region (the scheduler's
// own prefix rule, as region health counts it) and the region sweep holds no
// outage marker for it (dark or stalled).
func (s *Service) loadPlacementEnv(ctx context.Context, orgUID string) (*placementEnv, error) {
	orgDefaults, err := s.regions.GetOrgDefaultRegions(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	systemDefaults, err := s.regions.SystemDefaultRegions(ctx)
	if err != nil {
		return nil, err
	}

	declared, err := s.regions.GetGlobalRegions(ctx)
	if err != nil {
		return nil, err
	}

	index, err := s.regions.CapabilityIndex(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	candidates := regions.CandidateOrder(orgDefaults, systemDefaults, declared)

	healthy, err := s.cloudRegionHealth(ctx, candidates)
	if err != nil {
		return nil, err
	}

	return &placementEnv{candidates: candidates, capabilities: index, healthy: healthy}, nil
}

// cloudRegionHealth is the healthy subset of the given cloud regions.
func (s *Service) cloudRegionHealth(ctx context.Context, slugs []string) (map[string]bool, error) {
	now := s.now()

	workers, err := s.db.ListLiveWorkers(ctx, regions.LivenessCutoff(now))
	if err != nil {
		return nil, fmt.Errorf("list live workers: %w", err)
	}

	markers, err := regionoutage.List(ctx, s.db)
	if err != nil {
		return nil, err
	}

	cutoff := regions.LivenessCutoff(now)
	healthy := make(map[string]bool, len(slugs))

	for _, slug := range slugs {
		if markers[slug] != nil {
			continue
		}

		if live, _ := workerCoverageForSlug(workers, slug, cutoff); live > 0 {
			healthy[slug] = true
		}
	}

	return healthy, nil
}

// defaultPlacement is the placement a request that names none gets: auto,
// unless the org's own default_regions names a private location — an org that
// chose to run everything from its own agent keeps that choice.
func (s *Service) defaultPlacement(ctx context.Context, orgUID string) (string, error) {
	orgDefaults, err := s.regions.GetOrgDefaultRegions(ctx, orgUID)
	if err != nil {
		return "", err
	}

	if slices.ContainsFunc(orgDefaults, regions.IsPrivateRegion) {
		return models.PlacementPinned, nil
	}

	return models.PlacementAuto, nil
}

// placementSubject is the check a placement is computed for.
type placementSubject struct {
	orgUID string
	// excludeUID is the stored check being edited (empty on create), so the
	// rate projection replaces its row instead of adding a second one.
	excludeUID string
	checkType  string
	config     map[string]any
	period     time.Duration
	enabled    bool
	// current is the check's current cloud placement (nil on create).
	current []string
}

// resolveCreatePlacement decides the placement of a new check.
func (s *Service) resolveCreatePlacement(
	ctx context.Context, subject *placementSubject, req *placementRequest,
) (*placementOutcome, error) {
	if checkerdef.CheckType(subject.checkType).IsPassive() {
		return &placementOutcome{placement: models.PlacementPinned, regions: []string{}}, nil
	}

	if err := firstPlacementError(req); err != nil {
		return nil, err
	}

	intent, err := s.requestedIntent(ctx, subject.orgUID, req)
	if err != nil {
		return nil, err
	}

	if intent == models.PlacementPinned {
		resolved, resolveErr := s.regions.ResolveRegionsForCheck(ctx, req.Regions, subject.orgUID)
		if resolveErr != nil {
			return nil, resolveErr
		}

		return &placementOutcome{placement: models.PlacementPinned, regions: resolved}, nil
	}

	count := regions.DefaultAutoRegionCount
	if req.RegionCount != nil {
		count = *req.RegionCount
	}

	return s.autoPlace(ctx, subject, count, req.RegionCount != nil, poolOf(req.RegionPool))
}

// requestedIntent is the placement a request asks for, explicitly or not.
func (s *Service) requestedIntent(ctx context.Context, orgUID string, req *placementRequest) (string, error) {
	switch {
	case req.Placement != nil:
		return *req.Placement, nil
	case len(req.Regions) > 0:
		// An explicit region list is the historical meaning of "pinned".
		return models.PlacementPinned, nil
	case req.RegionCount != nil || req.RegionPool != nil:
		return models.PlacementAuto, nil
	default:
		return s.defaultPlacement(ctx, orgUID)
	}
}

// resolveUpdatePlacement decides the placement a PATCH leaves the stored
// check with. A nil outcome means the placement does not change.
//
//   - `placement` set: that intent. Switching to pinned freezes the current
//     placement unless the request also names regions.
//   - `regions` set, non-empty: pinned to those regions.
//   - `regions: []`: back to the default (automatic, unless the org's own
//     defaults name a private location).
//   - `regionCount` / `regionPool` set: automatic.
//   - an auto check whose config changed (a new ipVersion or tunnel may change
//     which regions are eligible) or that is re-enabled: re-evaluated, which
//     keeps every current region that is still eligible and healthy.
func (s *Service) resolveUpdatePlacement(
	ctx context.Context, check *models.Check, subject *placementSubject, req *placementRequest, reevaluate bool,
) (*placementOutcome, error) {
	if check.IsPassive() {
		if !req.RegionsSet {
			return nil, nil //nolint:nilnil // nil outcome = no placement change
		}

		return &placementOutcome{placement: models.PlacementPinned, regions: []string{}}, nil
	}

	if err := firstPlacementError(req); err != nil {
		return nil, err
	}

	reset := req.RegionsSet && len(req.Regions) == 0 && req.Placement == nil &&
		req.RegionCount == nil && req.RegionPool == nil

	var intent string

	switch {
	case req.Placement != nil || req.RegionsSet || req.RegionCount != nil || req.RegionPool != nil:
		var err error

		intent, err = s.requestedIntent(ctx, check.OrganizationUID, req)
		if err != nil {
			return nil, err
		}
	case check.IsAutoPlaced() && reevaluate:
		intent = models.PlacementAuto
	default:
		return nil, nil //nolint:nilnil // nil outcome = no placement change
	}

	if intent == models.PlacementPinned {
		regionList := check.Regions

		if req.RegionsSet {
			resolved, err := s.regions.ResolveRegionsForCheck(ctx, req.Regions, check.OrganizationUID)
			if err != nil {
				return nil, err
			}

			regionList = resolved
		}

		return &placementOutcome{placement: models.PlacementPinned, regions: regionList}, nil
	}

	pool := poolOf(req.RegionPool)
	if req.RegionPool == nil && check.IsAutoPlaced() {
		pool = check.RegionPool
	}

	count, explicit := s.updateRegionCount(check, subject, req, reset)

	return s.autoPlace(ctx, subject, count, explicit, pool)
}

// updateRegionCount is N for a PATCH that leaves the check automatic: the
// request's value; else the check's own when it already is automatic; else
// (switching) its current cloud region count, so a switch keeps the cost
// unchanged — except for a reset to the defaults, which gets the default N.
func (s *Service) updateRegionCount(
	check *models.Check, subject *placementSubject, req *placementRequest, reset bool,
) (int, bool) {
	switch {
	case req.RegionCount != nil:
		return *req.RegionCount, true
	case check.IsAutoPlaced() && check.RegionCount != nil && *check.RegionCount > 0:
		return *check.RegionCount, false
	case !reset && len(subject.current) > 0:
		return len(subject.current), false
	default:
		return regions.DefaultAutoRegionCount, false
	}
}

// poolOf dereferences an optional pool; empty means any.
func poolOf(pool *[]string) []string {
	if pool == nil || len(*pool) == 0 {
		return nil
	}

	return slices.Clone(*pool)
}

// cloudRegionsOf keeps the cloud regions of a region list.
func cloudRegionsOf(regionList []string) []string {
	out := make([]string, 0, len(regionList))

	for _, slug := range regionList {
		if !regions.IsPrivateRegion(slug) {
			out = append(out, slug)
		}
	}

	return out
}

// autoPlace computes an automatic placement: N capped by the eligible regions
// and by the org's checks-per-minute limit, then regions.Place.
func (s *Service) autoPlace(
	ctx context.Context, subject *placementSubject, requested int, explicit bool, pool []string,
) (*placementOutcome, error) {
	env, err := s.loadPlacementEnv(ctx, subject.orgUID)
	if err != nil {
		return nil, err
	}

	for _, slug := range pool {
		if !slices.Contains(env.candidates, slug) {
			return nil, fmt.Errorf("%w: %q (declared: %s)", errRegionPoolUnknown, slug, strings.Join(env.candidates, ", "))
		}
	}

	input := &regions.PlacementInput{
		Candidates:   env.candidates,
		Pool:         pool,
		Required:     requiredCapabilities(subject.checkType, subject.config),
		Capabilities: env.capabilities,
		Healthy:      env.healthy,
		Current:      subject.current,
	}

	eligible := regions.Eligible(input)
	if len(eligible) == 0 {
		return nil, errNoEligibleRegion
	}

	var warnings []base.ValidationErrorField

	count := min(requested, len(eligible))
	if explicit && count < requested {
		warnings = append(warnings, base.ValidationErrorField{
			Name: fieldRegionCount, Severity: base.SeverityWarning, Code: CodePlacementRegionCountReduced,
			Message: fmt.Sprintf("only %d region(s) can run this check (%s), so it runs from %d instead of %d",
				len(eligible), strings.Join(eligible, ", "), count, requested),
		})
	}

	// Never reduce below what the check already runs from: an unrelated edit
	// on an org that is already over its limit must not silently halve a
	// check's coverage. Only a growth (or a new check) is held to the limit.
	floor := min(count, max(1, len(subject.current)))

	capped, limit := s.capRegionCountForRate(ctx, subject, count, floor)
	if capped < count {
		warnings = append(warnings, base.ValidationErrorField{
			Name: fieldRegionCount, Severity: base.SeverityWarning, Code: CodePlacementRegionCountReduced,
			Message: fmt.Sprintf("the organization's limit of %d checks/minute only fits %d region(s) for "+
				"this check at its period, so it runs from %d instead of %d", limit, capped, capped, count),
		})
		count = capped
	}

	input.Count = count
	placed := regions.Place(input)
	placedCount := len(placed)

	return &placementOutcome{
		placement:   models.PlacementAuto,
		regions:     placed,
		regionCount: &placedCount,
		regionPool:  pool,
		warnings:    warnings,
	}, nil
}

// capRegionCountForRate returns the largest N in [floor, count] whose
// projected demand fits the org's MaxChecksPerMinute, and the limit. Without
// a limit (or entitlements) it returns count unchanged. When even floor does
// not fit, floor is returned: the existing over-limit warning covers that
// case, and a check always runs from at least one region.
func (s *Service) capRegionCountForRate(
	ctx context.Context, subject *placementSubject, count, floor int,
) (int, int) {
	if s.entitlements == nil || subject.period <= 0 || !subject.enabled {
		return count, 0
	}

	for n := count; n >= floor; n-- {
		projected, err := s.entitlements.ProjectChecksPerMinute(ctx, subject.orgUID, entcore.CheckRateProposal{
			ExcludeCheckUID: subject.excludeUID,
			Type:            subject.checkType,
			Period:          subject.period,
			Regions:         make([]string, n),
			Enabled:         true,
		})
		if err != nil || projected.Limit == nil {
			return count, 0
		}

		if !projected.Over() {
			return n, *projected.Limit
		}

		if n == floor {
			return floor, *projected.Limit
		}
	}

	return floor, 0
}

// autoOnlyCount is the regionCount an API response carries: an automatic
// check's N, nothing for a pinned one.
func autoOnlyCount(check *models.Check) *int {
	if !check.IsAutoPlaced() {
		return nil
	}

	return check.RegionCount
}

// autoOnlyPool is the regionPool an API response carries.
func autoOnlyPool(check *models.Check) []string {
	if !check.IsAutoPlaced() || len(check.RegionPool) == 0 {
		return nil
	}

	return check.RegionPool
}

// createPlacementRequest is the placement half of a create request.
func createPlacementRequest(req *CreateCheckRequest) *placementRequest {
	out := &placementRequest{
		Placement:   req.Placement,
		Regions:     req.Regions,
		RegionsSet:  len(req.Regions) > 0,
		RegionCount: req.RegionCount,
	}

	if req.RegionPool != nil {
		pool := req.RegionPool
		out.RegionPool = &pool
	}

	return out
}

// updatePlacementRequest is the placement half of a PATCH.
func updatePlacementRequest(req *UpdateCheckRequest) *placementRequest {
	out := &placementRequest{
		Placement:   req.Placement,
		RegionCount: req.RegionCount,
		RegionPool:  req.RegionPool,
	}

	if req.Regions != nil {
		out.Regions = *req.Regions
		out.RegionsSet = true
	}

	return out
}

// updatePlacementSubject describes the check as this PATCH will leave it,
// for the capability filter and the rate projection.
func (s *Service) updatePlacementSubject(check *models.Check, req *UpdateCheckRequest) *placementSubject {
	subject := &placementSubject{
		orgUID:     check.OrganizationUID,
		excludeUID: check.UID,
		checkType:  check.Type,
		config:     check.Config,
		period:     time.Duration(check.Period),
		enabled:    check.Enabled,
		current:    cloudRegionsOf(check.Regions),
	}

	if req.Config != nil {
		// PATCH replaces the config wholesale (secrets aside, which never carry
		// a capability requirement), so the incoming one is what counts.
		subject.config = *req.Config
	}

	if req.Enabled != nil {
		subject.enabled = *req.Enabled
	}

	if req.Period != nil {
		var period timeutils.Duration
		if err := period.Scan(*req.Period); err == nil {
			subject.period = time.Duration(period)
		}
	}

	return subject
}

// applyUpsertPlacement maps a PUT-by-slug document's placement onto the
// PATCH it becomes. A document is declarative: `placement: auto` with no
// regionPool means any region, not "keep the stored pool".
func applyUpsertPlacement(updateReq *UpdateCheckRequest, req *UpsertCheckRequest) {
	updateReq.Placement = req.Placement
	updateReq.RegionCount = req.RegionCount
	updateReq.RegionPool = req.RegionPool

	if req.Placement != nil && *req.Placement == models.PlacementAuto && req.RegionPool == nil {
		empty := []string{}
		updateReq.RegionPool = &empty
	}
}

// validatePlacementRequest is the placement half of a validate request.
func validatePlacementRequest(req *ValidateCheckRequest) *placementRequest {
	out := &placementRequest{
		Placement:   req.Placement,
		Regions:     req.Regions,
		RegionsSet:  len(req.Regions) > 0,
		RegionCount: req.RegionCount,
	}

	if req.RegionPool != nil {
		pool := req.RegionPool
		out.RegionPool = &pool
	}

	return out
}

// validatePlacementFindings resolves the placement the write path would store
// for a validate request, reporting what only the org's regions can tell (an
// unknown pool slug, a pool nothing can serve: blocking) and the reduced-N
// advisories. It returns the region set to reason about for the capability and
// rate warnings: the placement when it resolves, the raw request otherwise.
func (s *Service) validatePlacementFindings(
	ctx context.Context, orgUID string, req *ValidateCheckRequest, config map[string]any,
	period time.Duration, findings *validateFindings,
) []string {
	placementReq := validatePlacementRequest(req)
	if firstPlacementError(placementReq) != nil {
		// Already reported by validateRequestFieldFindings.
		return req.Regions
	}

	if period <= 0 {
		period = defaultPeriodForType(req.Type)
	}

	subject := &placementSubject{
		orgUID:     orgUID,
		excludeUID: req.ExcludeCheckUID,
		checkType:  req.Type,
		config:     config,
		period:     period,
		enabled:    req.Enabled == nil || *req.Enabled,
	}

	if req.ExcludeCheckUID != "" {
		if stored, err := s.db.GetCheck(ctx, orgUID, req.ExcludeCheckUID); err == nil && stored != nil {
			subject.current = cloudRegionsOf(stored.Regions)
		}
	}

	outcome, err := s.resolveCreatePlacement(ctx, subject, placementReq)
	if err != nil {
		if isPlacementError(err) {
			findings.addError(placementFieldOf(err), CodeInvalidPlacement, err.Error())
		}

		return req.Regions
	}

	findings.warnings = append(findings.warnings, outcome.warnings...)

	return outcome.regions
}
