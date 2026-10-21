package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// nbsp is the no-break space fr, de and es put between a number and its unit.
const nbsp = " "

// The readers of these tests, each in a summer-time zone.
func langFR() i18n.Locale { return i18n.New(i18n.LangFrench, "Europe/Paris") }
func langDE() i18n.Locale { return i18n.New(i18n.LangGerman, "Europe/Berlin") }
func langES() i18n.Locale { return i18n.New(i18n.LangSpanish, "Europe/Madrid") }

// localeDegradedInfo fired both rules, over a window that starts and ends in
// the summer (CEST in Paris).
func localeDegradedInfo() *DegradedInfo {
	pct := 93.8

	return &DegradedInfo{
		FailuresFired: true, FailureCount: 7, FailureSlots: 60, FailureThreshold: 5, FailureWindow: 60,
		SlowFired: true, SlowCount: 3, SlowSlots: 6, SlowThreshold: 3, SlowWindow: 6, SlowMs: 1000,
		AvailabilityPct: &pct,
		WindowFrom:      time.Date(2026, time.July, 5, 14, 2, 0, 0, time.UTC),
		WindowTo:        time.Date(2026, time.July, 5, 14, 47, 0, 0, time.UTC),
		CurrentlyUp:     true,
	}
}

func localeRegionPayload(eventType string) *Payload {
	return &Payload{
		EventType: eventType,
		Incident:  &models.Incident{UID: "inc-1"},
		Regions: &RegionInfo{
			Failing: []string{"eu-west", "us-east"}, RegionCount: 3, FailQuorum: 2,
			Labels: map[string]RegionLabel{
				"eu-west": {Emoji: "🇪🇺", Name: "Europe West"},
				"us-east": {Emoji: "🇺🇸", Name: "US East"},
			},
		},
	}
}

