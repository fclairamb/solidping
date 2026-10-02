package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// escalationFixture is a phone env plus a real job service, a user, a webhook
// channel and an escalation policy whose single step the test shapes.
type escalationFixture struct {
	env     *phoneTestEnv
	user    *models.User
	channel *models.Integration
	policy  *models.EscalationPolicy
	jobs    jobsvc.Service
}

func newEscalationFixture(t *testing.T, repeatMax int, repeatAfter *int) *escalationFixture {
	t.Helper()

	ctx := context.Background()
	env := setupPhoneEnv(t, false, "")

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	jobs := jobsvc.NewService(env.db.DB(), env.db, events, nil)
	env.jctx.Services.Jobs = jobs

	user := models.NewUser("alice@acme.com")
	require.NoError(t, env.db.CreateUser(ctx, user))

	channel := models.NewIntegration(env.org.UID, models.ConnectionTypeWebhook, "acme hook")
	channel.Enabled = true
	channel.Settings = models.JSONMap{"url": "https://hooks.acme.com/x"}
	require.NoError(t, env.db.CreateChannel(ctx, channel))

	policy := models.NewEscalationPolicy(env.org.UID, "acme policy")
	policy.RepeatMax = repeatMax
	policy.RepeatAfterSeconds = repeatAfter
	require.NoError(t, env.db.CreateEscalationPolicy(ctx, policy))

	return &escalationFixture{env: env, user: user, channel: channel, policy: policy, jobs: jobs}
}

// setStep stores a single-step policy and returns the step.
func (f *escalationFixture) setStep(
	t *testing.T, severityUID *string, targets ...*models.EscalationPolicyTarget,
) *models.EscalationPolicyStep {
	t.Helper()

	step := models.NewEscalationPolicyStep(f.policy.UID, 0, 0)
	step.SeverityUID = severityUID
	for _, tg := range targets {
		tg.StepUID = step.UID
	}

	require.NoError(t, f.env.db.ReplaceEscalationPolicySteps(
		context.Background(), f.policy.UID, []*models.EscalationPolicyStep{step},
		map[int][]*models.EscalationPolicyTarget{0: targets},
	))

	steps, err := f.env.db.ListEscalationPolicySteps(context.Background(), f.policy.UID)
	require.NoError(t, err)
	require.Len(t, steps, 1)

	return steps[0]
}

func (f *escalationFixture) run(stepUID string, repeatIndex int, last bool) error {
	cfg := EscalationStepJobConfig{
		IncidentUID: f.env.incident.UID, StepUID: stepUID, PolicyUID: f.policy.UID,
		RepeatIndex: repeatIndex, IsLastStep: last,
	}

	return (&EscalationStepJobRun{config: cfg}).Run(context.Background(), f.env.jctx)
}

func (f *escalationFixture) eventTypes(t *testing.T) []models.EventType {
	t.Helper()

	events, err := f.env.db.ListEvents(context.Background(), &models.ListEventsFilter{
		OrganizationUID: f.env.org.UID,
	})
	require.NoError(t, err)

	out := make([]models.EventType, 0, len(events))
	for _, e := range events {
		out = append(out, e.EventType)
	}

	return out
}

func (f *escalationFixture) jobCount(t *testing.T, jobType jobdef.JobType) int {
	t.Helper()

	list, err := f.jobs.ListJobs(context.Background(), f.env.org.UID, jobsvc.ListJobsOptions{Type: string(jobType)})
	require.NoError(t, err)

	return len(list)
}

func uidPtr(s string) *string { return &s }

func TestEscalationStepJobDefinition(t *testing.T) {
	t.Parallel()

	def := &EscalationStepJobDefinition{}
	require.Equal(t, jobdef.JobTypeEscalationStep, def.Type())

	tests := []struct {
		name    string
		config  string
		wantErr error
		anyErr  bool
	}{
		{name: "valid", config: `{"incidentUid":"i","stepUid":"s"}`},
		{name: "bad json", config: `{`, anyErr: true},
		{name: "missing incident", config: `{"stepUid":"s"}`, wantErr: ErrMissingIncidentUID},
		{name: "missing step", config: `{"incidentUid":"i"}`, wantErr: ErrMissingStepUID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			run, err := def.CreateJobRun(json.RawMessage(tt.config))
			switch {
			case tt.wantErr != nil:
				r.ErrorIs(err, tt.wantErr)
			case tt.anyErr:
				r.Error(err)
			default:
				r.NoError(err)
				r.NotNil(run)
			}
		})
	}
}

