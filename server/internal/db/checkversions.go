package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/checkversion"
	"github.com/fclairamb/solidping/server/internal/db/dbctx"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Check version history (spec 2026-10-03-06). Both engines delegate here:
// every query is plain bun, identical on PostgreSQL and SQLite, and keeping a
// single copy is what keeps the two engines from recording differently.

// ErrCheckVersionNotProposed is returned when deciding a version that is not
// a pending proposal.
var ErrCheckVersionNotProposed = errors.New("check version is not a proposal")

// eventPayloadVersionKey is the check.updated payload key carrying the
// version number.
const eventPayloadVersionKey = "version"

// CheckDefinitionTouched reports whether an update writes at least one column
// a check version snapshot holds. Runtime-only writes (status, clocks, the
// degraded evaluator's stamp) and secret-only writes skip the recorder
// entirely, so the hot worker path pays nothing for the history.
func CheckDefinitionTouched(update *models.CheckUpdate) bool {
	return update.CheckGroupUID != nil || update.Name != nil || update.Slug != nil ||
		update.Description != nil || update.Type != nil || update.Config != nil ||
		update.Enabled != nil || update.Period != nil || placementTouched(update)
}

func placementTouched(update *models.CheckUpdate) bool {
	return update.Regions != nil || update.Placement != nil ||
		update.RegionCount != nil || update.ClearRegionCount ||
		update.RegionPool != nil || update.ClearRegionPool ||
		update.FailQuorum != nil || update.ClearFailQuorum
}

// lockCheckForVersion serializes version numbering per check. PostgreSQL
// takes the check row lock; SQLite has a single writer already.
func lockCheckForVersion(ctx context.Context, idb bun.IDB, checkUID string) error {
	if idb.Dialect().Name() != dialect.PG {
		return nil
	}

	var uid string

	err := idb.NewSelect().
		Table("checks").
		Column("uid").
		Where("uid = ?", checkUID).
		For("UPDATE").
		Scan(ctx, &uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("locking check for versioning: %w", err)
	}

	return nil
}

// currentSnapshot loads the check and its labels and builds the snapshot.
// Returns nil when the check does not exist or is deleted.
func currentSnapshot(
	ctx context.Context, idb bun.IDB, checkUID string,
) (*models.Check, *checkversion.Snapshot, error) {
	check := new(models.Check)

	err := idb.NewSelect().
		Model(check).
		Where("uid = ?", checkUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("loading check for versioning: %w", err)
	}

	var labels []*models.Label

	err = idb.NewSelect().
		Model(&labels).
		Join("JOIN check_labels cl ON cl.label_uid = label.uid").
		Where("cl.check_uid = ?", checkUID).
		Where("label.deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("loading check labels for versioning: %w", err)
	}

	return check, checkversion.Build(check, checkversion.LabelMap(labels)), nil
}

func nextCheckVersion(ctx context.Context, idb bun.IDB, checkUID string) (int, error) {
	var maxVersion sql.NullInt64

	err := idb.NewSelect().
		Model((*models.CheckVersion)(nil)).
		ColumnExpr("MAX(version)").
		Where("check_uid = ?", checkUID).
		Scan(ctx, &maxVersion)
	if err != nil {
		return 0, fmt.Errorf("reading the latest check version: %w", err)
	}

	return int(maxVersion.Int64) + 1, nil
}

// LatestAppliedCheckVersion returns the newest applied version, or nil.
func LatestAppliedCheckVersion(ctx context.Context, idb bun.IDB, checkUID string) (*models.CheckVersion, error) {
	version := new(models.CheckVersion)

	err := idb.NewSelect().
		Model(version).
		Where("check_uid = ?", checkUID).
		Where("status = ?", models.CheckVersionStatusApplied).
		OrderExpr("version DESC").
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no version yet is not an error
	}

	if err != nil {
		return nil, fmt.Errorf("reading the latest applied check version: %w", err)
	}

	return version, nil
}

func hasCheckVersions(ctx context.Context, idb bun.IDB, checkUID string) (bool, error) {
	exists, err := idb.NewSelect().
		Model((*models.CheckVersion)(nil)).
		Where("check_uid = ?", checkUID).
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("checking for check versions: %w", err)
	}

	return exists, nil
}