// TestSharedProducersKeepTheirEnglish is the contract with Slack, Discord,
// Telegram, SMS and the other channels: each producer the email localizes
// still renders, for them, exactly the English it always did. Both halves are
// pinned: X() == XIn(i18n.English), and that English, literally.
func TestSharedProducersKeepTheirEnglish(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("Someone", ackActor(nil))
	r.Equal(ackActor(nil), ackActorIn(i18n.English, nil))
	r.Equal("Someone", commentAuthor(&CommentInfo{Text: "x"}))
	r.Equal(commentAuthor(nil), commentAuthorIn(i18n.English, nil))

	for via, want := range map[string]string{
		"slack": "via Slack", "discord": "via Discord", "telegram": "via Telegram",
		"email": "via email", "phone": "via phone", "web": "", "": "",
	} {
		ack := &AckInfo{ActorName: "alice", Via: via}
		r.Equal(want, ackViaLabel(ack), via)
		r.Equal(strings.TrimPrefix(want, "via "), ackViaName(ack), via)
		r.Equal(ackViaName(ack), ackChannelIn(i18n.English, ack), via)
	}

	for source, want := range map[string]string{
		"slack": "via Slack", "telegram": "via Telegram", "web": "", "discord": "",
	} {
		comment := &CommentInfo{Source: source}
		r.Equal(want, commentSourceLabel(comment), source)
		r.Equal(strings.TrimPrefix(want, "via "), commentChannelIn(i18n.English, comment), source)
	}

	reminder := &Payload{
		Incident:    &models.Incident{StartedAt: time.Now().Add(-75*time.Minute + 10*time.Second)},
		ReminderSeq: 3,
	}
	r.Equal("Still down after 1 h 15 min, not acknowledged (reminder 3)", reminderSentence(reminder))
	r.Equal(reminderSentence(reminder), reminderSentenceIn(i18n.English, reminder, time.Now()))

	created := localeRegionPayload(string(models.EventTypeIncidentCreated))
	title, text := RegionLabelFor(created)
	r.Equal("Failing from", title)
	r.Equal("🇪🇺 Europe West, 🇺🇸 US East (2 of 3 regions)", text)
	enTitle, enText := regionLabelForIn(i18n.English, created)
	r.Equal([2]string{title, text}, [2]string{enTitle, enText})
	r.Equal("Failing from: 🇪🇺 Europe West, 🇺🇸 US East (2 of 3 regions)", RegionLine(created))

	resolvedTitle, _ := RegionLabelFor(localeRegionPayload(string(models.EventTypeIncidentResolved)))
	r.Equal("Failed from", resolvedTitle)

	info := localeDegradedInfo()
	r.Equal("7 failures in the last 60 probes (93.8% available), "+
		"and 3 of the last 6 probes were slower than 1000ms", info.Reason())
	r.Equal(info.Reason(), info.reasonIn(i18n.English))
	r.Equal("Currently up.", info.StatusText())
	r.Equal(info.StatusText(), info.statusTextIn(i18n.English))
	r.Equal("14:02 – 14:47 UTC", info.WindowText())
	r.Equal(info.WindowText(), info.windowTextIn(i18n.English))
	r.Equal("recent probes", (&DegradedInfo{}).WindowText())

	burn := BurnInfoFor(burnIncident(t))
	r.Equal("Fast burn", burn.PolicyLabel)
	r.Equal(burn.PolicyLabel, burn.policyLabelIn(i18n.English))
	r.Equal("31.0x", burn.RateText())
	r.Equal("44.5x", burn.ShortRateText())
	r.Equal("14.4x", burn.ThresholdText())
	r.Equal("1h30m", burn.BudgetRemainingText())
	r.Equal(burn.BudgetRemainingText(), burn.budgetRemainingTextIn(i18n.English))
	r.Equal("Aug 23, 04:30 UTC", burn.ProjectedExhaustionText())
	r.Equal(burn.ProjectedExhaustionText(), burn.projectedExhaustionTextIn(i18n.English))

	// Chat channels never print a year, even for an exhaustion projected
	// into the next one.
	burn.ProjectedExhaustion = time.Date(2031, time.January, 2, 3, 4, 0, 0, time.UTC)
	r.Equal("Jan 2, 03:04 UTC", burn.ProjectedExhaustionText())
	burn.ProjectedExhaustion = time.Time{}
	r.Equal("not within this window", burn.ProjectedExhaustionText())

	for in, want := range map[time.Duration]string{
		0: "0m", 5 * time.Minute: "5m", 90 * time.Minute: "1h30m", 6 * time.Hour: "6h",
		30 * 24 * time.Hour: "30d", 26 * time.Hour: "1d2h",
	} {
		r.Equal(want, humanDuration(in))
		r.Equal(humanDuration(in), humanDurationIn(i18n.English, in))
	}
}

// TestEnglishPluralsAgree: the catalog fixes "1 failures" and "1 of ... were".
func TestEnglishPluralsAgree(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	one := &DegradedInfo{FailuresFired: true, FailureCount: 1, FailureSlots: 60}
	r.Equal("1 failure in the last 60 probes", one.Reason())

	slow := &DegradedInfo{SlowFired: true, SlowCount: 1, SlowSlots: 6, SlowMs: 1000}
	r.Equal("1 of the last 6 probes was slower than 1000ms", slow.Reason())

	single := localeRegionPayload(string(models.EventTypeIncidentCreated))
	single.Regions.Failing = []string{"eu-west"}
	_, text := RegionLabelFor(single)
	r.Equal("🇪🇺 Europe West (1 of 3 regions)", text)
}

// TestOneDecimalInRoundsLikePrintf: a shared producer must not round
// differently from the %.1f it replaced, and must never group thousands.
func TestOneDecimalInRoundsLikePrintf(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, value := range []float64{0, 0.05, 0.35, 14.45, 88.35, 93.75, 99.95, 31, 10000} {
		r.Equal(fmt.Sprintf("%.1f", value), oneDecimalIn(i18n.English, value), "%v", value)
		r.Equal(strings.Replace(fmt.Sprintf("%.1f", value), ".", ",", 1), oneDecimalIn(langFR(), value), "%v", value)
	}

	r.Equal("10000,0x", multiplierIn(langDE(), 10000))
}

