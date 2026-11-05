package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// MaxBulkUpdateChecks caps the checks one bulk update may name.
const MaxBulkUpdateChecks = 1000

// Reasons a check is left out of a bulk update.
const (
	BulkUpdateSkipNotFound = "not_found"
	BulkUpdateSkipReadOnly = "read_only"
	BulkUpdateSkipRejected = "rejected"
	BulkUpdateSkipError    = "error"
)

// bulkUpdatableFields are the PATCH fields a bulk update accepts: values that
// make sense on many checks at once and reference nothing else of the org.
// Anything else (name, slug, config, labels, checkGroupUid...) is refused, never
// silently ignored.
func bulkUpdatableFields() []string {
	return []string{
		"enabled", "resolveOpenIncidents", "period",
		"placement", "regions", "regionCount", "regionPool",
		"confirmationPeriodSeconds", "recoveryPeriodSeconds",
	}
}

// bulkPlacementFields are the fields a passive check (no region) cannot take.
func bulkPlacementFields() []string {
	return []string{"placement", "regions", "regionCount", "regionPool"}
}

var (
	errBulkNoChecks         = errors.New("checkUids: at least one check is required")
	errBulkTooManyChecks    = fmt.Errorf("checkUids: at most %d checks per request", MaxBulkUpdateChecks)
	errBulkNoChanges        = errors.New("changes: at least one field is required")
	errBulkInvalidChanges   = errors.New("changes: invalid")
	errBulkUnsupportedField = errors.New("changes: field not supported by a bulk update")
	errBulkUnknownRegion    = errors.New("changes.regions: unknown region")
	errBulkPassive          = errors.New("a passive check has no regions")
	errBulkInternal         = errors.New("internal error")
)

// IsBulkUpdateRequestError reports whether err rejects the bulk request
// itself (a 400), as opposed to one check of it.
func IsBulkUpdateRequestError(err error) bool {
	return errors.Is(err, errBulkNoChecks) || errors.Is(err, errBulkTooManyChecks) ||
		errors.Is(err, errBulkNoChanges) || errors.Is(err, errBulkInvalidChanges) ||
		errors.Is(err, errBulkUnsupportedField) || errors.Is(err, errBulkUnknownRegion) ||
		errors.Is(err, regions.ErrForeignPrivateRegion) || errors.Is(err, regions.ErrInvalidPrivateRegionSlug)
}

// BulkUpdateChecksRequest is the body of PATCH /api/v1/orgs/:org/checks.
type BulkUpdateChecksRequest struct {
	// CheckUIDs are the checks to update.
	CheckUIDs []string `json:"checkUids"`
	// Changes has the shape of a single-check PATCH body, restricted to
	// bulkUpdatableFields.
	Changes json.RawMessage `json:"changes"`
	// DryRun validates every check and reports what would be updated,
	// writing nothing.
	DryRun bool `json:"dryRun,omitempty"`
}

// BulkUpdateItem is one check updated (or that would be).
type BulkUpdateItem struct {
	UID  string  `json:"uid"`
	Slug *string `json:"slug,omitempty"`
	Name *string `json:"name,omitempty"`
}

// BulkUpdateSkip is one requested check left untouched, and why.
type BulkUpdateSkip struct {
	UID     string  `json:"uid"`
	Slug    *string `json:"slug,omitempty"`
	Reason  string  `json:"reason"`
	Message string  `json:"message,omitempty"`
}

// BulkUpdateChecksResponse is the answer of a bulk update.
type BulkUpdateChecksResponse struct {
	Data     []BulkUpdateItem            `json:"data"`
	Skipped  []BulkUpdateSkip            `json:"skipped"`
	Warnings []base.ValidationErrorField `json:"warnings,omitempty"`
	DryRun   bool                        `json:"dryRun"`
}

