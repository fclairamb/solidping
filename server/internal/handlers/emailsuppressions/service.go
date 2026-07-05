// Package emailsuppressions exposes the dashboard-facing REST API for the
// D4 per-recipient unsubscribe list (spec 2026-07-05-10): list current
// suppressions for an org and remove one (re-subscribe). Creation is always
// recipient-initiated via the public handlers/unsubscribe surface — this
// package is read+delete only, mirroring the "admins can see and undo, but
// only recipients can opt out" shape the spec describes.
package emailsuppressions

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrOrganizationNotFound is returned when the org slug doesn't resolve.
var ErrOrganizationNotFound = errors.New("organization not found")

// ErrSuppressionNotFound is returned when the uid doesn't resolve within the
// given org — either it never existed or it belongs to a different org
// (deliberately not distinguished, so a caller can't probe for other orgs'
// UIDs by timing/error-shape).
var ErrSuppressionNotFound = errors.New("email suppression not found")

// Row is the JSON-shaped suppression returned to the dashboard.
type Row struct {
	UID       string `json:"uid"`
	Email     string `json:"email"`
	CheckUID  string `json:"checkUid,omitempty"` // omitted (org-wide) when the suppression covers every check
	CheckName string `json:"checkName,omitempty"`
	Source    string `json:"source"`
	CreatedAt string `json:"createdAt"`
}

// Service wraps DB calls for the suppression list/delete API.
type Service struct {
	db db.Service
}

// NewService builds a Service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// List returns every suppression for orgSlug, newest first, with the check
// name resolved for check-scoped rows (best-effort — a deleted check just
// leaves CheckName empty rather than failing the whole list).
func (s *Service) List(ctx context.Context, orgSlug string) ([]Row, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return nil, ErrOrganizationNotFound
	}

	rows, err := s.db.ListEmailSuppressions(ctx, org.UID)
	if err != nil {
		return nil, fmt.Errorf("list email suppressions: %w", err)
	}

	out := make([]Row, len(rows))
	for i, row := range rows {
		out[i] = s.toRow(ctx, org.UID, row)
	}

	return out, nil
}

// Delete removes a suppression (re-subscribe), verifying it belongs to
// orgSlug first — mirrors the "GET to confirm ownership, then delete"
// pattern used by DeleteIntegration and similar org-scoped resources, so a
// UID from a different org 404s instead of silently deleting nothing (which
// would look identical to success) or leaking a cross-org row.
func (s *Service) Delete(ctx context.Context, orgSlug, uid string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return ErrOrganizationNotFound
	}

	if _, err := s.db.GetEmailSuppression(ctx, org.UID, uid); err != nil {
		return ErrSuppressionNotFound
	}

	if err := s.db.DeleteEmailSuppression(ctx, uid); err != nil {
		return fmt.Errorf("delete email suppression: %w", err)
	}

	return nil
}

// toRow converts a DB model to the JSON shape, resolving the check name for
// check-scoped rows.
func (s *Service) toRow(ctx context.Context, orgUID string, sup *models.EmailSuppression) Row {
	row := Row{
		UID:       sup.UID,
		Email:     sup.Email,
		Source:    sup.Source,
		CreatedAt: sup.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}

	if sup.CheckUID == nil {
		return row
	}

	row.CheckUID = *sup.CheckUID

	check, err := s.db.GetCheck(ctx, orgUID, *sup.CheckUID)
	if err != nil || check == nil {
		return row
	}

	if check.Name != nil {
		row.CheckName = *check.Name
	} else if check.Slug != nil {
		row.CheckName = *check.Slug
	}

	return row
}
