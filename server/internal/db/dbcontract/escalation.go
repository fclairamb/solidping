package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runEscalation(t *testing.T, f *fixture) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := f.svc

	policy := models.NewEscalationPolicy(f.org.UID, "Primary")
	r.NoError(svc.CreateEscalationPolicy(ctx, policy))

	policies, err := svc.ListEscalationPolicies(ctx, f.org.UID)
	r.NoError(err)
	r.Len(policies, 1)

	steps := []*models.EscalationPolicyStep{
		models.NewEscalationPolicyStep(policy.UID, 0, 0),
		models.NewEscalationPolicyStep(policy.UID, 1, 300),
	}
	user := f.user.UID
	targets := map[int][]*models.EscalationPolicyTarget{
		0: {models.NewEscalationPolicyTarget(steps[0].UID, models.EscalationTargetUser, &user, 0)},
		1: {models.NewEscalationPolicyTarget(steps[1].UID, models.EscalationTargetAllAdmins, nil, 0)},
	}
	r.NoError(svc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps, targets))

	counts, err := svc.CountEscalationPolicyStepsByPolicy(ctx, []string{policy.UID})
	r.NoError(err)
	r.Equal(2, counts[policy.UID])

	// Replacing again swaps the whole list.
	r.NoError(svc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps[:1], map[int][]*models.EscalationPolicyTarget{}))
	counts, err = svc.CountEscalationPolicyStepsByPolicy(ctx, []string{policy.UID})
	r.NoError(err)
	r.Equal(1, counts[policy.UID])

	// The delete guard counts direct references from checks and groups.
	group := models.NewCheckGroup(f.org.UID, "Escalated", "escalated")
	group.EscalationPolicyUID = &policy.UID
	r.NoError(svc.CreateCheckGroup(ctx, group))

	byGroup, err := svc.CountCheckGroupsByEscalationPolicy(ctx, f.org.UID)
	r.NoError(err)
	r.Equal(1, byGroup[policy.UID])

	byCheck, err := svc.CountChecksByEscalationPolicy(ctx, f.org.UID)
	r.NoError(err)
	r.Zero(byCheck[policy.UID])

	inheriting, err := svc.CountChecksInheritingOrgDefault(ctx, f.org.UID)
	r.NoError(err)
	r.Positive(inheriting, "checks with no policy resolve to the org default")

	name := "Primary (renamed)"
	repeat := 2
	r.NoError(svc.UpdateEscalationPolicy(ctx, policy.UID, &models.EscalationPolicyUpdate{Name: &name, RepeatMax: &repeat}))
	got, err := svc.GetEscalationPolicy(ctx, f.org.UID, policy.UID)
	r.NoError(err)
	r.Equal(name, got.Name)

	r.NoError(svc.DeleteEscalationPolicy(ctx, policy.UID))
	_, err = svc.GetEscalationPolicy(ctx, f.org.UID, policy.UID)
	r.Error(err)
}

func runOnCall(t *testing.T, f *fixture) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := f.svc

	sched := models.NewOnCallSchedule(f.org.UID, "Primary rota", "UTC", models.RotationTypeDaily)
	sched.HandoffTime = "09:00"
	secret := "ical-secret-contract"
	sched.ICalSecret = &secret
	r.NoError(svc.CreateOnCallSchedule(ctx, sched))

	bySecret, err := svc.GetOnCallScheduleByICalSecret(ctx, secret)
	r.NoError(err)
	r.Equal(sched.UID, bySecret.UID)

	list, err := svc.ListOnCallSchedules(ctx, f.org.UID)
	r.NoError(err)
	r.Len(list, 1)

	name := "Renamed rota"
	r.NoError(svc.UpdateOnCallSchedule(ctx, sched.UID, &models.OnCallScheduleUpdate{Name: &name, ClearICalSecret: true}))
	got, err := svc.GetOnCallSchedule(ctx, f.org.UID, sched.UID)
	r.NoError(err)
	r.Equal(name, got.Name)
	r.Nil(got.ICalSecret, "the iCal feed is disabled once the secret is cleared")

	_, err = svc.GetOnCallScheduleByICalSecret(ctx, secret)
	r.Error(err)

	now := time.Now()
	reason := "swap"
	override := models.NewOnCallScheduleOverride(sched.UID, f.user.UID, now, now.Add(time.Hour))
	override.Reason = &reason
	r.NoError(svc.CreateOnCallScheduleOverride(ctx, override))

	gotOverride, err := svc.GetOnCallScheduleOverride(ctx, override.UID)
	r.NoError(err)
	r.Equal(f.user.UID, gotOverride.UserUID)

	from := now.Add(-time.Hour)
	until := now.Add(2 * time.Hour)
	overrides, err := svc.ListOnCallScheduleOverrides(ctx, sched.UID, &from, &until)
	r.NoError(err)
	r.Len(overrides, 1)

	r.NoError(svc.DeleteOnCallScheduleOverride(ctx, override.UID))
	_, err = svc.GetOnCallScheduleOverride(ctx, override.UID)
	r.Error(err)

	r.NoError(svc.DeleteOnCallSchedule(ctx, sched.UID))
	_, err = svc.GetOnCallSchedule(ctx, f.org.UID, sched.UID)
	r.Error(err)
}

func runCheckDependencies(t *testing.T, f *fixture) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := f.svc

	child := models.NewCheck(f.org.UID, "dep-child", "http")
	r.NoError(svc.CreateCheck(ctx, child))

	desc := "depends on the contract check"
	dep := models.NewCheckDependency(f.org.UID, f.check.UID, child.UID, models.CheckDependencyKindHard, &desc)
	r.NoError(svc.CreateCheckDependency(ctx, dep))

	got, err := svc.GetCheckDependency(ctx, f.org.UID, dep.UID)
	r.NoError(err)
	r.Equal(models.CheckDependencyKindHard, got.Kind)

	byOrg, err := svc.ListCheckDependenciesByOrg(ctx, f.org.UID)
	r.NoError(err)
	r.Len(byOrg, 1)

	soft := models.CheckDependencyKindSoft
	r.NoError(svc.UpdateCheckDependency(ctx, dep.UID, &models.CheckDependencyUpdate{Kind: &soft, ClearDescription: true}))
	got, err = svc.GetCheckDependency(ctx, f.org.UID, dep.UID)
	r.NoError(err)
	r.Equal(soft, got.Kind)
	r.Nil(got.Description)

	// Rolling a child incident up under its parent's incident.
	parentInc := models.NewIncident(f.org.UID, f.check.UID, time.Now(), "parent down")
	childInc := models.NewIncident(f.org.UID, child.UID, time.Now(), "child down")
	r.NoError(svc.CreateIncident(ctx, parentInc))
	r.NoError(svc.CreateIncident(ctx, childInc))

	attached, err := svc.AttachIncidentToRollupParent(ctx, childInc.UID, parentInc.UID)
	r.NoError(err)
	r.True(attached)

	again, err := svc.AttachIncidentToRollupParent(ctx, childInc.UID, parentInc.UID)
	r.NoError(err)
	r.False(again, "an already-attached child is not attached twice")

	r.NoError(svc.DeleteCheckDependency(ctx, dep.UID))
	_, err = svc.GetCheckDependency(ctx, f.org.UID, dep.UID)
	r.Error(err)
}
