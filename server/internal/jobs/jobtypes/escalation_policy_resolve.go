package jobtypes

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ResolveEscalationPolicyUID picks the effective policy for a check, in
// precedence order: the check's own policy wins; otherwise the check group's;
// otherwise the organization's default; otherwise none.
//
// The org default is opt-in: an org that never sets one keeps the historical
// check → group → none behavior exactly (the extra lookups only fire when the
// higher-precedence levels resolve to nothing).
//
// A candidate at any level is honored only when the referenced policy still
// exists (livePolicyUID drops soft-deleted ones). This is what makes the
// delete-guard promise true: policy deletion is a soft delete, so the FK
// `on delete set null` never fires and the stale UID lingers on the row — but
// the resolver ignores it and continues the chain, so a deleted policy's checks
// "fall back to inherited escalation" (group → org default → none) exactly as
// the delete confirmation says.
//
// Lives here rather than in handlers/incidents because two callers now need
// it: incident-open (which schedules the escalation cycle) and the Slack
// on-call mention resolver, which must name the same humans the escalation
// machinery would page. Duplicating the precedence chain would let the two
// drift, and a mention that disagrees with who actually gets paged is worse
// than no mention at all.
func ResolveEscalationPolicyUID(ctx context.Context, dbSvc db.Service, check *models.Check) string {
	if check == nil {
		return ""
	}

	if uid := livePolicyUID(ctx, dbSvc, check.OrganizationUID, check.EscalationPolicyUID); uid != "" {
		return uid
	}

	if check.CheckGroupUID != nil && *check.CheckGroupUID != "" {
		group, err := dbSvc.GetCheckGroup(ctx, check.OrganizationUID, *check.CheckGroupUID)
		if err == nil && group != nil {
			if uid := livePolicyUID(ctx, dbSvc, check.OrganizationUID, group.EscalationPolicyUID); uid != "" {
				return uid
			}
		}
	}

	// Neither the check nor its group named a live policy — fall back to the
	// org default (null-safe: an org without one resolves to none).
	org, err := dbSvc.GetOrganization(ctx, check.OrganizationUID)
	if err == nil && org != nil {
		if uid := livePolicyUID(ctx, dbSvc, check.OrganizationUID, org.DefaultEscalationPolicyUID); uid != "" {
			return uid
		}
	}

	return ""
}

// livePolicyUID returns *policyUID when it names a policy that still exists in
// the org (not soft-deleted), else "". A nil/empty pointer, or a UID whose
// policy has been deleted, both resolve to "" so the caller continues the
// resolution chain.
func livePolicyUID(ctx context.Context, dbSvc db.Service, orgUID string, policyUID *string) string {
	if policyUID == nil || *policyUID == "" {
		return ""
	}

	if _, err := dbSvc.GetEscalationPolicy(ctx, orgUID, *policyUID); err != nil {
		return ""
	}

	return *policyUID
}
