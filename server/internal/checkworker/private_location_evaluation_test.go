package checkworker

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// Spec 2026-09-25-05: the private-location liveness monitor.

func plAgent(name, status string, lastSeen *time.Time) *models.Agent {
	org := "org"

	return &models.Agent{
		UID: uuid.NewString(), OrganizationUID: &org, Kind: models.AgentKindOrg,
		Region: "@office", Name: name, Status: status, LastSeenAt: lastSeen,
	}
}

func ago(now time.Time, d time.Duration) *time.Time {
	at := now.Add(-d)

	return &at
}

// TestPrivateLocationEvaluationTable pins the §1 verdict table.
func TestPrivateLocationEvaluationTable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 13, 50, 0, 0, time.UTC)
	stale := time.Date(2026, 9, 25, 13, 41, 0, 0, time.UTC)

	tests := []struct {
		name          string
		agents        []*models.Agent
		everEvaluated bool
		wantGrace     bool
		wantStatus    checkerdef.Status
		wantMessage   string
		wantOnline    int
		wantTotal     int
	}{
		{
			name:        "one agent seen 30s ago is up",
			agents:      []*models.Agent{plAgent("office-1", models.AgentStatusActive, ago(now, 30*time.Second))},
			wantStatus:  checkerdef.StatusUp,
			wantMessage: "1 agent connected",
			wantOnline:  1, wantTotal: 1,
		},
		{
			name: "two agents seen are up",
			agents: []*models.Agent{
				plAgent("office-1", models.AgentStatusActive, ago(now, time.Minute)),
				plAgent("office-2", models.AgentStatusActive, ago(now, 5*time.Minute)), // window is inclusive
			},
			wantStatus:  checkerdef.StatusUp,
			wantMessage: "2 agents connected",
			wantOnline:  2, wantTotal: 2,
		},
		{
			name: "one of two stale is a warning naming it",
			agents: []*models.Agent{
				plAgent("office-1", models.AgentStatusActive, ago(now, 10*time.Second)),
				plAgent("office-2", models.AgentStatusActive, &stale),
			},
			wantStatus:  checkerdef.StatusWarning,
			wantMessage: "1 of 2 agents offline: office-2 last seen 13:41 UTC",
			wantOnline:  1, wantTotal: 2,
		},
		{
			name: "all stale for more than 5 minutes is down, citing the newest last seen",
			agents: []*models.Agent{
				plAgent("office-1", models.AgentStatusActive, &stale),
				plAgent("office-2", models.AgentStatusActive, ago(now, time.Hour)),
			},
			wantStatus:  checkerdef.StatusDown,
			wantMessage: "No agent connected since 13:41 UTC",
			wantOnline:  0, wantTotal: 2,
		},
		{
			name: "a revoked agent seen recently does not count as connected",
			agents: []*models.Agent{
				plAgent("office-1", models.AgentStatusActive, &stale),
				plAgent("old", models.AgentStatusRevoked, ago(now, time.Second)),
			},
			wantStatus:  checkerdef.StatusDown,
			wantMessage: "No agent connected since 13:49 UTC",
			wantOnline:  0, wantTotal: 1,
		},
		{
			name:      "no agent enrolled yet is created, nothing written",
			agents:    nil,
			wantGrace: true,
		},
		{
			name:      "only revoked agents and never evaluated is still created",
			agents:    []*models.Agent{plAgent("old", models.AgentStatusRevoked, &stale)},
			wantGrace: true,
		},
		{
			name:          "every agent gone after the monitor ran is down",
			agents:        []*models.Agent{plAgent("old", models.AgentStatusRevoked, &stale)},
			everEvaluated: true,
			wantStatus:    checkerdef.StatusDown,
			wantMessage:   "No agent connected since 13:41 UTC",
		},
		{
			name:          "no agent at all after the monitor ran is down without a time",
			everEvaluated: true,
			wantStatus:    checkerdef.StatusDown,
			wantMessage:   "No agent connected",
		},
		{
			name:        "an agent seen yesterday is quoted with its date",
			agents:      []*models.Agent{plAgent("office-1", models.AgentStatusActive, ago(now, 24*time.Hour))},
			wantStatus:  checkerdef.StatusDown,
			wantMessage: "No agent connected since 2026-09-24 13:50 UTC",
			wantTotal:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			status, output, grace := privateLocationEvaluation("@office", tt.agents, tt.everEvaluated, now)
			if tt.wantGrace {
				r.True(grace)
				r.Nil(output)

				return
			}

			r.False(grace)
			r.Equal(tt.wantStatus, status)
			r.Equal(tt.wantMessage, output[outputKeyMessage])
			r.Equal(tt.wantOnline, output[outputKeyAgentsOnline])
			r.Equal(tt.wantTotal, output[outputKeyAgentsTotal])
			r.Equal("@office", output[outputKeyRegion])
			r.Equal(true, output[outputKeyEvaluation])
		})
	}
}

