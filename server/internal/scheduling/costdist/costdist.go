// Package costdist computes an aggregate distribution of the scheduler's
// per-job cost_ewma_ms and delay_ewma_ms across all check_jobs. It is a
// read-only, low-cardinality analysis used by the mgmt cost-distribution
// endpoint to answer the Phase-3 go/no-go question (is the cost distribution
// bimodal? does fast-check delay inflate under a slow backlog?).
//
// It is intentionally NOT a Prometheus time series: it is an on-demand
// aggregate query, not per-job labels.
package costdist

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/uptrace/bun"
)

// DefaultThresholdMs is the candidate fast/slow boundary (ms) used when the
// caller does not specify one.
const DefaultThresholdMs = 1000

// Percentiles holds a p50/p90/p99/max summary of a single metric (ms).
type Percentiles struct {
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// Distribution is the full cost/delay distribution result.
type Distribution struct {
	// TotalJobs is the number of check_jobs rows considered.
	TotalJobs int `json:"totalJobs"`
	// ThresholdMs is the candidate fast/slow boundary used for the counts.
	ThresholdMs int `json:"thresholdMs"`
	// FastJobs is the count with cost_ewma_ms <= ThresholdMs.
	FastJobs int `json:"fastJobs"`
	// SlowJobs is the count with cost_ewma_ms > ThresholdMs.
	SlowJobs int `json:"slowJobs"`
	// CostEWMAMs is the distribution of cost_ewma_ms.
	CostEWMAMs Percentiles `json:"costEwmaMs"`
	// DelayEWMAMs is the distribution of delay_ewma_ms — probe start lateness
	// vs the user-configured scheduled_at, floored at 0. Pure telemetry (spec
	// 2026-07-01-02): it never steers claim ordering.
	DelayEWMAMs Percentiles `json:"delayEwmaMs"`
}

// Compute returns the cost/delay distribution across all check_jobs. It is
// dialect-aware: PostgreSQL uses percentile_cont(...) within group, SQLite (no
// such aggregate) fetches the values and computes percentiles in Go.
func Compute(ctx context.Context, db *bun.DB, isPostgres bool, thresholdMs int) (Distribution, error) {
	if thresholdMs <= 0 {
		thresholdMs = DefaultThresholdMs
	}

	dist := Distribution{ThresholdMs: thresholdMs}

	total, fast, err := counts(ctx, db, thresholdMs)
	if err != nil {
		return dist, err
	}

	dist.TotalJobs = total
	dist.FastJobs = fast
	dist.SlowJobs = total - fast

	if total == 0 {
		return dist, nil
	}

	if isPostgres {
		if err := computePostgres(ctx, db, &dist); err != nil {
			return dist, err
		}

		return dist, nil
	}

	if err := computePortable(ctx, db, &dist); err != nil {
		return dist, err
	}

	return dist, nil
}

// counts returns the total job count and the count at or below the threshold.
func counts(ctx context.Context, db *bun.DB, thresholdMs int) (int, int, error) {
	var row struct {
		Total int `bun:"total"`
		Fast  int `bun:"fast"`
	}

	err := db.NewSelect().
		TableExpr("check_jobs").
		ColumnExpr("count(*) AS total").
		ColumnExpr("count(*) FILTER (WHERE cost_ewma_ms <= ?) AS fast", thresholdMs).
		Scan(ctx, &row)
	if err == nil {
		return row.Total, row.Fast, nil
	}

	// SQLite has no FILTER clause in older builds; fall back to a portable form.
	var portable struct {
		Total int `bun:"total"`
		Fast  int `bun:"fast"`
	}

	if err2 := db.NewSelect().
		TableExpr("check_jobs").
		ColumnExpr("count(*) AS total").
		ColumnExpr("sum(CASE WHEN cost_ewma_ms <= ? THEN 1 ELSE 0 END) AS fast", thresholdMs).
		Scan(ctx, &portable); err2 != nil {
		return 0, 0, fmt.Errorf("count check_jobs: %w", err2)
	}

	return portable.Total, portable.Fast, nil
}

// computePostgres fills the percentile fields using percentile_cont.
func computePostgres(ctx context.Context, db *bun.DB, dist *Distribution) error {
	var row struct {
		CostP50  float64 `bun:"cost_p50"`
		CostP90  float64 `bun:"cost_p90"`
		CostP99  float64 `bun:"cost_p99"`
		CostMax  float64 `bun:"cost_max"`
		DelayP50 float64 `bun:"delay_p50"`
		DelayP90 float64 `bun:"delay_p90"`
		DelayP99 float64 `bun:"delay_p99"`
		DelayMax float64 `bun:"delay_max"`
	}

	err := db.NewSelect().
		TableExpr("check_jobs").
		ColumnExpr("percentile_cont(0.5) within group (order by cost_ewma_ms) AS cost_p50").
		ColumnExpr("percentile_cont(0.9) within group (order by cost_ewma_ms) AS cost_p90").
		ColumnExpr("percentile_cont(0.99) within group (order by cost_ewma_ms) AS cost_p99").
		ColumnExpr("max(cost_ewma_ms) AS cost_max").
		ColumnExpr("percentile_cont(0.5) within group (order by delay_ewma_ms) AS delay_p50").
		ColumnExpr("percentile_cont(0.9) within group (order by delay_ewma_ms) AS delay_p90").
		ColumnExpr("percentile_cont(0.99) within group (order by delay_ewma_ms) AS delay_p99").
		ColumnExpr("max(delay_ewma_ms) AS delay_max").
		Scan(ctx, &row)
	if err != nil {
		return fmt.Errorf("percentile query: %w", err)
	}

	dist.CostEWMAMs = Percentiles{P50: row.CostP50, P90: row.CostP90, P99: row.CostP99, Max: row.CostMax}
	dist.DelayEWMAMs = Percentiles{P50: row.DelayP50, P90: row.DelayP90, P99: row.DelayP99, Max: row.DelayMax}

	return nil
}

// computePortable fetches the values and computes percentiles in Go. Used for
// SQLite, which has no percentile_cont aggregate.
func computePortable(ctx context.Context, db *bun.DB, dist *Distribution) error {
	var rows []struct {
		Cost  float64 `bun:"cost_ewma_ms"`
		Delay float64 `bun:"delay_ewma_ms"`
	}

	if err := db.NewSelect().
		TableExpr("check_jobs").
		ColumnExpr("cost_ewma_ms").
		ColumnExpr("delay_ewma_ms").
		Scan(ctx, &rows); err != nil {
		return fmt.Errorf("fetch ewma values: %w", err)
	}

	costs := make([]float64, len(rows))
	delays := make([]float64, len(rows))

	for i := range rows {
		costs[i] = rows[i].Cost
		delays[i] = rows[i].Delay
	}

	dist.CostEWMAMs = percentilesOf(costs)
	dist.DelayEWMAMs = percentilesOf(delays)

	return nil
}

// percentilesOf computes p50/p90/p99/max over the given values, matching
// PostgreSQL's percentile_cont (continuous, linear-interpolating) semantics.
func percentilesOf(values []float64) Percentiles {
	if len(values) == 0 {
		return Percentiles{}
	}

	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	return Percentiles{
		P50: percentileCont(sorted, 0.5),
		P90: percentileCont(sorted, 0.9),
		P99: percentileCont(sorted, 0.99),
		Max: sorted[len(sorted)-1],
	}
}

// percentileCont computes the continuous percentile with linear interpolation,
// matching PostgreSQL's percentile_cont. `sorted` must be ascending and
// non-empty. `quantile` is in [0,1].
func percentileCont(sorted []float64, quantile float64) float64 {
	count := len(sorted)
	if count == 1 {
		return sorted[0]
	}

	// PostgreSQL: rank = quantile * (count - 1); interpolate floor↔ceil.
	rank := quantile * float64(count-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))

	if lower == upper {
		return sorted[lower]
	}

	frac := rank - float64(lower)

	return sorted[lower] + frac*(sorted[upper]-sorted[lower])
}
