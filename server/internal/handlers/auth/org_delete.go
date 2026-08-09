package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Organization-deletion errors.
var (
	// ErrOrgSlugConfirmationMismatch is returned when the confirmation slug in
	// the delete request does not match the organization being deleted.
	ErrOrgSlugConfirmationMismatch = errors.New("organization slug confirmation does not match")
)

// DeleteOrgRequest is the body of DELETE /api/v1/orgs/:org. The caller must
// re-type the org slug: deletion takes down every check, status page and
// session of the organization at once, so a bare DELETE with no payload is too
// easy to fire by accident (or by a stray script).
type DeleteOrgRequest struct {
	Slug string `json:"slug"`
}

// DeleteOrg soft-deletes an organization and everything that would otherwise
// keep serving after it is gone. Owner-only — the route is guarded by
// middleware.RequireOrgOwner; this function assumes that check already ran.
//
// Order matters, and each step exists for a reason:
//
//  1. check_jobs are HARD-deleted first. The scheduler's claim query
//     (checkjobsvc.selectAvailableJobs) never joins organizations, so a
//     surviving row would keep probing targets for a deleted org. Killing the
//     jobs is what actually stops the checks; soft-deleting the checks alone
//     would not.
//  2. checks are soft-deleted so nothing re-creates jobs for them.
//  3. memberships are soft-deleted — the org disappears from every member's
//     org switcher.
//  4. org-scoped tokens are revoked so no live session (including the
//     caller's own) keeps org-scoped API access.
//  5. the organization row itself is soft-deleted LAST. From that instant every
//     lookup 404s: dashboard API, status pages, badges and the embed widget all
//     resolve the org through GetOrganizationBySlug, which filters
//     `deleted_at IS NULL`. The slug also becomes claimable again — the unique
//     slug index is partial on `deleted_at is null` — with no alias, tombstone
//     or redirect left behind (a *renamed* org's previous slug is a different
//     mechanism entirely and must not resolve deleted orgs).
//
// There is deliberately no restore path: soft-delete is an implementation
// detail, and recovery is a manual database intervention.
func (s *Service) DeleteOrg(ctx context.Context, orgSlug string, req DeleteOrgRequest) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}

		return err
	}

	if req.Slug != org.Slug {
		return ErrOrgSlugConfirmationMismatch
	}

	if stopErr := s.stopOrgChecks(ctx, org.UID); stopErr != nil {
		return stopErr
	}

	if memberErr := s.deleteOrgMemberships(ctx, org.UID); memberErr != nil {
		return memberErr
	}

	revoked, tokenErr := s.db.DeleteUserTokensByOrg(ctx, org.UID)
	if tokenErr != nil {
		return fmt.Errorf("failed to revoke organization tokens: %w", tokenErr)
	}

	// Rename aliases go too. The alias lookup already refuses to resolve a
	// soft-deleted org, so this is belt-and-braces — but it also frees any slug
	// the org was still holding as a previous slug, matching "the slug becomes
	// claimable again" for every slug the org ever answered on.
	if aliasErr := s.db.ReleaseOrganizationPreviousSlugsForOrg(ctx, org.UID); aliasErr != nil {
		return fmt.Errorf("failed to release organization previous slugs: %w", aliasErr)
	}

	if delErr := s.db.DeleteOrganization(ctx, org.UID); delErr != nil {
		return fmt.Errorf("failed to delete organization: %w", delErr)
	}

	slog.InfoContext(ctx, "organization deleted",
		"orgUID", org.UID, "orgSlug", org.Slug, "revokedTokens", revoked)

	return nil
}

// internalFilterAll is the ListChecksFilter.Internal sentinel meaning "do not
// filter on internal at all" (both dialects switch on the string; see the
// "Apply internal filter" block in postgres.go / sqlite.go).
const internalFilterAll = "all"

// stopOrgChecks hard-deletes the org's scheduler rows and soft-deletes its
// checks, so no worker can pick anything up after the org is gone.
//
// The Internal filter MUST be "all": ListChecks defaults to `internal = FALSE`,
// so enumerating without it silently skips every internal check, leaving its
// check_jobs row behind — and the scheduler's claim query never joins
// organizations, so those jobs would keep being claimed and executed forever
// after the org was deleted. `internal` is client-settable at check creation,
// so this is reachable through the public API. Regression test:
// TestDeleteOrgStopsInternalChecksToo.
func (s *Service) stopOrgChecks(ctx context.Context, orgUID string) error {
	internalAll := internalFilterAll

	checks, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{Internal: &internalAll})
	if err != nil {
		return fmt.Errorf("failed to list organization checks: %w", err)
	}

	for _, check := range checks {
		jobs, jobErr := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
		if jobErr != nil {
			return fmt.Errorf("failed to list check jobs: %w", jobErr)
		}

		for _, job := range jobs {
			if delErr := s.db.DeleteCheckJob(ctx, job.UID); delErr != nil {
				return fmt.Errorf("failed to delete check job: %w", delErr)
			}
		}

		if delErr := s.db.DeleteCheck(ctx, check.UID); delErr != nil {
			return fmt.Errorf("failed to delete check: %w", delErr)
		}
	}

	return nil
}

// deleteOrgMemberships soft-deletes every membership of the organization.
func (s *Service) deleteOrgMemberships(ctx context.Context, orgUID string) error {
	members, err := s.db.ListMembersByOrg(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("failed to list organization members: %w", err)
	}

	for _, member := range members {
		if delErr := s.db.DeleteOrganizationMember(ctx, member.UID); delErr != nil {
			return fmt.Errorf("failed to delete organization member: %w", delErr)
		}
	}

	return nil
}
