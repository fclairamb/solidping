package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

//nolint:funlen // linear contract scenario
func runEscalation(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	policy := models.NewEscalationPolicy(fix.org.UID, "Primary")
	must.NoError(svc.CreateEscalationPolicy(ctx, policy))

	policies, err := svc.ListEscalationPolicies(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(policies, 1)

	steps := []*models.EscalationPolicyStep{
		models.NewEscalationPolicyStep(policy.UID, 0, 0),
		models.NewEscalationPolicyStep(policy.UID, 1, 300),
	}
	user := fix.user.UID
	targets := map[int][]*models.EscalationPolicyTarget{
		0: {models.NewEscalationPolicyTarget(steps[0].UID, models.EscalationTargetUser, &user, 0)},
		1: {models.NewEscalationPolicyTarget(steps[1].UID, models.EscalationTargetAllAdmins, nil, 0)},
	}
	must.NoError(svc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps, targets))

	counts, err := svc.CountEscalationPolicyStepsByPolicy(ctx, []string{policy.UID})
	must.NoError(err)
	must.Equal(2, counts[policy.UID])

	// Replacing again swaps the whole list.
	must.NoError(svc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps[:1], map[int][]*models.EscalationPolicyTarget{}))
	counts, err = svc.CountEscalationPolicyStepsByPolicy(ctx, []string{policy.UID})
	must.NoError(err)
	must.Equal(1, counts[policy.UID])

	// The delete guard counts direct references from checks and groups.
	group := models.NewCheckGroup(fix.org.UID, "Escalated", "escalated")
	group.EscalationPolicyUID = &policy.UID
	must.NoError(svc.CreateCheckGroup(ctx, group))

	byGroup, err := svc.CountCheckGroupsByEscalationPolicy(ctx, fix.org.UID)
	must.NoError(err)
	must.Equal(1, byGroup[policy.UID])

	byCheck, err := svc.CountChecksByEscalationPolicy(ctx, fix.org.UID)
	must.NoError(err)
	must.Zero(byCheck[policy.UID])

	inheriting, err := svc.CountChecksInheritingOrgDefault(ctx, fix.org.UID)
	must.NoError(err)
	must.Positive(inheriting, "checks with no policy resolve to the org default")

	name := "Primary (renamed)"
	repeat := 2
	update := &models.EscalationPolicyUpdate{Name: &name, RepeatMax: &repeat}
	must.NoError(svc.UpdateEscalationPolicy(ctx, policy.UID, update))
	got, err := svc.GetEscalationPolicy(ctx, fix.org.UID, policy.UID)
	must.NoError(err)
	must.Equal(name, got.Name)

	must.NoError(svc.DeleteEscalationPolicy(ctx, policy.UID))
	_, err = svc.GetEscalationPolicy(ctx, fix.org.UID, policy.UID)
	must.Error(err)
}

//nolint:funlen // linear contract scenario
func runOnCall(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	sched := models.NewOnCallSchedule(fix.org.UID, "Primary rota", "UTC", models.RotationTypeDaily)
	sched.HandoffTime = "09:00"
	secret := "ical-secret-contract"
	sched.ICalSecret = &secret
	must.NoError(svc.CreateOnCallSchedule(ctx, sched))

	bySecret, err := svc.GetOnCallScheduleByICalSecret(ctx, secret)
	must.NoError(err)
	must.Equal(sched.UID, bySecret.UID)

	list, err := svc.ListOnCallSchedules(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(list, 1)

	name := "Renamed rota"
	update := &models.OnCallScheduleUpdate{Name: &name, ClearICalSecret: true}
	must.NoError(svc.UpdateOnCallSchedule(ctx, sched.UID, update))
	got, err := svc.GetOnCallSchedule(ctx, fix.org.UID, sched.UID)
	must.NoError(err)
	must.Equal(name, got.Name)
	must.Nil(got.ICalSecret, "the iCal feed is disabled once the secret is cleared")

	_, err = svc.GetOnCallScheduleByICalSecret(ctx, secret)
	must.Error(err)

	now := time.Now()
	reason := "swap"
	override := models.NewOnCallScheduleOverride(sched.UID, fix.user.UID, now, now.Add(time.Hour))
	override.Reason = &reason
	must.NoError(svc.CreateOnCallScheduleOverride(ctx, override))

	gotOverride, err := svc.GetOnCallScheduleOverride(ctx, override.UID)
	must.NoError(err)
	must.Equal(fix.user.UID, gotOverride.UserUID)

	from := now.Add(-time.Hour)
	until := now.Add(2 * time.Hour)
	overrides, err := svc.ListOnCallScheduleOverrides(ctx, sched.UID, &from, &until)
	must.NoError(err)
	must.Len(overrides, 1)

	must.NoError(svc.DeleteOnCallScheduleOverride(ctx, override.UID))
	_, err = svc.GetOnCallScheduleOverride(ctx, override.UID)
	must.Error(err)

	must.NoError(svc.DeleteOnCallSchedule(ctx, sched.UID))
	_, err = svc.GetOnCallSchedule(ctx, fix.org.UID, sched.UID)
	must.Error(err)
}

func runCheckDependencies(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	child := models.NewCheck(fix.org.UID, "dep-child", "http")
	must.NoError(svc.CreateCheck(ctx, child))

	desc := "depends on the contract check"
	dep := models.NewCheckDependency(fix.org.UID, fix.check.UID, child.UID, models.CheckDependencyKindHard, &desc)
	must.NoError(svc.CreateCheckDependency(ctx, dep))

	got, err := svc.GetCheckDependency(ctx, fix.org.UID, dep.UID)
	must.NoError(err)
	must.Equal(models.CheckDependencyKindHard, got.Kind)

	byOrg, err := svc.ListCheckDependenciesByOrg(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(byOrg, 1)

	soft := models.CheckDependencyKindSoft
	update := &models.CheckDependencyUpdate{Kind: &soft, ClearDescription: true}
	must.NoError(svc.UpdateCheckDependency(ctx, dep.UID, update))
	got, err = svc.GetCheckDependency(ctx, fix.org.UID, dep.UID)
	must.NoError(err)
	must.Equal(soft, got.Kind)
	must.Nil(got.Description)

	// Rolling a child incident up under its parent's incident.
	parentInc := models.NewIncident(fix.org.UID, fix.check.UID, time.Now(), "parent down")
	childInc := models.NewIncident(fix.org.UID, child.UID, time.Now(), "child down")
	must.NoError(svc.CreateIncident(ctx, parentInc))
	must.NoError(svc.CreateIncident(ctx, childInc))

	attached, err := svc.AttachIncidentToRollupParent(ctx, childInc.UID, parentInc.UID)
	must.NoError(err)
	must.True(attached)

	again, err := svc.AttachIncidentToRollupParent(ctx, childInc.UID, parentInc.UID)
	must.NoError(err)
	must.False(again, "an already-attached child is not attached twice")

	must.NoError(svc.DeleteCheckDependency(ctx, dep.UID))
	_, err = svc.GetCheckDependency(ctx, fix.org.UID, dep.UID)
	must.Error(err)
}
