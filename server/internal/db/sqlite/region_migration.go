package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// jsonRegionsGuard is the SQLite-only precondition every regions predicate
// needs: the column is a JSON text array here (Postgres has a real text[]), so
// a NULL or malformed value must be excluded before json_each/json_array_length
// touch it.
const jsonRegionsGuard = "c.regions IS NOT NULL AND json_valid(c.regions) AND json_type(c.regions) = 'array'"

// ListCheckJobsByRegion returns every check_job carrying the given region
// slug, across all organizations. See db.Service for the full contract.
func (s *Service) ListCheckJobsByRegion(ctx context.Context, region string) ([]*models.CheckJob, error) {
	var jobs []*models.CheckJob

	if err := s.db.NewSelect().
		Model(&jobs).
		Where("region = ?", region).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list check jobs by region: %w", err)
	}

	return jobs, nil
}

// ListChecksReferencingRegion returns every non-deleted check that names the
// region in `checks.regions` OR owns a check_jobs row carrying it, across all
// organizations. See db.Service for the full contract.
func (s *Service) ListChecksReferencingRegion(ctx context.Context, region string) ([]*models.Check, error) {
	var uids []string

	if err := s.db.NewSelect().
		ColumnExpr("DISTINCT c.uid").
		TableExpr("checks AS c").
		Where("c.deleted_at IS NULL").
		Where(
			"((("+jsonRegionsGuard+") AND EXISTS ("+
				"SELECT 1 FROM json_each(c.regions) je WHERE je.value = ?))"+
				" OR EXISTS (SELECT 1 FROM check_jobs cj WHERE cj.check_uid = c.uid AND cj.region = ?))",
			region, region,
		).
		Scan(ctx, &uids); err != nil {
		return nil, fmt.Errorf("list checks referencing region: %w", err)
	}

	return s.loadChecksByUIDs(ctx, uids)
}

// ListChecksWithStaleJobRegions returns enabled, non-deleted checks whose
// check_jobs no longer line up with `checks.regions`. Mirrors the Postgres
// twin's shape so the startup reconcile behaves identically on both backends.
// See db.Service for the full contract.
func (s *Service) ListChecksWithStaleJobRegions(ctx context.Context) ([]*models.Check, error) {
	var uids []string

	// (a) a job sitting in a region the check no longer declares. Guarded on a
	// non-empty regions array so a legitimately region-less check — which owns
	// exactly one job with a NULL region — is never dragged in.
	if err := s.db.NewSelect().
		ColumnExpr("DISTINCT cj.check_uid").
		TableExpr("check_jobs AS cj").
		Join("JOIN checks AS c ON c.uid = cj.check_uid").
		Where("c.deleted_at IS NULL").
		Where("c.enabled = ?", true).
		Where(jsonRegionsGuard).
		Where("json_array_length(c.regions) > 0").
		Where("(cj.region IS NULL OR NOT EXISTS ("+
			"SELECT 1 FROM json_each(c.regions) je WHERE je.value = cj.region))").
		Scan(ctx, &uids); err != nil {
		return nil, fmt.Errorf("list checks with stale job regions: %w", err)
	}

	// (b) the symmetric hole: a region the check declares that has no job at
	// all. Same drift, opposite side — after a rename both show up together.
	var missing []string

	if err := s.db.NewSelect().
		ColumnExpr("DISTINCT c.uid").
		TableExpr("checks AS c").
		Join("JOIN json_each(c.regions) AS je").
		Where("c.deleted_at IS NULL").
		Where("c.enabled = ?", true).
		Where(jsonRegionsGuard).
		Where("NOT EXISTS (SELECT 1 FROM check_jobs cj "+
			"WHERE cj.check_uid = c.uid AND cj.region = je.value)").
		Scan(ctx, &missing); err != nil {
		return nil, fmt.Errorf("list checks missing region jobs: %w", err)
	}

	return s.loadChecksByUIDs(ctx, mergeUIDs(uids, missing))
}

// MigrateCheckRegionSlug rewrites `checks.regions` in ONE transaction,
// replacing `from` with `to` everywhere it appears. See db.Service for the
// full contract.
func (s *Service) MigrateCheckRegionSlug(ctx context.Context, from, to string) ([]*models.Check, error) {
	var updated []*models.Check

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var uids []string

		if err := tx.NewSelect().
			ColumnExpr("DISTINCT c.uid").
			TableExpr("checks AS c").
			Where("c.deleted_at IS NULL").
			Where(jsonRegionsGuard).
			Where("EXISTS (SELECT 1 FROM json_each(c.regions) je WHERE je.value = ?)", from).
			Order("c.uid").
			Scan(ctx, &uids); err != nil {
			return fmt.Errorf("select checks to migrate: %w", err)
		}

		if len(uids) == 0 {
			return nil
		}

		var rows []*models.Check
		if err := tx.NewSelect().
			Model(&rows).
			Where("uid IN (?)", bun.In(uids)).
			Scan(ctx); err != nil {
			return fmt.Errorf("load checks to migrate: %w", err)
		}

		now := time.Now()

		for _, check := range rows {
			next, changed := models.MigrateRegionList(check.Regions, from, to)
			if !changed {
				continue
			}

			encoded, jsonErr := json.Marshal(next)
			if jsonErr != nil {
				return fmt.Errorf("marshal regions: %w", jsonErr)
			}

			if _, err := tx.NewUpdate().
				Model((*models.Check)(nil)).
				Set("regions = ?", string(encoded)).
				Set("updated_at = ?", now).
				Where("uid = ?", check.UID).
				Exec(ctx); err != nil {
				return fmt.Errorf("update check regions: %w", err)
			}

			check.Regions = next
			updated = append(updated, check)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return updated, nil
}

// loadChecksByUIDs hydrates the check rows for a UID set, dropping soft-deleted
// rows. Returns nil for an empty set rather than issuing a pointless query.
func (s *Service) loadChecksByUIDs(ctx context.Context, uids []string) ([]*models.Check, error) {
	if len(uids) == 0 {
		return nil, nil
	}

	var checks []*models.Check
	if err := s.db.NewSelect().
		Model(&checks).
		Where("uid IN (?)", bun.In(uids)).
		Where("deleted_at IS NULL").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("load checks by uid: %w", err)
	}

	return checks, nil
}

// mergeUIDs unions two UID slices, preserving first-seen order.
func mergeUIDs(first, second []string) []string {
	seen := make(map[string]bool, len(first)+len(second))
	out := make([]string, 0, len(first)+len(second))

	for _, list := range [][]string{first, second} {
		for _, uid := range list {
			if seen[uid] {
				continue
			}

			seen[uid] = true

			out = append(out, uid)
		}
	}

	return out
}