// BulkUpdateChecks applies req.Changes to every listed check, with exactly the
// semantics of a single-check PATCH (spec 2026-11-05-01). It is per-check: a
// check the changes cannot apply to is skipped with its reason and the others
// are still updated. Checks of other organizations are reported as not_found.
//
// A dry run validates each check against the current state, so it cannot see
// a plan limit that only the earlier checks of the same batch would exhaust.
func (s *Service) BulkUpdateChecks(
	ctx context.Context, orgSlug string, req *BulkUpdateChecksRequest,
) (*BulkUpdateChecksResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	uids := uniqueCheckUIDs(req.CheckUIDs)

	switch {
	case len(uids) == 0:
		return nil, errBulkNoChecks
	case len(uids) > MaxBulkUpdateChecks:
		return nil, errBulkTooManyChecks
	}

	changes, err := parseBulkChanges(req.Changes)
	if err != nil {
		return nil, err
	}

	if changes.Regions != nil {
		if regionErr := s.validateBulkRegions(ctx, org.UID, *changes.Regions); regionErr != nil {
			return nil, regionErr
		}
	}

	var rows []*models.Check

	if err := s.db.DB().NewSelect().
		Model(&rows).
		Where("organization_uid = ?", org.UID).
		Where("deleted_at IS NULL").
		Where("uid IN (?)", bun.List(uids)).
		Order("created_at", "uid").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list checks to update: %w", err)
	}

	// Every write folds into one version per check, attributed to the caller,
	// as a single-check PATCH does.
	ctx = withCallerChangeSource(ctx)
	touchesPlacement := touchesBulkPlacement(req.Changes)

	resp := &BulkUpdateChecksResponse{Data: []BulkUpdateItem{}, Skipped: []BulkUpdateSkip{}, DryRun: req.DryRun}
	found := make(map[string]bool, len(rows))

	for _, check := range rows {
		found[check.UID] = true

		skip, warnings := s.bulkUpdateOne(ctx, check, req.Changes, touchesPlacement, req.DryRun)
		if skip != nil {
			resp.Skipped = append(resp.Skipped, *skip)

			continue
		}

		resp.Warnings = mergeWarnings(resp.Warnings, warnings)
		resp.Data = append(resp.Data, BulkUpdateItem{UID: check.UID, Slug: check.Slug, Name: check.Name})
	}

	for _, uid := range uids {
		if !found[uid] {
			resp.Skipped = append(resp.Skipped, BulkUpdateSkip{UID: uid, Reason: BulkUpdateSkipNotFound})
		}
	}

	return resp, nil
}

// parseBulkChanges validates the changes object once for the whole batch.
func parseBulkChanges(raw json.RawMessage) (*UpdateCheckRequest, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("%w: %w", errBulkInvalidChanges, err)
	}

	if len(fields) == 0 {
		return nil, errBulkNoChanges
	}

	supported := bulkUpdatableFields()

	var unsupported []string

	for name := range fields {
		if !slices.Contains(supported, name) {
			unsupported = append(unsupported, name)
		}
	}

	if len(unsupported) > 0 {
		slices.Sort(unsupported)

		return nil, fmt.Errorf("%w: %s (supported: %s)", errBulkUnsupportedField,
			strings.Join(unsupported, ", "), strings.Join(supported, ", "))
	}

	return decodeBulkChanges(raw)
}

// decodeBulkChanges decodes a fresh request, so no check of the batch sees
// what the update of a previous one did to it.
func decodeBulkChanges(raw json.RawMessage) (*UpdateCheckRequest, error) {
	var changes UpdateCheckRequest
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&changes); err != nil {
		return nil, fmt.Errorf("%w: %w", errBulkInvalidChanges, err)
	}

	return &changes, nil
}

// touchesBulkPlacement reports whether the (already validated) changes set a
// placement field.
func touchesBulkPlacement(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false
	}

	return slices.ContainsFunc(bulkPlacementFields(), func(name string) bool {
		_, ok := fields[name]

		return ok
	})
}