func TestProducersInFrench(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("Quelqu'un", ackActorIn(langFR(), &AckInfo{ActorName: " "}))
	r.Equal("alice", ackActorIn(langFR(), &AckInfo{ActorName: "alice"}))
	r.Equal("Quelqu'un", commentAuthorIn(langFR(), nil))
	r.Equal("e-mail", ackChannelIn(langFR(), &AckInfo{Via: "email"}))
	r.Equal("téléphone", ackChannelIn(langFR(), &AckInfo{Via: "phone"}))
	r.Empty(ackChannelIn(langFR(), &AckInfo{Via: "web"}))
	r.Equal("Telegram", commentChannelIn(langFR(), &CommentInfo{Source: "telegram"}))

	now := time.Date(2026, time.July, 5, 11, 15, 0, 0, time.UTC)
	reminder := &Payload{Incident: &models.Incident{StartedAt: now.Add(-75 * time.Minute)}, ReminderSeq: 3}
	r.Equal("Toujours en panne depuis 1"+nbsp+"h 15"+nbsp+"min, non acquitté (relance 3)",
		reminderSentenceIn(langFR(), reminder, now))

	reminder.ReminderSeq = 0
	r.Equal("Toujours en panne depuis 1"+nbsp+"h 15"+nbsp+"min, non acquitté",
		reminderSentenceIn(langFR(), reminder, now))

	title, text := regionLabelForIn(langFR(), localeRegionPayload(string(models.EventTypeIncidentCreated)))
	r.Equal("Régions en échec", title)
	r.Equal("🇪🇺 Europe West, 🇺🇸 US East (2 régions sur 3)", text)

	single := localeRegionPayload(string(models.EventTypeIncidentResolved))
	single.Regions.Failing = []string{"eu-west"}
	title, text = regionLabelForIn(langFR(), single)
	r.Equal("Régions touchées", title)
	r.Equal("🇪🇺 Europe West (1 région sur 3)", text)

	info := localeDegradedInfo()
	r.Equal("7 échecs sur les 60 dernières sondes (93,8"+nbsp+"% de disponibilité) "+
		"et 3 des 6 dernières sondes ont dépassé 1000"+nbsp+"ms", info.reasonIn(langFR()))
	r.Equal("Actuellement en ligne.", info.statusTextIn(langFR()))
	r.Equal("16:02 – 16:47 CEST", info.windowTextIn(langFR()), "the reader's zone")
	r.Equal("7 échecs sur les 60 dernières sondes (93,8"+nbsp+"% de disponibilité) (règle : 5 sur 60 sondes)",
		info.failureLineIn(langFR()))
	r.Equal("3 des 6 dernières sondes ont dépassé 1000"+nbsp+"ms (règle : 3 sur 6 sondes au-delà de 1000"+nbsp+"ms)",
		info.slowLineIn(langFR()))
	r.Equal("1 échec sur les 60 dernières sondes",
		(&DegradedInfo{FailuresFired: true, FailureCount: 1, FailureSlots: 60}).reasonIn(langFR()))

	burn := BurnInfoFor(burnIncident(t))
	r.Equal("Combustion rapide", burn.policyLabelIn(langFR()))
	r.Equal("critique", burn.severityLabelIn(langFR()))
	r.Equal("31,0x", multiplierIn(langFR(), burn.LongRate))
	r.Equal("1"+nbsp+"h 30"+nbsp+"min", burn.budgetRemainingTextIn(langFR()))
	r.Equal("23 août, 06:30 CEST", burn.projectedExhaustionTextIn(langFR()))

	burn.BudgetRemaining = -time.Minute
	r.Equal("épuisé", burn.budgetRemainingTextIn(langFR()))

	burn.ProjectedExhaustion = time.Time{}
	r.Equal("pas prévu dans cette fenêtre", burn.projectedExhaustionTextIn(langFR()))
	r.Equal("Vérification inconnue", emailCheckName(langFR(), &models.Check{}))
}

