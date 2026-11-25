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
const auditSourceOIDCGroup = models.MemberRoleSourceOIDCGroup

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

// Role changes the IdP group claim made during a login, reported in the
// login response (HandoffExchangeResponse.RoleChanges) next to the log line
// and the audit event of each one (spec 2026-11-21-02).
const (
	RoleChangeSuperAdminGranted = "super_admin_granted"
	RoleChangeSuperAdminRevoked = "super_admin_revoked"
	// RoleChangeSuperAdminKept: out of every super-admin group, but the last
	// super admin of the instance, so the claim grant was kept.
	RoleChangeSuperAdminKept  = "super_admin_kept_last"
	RoleChangeOrgOwnerGranted = "org_owner_granted"
	RoleChangeOrgAdminGranted = "org_admin_granted"
	RoleChangeOrgAdminRevoked = "org_admin_revoked"
	// RoleChangeOrgAdminKept: out of every org-admin group, but the last
	// owner/admin of the org, so the claim grant was kept.
	RoleChangeOrgAdminKept = "org_admin_kept_last"
)

// applyGroupMapping syncs the roles the IdP groups grant, when a mapping is
// configured, and returns the changes it made. userInfo.Groups come from the
// verified ID token; the userinfo endpoint is asked with the access token of
// the same exchange.
func (s *OIDCOAuthService) applyGroupMapping(
	ctx, exchangeCtx context.Context, token *oauth2.Token, userInfo *oidcUserInfo,
	user *models.User, org *models.Organization,
) ([]string, error) {
	if !groupMappingConfigured(&s.cfg.SSO().OIDC) {
		return nil, nil
	}

	groups := mergeGroups(userInfo.Groups, s.userInfoGroups(exchangeCtx, token, userInfo.Subject))

	return s.syncGroupRoles(ctx, user, org, groups)
}

// loginOptions are the CompleteOrgLogin options of an OIDC login. While
// super-admin groups are set, an empty org is reserved for their members.
func (s *OIDCOAuthService) loginOptions() []LoginOption {
	opts := []LoginOption{WithLoginMethod(signupMethodOIDC)}
	if len(s.cfg.SSO().OIDC.SuperAdminGroupList()) > 0 {
		opts = append(opts, WithoutOrgBootstrap())
	}

	return opts
}

// syncGroupRoles applies the IdP group claim to the user at login (spec
// 2026-11-21-02) and returns the role changes it made. groups come from the
// validated ID token (merged with the userinfo response), never from the
// request.
//
//   - Super admin is synced both ways, but only for a grant the claim made
//     (users.super_admin_source = 'oidc'): a locally granted super admin is
//     never demoted, and the last super admin of the instance is never
//     revoked.
//   - A member of an org-admin group gets the admin role in the login org,
//     joining it if needed (organization_members.role_source =
//     'oidc_group'). Leaving the group demotes that claim grant to user at
//     the next login to the org; a local role and an owner are never
//     touched, and the last owner/admin of the org is never demoted.
//   - A claim super admin logging into an org with no member at all becomes
//     its owner: the OIDC-only first boot creates the default org empty.
func (s *OIDCOAuthService) syncGroupRoles(
	ctx context.Context, user *models.User, org *models.Organization, groups []string,
) ([]string, error) {
	superGroups := s.cfg.SSO().OIDC.SuperAdminGroupList()
	orgGroups := s.cfg.SSO().OIDC.OrgAdminGroupList()

	if len(superGroups) == 0 && len(orgGroups) == 0 {
		return nil, nil
	}

	var changes []string

	superMatch := matchingGroups(groups, superGroups)

	if len(superGroups) > 0 {
		change, err := s.syncSuperAdmin(ctx, user, org, superMatch)
		if err != nil {
			return changes, err
		}

		changes = appendChange(changes, change)
	}

	if len(superMatch) > 0 {
		bootstrapped, err := s.bootstrapOrgOwner(ctx, user, org, superMatch)
		if err != nil {
			return changes, err
		}

		if bootstrapped {
			return append(changes, RoleChangeOrgOwnerGranted), nil
		}
	}

	if len(orgGroups) == 0 {
		return changes, nil
	}

	var (
		change string
		err    error
	)

	if len(matchingGroups(groups, orgGroups)) > 0 {
		change, err = s.grantOrgAdmin(ctx, user, org)
	} else {
		change, err = s.revokeOrgAdmin(ctx, user, org)
	}

	return appendChange(changes, change), err
}

// appendChange appends a non-empty change.
func appendChange(changes []string, change string) []string {
	if change == "" {
		return changes
	}

	return append(changes, change)
}

