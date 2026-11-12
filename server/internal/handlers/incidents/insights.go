package incidents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/incidentinsights"
)

const (
	insightsMaxIncidents = 5000
	insightsPageSize     = 500
	insightsTopChecks    = 20
	hoursPerDay          = 24
)

// ErrInvalidInsightsPeriod is returned for a period other than 7d, 30d or 90d.
var ErrInvalidInsightsPeriod = errors.New("invalid period: must be one of 7d, 30d, 90d")

func insightsPeriodDays(period string) (int, bool) {
	switch period {
	case "7d":
		return 7, true
	case "30d":
		return 30, true
	case "90d":
		return 90, true
	default:
		return 0, false
	}
}

// InsightsCheckRow is one line of the failing checks table.
type InsightsCheckRow struct {
	incidentinsights.CheckRow
	Name *string `json:"name,omitempty"`
	Slug *string `json:"slug,omitempty"`
}

// InsightsResponse is the body of GET /incidents/insights.
type InsightsResponse struct {
	Period      string                   `json:"period"`
	WindowStart time.Time                `json:"windowStart"`
	WindowEnd   time.Time                `json:"windowEnd"`
	Summary     incidentinsights.Summary `json:"summary"`
	Checks      []InsightsCheckRow       `json:"checks"`
	Truncated   bool                     `json:"truncated"`
}

// GetInsights aggregates the org's check outages over the period.
func (s *Service) GetInsights(ctx context.Context, orgSlug, period string) (*InsightsResponse, error) {
	days, ok := insightsPeriodDays(period)
	if !ok {
		return nil, ErrInvalidInsightsPeriod
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	now := s.clock.Now().UTC()
	start := now.Add(-time.Duration(days) * hoursPerDay * time.Hour)

	all := make([]*models.Incident, 0, insightsPageSize)
	truncated := false
	filter := &models.ListIncidentsFilter{
		OrganizationUID: org.UID,
		Since:           &start,
		Kinds:           []string{models.IncidentKindCheck},
		Limit:           insightsPageSize,
	}

	for {
		page, _, listErr := s.db.ListIncidents(ctx, filter)
		if listErr != nil {
			return nil, fmt.Errorf("failed to list incidents: %w", listErr)
		}

		all = append(all, page...)

		if len(all) >= insightsMaxIncidents {
			all = all[:insightsMaxIncidents]
			truncated = true

			break
		}

		if len(page) < insightsPageSize {
			break
		}

		last := page[len(page)-1]
		filter.CursorTimestamp = &last.StartedAt
		filter.CursorUID = &last.UID
	}

	result := incidentinsights.Compute(all, start, now, now)

	rows := result.Checks
	if len(rows) > insightsTopChecks {
		rows = rows[:insightsTopChecks]
	}

	return &InsightsResponse{
		Period: period, WindowStart: start, WindowEnd: now,
		Summary: result.Summary, Checks: s.insightsCheckRows(ctx, org.UID, rows), Truncated: truncated,
	}, nil
}

func (s *Service) insightsCheckRows(
	ctx context.Context, orgUID string, rows []incidentinsights.CheckRow,
) []InsightsCheckRow {
	uids := make([]string, 0, len(rows))
	for idx := range rows {
		uids = append(uids, rows[idx].CheckUID)
	}

	checks, err := s.db.GetChecksByUIDs(ctx, orgUID, uids)
	if err != nil {
		checks = nil
	}

	out := make([]InsightsCheckRow, 0, len(rows))

	for idx := range rows {
		item := InsightsCheckRow{CheckRow: rows[idx]}
		if check := checks[rows[idx].CheckUID]; check != nil {
			item.Name, item.Slug = check.Name, check.Slug
		}

		out = append(out, item)
	}

	return out
}

// GetInsights handles GET /orgs/:org/incidents/insights.
func (h *Handler) GetInsights(writer http.ResponseWriter, req *http.Request) error {
	period := req.URL.Query().Get("period")
	if period == "" {
		period = "30d"
	}

	response, err := h.svc.GetInsights(req.Context(), httpx.Param(req, "org"), period)
	if errors.Is(err, ErrInvalidInsightsPeriod) {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	}

	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}
