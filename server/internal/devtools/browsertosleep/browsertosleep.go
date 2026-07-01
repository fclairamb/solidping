// Package browsertosleep implements a guarded, reversible dev/admin harness that
// converts every `browser` check in the current instance into a synthetic
// `sleep` check. Each converted check's sleep_ms is seeded from that check's
// current cost_ewma_ms (fallback to a default when there is no cost signal yet),
// so the synthetic load mirrors the cost distribution of the real browser load
// it replaces — without headless Chrome's CPU/RAM/nondeterminism.
//
// This is an OPERATIONAL/DATA step, never a schema migration. It is scoped to
// the running instance and reversible via a DB reset in dev. It is idempotent:
// re-running converts any remaining browser checks and is a no-op when none
// remain.
package browsertosleep

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// DefaultSleepMs is the sleep_ms seeded when a browser check has no cost
	// signal yet (cost_ewma_ms == 0).
	DefaultSleepMs = 1000

	// maxSleepMs mirrors the sleep checker's configured cap; a seeded sleep is
	// clamped to it so a pathological cost EWMA cannot exceed the checker bound.
	maxSleepMs = 120000

	browserType = string(checkerdef.CheckTypeBrowser)
	sleepType   = string(checkerdef.CheckTypeSleep)
)

// Result summarizes a conversion run.
type Result struct {
	// ChecksConverted is the number of browser checks turned into sleep checks.
	ChecksConverted int
	// JobsConverted is the number of check_jobs rows retyped to sleep.
	JobsConverted int
}

// Converter converts browser checks to sleep checks against a db.Service.
type Converter struct {
	db     db.Service
	logger *slog.Logger
}

// New builds a Converter over the given db service.
func New(dbSvc db.Service, logger *slog.Logger) *Converter {
	if logger == nil {
		logger = slog.Default()
	}

	return &Converter{db: dbSvc, logger: logger}
}

// Run converts every browser check across every organization into a sleep
// check, seeding sleep_ms from each check's current cost_ewma_ms. It is
// idempotent: a second run finds no browser checks and returns a zero Result.
func (c *Converter) Run(ctx context.Context) (Result, error) {
	var result Result

	orgs, err := c.db.ListOrganizations(ctx)
	if err != nil {
		return result, fmt.Errorf("list organizations: %w", err)
	}

	for _, org := range orgs {
		orgResult, orgErr := c.convertOrg(ctx, org.UID)
		if orgErr != nil {
			return result, fmt.Errorf("convert org %s: %w", org.UID, orgErr)
		}

		result.ChecksConverted += orgResult.ChecksConverted
		result.JobsConverted += orgResult.JobsConverted
	}

	c.logger.InfoContext(ctx, "browser-to-sleep conversion complete",
		"checksConverted", result.ChecksConverted,
		"jobsConverted", result.JobsConverted)

	return result, nil
}

// convertOrg converts all browser checks within a single organization.
func (c *Converter) convertOrg(ctx context.Context, orgUID string) (Result, error) {
	var result Result

	// ListChecks with a "browser" filter is not directly supported, so we fetch
	// the org's checks and filter in-code. Limit 0 = no limit. "all" includes
	// internal checks. The total count is unused here.
	checks, _, err := c.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{
		Internal: ptr("all"),
	})
	if err != nil {
		return result, fmt.Errorf("list checks: %w", err)
	}

	for i := range checks {
		check := checks[i]
		if check.Type != browserType {
			continue
		}

		jobs, convErr := c.convertCheck(ctx, check)
		if convErr != nil {
			return result, convErr
		}

		result.ChecksConverted++
		result.JobsConverted += jobs
	}

	return result, nil
}

// convertCheck retypes a single browser check (and its check_jobs) to sleep,
// seeding sleep_ms from the check's current cost_ewma_ms.
func (c *Converter) convertCheck(ctx context.Context, check *models.Check) (int, error) {
	sleepMs := c.seedSleepMs(ctx, check.UID)

	newType := sleepType
	newConfig := models.JSONMap{"sleep_ms": sleepMs}

	// Update the check definition itself. This also clears the encrypted
	// browser config envelope (browser checks have no secrets, but be explicit).
	update := &models.CheckUpdate{
		Type:               &newType,
		Config:             &newConfig,
		ClearConfigPrivate: true,
	}
	if err := c.db.UpdateCheck(ctx, check.UID, update); err != nil {
		return 0, fmt.Errorf("update check %s: %w", check.UID, err)
	}

	// Retype the materialized check_jobs so the scheduler runs sleep. There is
	// no Type field on CheckJobUpdate, so update the rows directly. Keep the
	// same UID/scheduling state; only type + config change.
	res, err := c.db.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("type = ?", sleepType).
		Set("config = ?", newConfig).
		Set("config_private = NULL").
		Set("config_private_keys = NULL").
		Set("encrypted = ?", false).
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("retype check_jobs for %s: %w", check.UID, err)
	}

	jobsAffected := 0
	if n, rowsErr := res.RowsAffected(); rowsErr == nil {
		jobsAffected = int(n)
	}

	c.logger.InfoContext(ctx, "converted browser check to sleep",
		"checkUID", check.UID,
		"sleepMs", sleepMs,
		"jobs", jobsAffected)

	return jobsAffected, nil
}

// seedSleepMs derives a sleep_ms for a check from its check_jobs' cost_ewma_ms.
// It uses the maximum cost_ewma_ms across the check's jobs (the check runs as
// slowly as its slowest region), rounds to the nearest ms, clamps to
// [1, maxSleepMs], and falls back to DefaultSleepMs when there is no cost
// signal (all zero / no jobs).
func (c *Converter) seedSleepMs(ctx context.Context, checkUID string) int {
	jobs, err := c.db.ListCheckJobsByCheckUID(ctx, checkUID)
	if err != nil {
		c.logger.WarnContext(ctx, "failed to read check_jobs for cost seed; using default",
			"checkUID", checkUID, "error", err)

		return DefaultSleepMs
	}

	maxCost := 0.0

	for i := range jobs {
		if jobs[i].CostEWMAMs > maxCost {
			maxCost = jobs[i].CostEWMAMs
		}
	}

	return seedFromCost(maxCost)
}

// seedFromCost turns a cost_ewma_ms value into a bounded sleep_ms.
func seedFromCost(costEWMAMs float64) int {
	if costEWMAMs <= 0 {
		return DefaultSleepMs
	}

	ms := int(math.Round(costEWMAMs))

	if ms < 1 {
		ms = 1
	}

	if ms > maxSleepMs {
		ms = maxSleepMs
	}

	return ms
}

func ptr[T any](v T) *T { return &v }
