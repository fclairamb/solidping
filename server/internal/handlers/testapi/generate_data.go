package testapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/synthdata"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// GenerateDataRequest represents the request body for generating test data.
type GenerateDataRequest struct {
	Org             string  `json:"org"`
	Name            string  `json:"name"`
	CheckPeriodSec  int     `json:"checkPeriodSec"`
	StartDate       string  `json:"startDate"`
	FailureRate     float64 `json:"failureRate"`
	FailureBurstSec int     `json:"failureBurstSec"`
	AvgDurationMs   float64 `json:"avgDurationMs"`
}

// GenerateDataResponse represents the response from data generation.
type GenerateDataResponse struct {
	CheckUID     string `json:"checkUid"`
	CheckSlug    string `json:"checkSlug"`
	ResultsCount int    `json:"resultsCount"`
}

func (r *GenerateDataRequest) applyDefaults() {
	if r.Org == "" {
		r.Org = defaultOrg
	}

	if r.CheckPeriodSec < 1 {
		r.CheckPeriodSec = 60
	}

	if r.AvgDurationMs <= 0 {
		r.AvgDurationMs = 150
	}

	if r.Name == "" {
		r.Name = "Generated Data Check"
	}
}

func parseStartDate(dateStr string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, dateStr)
	if err == nil {
		return parsed, nil
	}

	return time.Parse("2006-01-02", dateStr)
}

// GenerateData creates a check and inserts historical results.
// POST /api/v1/test/generate-data.
func (h *Handler) GenerateData(writer http.ResponseWriter, req *http.Request) error {
	var body GenerateDataRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body")
	}

	body.applyDefaults()

	if body.StartDate == "" {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "startDate is required")
	}

	startTime, err := parseStartDate(body.StartDate)
	if err != nil {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR",
			"startDate must be RFC3339 or YYYY-MM-DD format")
	}

	org, err := h.dbService.GetOrganizationBySlug(req.Context(), body.Org)
	if err != nil {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR",
			fmt.Sprintf("Organization '%s' not found", body.Org))
	}

	// Create the check
	slug := fmt.Sprintf("gen-%d", time.Now().UnixMilli())
	check := models.NewCheck(org.UID, slug, "http")
	check.Name = &body.Name
	check.Period = timeutils.Duration(time.Duration(body.CheckPeriodSec) * time.Second)
	check.Config = map[string]any{
		"url":    "https://1.1.1.1",
		"method": "GET",
	}

	if createErr := h.dbService.CreateCheck(req.Context(), check); createErr != nil {
		return h.writeInternalError(writer, createErr)
	}

	resultsCount, err := h.generateResults(req.Context(), org.UID, check.UID, startTime, &body)
	if err != nil {
		return h.writeInternalError(writer, err)
	}

	// Trigger aggregation job so hourly/daily/monthly rollups are computed
	now := time.Now()
	if _, jobErr := h.jobSvc.CreateJob(
		req.Context(), org.UID, string(jobdef.JobTypeAggregation), nil,
		&jobsvc.JobOptions{ScheduledAt: &now},
	); jobErr != nil {
		slog.WarnContext(req.Context(), "Failed to schedule aggregation job", "error", jobErr)
	}

	return h.writeJSON(writer, http.StatusOK, GenerateDataResponse{
		CheckUID:     check.UID,
		CheckSlug:    slug,
		ResultsCount: resultsCount,
	})
}

// generateResults delegates to internal/synthdata, which is the shared
// implementation this endpoint used to own outright. The public live demo's
// 30-day backfill (spec 2026-09-06-02) needs exactly these shapes with no HTTP
// request, no handler and no test-mode gate behind them, so the generator moved
// out and this became a thin adapter over the request body.
func (h *Handler) generateResults(
	ctx context.Context, orgUID, checkUID string, startTime time.Time, body *GenerateDataRequest,
) (int, error) {
	return synthdata.Generate(ctx, h.dbService, &synthdata.Options{
		OrganizationUID: orgUID,
		CheckUID:        checkUID,
		Start:           startTime,
		Period:          time.Duration(body.CheckPeriodSec) * time.Second,
		AvgDurationMs:   body.AvgDurationMs,
		FailureRate:     body.FailureRate,
		FailureBurstSec: body.FailureBurstSec,
	})
}
