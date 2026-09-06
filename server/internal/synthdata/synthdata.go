// Package synthdata generates synthetic historical check results.
//
// It exists so two very different callers can share one generator: the
// test-only `POST /api/v1/test/generate-data` endpoint (which is how this code
// started life, in internal/handlers/testapi), and the public live demo's
// one-shot 30-day backfill at seed time (spec 2026-09-06-02), which needs the
// same shapes but has no HTTP request, no handler and no test-mode gate.
//
// Deliberately free of HTTP, of the job system and of anything testapi-shaped:
// it takes a period, a window and a failure model, and writes rows.
package synthdata

import (
	"context"
	"math"
	"math/rand"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ResultWriter is the slice of the database service this package needs.
// Narrow on purpose: a generator that could reach the whole db.Service would
// invite it to start creating checks and orgs too, which is the caller's job.
type ResultWriter interface {
	CreateResult(ctx context.Context, result *models.Result) error
}

// Options describes the history to synthesize for ONE check.
type Options struct {
	// OrganizationUID and CheckUID identify the rows written.
	OrganizationUID string
	CheckUID        string
	// Region is stamped on every generated row. Empty means "default".
	Region string
	// Start is the beginning of the synthesized window; End its exclusive end
	// (zero means "now").
	Start time.Time
	End   time.Time
	// Period is the interval between generated results. Must be > 0.
	Period time.Duration
	// AvgDurationMs is the mean response time; each sample is drawn around it
	// with a 20% standard deviation.
	AvgDurationMs float64
	// FailureRate is the fraction of time spent down, in [0, 1]. Zero means a
	// perfectly healthy history.
	FailureRate float64
	// FailureBurstSec, when > 0, turns FailureRate into CLUSTERED outages of
	// this length rather than independent per-sample coin flips — which is
	// what makes a chart look like a real incident instead of static.
	FailureBurstSec int
	// Seed makes a run reproducible. Zero uses the wall clock.
	Seed int64
	// MaxResults bounds one run. Zero means the default ceiling. It is a
	// guardrail, not a tuning knob: a caller that asks for a year of 10-second
	// samples is asking for three million inserts, and the honest answer is to
	// stop rather than to spend an hour writing them.
	MaxResults int
}

// DefaultMaxResults bounds a single Generate call. 200k rows is roughly 30 days
// at a 15-second period — comfortably more than any legitimate caller needs,
// and far short of "wedge the database".
const DefaultMaxResults = 200_000

const (
	defaultAvgDurationMs = 150.0
	durationJitterRatio  = 0.2
	minDurationMs        = 1
)

// Generate writes synthetic raw results for one check and returns how many rows
// it created. It stops early — without an error — at the MaxResults ceiling.
func Generate(ctx context.Context, writer ResultWriter, opts Options) (int, error) {
	if writer == nil || opts.Period <= 0 {
		return 0, nil
	}

	end := opts.End
	if end.IsZero() {
		end = time.Now()
	}

	region := opts.Region
	if region == "" {
		region = "default"
	}

	avg := opts.AvgDurationMs
	if avg <= 0 {
		avg = defaultAvgDurationMs
	}

	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}

	seed := opts.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}

	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // synthetic sample data, not cryptography

	count := 0

	for cursor := opts.Start; cursor.Before(end) && count < maxResults; cursor = cursor.Add(opts.Period) {
		if ctx.Err() != nil {
			return count, ctx.Err()
		}

		status, duration := Simulate(rng, opts, avg, cursor)
		statusInt := int(status)
		regionValue := region

		result := &models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: opts.OrganizationUID,
			CheckUID:        opts.CheckUID,
			PeriodType:      "raw",
			PeriodStart:     cursor,
			Region:          &regionValue,
			Status:          &statusInt,
			Duration:        &duration,
			Metrics:         make(models.JSONMap),
			Output:          make(models.JSONMap),
			CreatedAt:       cursor,
		}

		if err := writer.CreateResult(ctx, result); err != nil {
			return count, err
		}

		count++
	}

	return count, nil
}

// Simulate produces one sample's status and duration.
//
// Exported so a caller can unit-test the failure model without a database.
// With FailureBurstSec set the outage pattern is DETERMINISTIC in the sample's
// wall-clock time — the same second always produces the same verdict — which is
// what makes multi-region synthetic history agree with itself instead of
// showing three unrelated random walks.
func Simulate(rng *rand.Rand, opts Options, avgDurationMs float64, timestamp time.Time) (models.ResultStatus, float32) {
	duration := float32(avgDurationMs + rng.NormFloat64()*avgDurationMs*durationJitterRatio)
	if duration < minDurationMs {
		duration = minDurationMs
	}

	if opts.FailureRate <= 0 {
		return models.ResultStatusUp, duration
	}

	if opts.FailureBurstSec > 0 {
		cycleSec := float64(opts.FailureBurstSec) / opts.FailureRate
		posInCycle := math.Mod(float64(timestamp.Unix()), cycleSec)

		if posInCycle < float64(opts.FailureBurstSec) {
			return models.ResultStatusDown, 0
		}

		return models.ResultStatusUp, duration
	}

	if rng.Float64() < opts.FailureRate {
		return models.ResultStatusDown, 0
	}

	return models.ResultStatusUp, duration
}
