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
	CheckUID           *string   `json:"checkUid,omitempty"`
	CheckGroupUID      *string   `json:"checkGroupUid,omitempty"`
	CheckName          *string   `json:"checkName,omitempty"`
	CheckGroupName     *string   `json:"checkGroupName,omitempty"`
	TargetPct          float64   `json:"targetPct"`
	Timezone           string    `json:"timezone"`
	ExcludeMaintenance bool      `json:"excludeMaintenance"`
	Enabled            bool      `json:"enabled"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
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

// decorate resolves the human-readable scope names for a batch of SLOs.
func (s *Service) decorate(ctx context.Context, orgUID string, rows []*models.SLO) []Response {
	out := make([]Response, 0, len(rows))

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
		rawHours, hourDays := systemconfig.ResolveReadSideRetention(ctx, s.db, s.cfg)
		hints := uptimebar.Hints{
			RetentionRawHours: rawHours,
			RetentionHourDays: hourDays,
			RawRowsPerHour:    uptimebar.MeasureRawRowsPerHour(ctx, s.db, orgUID),
		}

		byCheck, err := uptimebar.WindowAvailability(
			ctx, s.db, orgUID, scoped.checkUIDs, window.Start, window.End, hints,
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
			Since:           &window.Start,
			Until:           &window.End,
			Limit:           incidentFetchLimit,
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
	IdealRemainingSeconds int64 `json:"idealRemainingSeconds"`
	// AttainmentPct is the window-to-date attainment, nil when nothing
	// countable has been recorded yet. Never rendered as 100%.
	AttainmentPct *float64 `json:"attainmentPct"`
	HasData       bool     `json:"hasData"`
}

// BurndownResponse is the /burndown payload.
type BurndownResponse struct {
	Window             WindowResponse  `json:"window"`
	TargetPct          float64         `json:"targetPct"`
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
// It is deliberately CUMULATIVE: each point re-evaluates the whole window from
// its start up to that instant, which is what "budget remaining" means. A chart
// of per-day consumption would look similar and mean something else entirely.
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

	points, err := s.burndownPoints(ctx, org.UID, row, scoped, window, now)
	if err != nil {
		return nil, err
	}

	resp.Data = points

	return resp, nil
}

// burndownPoints walks the elapsed part of the window one step at a time,
// accumulating buckets and re-running the budget math on each prefix.
func (s *Service) burndownPoints(
	ctx context.Context, orgUID string, row *models.SLO, scoped scope, window slo.Window, now time.Time,
) ([]BurndownPoint, error) {
	elapsedEnd := window.End
	if now.Before(elapsedEnd) {
		elapsedEnd = now
	}

	if !elapsedEnd.After(window.Start) || len(scoped.checkUIDs) == 0 {
		return []BurndownPoint{}, nil
	}

	steps := int(elapsedEnd.Sub(window.Start)/burndownStep) + 1
	if steps > maxBurndownPoints {
		steps = maxBurndownPoints
	}

	rawHours, hourDays := systemconfig.ResolveReadSideRetention(ctx, s.db, s.cfg)
	hints := uptimebar.Hints{
		RetentionRawHours: rawHours,
		RetentionHourDays: hourDays,
		RawRowsPerHour:    uptimebar.MeasureRawRowsPerHour(ctx, s.db, orgUID),
	}

	byCheck, err := uptimebar.BucketAvailability(
		ctx, s.db, orgUID, scoped.checkUIDs, burndownStep, window.Start, steps, hints,
	)
	if err != nil {
		return nil, fmt.Errorf("bucket availability: %w", err)
	}

	windowSeconds := window.Seconds()
	points := make([]BurndownPoint, 0, steps)

	var cumulative uptimebar.BucketStats

	for i := range steps {
		bucketStart := window.Start.UTC().Add(time.Duration(i) * burndownStep)

		for _, uid := range scoped.checkUIDs {
			cumulative.Add(byCheck[uid][bucketStart])
		}

		stepEnd := bucketStart.Add(burndownStep)
		if stepEnd.After(elapsedEnd) {
			stepEnd = elapsedEnd
		}

		computed := slo.Compute(&slo.Input{
			TargetPct:          row.TargetPct,
			Stats:              cumulative,
			Window:             window,
			Now:                stepEnd,
			CoverageStart:      scoped.coverageStart,
			ExcludeMaintenance: row.ExcludeMaintenance,
		})

		ideal := float64(0)
		if windowSeconds > 0 {
			progress := stepEnd.Sub(window.Start).Seconds() / windowSeconds
			ideal = float64(computed.BudgetTotalSeconds) * (1 - progress)
		}

		points = append(points, BurndownPoint{
			At:                     stepEnd.UTC(),
			BudgetRemainingSeconds: computed.BudgetRemainingSeconds,
			IdealRemainingSeconds:  int64(math.Round(ideal)),
			AttainmentPct:          computed.AttainmentPct,
			HasData:                computed.HasData,
		})
	}

	return points, nil
}
