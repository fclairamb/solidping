package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// GetAutoPersonalOrgByOwner returns the auto-created personal organization of
// userUID (spec 2026-10-30-04), or nil when there is none. includeDeleted also
// considers soft-deleted ones, most recent first: a user gets one personal org
// in their lifetime, so one they deleted (or the cleanup removed) is never
// re-created behind their back.
func (s *Service) GetAutoPersonalOrgByOwner(
	ctx context.Context, userUID string, includeDeleted bool,
) (*models.Organization, error) {
	org := new(models.Organization)

	query := s.db.NewSelect().
		Model(org).
		Where("auto_personal_owner_uid = ?", userUID).
		Order("created_at DESC").
		Limit(1)

	if !includeDeleted {
		query = query.Where("deleted_at IS NULL")
	}

	if err := query.Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // "no personal org" is a legitimate answer.
		}

		return nil, err
	}

	return org, nil
}

// ListAbandonedAutoPersonalOrgs returns the live auto-created personal orgs
// created before createdBefore that never held anything: no check row at all
// (soft-deleted ones count, so an org that EVER had a check is excluded), no
// more than one membership row ever, and no entitlements row or entitlement
// audit (a paid or admin-granted plan). Oldest first.
func (s *Service) ListAbandonedAutoPersonalOrgs(
	ctx context.Context, createdBefore time.Time,
) ([]*models.Organization, error) {
	var orgs []*models.Organization

	err := s.db.NewSelect().
		Model(&orgs).
		Where("organization.auto_personal_owner_uid IS NOT NULL").
		Where("organization.deleted_at IS NULL").
		Where("organization.created_at < ?", createdBefore).
		Where("NOT EXISTS (SELECT 1 FROM checks c WHERE c.organization_uid = organization.uid)").
		Where("(SELECT COUNT(*) FROM organization_members m WHERE m.organization_uid = organization.uid) <= 1").
		Where("NOT EXISTS (SELECT 1 FROM org_entitlements e WHERE e.organization_uid = organization.uid)").
		Where("NOT EXISTS (SELECT 1 FROM org_entitlement_audits a WHERE a.organization_uid = organization.uid)").
		Order("organization.created_at ASC").
		Scan(ctx)

	return orgs, err
}

// ListOrgsWithMemberEmailDomain returns up to limit live organizations, other
// than auto-created personal ones and the ones excludeUserUID belongs to, that
// have a live member whose email is at domain (case-insensitive). Feeds the
// colleague hint, which only needs to know whether exactly one such org exists.
func (s *Service) ListOrgsWithMemberEmailDomain(
	ctx context.Context, domain, excludeUserUID string, limit int,
) ([]*models.Organization, error) {
	var orgs []*models.Organization

	if domain == "" || limit <= 0 {
		return orgs, nil
	}

	pattern := "%@" + escapeLikePrefix(strings.ToLower(domain))

	err := s.db.NewSelect().
		Model(&orgs).
		Where("organization.deleted_at IS NULL").
		Where("organization.auto_personal_owner_uid IS NULL").
		Where("EXISTS (SELECT 1 FROM organization_members m JOIN users u ON u.uid = m.user_uid"+
			" WHERE m.organization_uid = organization.uid AND m.deleted_at IS NULL AND u.deleted_at IS NULL"+
			" AND lower(u.email) LIKE ? ESCAPE '\\')", pattern).
		Where("NOT EXISTS (SELECT 1 FROM organization_members x"+
			" WHERE x.organization_uid = organization.uid AND x.user_uid = ? AND x.deleted_at IS NULL)", excludeUserUID).
		Order("organization.created_at ASC").
		Limit(limit).
		Scan(ctx)

	return orgs, err
}
