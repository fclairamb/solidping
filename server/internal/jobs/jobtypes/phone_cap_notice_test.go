package jobtypes

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// seedAdminWithPreferences adds an admin member whose account has preferences.
func seedAdminWithPreferences(t *testing.T, env *phoneTestEnv, address, locale, timezone string) {
	t.Helper()

	user := models.NewUser(address)
	if locale != "" || timezone != "" {
		user.Preferences = &models.UserPreferences{Locale: locale, Timezone: timezone}
	}

	require.NoError(t, env.db.CreateUser(t.Context(), user))
	require.NoError(t, env.db.CreateOrganizationMember(t.Context(),
		models.NewOrganizationMember(env.org.UID, user.UID, models.MemberRoleAdmin)))
}

// capNoticeJobs decodes the queued email jobs by recipient.
func capNoticeJobs(t *testing.T, jobs *capturingJobService) map[string]EmailJobConfig {
	t.Helper()

	out := map[string]EmailJobConfig{}

	for _, job := range jobs.created {
		var cfg EmailJobConfig
		require.NoError(t, json.Unmarshal(job.config, &cfg))
		out[cfg.To[0]] = cfg
	}

	return out
}

// TestCapNotice_SpeaksEachAdminsLanguage: one job per admin, each carrying the
// admin's language and zone, and the job's JSON round trip (float64 limit,
// RFC 3339 reset date) renders the per-channel sentences with a real date.
func TestCapNotice_SpeaksEachAdminsLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")

	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	saasEntitlements(env, func() time.Time { return now })
	env.jctx.Services.Clock = clock.NewFake(now)

	jobs := &capturingJobService{}
	env.jctx.Services.Jobs = jobs

	seedAdminWithPreferences(t, env, "lea@acme.com", "fr", "Europe/Paris")
	seedAdminWithPreferences(t, env, "sam@acme.com", "", "")

	notifyMonthlyCapReached(t.Context(), env.jctx, slog.Default(), env.org.UID, models.UsageCounterKindVoice,
		&entitlements.QuotaError{LimitName: "MaxVoicePerMonth", Limit: 1, CurrentUsage: 1})

	cfgs := capNoticeJobs(t, jobs)
	r.Len(cfgs, 2)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	resetsAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	lea := cfgs["lea@acme.com"]
	r.Equal("fr", lea.Locale)
	r.Equal("Europe/Paris", lea.Timezone)

	loc := i18n.New(lea.Locale, lea.Timezone)
	subject, _, text, err := formatter.Format(lea.Template, loc, lea.TemplateData)
	r.NoError(err)
	r.Equal("🟠 Test Org a utilisé toutes ses alertes par appel vocal du mois", subject)
	r.Contains(text, "Test Org a épuisé son quota de 1 appel vocal ce mois-ci.")
	r.Contains(text, "jusqu'au "+loc.Date(resetsAt)+".")
	r.Contains(loc.Date(resetsAt), "02:00 CEST", "the reset is shown in the reader's zone")
	r.Contains(text, "compte Twilio")

	sam := cfgs["sam@acme.com"]
	r.Equal(i18n.LangEnglish, sam.Locale, "an admin without preferences reads English, never the owner's")
	r.Equal("UTC", sam.Timezone)

	subject, _, text, err = formatter.Format(sam.Template, i18n.New(sam.Locale, sam.Timezone), sam.TemplateData)
	r.NoError(err)
	r.Equal("🟠 Test Org used all its voice call alerts for this month", subject)
	r.Contains(text, "Test Org has used its 1 voice call alert included this month.")
	r.Contains(text, "until "+i18n.English.Date(resetsAt)+".")
}

// TestUsageCapEmailData_ChannelPerKind: the channel picks a whole set of
// sentences; WhatsApp has no Twilio alternative.
func TestUsageCapEmailData_ChannelPerKind(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	resetsAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		kind, channel, subject string
		twilio                 bool
	}{
		{models.UsageCounterKindSMS, capNoticeChannelSMS, "SMS alerts", true},
		{models.UsageCounterKindVoice, capNoticeChannelVoice, "voice call alerts", true},
		{models.UsageCounterKindWhatsApp, capNoticeChannelWhatsApp, "WhatsApp alerts", false},
	}

	for _, tc := range cases {
		data := UsageCapEmailData(tc.kind, 1000, resetsAt, "https://acme.com/usage", "")
		r.Equal(tc.channel, data["Channel"])

		for _, lang := range i18n.Supported() {
			subject, html, text, err := formatter.Format(email.TemplateUsageCapReached, i18n.New(lang, ""), data)
			r.NoError(err, "%s/%s", tc.kind, lang)
			r.NotContains(subject+html+text, "alerts.usageCap", "%s/%s: a key leaked", tc.kind, lang)
			r.Equal(tc.twilio, strings.Contains(text, "Twilio"), "%s/%s", tc.kind, lang)
		}

		subject, _, text, err := formatter.Format(email.TemplateUsageCapReached, i18n.English, data)
		r.NoError(err)
		r.Contains(subject, tc.subject)
		r.Contains(text, "has used its 1000 ", "the limit prints like the old %d")
	}
}

// TestUsageCapEmailData_LimitSurvivesTheJob: the email job decodes the limit
// as a float64, which fmt prints "1e+06". The count goes through the plural
// forms, which print it whole, without a thousands separator (like the old
// %d of the English mail).
func TestUsageCapEmailData_LimitSurvivesTheJob(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	raw, err := json.Marshal(UsageCapEmailData(models.UsageCounterKindSMS, 1000000,
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), "", ""))
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(raw, &decoded))

	want := map[string]string{
		"en": "the plan includes 1000000 per month.",
		"fr": "en inclut 1000000 par mois.",
		"de": "Der Tarif enthält 1000000 pro Monat.",
		"es": "el plan incluye 1000000 al mes.",
	}

	for lang, sentence := range want {
		_, html, text, err := formatter.Format(email.TemplateUsageCapReached, i18n.New(lang, ""), decoded)
		r.NoError(err, lang)
		r.Contains(html, sentence, "%s: the preheader", lang)
		r.Contains(text, " 1000000 ", lang)
		r.NotContains(html+text, "e+06", lang)
	}
}