// EnsureCheckVersionBaseline records the check's CURRENT definition as
// version 1 when it has no version at all, i.e. a check created before the
// history existed. Called before a write, so the first edit of such a check
// still has a "before" to diff and restore against.
func EnsureCheckVersionBaseline(ctx context.Context, idb bun.IDB, checkUID string) error {
	if err := lockCheckForVersion(ctx, idb, checkUID); err != nil {
		return err
	}

	exists, err := hasCheckVersions(ctx, idb, checkUID)
	if err != nil || exists {
		return err
	}

	check, snap, err := currentSnapshot(ctx, idb, checkUID)
	if err != nil || check == nil {
		return err
	}

	row, err := newVersionRow(check, snap)
	if err != nil {
		return err
	}

	row.Version = 1
	reason := "initial version"
	row.Reason = &reason

	if _, err := idb.NewInsert().Model(row).Exec(ctx); err != nil {
		return fmt.Errorf("inserting the baseline check version: %w", err)
	}

	return nil
}

func newVersionRow(check *models.Check, snap *checkversion.Snapshot) (*models.CheckVersion, error) {
	stored, err := snap.ToJSONMap()
	if err != nil {
		return nil, err
	}

	hash, err := snap.Hash()
	if err != nil {
		return nil, err
	}

	return models.NewCheckVersion(check.OrganizationUID, check.UID, stored, hash), nil
}

// RecordCheckVersion snapshots the check after a write and records a new
// applied version when the snapshot differs from the latest applied one. Run
// it inside the write's transaction. Writes made under the same change source
// (dbctx.ChangeSource) amend the version that change already recorded.
//
// Emits check.updated with the version number for every new version past the
// first (check.created is emitted by the create handlers).
func RecordCheckVersion(ctx context.Context, idb bun.IDB, checkUID string) (*models.CheckVersion, error) {
	if err := lockCheckForVersion(ctx, idb, checkUID); err != nil {
		return nil, err
	}

	check, snap, err := currentSnapshot(ctx, idb, checkUID)
	if err != nil || check == nil {
		return nil, err
	}

	row, err := newVersionRow(check, snap)
	if err != nil {
		return nil, err
	}

	source := dbctx.ChangeSourceFromContext(ctx)

	handled, err := recordAmendment(ctx, idb, source, check, row)
	if err != nil || handled {
		return nil, err
	}

	latest, err := LatestAppliedCheckVersion(ctx, idb, checkUID)
	if err != nil {
		return nil, err
	}

	if latest != nil && latest.SnapshotHash == row.SnapshotHash {
		return nil, nil //nolint:nilnil // nothing changed, nothing recorded
	}

	if err := insertNewVersion(ctx, idb, source, check, row); err != nil {
		return nil, err
	}

	return row, nil
}

func insertNewVersion(
	ctx context.Context, idb bun.IDB, source *dbctx.ChangeSource, check *models.Check, row *models.CheckVersion,
) error {
	version, err := nextCheckVersion(ctx, idb, check.UID)
	if err != nil {
		return err
	}

	row.Version = version
	applyChangeSource(row, source)

	if _, err := idb.NewInsert().Model(row).Exec(ctx); err != nil {
		return fmt.Errorf("inserting check version: %w", err)
	}

	source.SetRecordedVersion(check.UID, row.Version)

	if err := pruneCheckVersions(ctx, idb, check.UID); err != nil {
		return err
	}

	if row.Version > 1 {
		return insertCheckUpdatedEvent(ctx, idb, check, row.Version)
	}

	return nil
}

// amendOutcome says what amendRecordedVersion did.
type amendOutcome int

const (
	// amendNone: no row to amend, the caller records a new version.
	amendNone amendOutcome = iota
	// amendedApplied: a version this change already recorded was rewritten.
	amendedApplied
	// amendedProposal: a proposal being approved became the applied version.
	amendedProposal
)

