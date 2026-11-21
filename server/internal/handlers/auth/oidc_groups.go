package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"golang.org/x/oauth2"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// auditSourceOIDCGroup is the source recorded on memberships and role changes
// made by the OIDC org-admin group claim (spec 2026-11-21-02).
const auditSourceOIDCGroup = "oidc_group"

// Audit payload keys of auth.super_admin_changed.
const (
	auditKeyGranted      = "granted"
	auditKeyGroups       = "groups"
	auditKeyPreviousRole = "previous_role"
	auditValueSourceOIDC = models.SuperAdminSourceOIDC
)

// parseGroupsClaim reads an IdP groups claim: an array of strings (non-string
// entries are skipped), a single string, or a space-separated string. Any
// other type, or an absent claim, yields no group.
func parseGroupsClaim(value any) []string {
	var groups []string

	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if str, ok := item.(string); ok {
				if str = strings.TrimSpace(str); str != "" {
					groups = append(groups, str)
				}
			}
		}
	case []string:
		for _, str := range typed {
			if str = strings.TrimSpace(str); str != "" {
				groups = append(groups, str)
			}
		}
	case string:
		groups = strings.Fields(typed)
	}

	return groups
}

// groupsClaimName is the configured groups claim, "groups" when blank.
func groupsClaimName(cfg *config.OIDCOAuthConfig) string {
	if name := strings.TrimSpace(cfg.GroupsClaim); name != "" {
		return name
	}

	return config.DefaultOIDCGroupsClaim
}

// groupMappingConfigured reports whether any group is mapped to a role.
func groupMappingConfigured(cfg *config.OIDCOAuthConfig) bool {
	return len(cfg.SuperAdminGroupList()) > 0 || len(cfg.OrgAdminGroupList()) > 0
}

// matchingGroups returns the user's groups that appear in wanted.
func matchingGroups(groups, wanted []string) []string {
	var out []string

	for _, group := range groups {
		if slices.Contains(wanted, group) && !slices.Contains(out, group) {
			out = append(out, group)
		}
	}

	return out
}

// mergeGroups returns the union of a and b, in order, without duplicates.
func mergeGroups(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))

	for _, group := range append(slices.Clone(a), b...) {
		if !slices.Contains(out, group) {
			out = append(out, group)
		}
	}

	return out
}

// applyGroupMapping syncs the roles the IdP groups grant, when a mapping is
// configured. userInfo.Groups come from the verified ID token; the userinfo
// endpoint is asked with the access token of the same exchange.
func (s *OIDCOAuthService) applyGroupMapping(
	ctx, exchangeCtx context.Context, token *oauth2.Token, userInfo *oidcUserInfo,
	user *models.User, org *models.Organization,
) error {
	if !groupMappingConfigured(&s.cfg.OIDC) {
		return nil
	}

	groups := mergeGroups(userInfo.Groups, s.userInfoGroups(exchangeCtx, token, userInfo.Subject))

	return s.syncGroupRoles(ctx, user, org, groups)
}

// loginOptions are the CompleteOrgLogin options of an OIDC login. While
// super-admin groups are set, an empty org is reserved for their members.
func (s *OIDCOAuthService) loginOptions() []LoginOption {
	opts := []LoginOption{WithLoginMethod(signupMethodOIDC)}
	if len(s.cfg.OIDC.SuperAdminGroupList()) > 0 {
		opts = append(opts, WithoutOrgBootstrap())
	}

	return opts
}

// syncGroupRoles applies the IdP group claim to the user at login (spec
// 2026-11-21-02). groups come from the validated ID token (merged with the
// userinfo response), never from the request.
//
//   - Super admin is synced both ways, but only for a grant the claim made
//     (users.super_admin_source = 'oidc'): a locally granted super admin is
//     never demoted, and the last super admin of the instance is never
//     revoked.
//   - A member of an org-admin group gets the admin role in the login org,
//     joining it if needed. This is grant-only: an owner is never touched,
//     and leaving the group does not demote (no source is recorded on
//     memberships).
//   - A claim super admin logging into an org with no member at all becomes
//     its owner: the OIDC-only first boot creates the default org empty.
func (s *OIDCOAuthService) syncGroupRoles(
	ctx context.Context, user *models.User, org *models.Organization, groups []string,
) error {
	superGroups := s.cfg.OIDC.SuperAdminGroupList()
	orgGroups := s.cfg.OIDC.OrgAdminGroupList()

	if len(superGroups) == 0 && len(orgGroups) == 0 {
		return nil
	}

	superMatch := matchingGroups(groups, superGroups)
	claimSuperAdmin := len(superGroups) > 0 && len(superMatch) > 0

	if len(superGroups) > 0 {
		if err := s.syncSuperAdmin(ctx, user, org, superMatch); err != nil {
			return err
		}
	}

	if claimSuperAdmin {
		bootstrapped, err := s.bootstrapOrgOwner(ctx, user, org, superMatch)
		if err != nil || bootstrapped {
			return err
		}
	}

	if orgMatch := matchingGroups(groups, orgGroups); len(orgMatch) > 0 {
		return s.grantOrgAdmin(ctx, user, org)
	}

	return nil
}

