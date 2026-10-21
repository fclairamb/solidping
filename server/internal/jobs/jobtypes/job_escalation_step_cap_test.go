package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// Spec 2026-10-17-03: a bring-your-own send skips the plan's monthly cap; an
// instance-credential send consumes it; the first monthly-cap refusal of a
// kind in a month emails the org's admins.

func smsVoiceFilter() map[string]bool { return map[string]bool{"sms": true, "voice": true} }

// saasEntitlements wires the SaaS defaults, where every phone channel is
// capped at 0 per month, with the given clock.
func saasEntitlements(env *phoneTestEnv, now func() time.Time) *entitlements.Service {
	svc := entitlements.NewService(
		env.db, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0, entitlements.WithClock(now),
	)
	env.jctx.Services.Entitlements = svc

	return svc
}

// capRun seeds a real escalation step (the audit row's step_uid is a foreign
// key) and returns a run on it, so skipped rows are actually written.
func capRun(t *testing.T, env *phoneTestEnv) func() *EscalationStepJobRun {
	t.Helper()

	policy := models.NewEscalationPolicy(env.org.UID, "cap-pol")
	require.NoError(t, env.db.CreateEscalationPolicy(t.Context(), policy))

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	require.NoError(t, env.db.ReplaceEscalationPolicySteps(t.Context(), policy.UID,
		[]*models.EscalationPolicyStep{step}, map[int][]*models.EscalationPolicyTarget{}))

	return func() *EscalationStepJobRun {
		return &EscalationStepJobRun{config: EscalationStepJobConfig{
			IncidentUID: env.incident.UID, StepUID: step.UID,
		}}
	}
}

// skipReasons lists the skip reasons of the env incident's audit rows.
func skipReasons(t *testing.T, env *phoneTestEnv) []string {
	t.Helper()

	var rows []models.IncidentNotification
	require.NoError(t, env.db.DB().NewSelect().Model(&rows).
		Where("incident_uid = ?", env.incident.UID).Scan(t.Context()))

	out := make([]string, 0, len(rows))

	for i := range rows {
		if rows[i].SkipReason != nil {
			out = append(out, *rows[i].SkipReason)
		}
	}

	return out
}

func monthlyCount(t *testing.T, env *phoneTestEnv, kind string) int {
	t.Helper()

	now := time.Now().UTC()
	period := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")

	count, err := env.db.GetMonthlyUsage(t.Context(), env.org.UID, kind, period)
	require.NoError(t, err)

	return count
}

// TestCap_BringYourOwnIgnoresMonthlyCap is the bug fix: SaaS defaults (cap 0)
// plus the org's own Twilio account with a voice number. SMS and voice both go
// out through the org's account and no *_quota_exhausted row is written.
func TestCap_BringYourOwnIgnoresMonthlyCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "+15559990001")
	run := capRun(t, env)
	orgFake, orgSrv := newFakeTwilio(t)
	env.useFakeTwilio(orgSrv)
	saasEntitlements(env, time.Now)

	sent := run().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsVoiceFilter())

	r.Equal(2, sent)
	r.Equal(1, orgFake.smsCount())
	r.Equal(1, orgFake.callCount())
	r.Empty(skipReasons(t, env))
	r.Equal(0, monthlyCount(t, env, models.UsageCounterKindSMS), "bring-your-own consumes no allowance")
	r.Equal(0, monthlyCount(t, env, models.UsageCounterKindVoice))
}

// TestCap_InstanceCredentialsRefusedAtZero is today's behavior, kept: the same
// org on instance credentials is refused on both channels.
func TestCap_InstanceCredentialsRefusedAtZero(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	run := capRun(t, env)
	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL),
		instanceTwilioVoice(t, instanceSrv.URL), "+15550001111")
	saasEntitlements(env, time.Now)

	sent := run().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsVoiceFilter())

	r.Equal(0, sent)
	r.Equal(0, instanceFake.smsCount())
	r.Equal(0, instanceFake.callCount())
	r.ElementsMatch([]string{"sms_quota_exhausted", "voice_quota_exhausted"}, skipReasons(t, env))
}

// TestCap_MixedModes: the org's integration has no voice number, so SMS is
// bring-your-own (sent at cap 0) while voice falls back to the instance and is
// refused at cap 0.
func TestCap_MixedModes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "") // own Twilio, no voice_from_number
	run := capRun(t, env)
	orgFake, orgSrv := newFakeTwilio(t)
	env.useFakeTwilio(orgSrv)

	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL),
		instanceTwilioVoice(t, instanceSrv.URL), "+15550001111")
	saasEntitlements(env, time.Now)

	sent := run().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsVoiceFilter())

	r.Equal(1, sent)
	r.Equal(1, orgFake.smsCount(), "SMS goes through the org's own account")
	r.Equal(0, instanceFake.callCount(), "instance voice is refused at cap 0")
	r.Equal(0, orgFake.callCount())
	r.Equal([]string{"voice_quota_exhausted"}, skipReasons(t, env))
}

