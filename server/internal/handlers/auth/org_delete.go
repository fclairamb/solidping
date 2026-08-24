package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/support"
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
//     caller's own) keeps org-scoped API access. The caller does not lose their
//     *authentication* over it: step 6 hands them a brand-new session. Every
//     OTHER member is signed out of the org for good, which is the deliberate
//     behavior from spec 2026-08-08-11.
//  5. the organization row itself is soft-deleted LAST. From that instant every
//     lookup 404s: dashboard API, status pages, badges and the embed widget all
//     resolve the org through GetOrganizationBySlug, which filters
//     `deleted_at IS NULL`. The slug also becomes claimable again — the unique
//     slug index is partial on `deleted_at is null` — with no alias, tombstone
//     or redirect left behind (a *renamed* org's previous slug is a different
//     mechanism entirely and must not resolve deleted orgs).
//  6. the CALLER is handed a replacement session. Steps 3-4 took away the org
//     their access token names, so without this they would be logged out by
//     their own maintenance action (GitHub issue #206). See
//     mintPostDeletionSession.
//
// There is deliberately no restore path: soft-delete is an implementation
// detail, and recovery is a manual database intervention.
func (s *Service) DeleteOrg(
	ctx context.Context, orgSlug, callerUserUID string, req DeleteOrgRequest, authContext Context,
) (*LoginResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	if req.Slug != org.Slug {
		return nil, ErrOrgSlugConfirmationMismatch
	}

	if stopErr := s.stopOrgChecks(ctx, org.UID); stopErr != nil {
		return nil, stopErr
	}

	if memberErr := s.deleteOrgMemberships(ctx, org.UID); memberErr != nil {
		return nil, memberErr
	}

	revoked, tokenErr := s.db.DeleteUserTokensByOrg(ctx, org.UID)
	if tokenErr != nil {
		return nil, fmt.Errorf("failed to revoke organization tokens: %w", tokenErr)
	}

	// Rename aliases go too. The alias lookup already refuses to resolve a
	// soft-deleted org, so this is belt-and-braces — but it also frees any slug
	// the org was still holding as a previous slug, matching "the slug becomes
	// claimable again" for every slug the org ever answered on.
	if aliasErr := s.db.ReleaseOrganizationPreviousSlugsForOrg(ctx, org.UID); aliasErr != nil {
		return nil, fmt.Errorf("failed to release organization previous slugs: %w", aliasErr)
	}

	// External identity links (Slack workspace, Discord guild, …) go too. The
	// `on delete cascade` on organization_providers.organization_uid only fires
	// for HARD deletes, so a soft-deleted org used to leave its links live —
	// and a live link pointing at a dead org bricked every later SSO login for
	// that workspace/guild (spec 2026-08-25-01). Releasing them here prevents
	// the class; resolveLinkedOrganization heals rows that already went stale.
	if linkErr := s.releaseOrgProviderLinks(ctx, org.UID); linkErr != nil {
		return nil, linkErr
	}

	if delErr := s.db.DeleteOrganization(ctx, org.UID); delErr != nil {
		return nil, fmt.Errorf("failed to delete organization: %w", delErr)
	}

	// Reap the org's public attachments — today just its logo. A logo blob is
	// public exactly while its file row is live (spec 2026-08-22-03), so
	// without this the deleted org's logo stays readable on /pub/assets/:uid
	// forever. Best-effort: the org is already gone, and failing the whole
	// deletion over a leftover file row would be worse.
	if _, reapErr := s.db.DeleteFilesByTopicPrefix(
		ctx, org.UID, files.OrganizationTopicPrefix(org.UID),
	); reapErr != nil {
		slog.WarnContext(ctx, "failed to reap organization attachments",
			"error", reapErr, "orgUID", org.UID)
	}

	// Strip support-thread attribution pointing at the deleted org. The THREADS
	// SURVIVE: a support conversation belongs to the instance, and attribution
	// only records who we thought the sender was — deleting an org must not
	// delete a stranger's conversation, and must not leave a dangling reference
	// either (spec 2026-08-22-02). Best-effort, same reasoning as the
	// attachment reap above.
	if s.support != nil {
		if detached, detachErr := s.support.DetachOrganization(ctx, org.UID); detachErr != nil {
			slog.WarnContext(ctx, "failed to detach support thread attribution",
				"error", detachErr, "orgUID", org.UID)
		} else if detached > 0 {
			slog.InfoContext(ctx, "detached support thread attribution",
				"orgUID", org.UID, "threads", detached)
		}
	}

	slog.InfoContext(ctx, "organization deleted",
		"orgUID", org.UID, "orgSlug", org.Slug, "revokedTokens", revoked)

	return s.mintPostDeletionSession(ctx, callerUserUID, authContext)
}