// TestPrivateLocationDownLinksLastDisconnect: a Down output names the last
// recorded disconnect reason.
func TestPrivateLocationDownLinksLastDisconnect(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 9, 25, 13, 50, 0, 0, time.UTC)
	output := map[string]any{outputKeyMessage: "No agent connected since 13:41 UTC"}

	event := models.NewEvent("org", models.EventTypeAgentDisconnected, models.ActorTypeSystem)
	event.CreatedAt = time.Date(2026, 9, 25, 13, 42, 0, 0, time.UTC)
	event.Payload = models.JSONMap{
		models.AgentEventPayloadReason: models.AgentDisconnectReasonPingTimeout,
		models.AgentEventPayloadRegion: "@office",
		audit.PayloadKeyTargetName:     "office-1",
	}

	attachLastDisconnect(output, event, now)

	r.Equal("No agent connected since 13:41 UTC (last disconnect: ping timeout at 13:42 UTC)", output[outputKeyMessage])
	r.Equal(models.AgentDisconnectReasonPingTimeout, output[outputKeyLastDisconnectReason])
	r.Equal(event.UID, output[outputKeyLastDisconnectEvent])
	r.Equal("office-1", output[outputKeyLastDisconnectAgent])
}

// TestPrivateLocationNotEvaluableOnAnAgent: a backend with no database access
// (a deported agent) refuses the type rather than guessing.
func TestPrivateLocationNotEvaluableOnAnAgent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var be backend.WorkerBackend = &backend.WSBackend{}

	job := &models.CheckJob{Type: string(checkerdef.CheckTypePrivateLocation), Config: models.JSONMap{"region": "@office"}}

	status, output, grace, err := passiveVerdict(t.Context(), be, job, time.Now())
	r.ErrorIs(err, errPrivateLocationNotEvaluable)
	r.Zero(status)
	r.Nil(output)
	r.False(grace)
}

// plEnv adds a private-location monitor and agents to a passiveEvalEnv.
func (env *passiveEvalEnv) privateLocationMonitor(t *testing.T, confirmSeconds int) *models.Check {
	t.Helper()

	check := models.NewCheck(env.org.UID, "private-location-office", string(checkerdef.CheckTypePrivateLocation))
	check.Config = models.JSONMap{"region": "@office"}
	check.ConfirmationPeriodSeconds = confirmSeconds
	check.RecoveryPeriodSeconds = 0
	require.NoError(t, env.db.CreateCheck(env.ctx, check))

	jobs, err := env.db.ListCheckJobsByCheckUID(env.ctx, check.UID)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Nil(t, jobs[0].Region, "the monitor runs nowhere: one NULL-region job")

	env.makeDue(t, check)

	return check
}

func (env *passiveEvalEnv) enrollAgent(t *testing.T, name string, lastSeen *time.Time) *models.Agent {
	t.Helper()

	agent := models.NewAgent(env.org.UID, "@office", name, "ed-"+name, "age1"+name, "fp-"+name)
	agent.LastSeenAt = lastSeen
	_, err := env.db.DB().NewInsert().Model(agent).Exec(env.ctx)
	require.NoError(t, err)

	return agent
}

func (env *passiveEvalEnv) setLastSeen(t *testing.T, agent *models.Agent, at time.Time) {
	t.Helper()
	require.NoError(t, env.db.UpdateAgentLastSeen(env.ctx, agent.UID, at))
}

