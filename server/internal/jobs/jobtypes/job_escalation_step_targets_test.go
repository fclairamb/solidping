package jobtypes

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

func escalatedPayload(
	t *testing.T, env *phoneTestEnv, run *EscalationStepJobRun,
	targets []*models.EscalationPolicyTarget, filter map[string]bool,
) models.JSONMap {
	t.Helper()
	ctx := t.Context()

	env.jctx.Services.Jobs = jobsvc.NewService(env.db.DB(), env.db, notifier.NewLocalEventNotifier(), nil)

	policy := models.NewEscalationPolicy(env.org.UID, "Night Ops")
	step := models.NewEscalationPolicyStep(policy.UID, 1, 0)

	stats := run.fanOutWithSeverity(ctx, env.jctx, slog.Default(), env.incident, targets, filter)
	run.emitEscalatedEvent(ctx, env.jctx, slog.Default(), env.incident, policy, step, len(targets), stats)

	uid := env.incident.UID
	events, err := env.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: env.org.UID, IncidentUID: &uid,
		EventTypes: []models.EventType{models.EventTypeIncidentEscalated},
	})
	require.NoError(t, err)
	require.Len(t, events, 1)

	return events[0].Payload
}

func TestEscalatedEvent_NamesPagedTargets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")
	user := seedEscalationUser(t, env, "ana@acme.com", "en", "UTC")

	integ := models.NewIntegration(env.org.UID, models.ConnectionTypeEmail, "Ops Mail")
	integ.Enabled = true
	r.NoError(env.db.CreateChannel(t.Context(), integ))

	targets := []*models.EscalationPolicyTarget{
		{UID: "t1", TargetType: models.EscalationTargetConnection, TargetUID: &integ.UID},
		{UID: "t2", TargetType: models.EscalationTargetUser, TargetUID: &user.UID},
	}
	p := escalatedPayload(t, env, newRun(), targets, nil)

	r.Equal("Night Ops", p["policy_name"])
	r.Equal("API health", p["check_name"])
	got, ok := p["targets"].([]any)
	r.True(ok)
	r.Len(got, 2)
	first, _ := got[0].(map[string]any)
	r.Equal("integration", first["type"])
	r.Equal(integ.UID, first["uid"])
	r.Equal("Ops Mail", first["name"])
	second, _ := got[1].(map[string]any)
	r.Equal("user", second["type"])
	r.Equal(user.UID, second["uid"])
}

func TestEscalatedEvent_SkippedIntegrationNotListed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")
	user := seedEscalationUser(t, env, "ana@acme.com", "en", "UTC")

	integ := models.NewIntegration(env.org.UID, models.ConnectionTypeEmail, "Ops Mail")
	integ.Enabled = true
	r.NoError(env.db.CreateChannel(t.Context(), integ))

	targets := []*models.EscalationPolicyTarget{
		{UID: "t1", TargetType: models.EscalationTargetConnection, TargetUID: &integ.UID},
		{UID: "t2", TargetType: models.EscalationTargetUser, TargetUID: &user.UID},
	}
	// Severity allows only sms: the email integration is filtered out.
	p := escalatedPayload(t, env, newRun(), targets, map[string]bool{"sms": true})

	got, ok := p["targets"].([]any)
	r.True(ok)
	r.Len(got, 1)
	only, _ := got[0].(map[string]any)
	r.Equal("user", only["type"])
	r.EqualValues(1, p["skipped"])
}
