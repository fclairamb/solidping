package jobtypes

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// TestSendEscalationSlackDM_DeliveryFailureFlag covers spec 2026-11-12-07: a
// recipient-class Slack error flags the contact and warns once, a repeat logs
// DEBUG, a success clears the flag, and a token error never flags.
//
//nolint:paralleltest // repoints the package-level Slack API endpoint
func TestSendEscalationSlackDM_DeliveryFailureFlag(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	var answer atomic.Pointer[string]

	setAnswer := func(v string) { answer.Store(&v) }
	setAnswer(`{"ok":false,"error":"channel_not_found"}`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(*answer.Load()))
	}))
	t.Cleanup(server.Close)

	original := slackDMBaseURL
	slackDMBaseURL = server.URL

	t.Cleanup(func() { slackDMBaseURL = original })

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("esc-slack-fail-org", "Escalation Slack Fail Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("adam@acme.test")
	r.NoError(dbSvc.CreateUser(ctx, user))

	envelope, err := credentials.SealPlaintext(map[string]any{"access_token": "xoxb-escalation"})
	r.NoError(err)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "workspace")
	conn.Enabled = true
	conn.IsDefault = true
	conn.Settings = models.JSONMap{"team_id": "T1"}
	conn.SettingsPrivate = &envelope
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	check := models.NewCheck(org.UID, "escalation-check", "http")
	check.Config = models.JSONMap{"url": "https://example.com"}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now(), "escalation incident")
	r.NoError(dbSvc.CreateIncident(ctx, incident))

	contact := models.NewUserContact(user.UID, org.UID, models.UserContactTypeSlackUser, "U123", "Slack")
	r.NoError(dbSvc.UpsertUserContact(ctx, contact))

	var logs bytes.Buffer

	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	jctx := &jobdef.JobContext{DBService: dbSvc, Services: &services.Registry{}, Logger: log}
	run := &EscalationStepJobRun{}

	send := func() int {
		// Re-read like dispatch does: the route carries the stored contact.
		stored, getErr := dbSvc.GetUserContact(ctx, contact.UID)
		r.NoError(getErr)

		route := &models.UserNotificationRoute{UID: "route", Enabled: true, UserUID: user.UID, Contact: stored}

		return run.sendEscalationSlackDM(ctx, jctx, log, incident, route, nil)
	}

	countLevel := func(level string) int {
		n := 0

		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "failed to send escalation Slack DM") && strings.Contains(line, "level="+level) {
				n++
			}
		}

		return n
	}

	r.Equal(0, send())

	stored, err := dbSvc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(stored.DeliveryFailedAt)
	r.NotNil(stored.DeliveryFailureCode)
	r.Equal("channel_not_found", *stored.DeliveryFailureCode)
	r.Equal(1, countLevel("WARN"))
	r.Contains(logs.String(), "slackError=channel_not_found")

	// Second send: still tried, but quieter.
	r.Equal(0, send())
	r.Equal(1, countLevel("WARN"))
	r.Equal(1, countLevel("DEBUG"))

	// Success clears the flag.
	setAnswer(`{"ok":true,"channel":"D1","ts":"1.0"}`)
	r.Equal(1, send())

	stored, err = dbSvc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.Nil(stored.DeliveryFailedAt)
	r.Nil(stored.DeliveryFailureCode)

	// A token-class error is a connection problem, not a contact problem.
	setAnswer(`{"ok":false,"error":"invalid_auth"}`)
	r.Equal(0, send())

	stored, err = dbSvc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.Nil(stored.DeliveryFailedAt)
	r.Equal(2, countLevel("WARN"))
}
