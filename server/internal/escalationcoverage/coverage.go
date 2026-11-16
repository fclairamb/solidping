// Package escalationcoverage answers "would an escalation policy ever page this
// member?". Personal notification methods (Telegram, WhatsApp, SMS, DMs) only
// fire when an escalation step targets the member, so a member of an org with
// no such policy has methods that are silently dead.
package escalationcoverage

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CoveredBy says how an escalation policy reaches a member.
type CoveredBy string

const (
	// CoveredByOrgDefault means the org default policy pages the member, so every
	// check without a closer policy pages them.
	CoveredByOrgDefault CoveredBy = "org_default"
	// CoveredBySomeChecks means only policies assigned to some checks or groups page
	// the member.
	CoveredBySomeChecks CoveredBy = "some_checks"
	// CoveredByNone means no live policy pages the member.
	CoveredByNone CoveredBy = "none"
)

// Paging is the coverage of one member in one org.
type Paging struct {
	CoveredBy CoveredBy `json:"coveredBy"`
	// PolicyCount is the number of live escalation policies in the org.
	PolicyCount int `json:"policyCount"`
}

// Compute evaluates the coverage of userUID in orgUID. A step targets the
// member directly (`user`), through `all_admins` when they are an admin, or
// through a `schedule` they belong to. Soft-deleted policies are ignored.
func Compute(ctx context.Context, dbSvc db.Service, orgUID, userUID string) (*Paging, error) {
	policies, err := dbSvc.ListEscalationPolicies(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("list escalation policies: %w", err)
	}

	paging := &Paging{CoveredBy: CoveredByNone, PolicyCount: len(policies)}
	if len(policies) == 0 {
		return paging, nil
	}

	pagesUser := &targetMatcher{db: dbSvc, userUID: userUID, isAdmin: memberIsAdmin(ctx, dbSvc, orgUID, userUID)}

	var assigned map[string]int

	org, err := dbSvc.GetOrganization(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("get organization: %w", err)
	}

	for _, policy := range policies {
		pages, pageErr := policyPagesUser(ctx, dbSvc, pagesUser, policy.UID)
		if pageErr != nil {
			return nil, pageErr
		}

		if !pages {
			continue
		}

		if org != nil && org.DefaultEscalationPolicyUID != nil && *org.DefaultEscalationPolicyUID == policy.UID {
			paging.CoveredBy = CoveredByOrgDefault

			return paging, nil
		}

		if assigned == nil {
			if assigned, err = assignedPolicies(ctx, dbSvc, orgUID); err != nil {
				return nil, err
			}
		}

		if assigned[policy.UID] > 0 {
			paging.CoveredBy = CoveredBySomeChecks
		}
	}

	return paging, nil
}

// assignedPolicies counts, per policy UID, the checks and groups using it.
func assignedPolicies(ctx context.Context, dbSvc db.Service, orgUID string) (map[string]int, error) {
	checks, err := dbSvc.CountChecksByEscalationPolicy(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("count checks by policy: %w", err)
	}

	groups, err := dbSvc.CountCheckGroupsByEscalationPolicy(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("count groups by policy: %w", err)
	}

	total := make(map[string]int, len(checks)+len(groups))
	for uid, n := range checks {
		total[uid] += n
	}

	for uid, n := range groups {
		total[uid] += n
	}

	return total, nil
}

// memberIsAdmin is false for a non-member or a failed lookup: such a user
// cannot be reached through all_admins.
func memberIsAdmin(ctx context.Context, dbSvc db.Service, orgUID, userUID string) bool {
	member, err := dbSvc.GetMemberByUserAndOrg(ctx, userUID, orgUID)
	if err != nil || member == nil {
		return false
	}

	return member.Role.AtLeast(models.MemberRoleAdmin)
}

// targetMatcher decides whether a policy target reaches one member.
type targetMatcher struct {
	db      db.Service
	userUID string
	isAdmin bool
}

func (m *targetMatcher) matches(ctx context.Context, target *models.EscalationPolicyTarget) (bool, error) {
	switch target.TargetType {
	case models.EscalationTargetUser:
		return target.TargetUID != nil && *target.TargetUID == m.userUID, nil
	case models.EscalationTargetAllAdmins:
		return m.isAdmin, nil
	case models.EscalationTargetSchedule:
		if target.TargetUID == nil {
			return false, nil
		}

		users, err := m.db.ListOnCallScheduleUsers(ctx, *target.TargetUID)
		if err != nil {
			return false, fmt.Errorf("list schedule users: %w", err)
		}

		for _, u := range users {
			if u.UserUID == m.userUID {
				return true, nil
			}
		}
	case models.EscalationTargetConnection:
		// Integrations are org-level, not personal.
	}

	return false, nil
}

func policyPagesUser(ctx context.Context, dbSvc db.Service, matcher *targetMatcher, policyUID string) (bool, error) {
	steps, err := dbSvc.ListEscalationPolicySteps(ctx, policyUID)
	if err != nil {
		return false, fmt.Errorf("list policy steps: %w", err)
	}

	if len(steps) == 0 {
		return false, nil
	}

	stepUIDs := make([]string, 0, len(steps))
	for _, step := range steps {
		stepUIDs = append(stepUIDs, step.UID)
	}

	targets, err := dbSvc.ListEscalationPolicyTargets(ctx, stepUIDs)
	if err != nil {
		return false, fmt.Errorf("list policy targets: %w", err)
	}

	for _, target := range targets {
		ok, matchErr := matcher.matches(ctx, target)
		if matchErr != nil {
			return false, matchErr
		}

		if ok {
			return true, nil
		}
	}

	return false, nil
}
