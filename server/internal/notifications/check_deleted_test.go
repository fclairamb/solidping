package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// An incident closed because its check was deleted (spec 2026-10-08-02) gets
// exactly one resolved notification, and it must say the check was deleted,
// never that it recovered. Every test has the ordinary auto-resolved incident
// as its positive control.

func checkDeletedPayload(deleted bool) *Payload {
	name, slug := "acme api", "acme-api"
	startedAt := time.Now().Add(-time.Hour)
	resolvedAt := time.Now()

	resolutionType := models.ResolutionTypeAuto
	if deleted {
		resolutionType = models.ResolutionTypeCheckDeleted
	}

	return &Payload{
		EventType: eventTypeIncidentResolved,
		Check:     &models.Check{UID: "check-1", Name: &name, Slug: &slug, Type: "http"},
		Incident: &models.Incident{
			UID: "inc-9", Number: 9, Kind: models.IncidentKindCheck, StartedAt: startedAt,
			ResolvedAt: &resolvedAt, ResolutionType: &resolutionType,
		},
		Integration: &models.Integration{Settings: models.JSONMap{"to": []any{"a@acme.com"}}},
		OrgSlug:     "acme",
		AppBaseURL:  "https://solidping.example",
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	return string(raw)
}

func TestResolvedByCheckDeletion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(ResolvedByCheckDeletion(checkDeletedPayload(true).Incident))
	r.False(ResolvedByCheckDeletion(checkDeletedPayload(false).Incident))
	r.False(ResolvedByCheckDeletion(nil))
	r.False(ResolvedByCheckDeletion(&models.Incident{}))
}

// TestCheckDeletedWordingPerSender renders the resolved notification of every
// sender that words a resolution, for a deleted check and for a recovered one.
func TestCheckDeletedWordingPerSender(t *testing.T) {
	t.Parallel()

	renderers := map[string]func(t *testing.T, payload *Payload) string{
		"slack thread reply": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&SlackSender{}).buildIncidentResolvedThreadReply(p))
		},
		"slack resolved card": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&SlackSender{}).buildResolvedUpdateMessage(p))
		},
		"discord embed": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&DiscordSender{}).buildIncidentResolvedEmbed(p))
		},
		"discord resolved card": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&DiscordSender{}).buildResolvedUpdateMessage(p))
		},
		"gotify": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&GotifySender{}).buildMessage(&gotifySettings{}, p))
		},
		"ntfy": func(t *testing.T, p *Payload) string {
			t.Helper()
			title, body, _, _ := (&NtfySender{}).buildContent(&ntfySettings{}, p)

			return title + "\n" + body
		},
		"pushover": func(t *testing.T, p *Payload) string {
			t.Helper()
			title, body, _, _ := (&PushoverSender{}).buildContent(&pushoverSettings{}, p)

			return title + "\n" + body
		},
		"matrix": func(t *testing.T, p *Payload) string {
			t.Helper()
			plain, html := (&MatrixSender{}).buildContent(p)

			return plain + "\n" + html
		},
		"mattermost": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&MattermostSender{}).buildMessage(&mattermostSettings{}, p))
		},
		"zulip": func(t *testing.T, p *Payload) string {
			t.Helper()

			return (&ZulipSender{}).buildContent(p)
		},
		"google chat": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&GoogleChatSender{}).buildMessage(p))
		},
		"msteams": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&MSTeamsSender{}).buildMessage(p))
		},
		"web push": func(t *testing.T, p *Payload) string {
			t.Helper()
			title, body := buildWebPushContent(p, getCheckName(p.Check))

			return title + "\n" + body
		},
	}

	for name, render := range renderers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			deleted := render(t, checkDeletedPayload(true))
			r.Contains(deleted, "deleted", "a deleted check's resolution must say so")
			r.NotContains(deleted, "RECOVERED")
			r.NotContains(deleted, "back up")

			recovered := render(t, checkDeletedPayload(false))
			r.NotContains(recovered, "deleted", "an ordinary resolution keeps its wording")
		})
	}
}

func TestMSTeamsBotAndTwilioCheckDeletedWording(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	title, _ := (&MSTeamsBotSender{}).titleAndColor(checkDeletedPayload(true), "acme api")
	r.Contains(title, "DELETED")
	title, _ = (&MSTeamsBotSender{}).titleAndColor(checkDeletedPayload(false), "acme api")
	r.Contains(title, "RECOVERED")

	sms := (&TwilioSender{}).buildBody(nil, checkDeletedPayload(true), nil)
	r.Contains(sms, "was deleted")
	r.NotContains(sms, "RECOVERED")
	r.Contains((&TwilioSender{}).buildBody(nil, checkDeletedPayload(false), nil), "RECOVERED")
}

func TestEmailCheckDeletedWording(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		deleted     bool
		wantSubject string
		wantBody    string
		notInBody   string
	}{
		{"deleted", true, "[CHECK DELETED]", "The check was deleted, so this incident was closed", "recovered"},
		{"recovered", false, "[RECOVERED]", "has been resolved", "was deleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			sender := &fakeEmailSender{}
			jctx := &jobdef.JobContext{
				Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
				AppConfig: &config.Config{
					Auth:   config.AuthConfig{JWTSecret: "test-secret"},
					Server: config.ServerConfig{BaseURL: "https://solidping.example"},
				},
				Logger: slog.Default(),
			}

			r.NoError((&EmailSender{}).Send(context.Background(), jctx, checkDeletedPayload(tc.deleted)))
			r.Len(sender.sent, 1)

			msg := sender.sent[0]
			r.Contains(msg.Subject, tc.wantSubject)
			r.Contains(msg.HTML, tc.wantBody)
			r.Contains(msg.Text, tc.wantBody)
			r.NotContains(msg.Text, tc.notInBody)
			r.NotContains(msg.HTML, "{{")
		})
	}
}

func TestWebhookCarriesResolutionType(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := mustJSON(t, (&WebhookSender{}).buildPayload(checkDeletedPayload(true)))
	r.Contains(body, `"resolutionType":"check_deleted"`)
}