// mintPostDeletionSession issues the session the caller carries on with after
// they deleted an organization.
//
// It runs AFTER the teardown, so the memberships it reads are exactly the ones
// that survived: the deleted org's membership is already soft-deleted and
// cannot come back. Two outcomes, both of which keep the user signed in:
//
//   - at least one org left → a full org-scoped session for the first survivor,
//     minted by the ordinary SwitchOrg machinery (fresh refresh-token row +
//     access token whose orgSlug claim names that org). The dashboard adopts it
//     and lands on that org's dashboard.
//   - no org left → an org-less access token, the same shape completeLogin
//     issues for a user who belongs to nothing (resolvedOrg == nil). There is
//     deliberately no refresh token: a refresh row is org-scoped. The dashboard
//     lands on /no-org, exactly as after a zero-org login.
//
// A caller with no user UID at all (an unauthenticated code path that somehow
// reached here, or a test driving the service directly) gets a nil response
// rather than an error — the deletion itself already succeeded and must not be
// reported as failed.
func (s *Service) mintPostDeletionSession(
	ctx context.Context, callerUserUID string, authContext Context,
) (*LoginResponse, error) {
	if callerUserUID == "" {
		return nil, nil //nolint:nilnil // "deleted, no session to hand back" is a legitimate result.
	}

	remaining, err := s.getOrganizationsForUser(ctx, callerUserUID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve remaining organizations: %w", err)
	}

	if len(remaining) > 0 {
		session, switchErr := s.SwitchOrg(ctx, callerUserUID, remaining[0].Slug, authContext)
		if switchErr != nil {
			return nil, fmt.Errorf("failed to mint a session for the remaining organization: %w", switchErr)
		}

		// SwitchOrg answers a single-org question and leaves Organizations
		// empty; the dashboard needs the full switcher list here because the one
		// it was holding still contains the org that just died.
		session.Organizations = remaining
		session.LoginAction = LoginActionOrgRedirect

		return session, nil
	}

	user, err := s.db.GetUser(ctx, callerUserUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	role := ""
	if user.SuperAdmin {
		role = RoleSuperAdmin
	}

	return s.completeLogin(ctx, user, nil, role, LoginActionNoOrg, remaining, methodOrgDeleted, authContext)
}

// methodOrgDeleted tags the session minted by mintPostDeletionSession, so a
// forensic read of a token's created_with.method says why it exists.
const methodOrgDeleted = "org-deleted"

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
// releaseOrgProviderLinks soft-deletes every organization_providers row the org
// still holds, so its Slack team / Discord guild / OIDC issuer identity becomes
// claimable again instead of pointing at a soft-deleted org forever.
func (s *Service) releaseOrgProviderLinks(ctx context.Context, orgUID string) error {
	links, err := s.db.ListOrganizationProviders(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("failed to list organization provider links: %w", err)
	}

	for _, link := range links {
		if delErr := s.db.DeleteOrganizationProvider(ctx, link.UID); delErr != nil {
			return fmt.Errorf("failed to release organization provider link: %w", delErr)
		}
	}

	return nil
}

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

// SetSupportInbox wires the support inbox used to detach attribution on org
// deletion. Late injection: the support service is built after auth, and
// integrations that own it already import this package.
func (s *Service) SetSupportInbox(svc *support.Service) {
	s.support = svc
}