func TestEscalationStepRunErrorsAndSkips(t *testing.T) {
	t.Parallel()

	t.Run("unknown step", func(t *testing.T) {
		t.Parallel()
		f := newEscalationFixture(t, 0, nil)
		require.ErrorIs(t, f.run("missing-step", 0, false), ErrEscalationStepNotFound)
	})

	t.Run("unknown incident", func(t *testing.T) {
		t.Parallel()
		f := newEscalationFixture(t, 0, nil)
		step := f.setStep(t, nil)
		f.env.incident.UID = "nope"
		require.ErrorIs(t, f.run(step.UID, 0, false), ErrIncidentNotFound)
	})

	t.Run("acknowledged incident pages nobody", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f := newEscalationFixture(t, 0, nil)
		step := f.setStep(t, nil, models.NewEscalationPolicyTarget(
			"", models.EscalationTargetUser, uidPtr(f.user.UID), 0))

		now := time.Now()
		_, err := f.env.db.DB().NewUpdate().Model((*models.Incident)(nil)).
			Set("acknowledged_at = ?", now).Where("uid = ?", f.env.incident.UID).Exec(context.Background())
		r.NoError(err)

		r.NoError(f.run(step.UID, 0, true))
		r.Zero(f.env.emails.sends())
		r.Empty(f.eventTypes(t))
	})
}

func TestEscalationStepRunFansOutAndRepeats(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newEscalationFixture(t, 2, new(int))
	*f.policy.RepeatAfterSeconds = 60
	require.NoError(t, f.env.db.UpdateEscalationPolicy(context.Background(), f.policy.UID,
		&models.EscalationPolicyUpdate{RepeatAfterSeconds: f.policy.RepeatAfterSeconds}))

	step := f.setStep(t, nil,
		models.NewEscalationPolicyTarget("", models.EscalationTargetConnection, uidPtr(f.channel.UID), 0),
		models.NewEscalationPolicyTarget("", models.EscalationTargetConnection, nil, 1),
		models.NewEscalationPolicyTarget("", models.EscalationTargetUser, uidPtr(f.user.UID), 2),
		models.NewEscalationPolicyTarget("", models.EscalationTargetUser, nil, 3),
		models.NewEscalationPolicyTarget("", models.EscalationTargetSchedule, uidPtr("sched-1"), 4),
		models.NewEscalationPolicyTarget("", models.EscalationTargetSchedule, nil, 5),
		models.NewEscalationPolicyTarget("", models.EscalationTargetAllAdmins, nil, 6),
	)

	r.NoError(f.run(step.UID, 0, true))

	r.Equal(1, f.env.emails.sends(), "the user fallback email")
	r.Equal(1, f.jobCount(t, jobdef.JobTypeNotification), "the connection target")
	r.Equal(1, f.jobCount(t, jobdef.JobTypeEscalationStep), "the next cycle is queued")

	types := f.eventTypes(t)
	r.Contains(types, models.EventTypeIncidentEscalated)
	r.Contains(types, models.EventTypeIncidentEscalationFailed, "the unwired on-call resolver is a soft failure")

	// Last cycle: repeat budget is exhausted, nothing more is queued.
	r.NoError(f.run(step.UID, 2, true))
	r.Equal(1, f.jobCount(t, jobdef.JobTypeEscalationStep))
}