// syncSuperAdmin grants or revokes super admin from the claim, and returns
// the change it made ("" for none).
func (s *OIDCOAuthService) syncSuperAdmin(
	ctx context.Context, user *models.User, org *models.Organization, superMatch []string,
) (string, error) {
	claimGranted := user.SuperAdminSource != nil && *user.SuperAdminSource == models.SuperAdminSourceOIDC

	switch {
	case len(superMatch) > 0 && !user.SuperAdmin:
		granted, source := true, models.SuperAdminSourceOIDC
		if err := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{
			SuperAdmin: &granted, SuperAdminSource: &source,
		}); err != nil {
			return "", fmt.Errorf("failed to grant super admin: %w", err)
		}

		user.SuperAdmin = true
		user.SuperAdminSource = &source

		s.recordSuperAdminChanged(ctx, user, org, true, superMatch)

		return RoleChangeSuperAdminGranted, nil
	case len(superMatch) == 0 && user.SuperAdmin && claimGranted:
		count, err := s.db.CountSuperAdmins(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to count super admins: %w", err)
		}

		if count <= 1 {
			slog.WarnContext(ctx, "OIDC login without a super-admin group, but the user is the last "+
				"super admin of the instance: keeping super admin", "userUid", user.UID, "email", user.Email)

			return RoleChangeSuperAdminKept, nil
		}

		revoked := false
		if err := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{
			SuperAdmin: &revoked, ClearSuperAdminSource: true,
		}); err != nil {
			return "", fmt.Errorf("failed to revoke super admin: %w", err)
		}

		user.SuperAdmin = false
		user.SuperAdminSource = nil

		s.recordSuperAdminChanged(ctx, user, org, false, nil)

		return RoleChangeSuperAdminRevoked, nil
	}

	return "", nil
}

// bootstrapOrgOwner makes a claim super admin the owner of an org that has no
// member at all, and reports whether it did. The owner role is a local grant
// (no role_source): the org-admin sync never demotes it.
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

// loginOrgMember is the user's live membership of org, nil when none.
func (s *OIDCOAuthService) loginOrgMember(
	ctx context.Context, user *models.User, org *models.Organization,
) (*models.OrganizationMember, error) {
	member, err := s.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no membership is not an error here
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get membership: %w", err)
	}

	return member, nil
}

// grantOrgAdmin gives the user the admin role in org, joining it if needed,
// and returns the change it made ("" for none). An owner or an admin is left
// as is.
func (s *OIDCOAuthService) grantOrgAdmin(
	ctx context.Context, user *models.User, org *models.Organization,
) (string, error) {
	member, err := s.loginOrgMember(ctx, user, org)
	if err != nil {
		return "", err
	}

	source := models.MemberRoleSourceOIDCGroup

	if member == nil {
		member = models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		member.RoleSource = &source

		if createErr := s.db.CreateOrganizationMember(ctx, member); createErr != nil {
			return "", fmt.Errorf("failed to create admin membership: %w", createErr)
		}

		slog.InfoContext(ctx, "OIDC org-admin group: joined as admin", "userUid", user.UID, "org", org.Slug)

		s.authService.recordMemberJoined(ctx, org.UID, user.UID, user.Email, models.MemberRoleAdmin, auditSourceOIDCGroup)

		return RoleChangeOrgAdminGranted, nil
	}

	if member.Role == models.MemberRoleOwner || member.Role == models.MemberRoleAdmin {
		return "", nil
	}

	if err := s.changeMemberRole(ctx, user, org, member, models.MemberRoleAdmin, &source); err != nil {
		return "", fmt.Errorf("failed to grant org admin: %w", err)
	}

	return RoleChangeOrgAdminGranted, nil
}

// revokeOrgAdmin demotes to user an admin role the org-admin group claim
// granted, once the user is out of every org-admin group, and returns the
// change it made ("" for none). A locally granted role and an owner are never
// touched; the last owner/admin of the org is kept.
func (s *OIDCOAuthService) revokeOrgAdmin(
	ctx context.Context, user *models.User, org *models.Organization,
) (string, error) {
	member, err := s.loginOrgMember(ctx, user, org)
	if err != nil || member == nil {
		return "", err
	}

	if member.Role != models.MemberRoleAdmin || member.RoleSource == nil ||
		*member.RoleSource != models.MemberRoleSourceOIDCGroup {
		return "", nil
	}

	count, err := s.db.CountAdminsByOrg(ctx, org.UID)
	if err != nil {
		return "", fmt.Errorf("failed to count org admins: %w", err)
	}

	if count <= 1 {
		slog.WarnContext(ctx, "OIDC login without an org-admin group, but the user is the last "+
			"owner/admin of the organization: keeping admin", "userUid", user.UID, "org", org.Slug)

		return RoleChangeOrgAdminKept, nil
	}

	if err := s.changeMemberRole(ctx, user, org, member, models.MemberRoleUser, nil); err != nil {
		return "", fmt.Errorf("failed to revoke org admin: %w", err)
	}

	return RoleChangeOrgAdminRevoked, nil
}

// changeMemberRole sets the membership's role and its source, then logs and
// audits the change.
func (s *OIDCOAuthService) changeMemberRole(
	ctx context.Context, user *models.User, org *models.Organization,
	member *models.OrganizationMember, role models.MemberRole, source *string,
) error {
	previous := member.Role

	if err := s.db.UpdateOrganizationMember(ctx, member.UID, models.OrganizationMemberUpdate{
		Role: &role, RoleSource: source,
	}); err != nil {
		return err
	}

	member.Role = role
	member.RoleSource = source

	slog.InfoContext(ctx, "OIDC org-admin group sync: role changed",
		"userUid", user.UID, "org", org.Slug, "previousRole", string(previous), "role", string(role))

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
