package jobtypes

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// fakeSlackAPI records the Authorization header of every postMessage, which
// is the assertion that matters: before spec 2026-09-18-02 this path read the
// public settings map, found no token, logged "skipping route" and returned 0
// — a silently dropped page.
type fakeSlackAPI struct {
	server *httptest.Server
	tokens []string
}

func newFakeSlackAPI(t *testing.T) *fakeSlackAPI {
	t.Helper()

	fake := &fakeSlackAPI{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fake.tokens = append(fake.tokens, req.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"D1","ts":"1.0"}`))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

// TestSendEscalationSlackDM_SplitRowPostsTheDM proves the escalation step
// delivers for a row whose token is only in the encrypted envelope.
//
// Not parallel: it repoints the package-level Slack API endpoint.
//
//nolint:paralleltest // repoints the package-level Slack API endpoint
func TestSendEscalationSlackDM_SplitRowPostsTheDM(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	fake := newFakeSlackAPI(t)

	original := slackDMBaseURL
	slackDMBaseURL = fake.server.URL

	t.Cleanup(func() { slackDMBaseURL = original })

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("escalation-slack-org", "Escalation Slack Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

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

	jctx := &jobdef.JobContext{
		DBService: dbSvc,
		Services:  &services.Registry{},
		Logger:    slog.Default(),
	}

	route := &models.UserNotificationRoute{
		UID:     "route-uid",
		Enabled: true,
		Contact: &models.UserContact{
			UID:   "contact-uid",
			Type:  models.UserContactTypeSlackUser,
			Value: "U123",
		},
	}

	run := &EscalationStepJobRun{}

	sent := run.sendEscalationSlackDM(ctx, jctx, slog.Default(), incident, route, nil)
	r.Equal(1, sent, "a split row must still page the on-call human")
	r.Equal([]string{"Bearer xoxb-escalation"}, fake.tokens)
}

// TestSendEscalationSlackDM_NoTokenSkipsRoute is the negative control: a
// genuinely token-less stub still skips rather than erroring the step.
//
//nolint:paralleltest // repoints the package-level Slack API endpoint
func TestSendEscalationSlackDM_NoTokenSkipsRoute(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	fake := newFakeSlackAPI(t)

	original := slackDMBaseURL
	slackDMBaseURL = fake.server.URL

	t.Cleanup(func() { slackDMBaseURL = original })

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("esc-slack-stub-org", "Escalation Slack Stub Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "workspace")
	conn.Enabled = true
	conn.Settings = models.JSONMap{"team_id": "T1"}
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	check := models.NewCheck(org.UID, "escalation-check", "http")
	check.Config = models.JSONMap{"url": "https://example.com"}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now(), "escalation incident")
	r.NoError(dbSvc.CreateIncident(ctx, incident))

	jctx := &jobdef.JobContext{DBService: dbSvc, Services: &services.Registry{}, Logger: slog.Default()}

	route := &models.UserNotificationRoute{
		UID:     "route-uid",
		Enabled: true,
		Contact: &models.UserContact{UID: "c", Type: models.UserContactTypeSlackUser, Value: "U123"},
	}

	run := &EscalationStepJobRun{}
	r.Equal(0, run.sendEscalationSlackDM(ctx, jctx, slog.Default(), incident, route, nil))
	r.Empty(fake.tokens)
}