func TestEscalationStepSeverityFilter(t *testing.T) {
	t.Parallel()

	targets := func(f *escalationFixture) []*models.EscalationPolicyTarget {
		return []*models.EscalationPolicyTarget{
			models.NewEscalationPolicyTarget("", models.EscalationTargetConnection, uidPtr(f.channel.UID), 0),
			models.NewEscalationPolicyTarget("", models.EscalationTargetUser, uidPtr(f.user.UID), 1),
			models.NewEscalationPolicyTarget("", models.EscalationTargetSchedule, uidPtr("s"), 2),
			models.NewEscalationPolicyTarget("", models.EscalationTargetAllAdmins, nil, 3),
		}
	}

	tests := []struct {
		name         string
		channels     []string
		wantNotifs   int
		wantSkipped  float64
		wantEmailCnt int
	}{
		{name: "slack only skips everything here", channels: []string{"slack"}, wantSkipped: 4},
		{name: "webhook only delivers the connection", channels: []string{"webhook"}, wantNotifs: 1, wantSkipped: 3},
		{name: "webhook and email", channels: []string{"webhook", "email"}, wantNotifs: 1, wantEmailCnt: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := context.Background()

			f := newEscalationFixture(t, 0, nil)
			sev := models.NewSeverity(f.env.org.UID, "sev-"+tt.channels[0], "Sev", tt.channels, false)
			r.NoError(f.env.db.CreateSeverity(ctx, sev))

			step := f.setStep(t, &sev.UID, targets(f)...)
			r.NoError(f.run(step.UID, 0, false))

			r.Equal(tt.wantNotifs, f.jobCount(t, jobdef.JobTypeNotification))
			r.Equal(tt.wantEmailCnt, f.env.emails.sends())

			events, err := f.env.db.ListEvents(ctx, &models.ListEventsFilter{
				OrganizationUID: f.env.org.UID,
				EventTypes:      []models.EventType{models.EventTypeIncidentEscalated},
			})
			r.NoError(err)
			r.Len(events, 1)
			r.InDelta(tt.wantSkipped, payloadNumber(events[0].Payload["skipped"]), 0.001)
		})
	}

	t.Run("unknown severity fires without filter", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		f := newEscalationFixture(t, 0, nil)
		run := newRun()
		step := &models.EscalationPolicyStep{SeverityUID: uidPtr("ghost")}
		r.Nil(run.resolveSeverityChannels(context.Background(), f.env.jctx, slog.Default(), step, f.env.org.UID))

		step.SeverityUID = nil
		r.Nil(run.resolveSeverityChannels(context.Background(), f.env.jctx, slog.Default(), step, f.env.org.UID))
	})

	t.Run("connection filter edge cases", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		ctx := context.Background()

		f := newEscalationFixture(t, 0, nil)
		run := newRun()
		log := slog.Default()

		withUID := func(uid *string) *models.EscalationPolicyTarget {
			return &models.EscalationPolicyTarget{TargetType: models.EscalationTargetConnection, TargetUID: uid}
		}

		r.True(run.connectionPassesSeverityFilter(ctx, f.env.jctx, log, withUID(nil), nil))
		r.False(run.connectionPassesSeverityFilter(ctx, f.env.jctx, log, withUID(nil), map[string]bool{"email": true}))
		r.False(run.connectionPassesSeverityFilter(ctx, f.env.jctx, log, withUID(uidPtr("ghost")),
			map[string]bool{"email": true}))

		twilio := models.NewIntegration(f.env.org.UID, models.ConnectionTypeTwilio, "acme phone")
		r.NoError(f.env.db.CreateChannel(ctx, twilio))

		r.True(run.connectionPassesSeverityFilter(ctx, f.env.jctx, log, withUID(&twilio.UID),
			map[string]bool{channelTokenSMS: true}))
		r.True(run.connectionPassesSeverityFilter(ctx, f.env.jctx, log, withUID(&twilio.UID),
			map[string]bool{channelTokenVoice: true}))
		r.False(run.connectionPassesSeverityFilter(ctx, f.env.jctx, log, withUID(&twilio.UID),
			map[string]bool{"email": true}))
	})
}

func TestEscalationStepScheduleNextCycleNoOps(t *testing.T) {
	t.Parallel()

	after := 30
	tests := []struct {
		name   string
		policy models.EscalationPolicy
		index  int
	}{
		{name: "no repeat", policy: models.EscalationPolicy{RepeatMax: 0, RepeatAfterSeconds: &after}},
		{name: "budget spent", policy: models.EscalationPolicy{RepeatMax: 1, RepeatAfterSeconds: &after}, index: 1},
		{name: "no delay", policy: models.EscalationPolicy{RepeatMax: 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := &EscalationStepJobRun{config: EscalationStepJobConfig{RepeatIndex: tt.index}}
			require.NoError(t, run.scheduleNextCycle(
				context.Background(), &jobdef.JobContext{}, slog.Default(), &models.Incident{}, &tt.policy))
		})
	}
}

func TestScheduleEscalationCycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newEscalationFixture(t, 0, nil)
	incident := f.env.incident
	log := slog.Default()

	// No steps: nothing is queued and no job service is needed.
	r.NoError(ScheduleEscalationCycle(context.Background(), nil, incident, f.policy, nil, time.Now(), 0, log))

	steps := []*models.EscalationPolicyStep{
		{UID: "s1", DelaySeconds: 0},
		{UID: "s2", DelaySeconds: 120},
	}
	start := time.Now().Add(time.Hour)
	r.NoError(ScheduleEscalationCycle(context.Background(), f.jobs, incident, f.policy, steps, start, 1, log))

	list, err := f.jobs.ListJobs(context.Background(), incident.OrganizationUID,
		jobsvc.ListJobsOptions{Type: string(jobdef.JobTypeEscalationStep)})
	r.NoError(err)
	r.Len(list, 2)

	lastCount := 0
	for _, job := range list {
		var cfg EscalationStepJobConfig
		raw, marshalErr := json.Marshal(job.Config)
		r.NoError(marshalErr)
		r.NoError(json.Unmarshal(raw, &cfg))
		r.Equal(1, cfg.RepeatIndex)

		if cfg.IsLastStep {
			lastCount++
			r.Equal("s2", cfg.StepUID)
		}
	}
	r.Equal(1, lastCount)
}

func TestEmitEscalationFailedRecordsCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newEscalationFixture(t, 0, nil)
	incident := *f.env.incident

	run := newRun()
	run.emitEscalationFailed(context.Background(), f.env.jctx, &incident, "schedule_resolve_failed", "boom")

	events, err := f.env.db.ListEvents(context.Background(), &models.ListEventsFilter{
		OrganizationUID: f.env.org.UID,
		EventTypes:      []models.EventType{models.EventTypeIncidentEscalationFailed},
	})
	r.NoError(err)
	r.Len(events, 1)
	r.Equal("boom", events[0].Payload["detail"])
	r.Contains(events[0].Payload, "check_slug")
}

func payloadNumber(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	}

	return -1
}

func TestSeverityAllowsMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		fn     func(map[string]bool) bool
		filter map[string]bool
		want   bool
	}{
		{"webpush nil", severityAllowsWebPush, nil, true},
		{"webpush email compat", severityAllowsWebPush, map[string]bool{channelTokenEmail: true}, true},
		{"webpush push", severityAllowsWebPush, map[string]bool{channelTokenPush: true}, true},
		{"webpush critical", severityAllowsWebPush, map[string]bool{channelTokenCriticalPush: true}, true},
		{"webpush sms only", severityAllowsWebPush, map[string]bool{channelTokenSMS: true}, false},
		{"sms nil", severityAllowsSMS, nil, true},
		{"sms token", severityAllowsSMS, map[string]bool{channelTokenSMS: true}, true},
		{"sms other", severityAllowsSMS, map[string]bool{channelTokenEmail: true}, false},
		{"voice nil is off", severityAllowsVoice, nil, false},
		{"voice token", severityAllowsVoice, map[string]bool{channelTokenVoice: true}, true},
		{"whatsapp nil is off", severityAllowsWhatsApp, nil, false},
		{"whatsapp token", severityAllowsWhatsApp, map[string]bool{channelTokenWhatsApp: true}, true},
		{"telegram nil", severityAllowsTelegram, nil, true},
		{"telegram token", severityAllowsTelegram, map[string]bool{channelTokenTelegram: true}, true},
		{"telegram other", severityAllowsTelegram, map[string]bool{channelTokenSMS: true}, false},
		{"discord nil", severityAllowsDiscord, nil, true},
		{"discord token", severityAllowsDiscord, map[string]bool{channelTokenDiscord: true}, true},
		{"discord other", severityAllowsDiscord, map[string]bool{channelTokenSMS: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.fn(tt.filter))
		})
	}
}

func TestEscalationPageAllAdmins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		role       models.MemberRole
		filter     map[string]bool
		wantEmails int
	}{
		{name: "admin gets the fallback email", role: models.MemberRoleAdmin, wantEmails: 1},
		{name: "plain member is not paged", role: models.MemberRoleViewer, wantEmails: 0},
		{
			name: "sms-only severity sends no email", role: models.MemberRoleAdmin,
			filter: map[string]bool{channelTokenSMS: true}, wantEmails: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := context.Background()

			f := newEscalationFixture(t, 0, nil)
			r.NoError(f.env.db.CreateOrganizationMember(ctx,
				models.NewOrganizationMember(f.env.org.UID, f.user.UID, tt.role)))

			sent := newRun().pageAllAdmins(ctx, f.env.jctx, slog.Default(), f.env.incident, tt.filter)
			r.Equal(tt.wantEmails, sent)
			r.Equal(tt.wantEmails, f.env.emails.sends())
		})
	}
}