// recordAmendment amends the version this change already recorded, and emits
// check.updated when that version is an approved proposal going live.
// Reports whether the write was handled.
func recordAmendment(
	ctx context.Context, idb bun.IDB, source *dbctx.ChangeSource, check *models.Check, row *models.CheckVersion,
) (bool, error) {
	outcome, err := amendRecordedVersion(ctx, idb, source, row)
	if err != nil {
		return false, err
	}

	if outcome == amendedProposal {
		version, _ := source.RecordedVersion(check.UID)
		if eventErr := insertCheckUpdatedEvent(ctx, idb, check, version); eventErr != nil {
			return false, eventErr
		}
	}

	return outcome != amendNone, nil
}

// amendRecordedVersion rewrites the version this change already recorded for
// the check (a second write in the same request, or the proposal being
// approved). amendNone when there is none, or when that row is gone (its
// transaction rolled back), so the caller records a new one.
func amendRecordedVersion(
	ctx context.Context, idb bun.IDB, source *dbctx.ChangeSource, row *models.CheckVersion,
) (amendOutcome, error) {
	version, ok := source.RecordedVersion(row.CheckUID)
	if !ok {
		return amendNone, nil
	}

	for _, fromStatus := range []models.CheckVersionStatus{
		models.CheckVersionStatusProposed, models.CheckVersionStatusApplied,
	} {
		res, updateErr := idb.NewUpdate().
			Model((*models.CheckVersion)(nil)).
			Set("snapshot = ?", row.Snapshot).
			Set("snapshot_hash = ?", row.SnapshotHash).
			Set("status = ?", models.CheckVersionStatusApplied).
			Set("updated_at = ?", time.Now()).
			Where("check_uid = ?", row.CheckUID).
			Where("version = ?", version).
			Where("status = ?", fromStatus).
			Exec(ctx)
		if updateErr != nil {
			return amendNone, fmt.Errorf("amending check version: %w", updateErr)
		}

		affected, affErr := res.RowsAffected()
		if affErr != nil {
			return amendNone, fmt.Errorf("amending check version: %w", affErr)
		}

		if affected > 0 {
			if fromStatus == models.CheckVersionStatusProposed {
				return amendedProposal, nil
			}

			return amendedApplied, nil
		}
	}

	return amendNone, nil
}

func applyChangeSource(row *models.CheckVersion, source *dbctx.ChangeSource) {
	if source == nil {
		return
	}

	if source.Origin != "" {
		row.Origin = models.CheckVersionOrigin(source.Origin)
	}

	if source.UserUID != "" {
		uid := source.UserUID
		row.ActorUserUID = &uid
	}

	if source.Reason != "" {
		reason := source.Reason
		row.Reason = &reason
	}

	if source.BaseVersion != nil {
		base := *source.BaseVersion
		row.BaseVersion = &base
	}
}

// pruneCheckVersions keeps the newest models.CheckVersionRetention applied
// versions of the check. Proposals and rejections are not counted.
func pruneCheckVersions(ctx context.Context, idb bun.IDB, checkUID string) error {
	var cutoff []int

	err := idb.NewSelect().
		Model((*models.CheckVersion)(nil)).
		Column("version").
		Where("check_uid = ?", checkUID).
		Where("status = ?", models.CheckVersionStatusApplied).
		OrderExpr("version DESC").
		Offset(models.CheckVersionRetention).
		Limit(1).
		Scan(ctx, &cutoff)
	if err != nil {
		return fmt.Errorf("finding check versions to prune: %w", err)
	}

	if len(cutoff) == 0 {
		return nil
	}

	_, err = idb.NewDelete().
		Model((*models.CheckVersion)(nil)).
		Where("check_uid = ?", checkUID).
		Where("status = ?", models.CheckVersionStatusApplied).
		Where("version <= ?", cutoff[0]).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("pruning check versions: %w", err)
	}

	return nil
}

