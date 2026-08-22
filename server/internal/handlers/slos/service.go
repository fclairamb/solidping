// Package slos provides HTTP handlers for service-level-objective management,
// plus the read-time status/history computations built on internal/slo.
package slos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/availability"
	"github.com/fclairamb/solidping/server/internal/slo"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// Service errors.
var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrSLONotFound is returned when an SLO is not found.
	ErrSLONotFound = errors.New("slo not found")
	// ErrNameRequired is returned when the name is missing.
	ErrNameRequired = errors.New("name is required")
	// ErrInvalidSlug is returned when the slug is not URL-safe.
	ErrInvalidSlug = errors.New("slug must be 2-64 lowercase alphanumeric characters or hyphens")
	// ErrSlugTaken is returned when another SLO in the org already owns the slug.
	ErrSlugTaken = errors.New("slug already in use")
	// ErrScopeRequired is returned when neither or both of the scope fields are set.
	ErrScopeRequired = errors.New("exactly one of checkUid or checkGroupUid is required")
	// ErrInvalidTarget is returned when the target is outside (0, 100].
	ErrInvalidTarget = errors.New("targetPct must be greater than 0 and at most 100")
	// ErrInvalidTimezone is returned when the timezone is not a known IANA zone.
	ErrInvalidTimezone = errors.New("timezone must be a valid IANA zone")
	// ErrScopeNotFound is returned when the referenced check or group does not exist.
	ErrScopeNotFound = errors.New("referenced check or check group not found")
)

// slugPattern is the shared URL-safe slug shape used across the product.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// maxHistoryMonths bounds the history endpoint. Month rollups are permanent, so
// the only reason for a cap is response size and query cost.
const maxHistoryMonths = 60

// defaultHistoryMonths is what the history endpoint returns without ?months=.
const defaultHistoryMonths = 12

// defaultTargetPct is the objective a create request gets when it names none.
const defaultTargetPct = 99.9

// incidentFetchLimit mirrors the availability API's per-window incident cap.
const incidentFetchLimit = 1000

// Service provides business logic for SLO management and evaluation.
type Service struct {
	db           db.Service
	cfg          *config.Config
	entitlements *entitlements.Service
}

// NewService creates a new SLO service. entitlementsSvc may be nil (tests,
// deployments without quotas).
func NewService(dbService db.Service, cfg *config.Config, entitlementsSvc *entitlements.Service) *Service {
	return &Service{db: dbService, cfg: cfg, entitlements: entitlementsSvc}
}