// uniqueCheckUIDs drops empty and repeated UIDs, keeping the request order.
func uniqueCheckUIDs(uids []string) []string {
	out := make([]string, 0, len(uids))

	for _, uid := range uids {
		if uid != "" && !slices.Contains(out, uid) {
			out = append(out, uid)
		}
	}

	return out
}

// validateBulkRegions checks the requested regions once for the whole batch:
// each must be a declared cloud region or a private (@) location of the org.
// An empty list (back to the default placement) has nothing to check.
func (s *Service) validateBulkRegions(ctx context.Context, orgUID string, requested []string) error {
	if len(requested) == 0 {
		return nil
	}

	resolved, err := s.regions.ResolveRegionsForCheck(ctx, requested, orgUID)
	if err != nil {
		return err
	}

	declared, err := s.regions.GetGlobalRegions(ctx)
	if err != nil {
		return fmt.Errorf("list declared regions: %w", err)
	}

	for _, slug := range resolved {
		if regions.IsPrivateRegion(slug) {
			continue
		}

		if !slices.ContainsFunc(declared, func(def regions.RegionDefinition) bool { return def.Slug == slug }) {
			return fmt.Errorf("%w: %q", errBulkUnknownRegion, slug)
		}
	}

	return nil
}

// bulkUpdateOne applies (or, on a dry run, only validates) the changes on one
// check. A nil skip means the check is updated.
func (s *Service) bulkUpdateOne(
	ctx context.Context, check *models.Check, raw json.RawMessage, touchesPlacement, dryRun bool,
) (*BulkUpdateSkip, []base.ValidationErrorField) {
	skip := func(reason string, err error) (*BulkUpdateSkip, []base.ValidationErrorField) {
		out := &BulkUpdateSkip{UID: check.UID, Slug: check.Slug, Reason: reason}
		if err != nil {
			out.Message = err.Error()
		}

		return out, nil
	}

	switch {
	case assertDemoMayWriteCheck(ctx, check) != nil:
		return skip(BulkUpdateSkipReadOnly, nil)
	case touchesPlacement && check.IsPassive():
		return skip(BulkUpdateSkipRejected, errBulkPassive)
	}

	changes, err := decodeBulkChanges(raw)
	if err != nil {
		return skip(BulkUpdateSkipRejected, err)
	}

	plan, err := s.prepareCheckUpdate(ctx, check, changes)
	if err != nil {
		return skip(s.bulkSkipReason(ctx, check, err))
	}

	if dryRun {
		if plan.placement != nil {
			return nil, plan.placement.warnings
		}

		return nil, nil
	}

	updated, err := s.writeCheckUpdate(ctx, plan, changes)
	if err != nil {
		return skip(s.bulkSkipReason(ctx, check, err))
	}

	return nil, updated.Warnings
}

// bulkSkipReason tells a check the caller can fix (rejected, with the
// validation message) from a server fault, which is logged and reported
// without its internals.
func (s *Service) bulkSkipReason(ctx context.Context, check *models.Check, err error) (string, error) {
	if isCheckUpdateRejection(err) {
		return BulkUpdateSkipRejected, err
	}

	slog.ErrorContext(ctx, "bulk update of a check failed", "check_uid", check.UID, "error", err)

	return BulkUpdateSkipError, errBulkInternal
}

// isCheckUpdateRejection reports whether a PATCH error is the caller's to fix:
// the cases the single-check PATCH answers with a 4xx.
func isCheckUpdateRejection(err error) bool {
	return checkerdef.IsConfigError(err) != nil ||
		errors.Is(err, ErrDemoReadOnly) ||
		isDemoFieldError(err) ||
		errors.Is(err, ErrTunnelRegionNarrowed) ||
		errors.Is(err, ErrNoAgentsToSealTo) ||
		isCheckFieldValidationError(err)
}

// mergeWarnings appends the warnings of extra not already in dst.
func mergeWarnings(dst, extra []base.ValidationErrorField) []base.ValidationErrorField {
	for idx := range extra {
		if !slices.Contains(dst, extra[idx]) {
			dst = append(dst, extra[idx])
		}
	}

	return dst
}