// TestPrivateLocationMonitorOnTheJobsNode runs the whole story through the
// jobs node's PassiveEvaluator, with no check worker and no agent process
// running anywhere: nothing enrolled (created, no incident), connected (up),
// silent past the window (down, incident after the confirmation period),
// reconnected (resolved).
func TestPrivateLocationMonitorOnTheJobsNode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, ctx := newPassiveEvalEnv(t)
	// Resolving an incident queues its notice job.
	env.svc.Jobs = jobsvc.NewService(env.db.DB(), env.db, env.svc.EventNotifier, nil)
	evaluator := env.evaluator(t, "jobs-node-a")

	check := env.privateLocationMonitor(t, 60)

	// No agent enrolled: created, nothing written, no incident — and the
	// schedule still moves on.
	evaluated, _, err := evaluator.RunOnce(ctx)
	r.NoError(err)
	r.Equal(1, evaluated)
	r.Empty(env.evaluations(t, check.UID))
	r.Equal(models.CheckStatusCreated, env.check(t, check.UID).Status)
	r.Zero(env.incidentCount(t, check.UID))
	r.True(env.job(t, check.UID).ScheduledAt.After(time.Now()))
	r.False(env.check(t, check.UID).IsDataStale(time.Now().Add(time.Hour)),
		"a monitor awaiting its first agent never reads as stale")

	// An agent connects: up.
	agent := env.enrollAgent(t, "office-1", ago(time.Now(), 30*time.Second))
	env.makeDue(t, check)

	_, _, err = evaluator.RunOnce(ctx)
	r.NoError(err)

	rows := env.evaluations(t, check.UID)
	r.Len(rows, 1)
	r.Equal(int(models.ResultStatusUp), *rows[0].Status)
	r.Equal("1 agent connected", rows[0].Output[outputKeyMessage])
	r.Nil(rows[0].Region)
	r.Equal(models.CheckStatusUp, env.check(t, check.UID).Status)

	// The agent goes silent past the window: down, no incident yet inside the
	// confirmation period.
	env.setLastSeen(t, agent, time.Now().Add(-10*time.Minute))
	env.makeDue(t, check)

	_, _, err = evaluator.RunOnce(ctx)
	r.NoError(err)

	rows = env.evaluations(t, check.UID)
	r.Len(rows, 2)
	r.Equal(int(models.ResultStatusDown), *rows[1].Status)
	r.Contains(rows[1].Output[outputKeyMessage], "No agent connected since")
	r.Zero(env.incidentCount(t, check.UID), "still inside the confirmation period")

	// Past the confirmation period: the incident opens.
	_, err = env.db.DB().NewUpdate().Model((*models.Check)(nil)).
		Set("first_failure_at = ?", time.Now().Add(-2*time.Minute)).
		Where("uid = ?", check.UID).Exec(ctx)
	r.NoError(err)
	env.makeDue(t, check)

	_, _, err = evaluator.RunOnce(ctx)
	r.NoError(err)
	r.Equal(1, env.incidentCount(t, check.UID))
	r.Equal(models.CheckStatusDown, env.check(t, check.UID).Status)

	// Reconnecting resolves it.
	env.setLastSeen(t, agent, time.Now())
	env.makeDue(t, check)

	_, _, err = evaluator.RunOnce(ctx)
	r.NoError(err)
	r.Equal(models.CheckStatusUp, env.check(t, check.UID).Status)

	incident, err := env.db.FindActiveIncidentByCheckUID(ctx, check.UID)
	r.Error(err, "no active incident is left")
	r.Nil(incident)
}

// TestPrivateLocationMonitorIgnoresAnotherOrgsAgents: liveness is keyed per
// (org, @slug) — another org's identically named location never counts.
func TestPrivateLocationMonitorIgnoresAnotherOrgsAgents(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, ctx := newPassiveEvalEnv(t)
	evaluator := env.evaluator(t, "jobs-node-a")

	check := env.privateLocationMonitor(t, 0)

	other := models.NewOrganization("other-org", "")
	r.NoError(env.db.CreateOrganization(ctx, other))

	foreign := models.NewAgent(other.UID, "@office", "foreign", "ed-f", "age1f", "fp-f")
	now := time.Now()
	foreign.LastSeenAt = &now
	_, err := env.db.DB().NewInsert().Model(foreign).Exec(ctx)
	r.NoError(err)

	_, _, err = evaluator.RunOnce(ctx)
	r.NoError(err)
	r.Empty(env.evaluations(t, check.UID), "the foreign agent does not enroll this org's location")
	r.Equal(models.CheckStatusCreated, env.check(t, check.UID).Status)
}