func TestProducersInGermanAndSpanish(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("Jemand", ackActorIn(langDE(), nil))
	r.Equal("E-Mail", ackChannelIn(langDE(), &AckInfo{Via: "email"}))
	r.Equal("Alguien", commentAuthorIn(langES(), nil))
	r.Equal("correo", ackChannelIn(langES(), &AckInfo{Via: "email"}))

	now := time.Date(2026, time.July, 5, 11, 15, 0, 0, time.UTC)
	reminder := &Payload{Incident: &models.Incident{StartedAt: now.Add(-51 * time.Hour)}, ReminderSeq: 2}
	r.Equal("Seit 2"+nbsp+"Tg. 3"+nbsp+"Std. ausgefallen, nicht bestätigt (Erinnerung 2)",
		reminderSentenceIn(langDE(), reminder, now))
	r.Equal("Sigue caído tras 2"+nbsp+"d 3"+nbsp+"h, sin reconocer (recordatorio 2)",
		reminderSentenceIn(langES(), reminder, now))

	slowOne := &DegradedInfo{SlowFired: true, SlowCount: 1, SlowSlots: 6, SlowMs: 1000}
	r.Equal("1 der letzten 6 Probes war langsamer als 1000"+nbsp+"ms", slowOne.reasonIn(langDE()))
	r.Equal("1 de las últimas 6 sondas tardó más de 1000"+nbsp+"ms", slowOne.reasonIn(langES()))
	r.Equal("ein intermittierendes Muster wurde erkannt", (&DegradedInfo{}).reasonIn(langDE()))
	r.Equal("Ahora mismo no funciona.", (&DegradedInfo{}).statusTextIn(langES()))

	_, text := regionLabelForIn(langES(), localeRegionPayload(eventTypeIncidentReminder))
	r.Equal("🇪🇺 Europe West, 🇺🇸 US East (2 de 3 regiones)", text) //nolint:misspell // Spanish copy

	burn := BurnInfoFor(burnIncident(t))
	r.Equal("Schneller Verbrauch", burn.policyLabelIn(langDE()))
	r.Equal("Consumo rápido", burn.policyLabelIn(langES()))
	r.Equal("1"+nbsp+"Std. 30"+nbsp+"Min.", humanDurationIn(langDE(), burn.BudgetRemaining))
	r.Equal("14,4x", multiplierIn(langES(), burn.Threshold))
}

// TestSlackAndDiscordStayEnglish renders the chat messages that read the
// localized producers and pins the English they carry.
func TestSlackAndDiscordStayEnglish(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checkName := "acme.com"
	check := &models.Check{UID: "check-1", Name: &checkName, Type: "http"}

	degraded := models.NewIncident("org", "check-1", time.Now(), "acme is degraded")
	degraded.Kind = models.IncidentKindDegraded
	degraded.Details = models.JSONMap{
		"degraded_failures_fired": true, "degraded_failure_count": 7.0, "degraded_failure_slots": 60.0,
		"degraded_availability_pct": 93.8, "degraded_currently_up": true,
		"degraded_window_from": "2026-07-05T14:02:00Z", "degraded_window_to": "2026-07-05T14:47:00Z",
	}

	slackDegraded := fmt.Sprintf("%+v", (&SlackSender{}).buildMessage(&Payload{
		EventType: eventTypeIncidentCreated, Check: check, Incident: degraded,
	}))
	r.Contains(slackDegraded, "acme.com is degraded: 7 failures in the last 60 probes (93.8% available)")
	r.Contains(slackDegraded, "*Right now:*\nCurrently up.")
	r.Contains(slackDegraded, "14:02 – 14:47 UTC")

	slackBurn := fmt.Sprintf("%+v", (&SlackSender{}).buildMessage(&Payload{
		EventType: eventTypeIncidentCreated, Check: check, Incident: burnIncident(t),
	}))
	r.Contains(slackBurn, "Fast burn: acme api burning at 31.0x")
	r.Contains(slackBurn, "31.0x over 1h (44.5x over 5m), threshold 14.4x")
	r.Contains(slackBurn, "*Budget remaining:*\n1h30m")

	regions := localeRegionPayload(eventTypeIncidentCreated)
	regions.Check = check
	regions.Incident = models.NewIncident("org", "check-1", time.Now(), "down")
	r.Contains(fmt.Sprintf("%+v", (&SlackSender{}).buildMessage(regions)),
		"*Failing from:*\n🇪🇺 Europe West, 🇺🇸 US East (2 of 3 regions)")

	ack := (&DiscordSender{}).buildEmbed(&Payload{
		EventType: eventTypeIncidentAcknowledged, Check: check,
		Incident:       models.NewIncident("org", "check-1", time.Now(), "x"),
		Acknowledgment: &AckInfo{Via: "phone"},
	})
	r.Contains(ack.Description, "Someone acknowledged this incident via phone.")
	r.Contains(fmt.Sprintf("%+v", ack.Fields), "{Name:Via Value:phone")

	reminder := (&DiscordSender{}).buildEmbed(&Payload{
		EventType: eventTypeIncidentReminder, Check: check, ReminderSeq: 3,
		Incident: models.NewIncident("org", "check-1", time.Now().Add(-75*time.Minute+10*time.Second), "x"),
	})
	r.Equal("Still down after 1 h 15 min, not acknowledged (reminder 3).", reminder.Description)
}

