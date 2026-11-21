package checks

import (
	"context"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Codes of a check left out of a bulk delete.
const (
	BulkDeleteSkipNotFound     = "not_found"
	BulkDeleteSkipReadOnly     = "read_only"
	BulkDeleteSkipInternal     = "internal"
	BulkDeleteSkipManaged      = "managed"
	BulkDeleteSkipTunnelInUse  = "tunnel_in_use"
	BulkDeleteSkipError        = "error"
	bulkDeleteManagedDetail    = "managed by a config-as-code apply: the next apply would recreate it"
	bulkDeleteInternalDetail   = "internal checks cannot be deleted"
	bulkDeleteNotFoundDetail   = "check not found"
	bulkDeleteReadOnlyDetail   = "not available in the demo"
	bulkDeleteGenericErrDetail = "the check could not be deleted"
)

// BulkDeleteChecksRequest is the body of DELETE /api/v1/orgs/:org/checks.
type BulkDeleteChecksRequest struct {
	CheckUIDs []string `json:"checkUids"`
	// DryRun evaluates every check and deletes nothing.
	DryRun bool `json:"dryRun,omitempty"`
}

// BulkDeleteSkip is one requested check left in place, and why.
type BulkDeleteSkip struct {
	CheckUID string `json:"checkUid"`
	Code     string `json:"code"`
	Detail   string `json:"detail,omitempty"`
}

// BulkDeleteChecksResponse is the answer of a bulk delete.
type BulkDeleteChecksResponse struct {
	Deleted []string         `json:"deleted"`
	Skipped []BulkDeleteSkip `json:"skipped"`
	DryRun  bool             `json:"dryRun"`
}

// BulkDeleteChecks deletes every listed check through the rules of a single
// delete. It is per-check: one that cannot be deleted is skipped with its
// reason and the others are still deleted.
func (s *Service) BulkDeleteChecks(
	ctx context.Context, orgSlug string, req *BulkDeleteChecksRequest,
) (*BulkDeleteChecksResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	if len(req.CheckUIDs) > MaxBulkUpdateChecks {
		return nil, errBulkTooManyChecks
	}

	uids := uniqueCheckUIDs(req.CheckUIDs)
	if len(uids) == 0 {
		return nil, errBulkNoChecks
	}

	var rows []*models.Check

	if err = s.db.DB().NewSelect().
		Model(&rows).
		Where("organization_uid = ?", org.UID).
		Where("deleted_at IS NULL").
		Where("uid IN (?)", bun.List(uids)).
		Order("created_at", "uid").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list checks to delete: %w", err)
	}

	rowUIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		rowUIDs = append(rowUIDs, row.UID)
	}

	labels, err := s.db.GetLabelsForChecks(ctx, rowUIDs)
	if err != nil {
		return nil, fmt.Errorf("list check labels: %w", err)
	}

	resp := &BulkDeleteChecksResponse{Deleted: []string{}, Skipped: []BulkDeleteSkip{}, DryRun: req.DryRun}
	found := make(map[string]bool, len(rows))

	for _, check := range rows {
		found[check.UID] = true

		if skip := s.bulkDeleteOne(ctx, org, check, labels[check.UID], req.DryRun); skip != nil {
			resp.Skipped = append(resp.Skipped, *skip)

			continue
		}

		resp.Deleted = append(resp.Deleted, check.UID)
	}

	for _, uid := range uids {
		if !found[uid] {
			resp.Skipped = append(resp.Skipped,
				BulkDeleteSkip{CheckUID: uid, Code: BulkDeleteSkipNotFound, Detail: bulkDeleteNotFoundDetail})
		}
	}

	return resp, nil
}

// bulkDeleteOne vets (and, unless dryRun, deletes) one check. It returns the
// skip, or nil when the check is (or would be) deleted.
func (s *Service) bulkDeleteOne(
	ctx context.Context, org *models.Organization, check *models.Check, labels []*models.Label, dryRun bool,
) *BulkDeleteSkip {
	skip := func(code, detail string) *BulkDeleteSkip {
		return &BulkDeleteSkip{CheckUID: check.UID, Code: code, Detail: detail}
	}

	if check.Internal {
		return skip(BulkDeleteSkipInternal, bulkDeleteInternalDetail)
	}

	for _, lbl := range labels {
		if lbl.Key == ManagedLabelKey || lbl.Key == LegacyManagedLabelKey {
			return skip(BulkDeleteSkipManaged, bulkDeleteManagedDetail)
		}
	}

	if guardErr := s.assertCheckDeletable(ctx, check); guardErr != nil {
		switch {
		case errors.Is(guardErr, ErrDemoReadOnly):
			return skip(BulkDeleteSkipReadOnly, bulkDeleteReadOnlyDetail)
		case errors.Is(guardErr, ErrTunnelInUse):
			return skip(BulkDeleteSkipTunnelInUse, guardErr.Error())
		default:
			return skip(BulkDeleteSkipError, bulkDeleteGenericErrDetail)
		}
	}

	if dryRun {
		return nil
	}

	if delErr := s.deleteCheckRow(ctx, org, check); delErr != nil {
		return skip(BulkDeleteSkipError, bulkDeleteGenericErrDetail)
	}

	return nil
}