// Response is an SLO as returned by the API.
type Response struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// Exactly one of CheckUID / CheckGroupUID is set.
	CheckUID           *string `json:"checkUid,omitempty"`
	CheckGroupUID      *string `json:"checkGroupUid,omitempty"`
	CheckName          *string `json:"checkName,omitempty"`
	CheckGroupName     *string `json:"checkGroupName,omitempty"`
	TargetPct          float64 `json:"targetPct"`
	Timezone           string  `json:"timezone"`
	ExcludeMaintenance bool    `json:"excludeMaintenance"`
	Enabled            bool    `json:"enabled"`
	// Burning is true while at least one burn-rate alert policy on this SLO
	// has an open incident. It is what the list's "Burning" badge renders —
	// derived from the incident rows rather than stored, so it cannot go stale
	// when somebody resolves the incident by hand (spec 2026-08-21-08).
	Burning   bool      `json:"burning"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateRequest is the POST body.
type CreateRequest struct {
	Name               string   `json:"name"`
	Slug               string   `json:"slug"`
	CheckUID           *string  `json:"checkUid"`
	CheckGroupUID      *string  `json:"checkGroupUid"`
	TargetPct          *float64 `json:"targetPct"`
	Timezone           string   `json:"timezone"`
	ExcludeMaintenance *bool    `json:"excludeMaintenance"`
	Enabled            *bool    `json:"enabled"`
}

// UpdateRequest is the PATCH body. Nil means "leave alone".
type UpdateRequest struct {
	Name               *string  `json:"name,omitempty"`
	Slug               *string  `json:"slug,omitempty"`
	CheckUID           *string  `json:"checkUid,omitempty"`
	CheckGroupUID      *string  `json:"checkGroupUid,omitempty"`
	TargetPct          *float64 `json:"targetPct,omitempty"`
	Timezone           *string  `json:"timezone,omitempty"`
	ExcludeMaintenance *bool    `json:"excludeMaintenance,omitempty"`
	Enabled            *bool    `json:"enabled,omitempty"`
}

// WindowResponse describes the calendar window a status/history row covers.
type WindowResponse struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Label string    `json:"label"`
}

// StatusRow is one evaluated window.
type StatusRow struct {
	Window WindowResponse `json:"window"`
	// AttainmentPct is null when the window carries no countable probe. It is
	// NEVER 100% by default — the same no-data rule the availability API
	// follows.
	AttainmentPct              *float64   `json:"attainmentPct"`
	HasData                    bool       `json:"hasData"`
	TargetPct                  float64    `json:"targetPct"`
	TotalChecks                int        `json:"totalChecks"`
	SuccessfulChecks           int        `json:"successfulChecks"`
	MonitoredSeconds           int64      `json:"monitoredSeconds"`
	ElapsedSeconds             int64      `json:"elapsedSeconds"`
	BudgetTotalSeconds         int64      `json:"budgetTotalSeconds"`
	BudgetConsumedSeconds      int64      `json:"budgetConsumedSeconds"`
	BudgetRemainingSeconds     int64      `json:"budgetRemainingSeconds"`
	ExcludedMaintenanceSeconds int64      `json:"excludedMaintenanceSeconds"`
	BurnRate                   *float64   `json:"burnRate"`
	ProjectedExhaustionAt      *time.Time `json:"projectedExhaustionAt"`
	State                      string     `json:"state"`
	Partial                    bool       `json:"partial"`
}

// StatusResponse is the /status payload: the current window plus the incident
// wall-clock context block reused from the availability API.
type StatusResponse struct {
	SLO       Response                     `json:"slo"`
	Current   StatusRow                    `json:"current"`
	Incidents availability.PeriodIncidents `json:"incidents"`
}

// HistoryResponse wraps past monthly windows per repo convention.
type HistoryResponse struct {
	Data []StatusRow `json:"data"`
}

// ListSLOs returns the org's SLOs.
func (s *Service) ListSLOs(ctx context.Context, orgSlug, checkUID string, limit int) ([]Response, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	rows, err := s.db.ListSLOs(ctx, org.UID, models.ListSLOsFilter{CheckUID: checkUID, Limit: limit})
	if err != nil {
		return nil, err
	}

	return s.decorate(ctx, org.UID, rows), nil
}

// GetSLO returns a single SLO by UID or slug.
func (s *Service) GetSLO(ctx context.Context, orgSlug, ident string) (Response, error) {
	org, row, err := s.resolve(ctx, orgSlug, ident)
	if err != nil {
		return Response{}, err
	}

	return s.decorate(ctx, org.UID, []*models.SLO{row})[0], nil
}

// CreateSLO validates and stores a new SLO.
func (s *Service) CreateSLO(ctx context.Context, orgSlug string, req *CreateRequest) (Response, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return Response{}, ErrOrganizationNotFound
	}

	if s.entitlements != nil {
		if quotaErr := s.entitlements.SloCreateAllowed(ctx, org.UID); quotaErr != nil {
			return Response{}, quotaErr
		}
	}

	slug, target, timezone, err := normalizeCreateRequest(req)
	if err != nil {
		return Response{}, err
	}

	if scopeErr := s.validateScope(ctx, org.UID, req.CheckUID, req.CheckGroupUID); scopeErr != nil {
		return Response{}, scopeErr
	}

	if takenErr := s.assertSlugFree(ctx, org.UID, slug); takenErr != nil {
		return Response{}, takenErr
	}

	row := models.NewSLO(org.UID, req.Name, slug, target)
	row.CheckUID = req.CheckUID
	row.CheckGroupUID = req.CheckGroupUID
	row.Timezone = timezone

	if req.ExcludeMaintenance != nil {
		row.ExcludeMaintenance = *req.ExcludeMaintenance
	}

	if req.Enabled != nil {
		row.Enabled = *req.Enabled
	}

	if err := s.db.CreateSLO(ctx, row); err != nil {
		return Response{}, err
	}

	// Both built-in burn policies exist from the first second, disabled. An
	// operator toggling one on must never have to wonder whether the row is
	// there yet.
	if _, err := s.EnsureAlertPolicies(ctx, org.UID, row.UID); err != nil {
		return Response{}, err
	}

	return s.decorate(ctx, org.UID, []*models.SLO{row})[0], nil
}

// UpdateSLO applies a partial update.
//
//nolint:cyclop // a flat validation list; splitting it only hides the rules.
func (s *Service) UpdateSLO(ctx context.Context, orgSlug, ident string, req UpdateRequest) (Response, error) {
	org, row, err := s.resolve(ctx, orgSlug, ident)
	if err != nil {
		return Response{}, err
	}

	if req.Name != nil && *req.Name == "" {
		return Response{}, ErrNameRequired
	}

	if req.Slug != nil {
		if !slugPattern.MatchString(*req.Slug) {
			return Response{}, ErrInvalidSlug
		}

		if existing, lookupErr := s.db.GetSLOBySlug(ctx, org.UID, *req.Slug); lookupErr == nil &&
			existing != nil && existing.UID != row.UID {
			return Response{}, ErrSlugTaken
		}
	}

	if req.TargetPct != nil && (*req.TargetPct <= 0 || *req.TargetPct > 100) {
		return Response{}, ErrInvalidTarget
	}

	if req.Timezone != nil {
		if _, tzErr := slo.LoadLocation(*req.Timezone); tzErr != nil {
			return Response{}, ErrInvalidTimezone
		}
	}

	// A scope change must name exactly one side; the schema's XOR constraint
	// would reject anything else anyway, but a 400 beats a 500.
	if req.CheckUID != nil || req.CheckGroupUID != nil {
		if scopeErr := s.validateScope(ctx, org.UID, req.CheckUID, req.CheckGroupUID); scopeErr != nil {
			return Response{}, scopeErr
		}
	}

	update := models.SLOUpdate{
		Name:               req.Name,
		Slug:               req.Slug,
		CheckUID:           req.CheckUID,
		CheckGroupUID:      req.CheckGroupUID,
		TargetPct:          req.TargetPct,
		Timezone:           req.Timezone,
		ExcludeMaintenance: req.ExcludeMaintenance,
		Enabled:            req.Enabled,
	}

	if updateErr := s.db.UpdateSLO(ctx, row.UID, update); updateErr != nil {
		return Response{}, updateErr
	}

	updated, err := s.db.GetSLO(ctx, org.UID, row.UID)
	if err != nil {
		return Response{}, err
	}

	return s.decorate(ctx, org.UID, []*models.SLO{updated})[0], nil
}

// DeleteSLO soft-deletes an SLO.
func (s *Service) DeleteSLO(ctx context.Context, orgSlug, ident string) error {
	org, row, err := s.resolve(ctx, orgSlug, ident)
	if err != nil {
		return err
	}

	return s.db.DeleteSLO(ctx, org.UID, row.UID)
}

// normalizeCreateRequest applies the defaults and validates the scalar fields,
// returning the effective slug, target and timezone.
func normalizeCreateRequest(req *CreateRequest) (string, float64, string, error) {
	if req.Name == "" {
		return "", 0, "", ErrNameRequired
	}

	slug := req.Slug
	if slug == "" {
		slug = Slugify(req.Name)
	}

	if !slugPattern.MatchString(slug) {
		return "", 0, "", ErrInvalidSlug
	}

	target := defaultTargetPct
	if req.TargetPct != nil {
		target = *req.TargetPct
	}

	if target <= 0 || target > 100 {
		return "", 0, "", ErrInvalidTarget
	}

	timezone := req.Timezone
	if timezone == "" {
		timezone = models.DefaultSLOTimezone
	}

	if _, err := slo.LoadLocation(timezone); err != nil {
		return "", 0, "", ErrInvalidTimezone
	}

	return slug, target, timezone, nil
}

// assertSlugFree rejects a slug another live objective in the org already owns.
func (s *Service) assertSlugFree(ctx context.Context, orgUID, slug string) error {
	existing, err := s.db.GetSLOBySlug(ctx, orgUID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		return err
	}

	if existing != nil {
		return ErrSlugTaken
	}

	return nil
}

// validateScope enforces the XOR and that the referenced entity exists.
func (s *Service) validateScope(ctx context.Context, orgUID string, checkUID, groupUID *string) error {
	hasCheck := checkUID != nil && *checkUID != ""
	hasGroup := groupUID != nil && *groupUID != ""

	if hasCheck == hasGroup {
		return ErrScopeRequired
	}

	if hasCheck {
		if _, err := s.db.GetCheck(ctx, orgUID, *checkUID); err != nil {
			return ErrScopeNotFound
		}

		return nil
	}

	if _, err := s.db.GetCheckGroup(ctx, orgUID, *groupUID); err != nil {
		return ErrScopeNotFound
	}

	return nil
}

func (s *Service) resolve(ctx context.Context, orgSlug, ident string) (*models.Organization, *models.SLO, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, nil, ErrOrganizationNotFound
	}

	row, err := s.db.GetSLO(ctx, org.UID, ident)
	if err == nil {
		return org, row, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}

	row, err = s.db.GetSLOBySlug(ctx, org.UID, ident)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrSLONotFound
		}

		return nil, nil, err
	}

	return org, row, nil
}

// decorate resolves the human-readable scope names for a batch of SLOs, plus
// the live burning flag.
func (s *Service) decorate(ctx context.Context, orgUID string, rows []*models.SLO) []Response {
	out := make([]Response, 0, len(rows))
	burning := s.burningSLOUIDs(ctx, orgUID, rows)

	for _, row := range rows {
		resp := Response{
			UID:                row.UID,
			Name:               row.Name,
			Slug:               row.Slug,
			CheckUID:           row.CheckUID,
			CheckGroupUID:      row.CheckGroupUID,
			TargetPct:          row.TargetPct,
			Timezone:           row.Timezone,
			ExcludeMaintenance: row.ExcludeMaintenance,
			Enabled:            row.Enabled,
			CreatedAt:          row.CreatedAt,
			UpdatedAt:          row.UpdatedAt,
		}

		_, resp.Burning = burning[row.UID]

		if row.CheckUID != nil {
			if check, err := s.db.GetCheck(ctx, orgUID, *row.CheckUID); err == nil {
				resp.CheckName = check.Name
			}
		}

		if row.CheckGroupUID != nil {
			if group, err := s.db.GetCheckGroup(ctx, orgUID, *row.CheckGroupUID); err == nil {
				name := group.Name
				resp.CheckGroupName = &name
			}
		}

		out = append(out, resp)
	}

	return out
}

// burningSLOUIDs returns the subset of the given SLOs that currently have an
// open burn incident. One query for the whole batch, and deliberately
// best-effort: a list page that cannot show a badge is a far smaller problem
// than a list page that 500s.
func (s *Service) burningSLOUIDs(
	ctx context.Context, orgUID string, rows []*models.SLO,
) map[string]struct{} {
	out := make(map[string]struct{})

	if len(rows) == 0 {
		return out
	}

	uids := make([]string, 0, len(rows))
	for _, row := range rows {
		uids = append(uids, row.UID)
	}

	incidents, err := s.db.ListActiveBurnIncidentsForSLOs(ctx, orgUID, uids)
	if err != nil {
		return out
	}

	for _, inc := range incidents {
		if inc.SLOUID != nil {
			out[*inc.SLOUID] = struct{}{}
		}
	}

	return out
}

// EnsureAlertPolicies materializes the two built-in burn-rate policies for an
// SLO, creating only the ones that are missing.
//
// Lazy rather than backfilled by the migration: SQLite has no UUID generator,
// so a dialect-neutral backfill would have to be written twice and would still
// miss SLOs created by a rolled-back-then-forward deploy. Creating on demand —
// at SLO creation, and again whenever the alerting API or the evaluator asks —
// makes "the two policies exist" true by construction for every SLO, however
// old.
func (s *Service) EnsureAlertPolicies(
	ctx context.Context, orgUID, sloUID string,
) ([]*models.SLOAlertPolicy, error) {
	existing, err := s.db.ListSLOAlertPolicies(ctx, sloUID)
	if err != nil {
		return nil, fmt.Errorf("list alert policies: %w", err)
	}

	byKind := make(map[string]struct{}, len(existing))
	for _, policy := range existing {
		byKind[policy.Kind] = struct{}{}
	}

	created := false

	for _, def := range models.DefaultSLOAlertPolicies() {
		if _, ok := byKind[def.Kind]; ok {
			continue
		}

		if createErr := s.db.CreateSLOAlertPolicy(
			ctx, models.NewSLOAlertPolicy(orgUID, sloUID, def),
		); createErr != nil {
			return nil, fmt.Errorf("create alert policy: %w", createErr)
		}

		created = true
	}

	if !created {
		return existing, nil
	}

	refreshed, err := s.db.ListSLOAlertPolicies(ctx, sloUID)
	if err != nil {
		return nil, fmt.Errorf("list alert policies: %w", err)
	}

	return refreshed, nil
}

// EvaluateWindows evaluates one SLO over several arbitrary windows, resolving
// the scope EXACTLY ONCE.
//
// This is the path the burn-rate evaluator uses, and it deliberately runs the
// same private `evaluate` that GetStatus / GetHistory / EvaluateWindow do:
// group aggregation, the coverage clamp and the `results.maintenance`
// exclusion are therefore identical to the objective's own denominator by
// construction. An evaluator that re-derived any of that could page for
// planned maintenance the SLO page says never happened.
func (s *Service) EvaluateWindows(
	ctx context.Context, orgUID string, row *models.SLO, windows []slo.Window, now time.Time,
) ([]StatusRow, error) {
	scoped, err := s.resolveScope(ctx, orgUID, row)
	if err != nil {
		return nil, err
	}

	out := make([]StatusRow, 0, len(windows))

	for i := range windows {
		status, evalErr := s.evaluate(ctx, orgUID, row, scoped, windows[i], now)
		if evalErr != nil {
			return nil, evalErr
		}

		out = append(out, status)
	}

	return out, nil
}

// Slugify derives a URL-safe slug from a display name.
func Slugify(name string) string {
	var builder strings.Builder

	prevHyphen := false

	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			builder.WriteRune(r)

			prevHyphen = false
		case !prevHyphen && builder.Len() > 0:
			builder.WriteRune('-')

			prevHyphen = true
		}
	}

	out := strings.Trim(builder.String(), "-")
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-")
	}

	return out
}

// scope is one SLO's resolved evaluation scope.
type scope struct {
	checkUIDs     []string
	coverageStart time.Time
}

// resolveScope expands an SLO to the check UIDs it evaluates over, plus the
// earliest instant any of them could have produced a probe.
//
// A group SLO means "the group's CURRENT members". Membership is not versioned
// (checks.check_group_uid is a plain 1:n), so a check that left the group last
// week is simply absent from today's evaluation of last month. That is a real
// semantic, documented rather than hidden.
func (s *Service) resolveScope(ctx context.Context, orgUID string, row *models.SLO) (scope, error) {
	if row.CheckUID != nil {
		// The FK cascades on hard delete, so a lookup miss here means the check
		// is soft-deleted. Report an empty scope, which reads as "no data"
		// rather than as a perfect month — deliberately swallowing the error.
		check, lookupErr := s.db.GetCheck(ctx, orgUID, *row.CheckUID)
		if lookupErr != nil {
			return scope{}, nil //nolint:nilerr // a missing check is "no data", not a failure
		}

		return scope{checkUIDs: []string{check.UID}, coverageStart: check.CreatedAt}, nil
	}

	if row.CheckGroupUID == nil {
		return scope{}, nil
	}

	groupUID := *row.CheckGroupUID

	checks, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{CheckGroupUID: &groupUID})
	if err != nil {
		return scope{}, fmt.Errorf("list group checks: %w", err)
	}

	out := scope{checkUIDs: make([]string, 0, len(checks))}

	for _, check := range checks {
		out.checkUIDs = append(out.checkUIDs, check.UID)

		if out.coverageStart.IsZero() || check.CreatedAt.Before(out.coverageStart) {
			out.coverageStart = check.CreatedAt
		}
	}

	return out, nil
}

// GetStatus evaluates the SLO over the current calendar window.
func (s *Service) GetStatus(ctx context.Context, orgSlug, ident string, now time.Time) (*StatusResponse, error) {
	org, row, err := s.resolve(ctx, orgSlug, ident)
	if err != nil {
		return nil, err
	}

	loc, err := slo.LoadLocation(row.Timezone)
	if err != nil {
		// A stored timezone that no longer resolves must not 500 the page.
		loc = time.UTC
	}

	scoped, err := s.resolveScope(ctx, org.UID, row)
	if err != nil {
		return nil, err
	}

	window := slo.MonthWindow(loc, now)

	status, err := s.evaluate(ctx, org.UID, row, scoped, window, now)
	if err != nil {
		return nil, err
	}

	incidents, err := s.incidentBlock(ctx, org.UID, scoped, window, now)
	if err != nil {
		return nil, err
	}

	return &StatusResponse{
		SLO:       s.decorate(ctx, org.UID, []*models.SLO{row})[0],
		Current:   status,
		Incidents: incidents,
	}, nil
}

// GetHistory evaluates the SLO over the last `months` calendar windows,
// most recent first.
func (s *Service) GetHistory(
	ctx context.Context, orgSlug, ident string, months int, now time.Time,
) (*HistoryResponse, error) {
	org, row, err := s.resolve(ctx, orgSlug, ident)
	if err != nil {
		return nil, err
	}

	if months <= 0 {
		months = defaultHistoryMonths
	}

	if months > maxHistoryMonths {
		months = maxHistoryMonths
	}

	loc, err := slo.LoadLocation(row.Timezone)
	if err != nil {
		loc = time.UTC
	}

	scoped, err := s.resolveScope(ctx, org.UID, row)
	if err != nil {
		return nil, err
	}

	windows := slo.PreviousMonthWindows(loc, now, months)
	rows := make([]StatusRow, 0, len(windows))

	for i := range windows {
		status, evalErr := s.evaluate(ctx, org.UID, row, scoped, windows[i], now)
		if evalErr != nil {
			return nil, evalErr
		}

		rows = append(rows, status)
	}

	return &HistoryResponse{Data: rows}, nil
}

// EvaluateWindow evaluates an SLO over an arbitrary window. It exists for the
// uptime report, which reports on a period that just closed rather than on the
// current one, and must use exactly the same math the dashboard shows.
func (s *Service) EvaluateWindow(
	ctx context.Context, orgUID string, row *models.SLO, window slo.Window, now time.Time,
) (StatusRow, error) {
	scoped, err := s.resolveScope(ctx, orgUID, row)
	if err != nil {
		return StatusRow{}, err
	}

	return s.evaluate(ctx, orgUID, row, scoped, window, now)
}

// ScopeCheckUIDs expands an SLO to the check UIDs it evaluates over.
func (s *Service) ScopeCheckUIDs(ctx context.Context, orgUID string, row *models.SLO) ([]string, error) {
	scoped, err := s.resolveScope(ctx, orgUID, row)
	if err != nil {
		return nil, err
	}

	return scoped.checkUIDs, nil
}

// evaluate computes one window's objective state.
func (s *Service) evaluate(
	ctx context.Context, orgUID string, row *models.SLO, scoped scope, window slo.Window, now time.Time,
) (StatusRow, error) {
	var merged uptimebar.BucketStats

	if len(scoped.checkUIDs) > 0 {
		byCheck, err := uptimebar.WindowAvailability(
			ctx, s.db, orgUID, scoped.checkUIDs, window.Start, window.End, s.uptimebarHints(ctx, orgUID),
		)
		if err != nil {
			return StatusRow{}, fmt.Errorf("window availability: %w", err)
		}

		stats := make([]uptimebar.BucketStats, 0, len(scoped.checkUIDs))
		for _, uid := range scoped.checkUIDs {
			stats = append(stats, byCheck[uid])
		}

		merged = slo.MergeStats(stats)
	}

	computed := slo.Compute(&slo.Input{
		TargetPct:          row.TargetPct,
		Stats:              merged,
		Window:             window,
		Now:                now,
		CoverageStart:      scoped.coverageStart,
		ExcludeMaintenance: row.ExcludeMaintenance,
	})

	return StatusRow{
		Window: WindowResponse{
			Start: window.Start,
			End:   window.End,
			Label: window.Start.Format("2006-01"),
		},
		AttainmentPct:              computed.AttainmentPct,
		HasData:                    computed.HasData,
		TargetPct:                  computed.TargetPct,
		TotalChecks:                computed.TotalChecks,
		SuccessfulChecks:           computed.SuccessfulChecks,
		MonitoredSeconds:           computed.MonitoredSeconds,
		ElapsedSeconds:             computed.ElapsedSeconds,
		BudgetTotalSeconds:         computed.BudgetTotalSeconds,
		BudgetConsumedSeconds:      computed.BudgetConsumedSeconds,
		BudgetRemainingSeconds:     computed.BudgetRemainingSeconds,
		ExcludedMaintenanceSeconds: computed.ExcludedMaintenanceSeconds,
		BurnRate:                   computed.BurnRate,
		ProjectedExhaustionAt:      computed.ProjectedExhaustionAt,
		State:                      computed.State,
		Partial:                    computed.Partial,
	}, nil
}

// incidentBlock summarizes the confirmed outages inside the window, reusing the
// availability API's definition so the two surfaces cannot disagree. Incidents
// are de-duplicated by UID: a group incident naming several members would
// otherwise be counted once per member.
func (s *Service) incidentBlock(
	ctx context.Context, orgUID string, scoped scope, window slo.Window, now time.Time,
) (availability.PeriodIncidents, error) {
	seen := make(map[string]struct{})
	all := make([]*models.Incident, 0)

	for _, checkUID := range scoped.checkUIDs {
		filter := &models.ListIncidentsFilter{
			OrganizationUID: orgUID,
			MemberCheckUID:  checkUID,
			// Check incidents only: the objective's own burn alert must not
			// show up as an outage against the objective (spec 2026-08-21-08).
			Kinds: []string{models.IncidentKindCheck},
			Since: &window.Start,
			Until: &window.End,
			Limit: incidentFetchLimit,
		}

		incidents, _, err := s.db.ListIncidents(ctx, filter)
		if err != nil {
			return availability.PeriodIncidents{}, fmt.Errorf("list incidents: %w", err)
		}

		for _, inc := range incidents {
			if _, dup := seen[inc.UID]; dup {
				continue
			}

			seen[inc.UID] = struct{}{}

			all = append(all, inc)
		}
	}

	return availability.IncidentBlock(all, window.Start.UTC(), window.End.UTC(), now.UTC()), nil
}

// BurndownPoint is one step of the error-budget burn-down.
type BurndownPoint struct {
	// At is the end of the step (exclusive), in UTC.
	At time.Time `json:"at"`
	// BudgetRemainingSeconds is what was left of the window's budget at that
	// instant, given everything observed from the window start up to it.
	// Negative once the budget is overspent — clamping it would hide the very
	// thing the chart exists to show.
	BudgetRemainingSeconds int64 `json:"budgetRemainingSeconds"`
	// IdealRemainingSeconds is the straight line from the full budget at the
	// window start to zero at the window end: the pace that spends the budget
	// exactly, no faster.
	//
	// It is drawn against BurndownResponse.BudgetTotalSeconds — the ONE
	// window-level budget the whole series shares. Recomputing a per-point
	// budget would let the reference line drift under the actual line as
	// maintenance accrues (excluded maintenance shrinks the monitored
	// denominator), so the two series would no longer be comparable, which is
	// the only reason the ideal line exists.
	IdealRemainingSeconds int64 `json:"idealRemainingSeconds"`
	// AttainmentPct is the window-to-date attainment, nil when nothing
	// countable has been recorded yet. Never rendered as 100%.
	AttainmentPct *float64 `json:"attainmentPct"`
	HasData       bool     `json:"hasData"`
}

// BurndownResponse is the /burndown payload.
type BurndownResponse struct {
	Window    WindowResponse `json:"window"`
	TargetPct float64        `json:"targetPct"`
	// BudgetTotalSeconds is the window's whole allowance, evaluated once. Every
	// point's BudgetRemainingSeconds is this value minus the consumption
	// accrued up to it, and every point's IdealRemainingSeconds is this value
	// decayed linearly — so the two series and this number are always the same
	// arithmetic.
	BudgetTotalSeconds int64           `json:"budgetTotalSeconds"`
	Data               []BurndownPoint `json:"data"`
}

// burndownStep is the resolution of the burn-down. A calendar month is at most
// 31 points at this step — enough to see a slope, few enough to stay legible
// on a phone.
const burndownStep = 24 * time.Hour

// maxBurndownPoints bounds the series defensively.
const maxBurndownPoints = 40

// GetBurndown returns the error-budget burn-down for the SLO's current window.
//
// Consumption is accrued INCREMENTALLY, one step at a time, and summed forward.
// That is what makes the series monotonic non-increasing — a burn-down that
// climbs back up is a wrong chart, not a cosmetic quirk.
//
// Steps are fixed 24h slices from the window start rather than local calendar
// days: the underlying bucketing engine works in fixed durations, and a DST day
// being an hour off is invisible at this resolution. The window itself stays
// calendar-exact — only the sampling grid is uniform.
func (s *Service) GetBurndown(
	ctx context.Context, orgSlug, ident string, now time.Time,
) (*BurndownResponse, error) {
	org, row, err := s.resolve(ctx, orgSlug, ident)
	if err != nil {
		return nil, err
	}

	loc, err := slo.LoadLocation(row.Timezone)
	if err != nil {
		loc = time.UTC
	}

	scoped, err := s.resolveScope(ctx, org.UID, row)
	if err != nil {
		return nil, err
	}

	window := slo.MonthWindow(loc, now)

	full, err := s.evaluate(ctx, org.UID, row, scoped, window, now)
	if err != nil {
		return nil, err
	}

	resp := &BurndownResponse{
		Window: WindowResponse{
			Start: window.Start,
			End:   window.End,
			Label: window.Start.Format("2006-01"),
		},
		TargetPct:          row.TargetPct,
		BudgetTotalSeconds: full.BudgetTotalSeconds,
	}

	points, err := s.burndownPoints(ctx, org.UID, row, scoped, window, now, full.BudgetTotalSeconds)
	if err != nil {
		return nil, err
	}

	resp.Data = points

	return resp, nil
}

// burndownPoints walks the elapsed part of the window one step at a time,
// accruing consumed budget per step and summing it forward.
//
// The increment is the whole point, and it is NOT equivalent to re-running the
// window-to-date budget math on each prefix. That form derives consumption as
// (window-to-date failure ratio) x (window-to-date wall clock), and the ratio
// moves when probe DENSITY changes — a group member created mid-window, a check
// paused after an outage, a period shortened. A day of sparse failures followed
// by a day of dense successes makes the ratio collapse faster than the wall
// clock grows, so the later point computes LESS consumption than the earlier
// one and the burn-down climbs back up.
//
// Summing per-step down-time forward can only ever add, so the series is
// monotonic non-increasing by construction rather than by hoping the density
// holds still.
func (s *Service) burndownPoints(
	ctx context.Context, orgUID string, row *models.SLO, scoped scope,
	window slo.Window, now time.Time, budgetTotalSeconds int64,
) ([]BurndownPoint, error) {
	elapsedEnd := window.End
	if now.Before(elapsedEnd) {
		elapsedEnd = now
	}

	if !elapsedEnd.After(window.Start) || len(scoped.checkUIDs) == 0 {
		return []BurndownPoint{}, nil
	}

	// A scope that did not exist for the start of the window owes nothing for
	// the time before it existed.
	coverageStart := window.Start
	if !scoped.coverageStart.IsZero() && scoped.coverageStart.After(coverageStart) {
		coverageStart = scoped.coverageStart
	}

	steps := burndownStepCount(window.Start, elapsedEnd)

	byCheck, err := uptimebar.BucketAvailability(
		ctx, s.db, orgUID, scoped.checkUIDs, burndownStep, window.Start, steps, s.uptimebarHints(ctx, orgUID),
	)
	if err != nil {
		return nil, fmt.Errorf("bucket availability: %w", err)
	}

	points := make([]BurndownPoint, 0, steps)

	var (
		consumed   float64
		cumulative uptimebar.BucketStats
	)

	for i := range steps {
		bucketStart := window.Start.UTC().Add(time.Duration(i) * burndownStep)

		var bucket uptimebar.BucketStats
		for _, uid := range scoped.checkUIDs {
			bucket.Add(byCheck[uid][bucketStart])
		}

		cumulative.Add(bucket)

		stepEnd := bucketStart.Add(burndownStep)
		if stepEnd.After(elapsedEnd) {
			stepEnd = elapsedEnd
		}

		sliceStart := bucketStart
		if coverageStart.After(sliceStart) {
			sliceStart = coverageStart
		}

		consumed += stepConsumption(bucket, sliceStart, stepEnd, row.ExcludeMaintenance)

		point := BurndownPoint{
			At:                     stepEnd.UTC(),
			BudgetRemainingSeconds: budgetTotalSeconds - int64(math.Round(consumed)),
			IdealRemainingSeconds:  idealRemaining(budgetTotalSeconds, window, stepEnd),
		}

		windowToDate := cumulative
		if row.ExcludeMaintenance {
			windowToDate = cumulative.ExcludingMaintenance()
		}

		if pct, ok := windowToDate.AvailabilityPct(); ok {
			point.HasData = true
			point.AttainmentPct = &pct
		}

		points = append(points, point)
	}

	return points, nil
}

// stepConsumption is the error budget one step spends: the step's own failure
// share applied to the step's own wall clock.
//
// A step with no countable probe spends NOTHING. That is the same rule the
// attainment number follows — no data is "we were not watching", not downtime —
// and it is why a gap in coverage flattens the burn-down instead of dropping it.
func stepConsumption(
	bucket uptimebar.BucketStats, sliceStart, sliceEnd time.Time, excludeMaintenance bool,
) float64 {
	seconds := math.Max(0, sliceEnd.Sub(sliceStart).Seconds())
	if seconds == 0 {
		return 0
	}

	effective := bucket

	if excludeMaintenance {
		// Probes are evenly spaced within a step, so the share of them tagged
		// maintenance is the share of the step's wall clock under maintenance.
		if bucket.Total > 0 && bucket.MaintTotal > 0 {
			share := math.Min(1, float64(bucket.MaintTotal)/float64(bucket.Total))
			seconds *= 1 - share
		}

		effective = bucket.ExcludingMaintenance()
	}

	pct, ok := effective.AvailabilityPct()
	if !ok {
		return 0
	}

	return math.Max(0, 1-pct/100) * seconds
}

// burndownStepCount is how many sampling slices the elapsed span needs.
//
// Ceil, not +1: an elapsed span that lands exactly on a step boundary must not
// emit a trailing zero-length step whose timestamp duplicates the one before it.
func burndownStepCount(start, elapsedEnd time.Time) int {
	steps := int(math.Ceil(elapsedEnd.Sub(start).Seconds() / burndownStep.Seconds()))
	if steps < 1 {
		return 1
	}

	if steps > maxBurndownPoints {
		return maxBurndownPoints
	}

	return steps
}

// uptimebarHints resolves the read-side retention hints the bucketing engine
// needs. Mirrors availability.Service.uptimebarHints: it must read the
// performance.* parameters, not the koanf struct alone, or the raw tier gets
// clamped shorter than the job actually keeps raw for.
func (s *Service) uptimebarHints(ctx context.Context, orgUID string) uptimebar.Hints {
	rawHours, hourDays := systemconfig.ResolveReadSideRetention(ctx, s.db, s.cfg)

	return uptimebar.Hints{
		RetentionRawHours: rawHours,
		RetentionHourDays: hourDays,
		RawRowsPerHour:    uptimebar.MeasureRawRowsPerHour(ctx, s.db, orgUID),
	}
}

// idealRemaining is the straight line from the full budget at the window start
// to zero at its end, evaluated at `at`.
func idealRemaining(budgetTotalSeconds int64, window slo.Window, instant time.Time) int64 {
	seconds := window.Seconds()
	if seconds <= 0 {
		return 0
	}

	progress := math.Min(1, math.Max(0, instant.Sub(window.Start).Seconds()/seconds))

	return int64(math.Round(float64(budgetTotalSeconds) * (1 - progress)))
}
