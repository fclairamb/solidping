package jobtypes

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
)

// TestEscalationEmailViewModelLetsTheRecipientAck pins what the escalation mail
// gives the person it wakes up: an "I'm on it" magic link signed for them, the
// recorded cause, and the probed target.
func TestEscalationEmailViewModelLetsTheRecipientAck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")

	checkName := "API health"
	check := &models.Check{
		UID: "check-with-url", OrganizationUID: env.org.UID, Name: &checkName, Type: "http",
		Config: models.JSONMap{"url": "https://api.acme.com/health"},
	}
	r.NoError(env.db.CreateCheck(ctx, check))

	incident := *env.incident
	incident.CheckUID = check.UID
	// Recent, so the 7-day token is still valid; 30s past the minute, which
	// the mail timestamp drops.
	incident.StartedAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Minute).Add(30 * time.Second)
	incident.Details = models.JSONMap{"failure_reason": "HTTP 503 Service Unavailable (expected 200)"}

	viewModel := newRun().buildEscalationEmailViewModel(ctx, env.jctx, &incident, "oncall@acme.com")

	r.Equal("HTTP 503 Service Unavailable (expected 200)", viewModel["FailureReason"])
	r.Equal("https://api.acme.com/health", viewModel["CheckTarget"])
	r.Equal(incident.StartedAt.Format("Jan 2, 15:04 UTC"), viewModel["StartedAt"])

	ackURL, ok := viewModel["AckURL"].(string)
	r.True(ok)
	r.True(strings.HasPrefix(ackURL,
		"https://app.example.com/api/v1/orgs/"+env.org.Slug+"/incidents/"+incident.UID+"/ack?token="), ackURL)

	parsed, err := url.Parse(ackURL)
	r.NoError(err)

	payload, err := incidentlinks.Verify([]byte("test-secret"), incident.UID, parsed.Query().Get("token"))
	r.NoError(err, "the token is signed for this incident with the app secret")
	r.Equal("oncall@acme.com", payload.RecipientEmail, "the link acks as the person it was sent to")
	r.Equal(incident.StartedAt.Add(incidentlinks.AckTokenTTL).Unix(), payload.Expiry.Unix())

	// Without app config there is no secret to sign with: no button, no crash.
	noConfig := *env.jctx
	noConfig.AppConfig = nil
	r.Empty(newRun().buildEscalationEmailViewModel(ctx, &noConfig, &incident, "oncall@acme.com")["AckURL"])
}

// TestDispatch_EscalationEmailThreadsWithTheIncident: the escalation mail joins
// the conversation of the other mails about the same incident, and offers the
// "I'm on it" button.
func TestDispatch_EscalationEmailThreadsWithTheIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")

	sent := newRun().dispatchRoute(context.Background(), env.jctx, slog.Default(), env.incident, emailRoute(),
		map[string]bool{"email": true})
	r.Equal(1, sent)

	msg := env.emails.lastMessage()
	r.NotNil(msg)
	r.Equal("incident."+env.incident.UID, msg.ThreadID)
	r.Contains(msg.HTML, "I&#39;m on it")
	r.Contains(msg.HTML, "/incidents/"+env.incident.UID+"/ack?token=")
	r.Contains(msg.Text, "I'm on it (acknowledge, stops escalation): ")
}
