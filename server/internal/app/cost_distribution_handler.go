package app

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/scheduling/costdist"
)

// costDistributionResponse wraps the distribution in the standard { data: ... }
// envelope.
type costDistributionResponse struct {
	Data costdist.Distribution `json:"data"`
}

// getCostDistribution serves GET /api/mgmt/scheduling/cost-distribution. It
// returns an aggregate (low-cardinality) distribution of the scheduler's
// per-job cost_ewma_ms and delay_ewma_ms across all check_jobs, plus fast/slow
// counts at a candidate threshold (query param `thresholdMs`, default 1000).
//
// Read-only and super-admin guarded (registered on the mgmtAdmin group). It is
// the Phase-3 measurement surface for deciding whether fast/slow lanes are
// warranted.
func (s *Server) getCostDistribution(writer http.ResponseWriter, req bunrouter.Request) error {
	threshold := costdist.DefaultThresholdMs

	if raw := req.URL.Query().Get("thresholdMs"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			threshold = parsed
		}
	}

	_, isPostgres := s.dbService.DB().Dialect().(*pgdialect.Dialect)

	dist, err := costdist.Compute(req.Context(), s.dbService.DB(), isPostgres, threshold)
	if err != nil {
		return err
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)

	data, marshalErr := json.Marshal(costDistributionResponse{Data: dist})
	if marshalErr != nil {
		return marshalErr
	}

	_, writeErr := writer.Write(data)

	return writeErr
}
