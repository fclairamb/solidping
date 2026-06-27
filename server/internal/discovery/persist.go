package discovery

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// UpsertDiscoveredChecks inserts or updates the given suggested-check rows for a
// scan, one DiscoveredCheck per SuggestedCheck. It upserts on
// (organization_uid, source, group_key, slug) so a re-scan updates rows in place
// rather than duplicating them.
//
// It preserves the per-item log-and-continue resilience contract: a single bad
// row is logged and skipped, never aborting the whole run. It returns an error
// only for a failure that is not row-specific.
func UpsertDiscoveredChecks(
	ctx context.Context,
	db *bun.DB,
	orgUID, jobUID string,
	source models.DiscoverySource,
	rows []SuggestedCheck,
	log *slog.Logger,
) error {
	if log == nil {
		log = slog.Default()
	}

	_, isPostgres := db.Dialect().(*pgdialect.Dialect)

	for i := range rows {
		row := rows[i]

		check := models.NewDiscoveredCheck(
			orgUID, jobUID, source,
			row.GroupKey, row.GroupLabel, row.Name, row.Slug, row.Type,
			row.Config, row.Metadata,
		)

		if err := upsertOne(ctx, db, check, isPostgres); err != nil {
			log.WarnContext(ctx, "failed to upsert discovered check",
				"group_key", row.GroupKey, "slug", row.Slug, "error", err)
			// Continue with other rows; don't abort the whole scan.
		}
	}

	return nil
}

// upsertOne upserts a single DiscoveredCheck on the group identity key.
func upsertOne(ctx context.Context, db *bun.DB, check *models.DiscoveredCheck, isPostgres bool) error {
	conflict := "CONFLICT (organization_uid, source, group_key, slug) " +
		"WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL DO UPDATE"

	excluded := "EXCLUDED"
	if !isPostgres {
		excluded = "excluded"
	}

	_, err := db.NewInsert().
		Model(check).
		On(conflict).
		Set("job_uid = " + excluded + ".job_uid").
		Set("group_label = " + excluded + ".group_label").
		Set("name = " + excluded + ".name").
		Set("type = " + excluded + ".type").
		Set("config = " + excluded + ".config").
		Set("metadata = " + excluded + ".metadata").
		Set("discovered_at = " + excluded + ".discovered_at").
		Set("updated_at = " + excluded + ".updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("upsert discovered check: %w", err)
	}

	return nil
}
