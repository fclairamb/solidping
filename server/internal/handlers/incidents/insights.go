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
	defaultInsightsPageSize  = 500
	defaultInsightsMaxLoaded = 5000
	insightsTopChecks        = 20
	insightsDefaultDays      = 30
)

var errInvalidInsightsPeriod = errors.New("invalid period: must be one of 7d, 30d, 90d")

// InsightsCheckRow is one row of the "top failing checks" table.
type InsightsCheckRow struct {
	incidentinsights.CheckRow
	CheckName string `json:"checkName"`
	CheckSlug string `json:"checkSlug"`
}

// InsightsResponse is the body of GET /orgs/:org/incidents/insights.
type InsightsResponse struct {
	Period      string                   `json:"period"`
	WindowStart time.Time                `json:"windowStart"`
	WindowEnd   time.Time                `json:"windowEnd"`
	Truncated   bool                     `json:"truncated"`
	Summary     incidentinsights.Summary `json:"summary"`
	Checks      []InsightsCheckRow       `json:"checks"`
}

func parseInsightsPeriod(raw string) (string, int, error) {
	switch raw {
	case "", "30d":
		return "30d", insightsDefaultDays, nil
	case "7d":
		return "7d", 7, nil
	case "90d":
		return "90d", 90, nil
	default:
		return "", 0, errInvalidInsightsPeriod
	}
}

// GetInsights handles GET /api/v1/orgs/:org/incidents/insights.
func (h *Handler) GetInsights(writer http.ResponseWriter, req *http.Request) error {
	period, days, err := parseInsightsPeriod(req.URL.Query().Get("period"))
	if err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	}

	response, err := h.svc.GetInsights(req.Context(), httpx.Param(req, "org"), period, days)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// GetInsights aggregates the check outages of the trailing window.
func (s *Service) GetInsights(ctx context.Context, orgSlug, period string, days int) (*InsightsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	now := s.clock.Now()
	windowStart := now.AddDate(0, 0, -days)

	incidents, truncated, err := s.loadInsightsIncidents(ctx, org.UID, windowStart, now)
	if err != nil {
		return nil, err
	}

	computed := incidentinsights.Compute(incidents, windowStart, now, now)

	rows := computed.Checks
	if len(rows) > insightsTopChecks {
		rows = rows[:insightsTopChecks]
	}

	response := &InsightsResponse{
		Period:      period,
		WindowStart: windowStart,
		WindowEnd:   now,
		Truncated:   truncated,
		Summary:     computed.Summary,
		Checks:      make([]InsightsCheckRow, 0, len(rows)),
	}

	checkUIDs := make([]string, 0, len(rows))
	for idx := range rows {
		checkUIDs = append(checkUIDs, rows[idx].CheckUID)
	}

	checks := map[string]*models.Check{}
	if len(checkUIDs) > 0 {
		checks, err = s.db.GetChecksByUIDs(ctx, org.UID, checkUIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to get checks: %w", err)
		}
	}

	for idx := range rows {
		row := &rows[idx]
		out := InsightsCheckRow{CheckRow: *row}

		if check := checks[row.CheckUID]; check != nil {
			if check.Name != nil {
				out.CheckName = *check.Name
			}

			if check.Slug != nil {
				out.CheckSlug = *check.Slug
			}
		}

		response.Checks = append(response.Checks, out)
	}

	return response, nil
}

// loadInsightsIncidents pages through the check outages active during the
// window (started before its end, still open or resolved inside it), stopping
// at the load cap (truncated is then true).
func (s *Service) loadInsightsIncidents(
	ctx context.Context, orgUID string, since, until time.Time,
) ([]*models.Incident, bool, error) {
	pageSize, maxLoaded := s.insightsPageSize, s.insightsMaxLoaded
	if pageSize <= 0 {
		pageSize = defaultInsightsPageSize
	}

	if maxLoaded <= 0 {
		maxLoaded = defaultInsightsMaxLoaded
	}

	var all []*models.Incident

	var (
		cursorTS  *time.Time
		cursorUID *string
	)

	for {
		page, _, err := s.db.ListIncidents(ctx, &models.ListIncidentsFilter{
			OrganizationUID: orgUID,
			Kinds:           []string{models.IncidentKindCheck},
			ActiveSince:     &since,
			Until:           &until,
			CursorTimestamp: cursorTS,
			CursorUID:       cursorUID,
			Limit:           pageSize + 1,
		})
		if err != nil {
			return nil, false, fmt.Errorf("failed to list incidents: %w", err)
		}

		hasMore := len(page) > pageSize
		if hasMore {
			page = page[:pageSize]
		}

		all = append(all, page...)

		if !hasMore {
			return all, false, nil
		}

		if len(all) >= maxLoaded {
			return all[:maxLoaded], true, nil
		}

		last := page[len(page)-1]
		ts, uid := last.StartedAt, last.UID
		cursorTS, cursorUID = &ts, &uid
	}
}