// TestEmailIsWrittenInEachRecipientsLanguage resolves every address of an
// integration on its own: an account's preferences, the organization owner's
// for an address with no account, and English/UTC for a member who set none
// (never the owner's).
func TestEmailIsWrittenInEachRecipientsLanguage(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	f := newSuppressionTestFixture(t)
	ctx := t.Context()

	addUser := func(email string, role models.MemberRole, prefs *models.UserPreferences) {
		user := models.NewUser(email)
		user.Preferences = prefs
		r.NoError(f.dbSvc.CreateUser(ctx, user))
		r.NoError(f.dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(f.org.UID, user.UID, role)))
	}

	addUser("owner@acme.com", models.MemberRoleOwner,
		&models.UserPreferences{Locale: i18n.LangGerman, Timezone: "America/New_York"})
	addUser("alice@acme.com", models.MemberRoleUser,
		&models.UserPreferences{Locale: "fr-FR", Timezone: "Europe/Paris"})
	addUser("bob@acme.com", models.MemberRoleUser, nil)

	checkName := "Production API"
	sender := &fakeEmailSender{}
	startedAt := time.Date(time.Now().Year(), time.July, 5, 10, 0, 0, 0, time.UTC)
	payload := &Payload{
		EventType: eventTypeIncidentCreated,
		Check:     &models.Check{UID: f.check.UID, Name: &checkName, Type: "http"},
		Incident: &models.Incident{
			UID: "inc-locale", Number: 42, StartedAt: startedAt,
			Details: models.JSONMap{"failure_reason": "timeout"},
		},
		Integration: &models.Integration{
			OrganizationUID: f.org.UID,
			Settings:        models.JSONMap{"to": []any{"ALICE@acme.com", "ops@acme.com", "bob@acme.com"}},
		},
		OrgSlug:    f.org.Slug,
		AppBaseURL: "https://solidping.example",
	}

	jctx := &jobdef.JobContext{
		Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
		AppConfig: &config.Config{
			Auth:   config.AuthConfig{JWTSecret: "test-secret"},
			Server: config.ServerConfig{BaseURL: "https://solidping.example"},
		},
		DBService: f.dbSvc,
		Logger:    slog.Default(),
	}

	r.NoError((&EmailSender{}).Send(context.Background(), jctx, payload))
	r.Len(sender.sent, 3)

	alice, ops, bob := sender.sent[0], sender.sent[1], sender.sent[2]

	r.Equal("🔴 Production API est en panne : timeout (#42)", alice.Subject)
	r.Equal([2]string{i18n.LangFrench, "Europe/Paris"}, [2]string{alice.Locale, alice.Timezone})
	r.Contains(alice.HTML, `<html lang="fr">`)
	r.Contains(alice.HTML, "5 juil., 12:00 CEST")
	r.Contains(alice.Text, "Début : 5 juil., 12:00 CEST")
	r.Contains(alice.Text, "Je m'en occupe : https://solidping.example/api/v1/orgs/")

	r.Equal("🔴 Production API ist ausgefallen: timeout (#42)", ops.Subject, "no account: the owner's language")
	r.Equal([2]string{i18n.LangGerman, "America/New_York"}, [2]string{ops.Locale, ops.Timezone})
	r.Contains(ops.HTML, "5. Juli, 06:00 EDT")

	r.Equal("🔴 Production API is down: timeout (#42)", bob.Subject, "a member without preferences reads English")
	r.Equal([2]string{i18n.LangEnglish, "UTC"}, [2]string{bob.Locale, bob.Timezone})
	r.Contains(bob.HTML, "Jul 5, 10:00 UTC")
}