// TestCap_WhatsAppCountedWhateverTwilioMode: WhatsApp is instance-only, so a
// bring-your-own Twilio account changes nothing for it.
func TestCap_WhatsAppCountedWhateverTwilioMode(t *testing.T) {
	t.Parallel()

	for _, withOrgTwilio := range []bool{false, true} {
		r := require.New(t)
		ctx := context.Background()

		env := setupPhoneEnv(t, withOrgTwilio, "+15559990001")
		fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
		enableWhatsApp(env, baseURL)
		entSvc := saasEntitlements(env, time.Now)

		filter := map[string]bool{"whatsapp": true}

		r.Equal(0, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
			whatsAppRoute(env.org.UID, true), filter), "cap 0 refuses (own twilio: %v)", withOrgTwilio)
		r.Equal(0, fake.count())

		r.NoError(entSvc.Set(ctx, env.org.UID, entitlements.Entitlements{
			Limits: entitlements.Limits{MaxWhatsappPerMonth: entitlements.Int(1)},
			Source: models.EntitlementSourceAdmin,
		}, "tester", "one per month"))

		r.Equal(1, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
			whatsAppRoute(env.org.UID, true), filter))
		r.Equal(1, monthlyCount(t, env, models.UsageCounterKindWhatsApp),
			"WhatsApp is counted (own twilio: %v)", withOrgTwilio)
	}
}

// seedMember adds a user to the env org with the given role.
func seedMember(t *testing.T, env *phoneTestEnv, mail string, role models.MemberRole) {
	t.Helper()

	user := models.NewUser(mail)
	require.NoError(t, env.db.CreateUser(t.Context(), user))
	require.NoError(t, env.db.CreateOrganizationMember(t.Context(),
		models.NewOrganizationMember(env.org.UID, user.UID, role)))
}

// TestCap_AdminNoticeOncePerMonth: two monthly-cap refusals in a month send one
// email per admin (not to plain members); a refusal the next month sends
// another. The mail renders and carries the upgrade URL.
func TestCap_AdminNoticeOncePerMonth(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	_, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")

	seedMember(t, env, "alice@acme.com", models.MemberRoleAdmin)
	seedMember(t, env, "bob@acme.com", models.MemberRoleUser)

	r.NoError(env.db.SetSystemParameter(ctx, entitlements.ParamUpgradeURLTemplate,
		"https://billing.acme.com/{org}/plans", false))

	jobs := &capturingJobService{}
	env.jctx.Services.Jobs = jobs

	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	saasEntitlements(env, func() time.Time { return now })

	for range 2 {
		r.Equal(0, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
			verifiedPhoneRoute(env.org.UID), smsFilter()))
	}

	r.Equal([]string{"alice@acme.com"}, jobs.recipients(t), "one notice per admin per month")

	var cfg EmailJobConfig
	r.NoError(json.Unmarshal(jobs.created[0].config, &cfg))
	r.Equal(email.TemplateUsageCapReached, cfg.Template)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	subject, _, text, err := formatter.Format(cfg.Template, i18n.English, cfg.TemplateData)
	r.NoError(err)
	r.Contains(subject, "SMS")
	r.Contains(text, "https://billing.acme.com/test-org/plans")
	r.Contains(text, "0 SMS alerts")

	now = now.AddDate(0, 1, 0)

	r.Equal(0, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter()))
	r.Equal([]string{"alice@acme.com", "alice@acme.com"}, jobs.recipients(t), "a new month sends a new notice")
}

// TestCap_RunawayRefusalSendsNoNotice: the hourly runaway guard is not the
// plan running out, so it never emails the admins.
func TestCap_RunawayRefusalSendsNoNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	run := capRun(t, env)
	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")
	seedMember(t, env, "alice@acme.com", models.MemberRoleAdmin)

	jobs := &capturingJobService{}
	env.jctx.Services.Jobs = jobs

	// Self-hosted: unlimited monthly, runaway cap of one SMS per hour.
	env.jctx.Services.Entitlements = entitlements.NewService(
		env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
		entitlements.WithRunawayCaps(1, 1),
	)

	r.Equal(1, run().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter()))
	r.Equal(0, run().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter()), "the runaway guard refuses the second")
	r.Equal(1, instanceFake.smsCount())
	r.Equal([]string{"sms_quota_exhausted"}, skipReasons(t, env))
	r.Empty(jobs.created, "a runaway refusal sends no admin notice")
}