// syncSuperAdmin grants or revokes super admin from the claim.
func (s *OIDCOAuthService) syncSuperAdmin(
	ctx context.Context, user *models.User, org *models.Organization, superMatch []string,
) error {
	claimGranted := user.SuperAdminSource != nil && *user.SuperAdminSource == models.SuperAdminSourceOIDC

	switch {
	case len(superMatch) > 0 && !user.SuperAdmin:
		granted, source := true, models.SuperAdminSourceOIDC
		if err := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{
			SuperAdmin: &granted, SuperAdminSource: &source,
		}); err != nil {
			return fmt.Errorf("failed to grant super admin: %w", err)
		}

		user.SuperAdmin = true
		user.SuperAdminSource = &source

		s.recordSuperAdminChanged(ctx, user, org, true, superMatch)
	case len(superMatch) == 0 && user.SuperAdmin && claimGranted:
		count, err := s.db.CountSuperAdmins(ctx)
		if err != nil {
			return fmt.Errorf("failed to count super admins: %w", err)
		}

		if count <= 1 {
			slog.WarnContext(ctx, "OIDC login without a super-admin group, but the user is the last "+
				"super admin of the instance: keeping super admin", "userUid", user.UID, "email", user.Email)

			return nil
		}

		revoked := false
		if err := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{
			SuperAdmin: &revoked, ClearSuperAdminSource: true,
		}); err != nil {
			return fmt.Errorf("failed to revoke super admin: %w", err)
		}

		user.SuperAdmin = false
		user.SuperAdminSource = nil

		s.recordSuperAdminChanged(ctx, user, org, false, nil)
	}

	return nil
}

// bootstrapOrgOwner makes a claim super admin the owner of an org that has no
// member at all, and reports whether it did.
func (s *OIDCOAuthService) bootstrapOrgOwner(
	ctx context.Context, user *models.User, org *models.Organization, superMatch []string,
) (bool, error) {
	count, err := s.db.CountMembersForOrg(ctx, org.UID)
	if err != nil {
		return false, fmt.Errorf("failed to count members: %w", err)
	}

	if count > 0 {
		return false, nil
	}

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleOwner)
	if err := s.db.CreateOrganizationMember(ctx, member); err != nil {
		return false, fmt.Errorf("failed to create owner membership: %w", err)
	}

	slog.InfoContext(ctx, "First SSO login of a super-admin group member: owner of the empty organization",
		"userUid", user.UID, "email", user.Email, "org", org.Slug, "groups", superMatch)

	s.authService.recordMemberJoined(ctx, org.UID, user.UID, user.Email, models.MemberRoleOwner, auditSourceOIDCGroup)

	return true, nil
}

// grantOrgAdmin gives the user the admin role in org, joining it if needed.
// An owner or an admin is left as is.
func (s *OIDCOAuthService) grantOrgAdmin(ctx context.Context, user *models.User, org *models.Organization) error {
	member, err := s.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to get membership: %w", err)
	}

	if member == nil || errors.Is(err, sql.ErrNoRows) {
		member = models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		if createErr := s.db.CreateOrganizationMember(ctx, member); createErr != nil {
			return fmt.Errorf("failed to create admin membership: %w", createErr)
		}

		s.authService.recordMemberJoined(ctx, org.UID, user.UID, user.Email, models.MemberRoleAdmin, auditSourceOIDCGroup)

		return nil
	}

	if member.Role == models.MemberRoleOwner || member.Role == models.MemberRoleAdmin {
		return nil
	}

	previous := member.Role
	role := models.MemberRoleAdmin

	if err := s.db.UpdateOrganizationMember(ctx, member.UID, models.OrganizationMemberUpdate{Role: &role}); err != nil {
		return fmt.Errorf("failed to grant org admin: %w", err)
	}

	slog.InfoContext(ctx, "OIDC org-admin group: role raised to admin",
		"userUid", user.UID, "org", org.Slug, "previousRole", string(previous))

	audit.Record(auditActorCtx(ctx, user.UID, Context{}), s.db, org.UID, models.EventTypeMemberRoleChanged,
		audit.Target{Type: auditTargetMember, UID: user.UID, Name: user.Email},
		models.JSONMap{
			auditKeyEmail:        user.Email,
			auditKeyPreviousRole: string(previous),
			auditKeyRole:         string(role),
			auditKeySource:       auditSourceOIDCGroup,
		})

	return nil
}

// recordSuperAdminChanged logs and audits a claim grant or revoke.
func (s *OIDCOAuthService) recordSuperAdminChanged(
	ctx context.Context, user *models.User, org *models.Organization, granted bool, groups []string,
) {
	slog.InfoContext(ctx, "OIDC super-admin group sync",
		"userUid", user.UID, "email", user.Email, "granted", granted, "groups", groups)

	groupList := make([]any, 0, len(groups))
	for _, group := range groups {
		groupList = append(groupList, group)
	}

	audit.Record(auditActorCtx(ctx, user.UID, Context{}), s.db, org.UID, models.EventTypeAuthSuperAdminChanged,
		audit.Target{Type: auditTargetUser, UID: user.UID, Name: user.Email},
		models.JSONMap{
			auditKeyEmail:   user.Email,
			auditKeyGranted: granted,
			auditKeySource:  auditValueSourceOIDC,
			auditKeyGroups:  groupList,
		})
}