func insertCheckUpdatedEvent(ctx context.Context, idb bun.IDB, check *models.Check, version int) error {
	name := check.UID
	if check.Name != nil && *check.Name != "" {
		name = *check.Name
	}

	event := audit.NewEvent(ctx, check.OrganizationUID, models.EventTypeCheckUpdated,
		audit.Target{Type: "check", UID: check.UID, Name: name},
		models.JSONMap{
			"check_uid":            check.UID,
			"check_slug":           check.Slug,
			"check_name":           check.Name,
			"check_type":           check.Type,
			eventPayloadVersionKey: version,
		})
	event.CheckUID = &check.UID

	if _, err := idb.NewInsert().Model(event).Exec(ctx); err != nil {
		return fmt.Errorf("inserting check.updated event: %w", err)
	}

	return nil
}

// CreateCheckVersionProposal inserts a proposed version of a check, numbered
// after every existing version. The caller fills the snapshot, base version,
// origin, actor and reason; status and number are set here.
func CreateCheckVersionProposal(ctx context.Context, idb bun.IDB, row *models.CheckVersion) error {
	// A check created before the history existed gets its baseline first, so
	// the proposal has an applied version to be based on and diffed against.
	if err := EnsureCheckVersionBaseline(ctx, idb, row.CheckUID); err != nil {
		return err
	}

	if row.BaseVersion == nil {
		latest, err := LatestAppliedCheckVersion(ctx, idb, row.CheckUID)
		if err != nil {
			return err
		}

		if latest != nil {
			base := latest.Version
			row.BaseVersion = &base
		}
	}

	snap, err := checkversion.FromJSONMap(row.Snapshot)
	if err != nil {
		return err
	}

	if row.Snapshot, err = snap.ToJSONMap(); err != nil {
		return err
	}

	if row.SnapshotHash, err = snap.Hash(); err != nil {
		return err
	}

	if row.Version, err = nextCheckVersion(ctx, idb, row.CheckUID); err != nil {
		return err
	}

	row.Status = models.CheckVersionStatusProposed

	if _, err := idb.NewInsert().Model(row).Exec(ctx); err != nil {
		return fmt.Errorf("inserting check version proposal: %w", err)
	}

	return nil
}

// ListCheckVersions returns a check's versions, newest first.
func ListCheckVersions(
	ctx context.Context, idb bun.IDB, checkUID string, limit int,
) ([]*models.CheckVersion, error) {
	var versions []*models.CheckVersion

	query := idb.NewSelect().
		Model(&versions).
		Where("check_uid = ?", checkUID).
		OrderExpr("version DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("listing check versions: %w", err)
	}

	return versions, nil
}

// GetCheckVersion returns one version of a check, or sql.ErrNoRows.
func GetCheckVersion(ctx context.Context, idb bun.IDB, checkUID string, version int) (*models.CheckVersion, error) {
	row := new(models.CheckVersion)

	err := idb.NewSelect().
		Model(row).
		Where("check_uid = ?", checkUID).
		Where("version = ?", version).
		Scan(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // callers test for sql.ErrNoRows
	}

	return row, nil
}

// DecideCheckVersion moves a proposal to applied or rejected and records who
// decided. ErrCheckVersionNotProposed when the row is not a pending proposal.
func DecideCheckVersion(
	ctx context.Context, idb bun.IDB, checkUID string, version int,
	status models.CheckVersionStatus, userUID string,
) error {
	now := time.Now()

	query := idb.NewUpdate().
		Model((*models.CheckVersion)(nil)).
		Set("status = ?", status).
		Set("decided_at = ?", now).
		Set("updated_at = ?", now).
		Where("check_uid = ?", checkUID).
		Where("version = ?", version)

	if userUID != "" {
		query = query.Set("decided_by_user_uid = ?", userUID)
	}

	// An approval that already went through the recorder flipped the row to
	// applied; stamping the decision on it is still allowed.
	if status == models.CheckVersionStatusApplied {
		query = query.Where("status IN (?)", bun.List([]models.CheckVersionStatus{
			models.CheckVersionStatusProposed, models.CheckVersionStatusApplied,
		}))
	} else {
		query = query.Where("status = ?", models.CheckVersionStatusProposed)
	}

	res, err := query.Exec(ctx)
	if err != nil {
		return fmt.Errorf("deciding check version: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("deciding check version: %w", err)
	}

	if affected == 0 {
		return ErrCheckVersionNotProposed
	}

	return nil
}
