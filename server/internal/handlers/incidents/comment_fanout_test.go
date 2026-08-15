package incidents_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// commentJobTargets returns the connection UIDs of every pending
// `incident.comment` notification job in the org, plus the comment body each
// job carries. Reading the job rows directly (rather than jobsvc.ListJobs) for
// the same reason pendingNotificationJobs does.
func commentJobTargets(
	t *testing.T, dbSvc *sqlite.Service, orgUID string,
) (map[string]string, []jobtypes.NotificationJobConfig) {
	t.Helper()

	var jobs []*models.Job

	err := dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeNotification)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	targets := make(map[string]string, len(jobs))
	configs := make([]jobtypes.NotificationJobConfig, 0, len(jobs))

	for _, job := range jobs {
		raw, marshalErr := json.Marshal(job.Config)
		require.NoError(t, marshalErr)

		var cfg jobtypes.NotificationJobConfig
		require.NoError(t, json.Unmarshal(raw, &cfg))

		if cfg.EventType != string(models.EventTypeIncidentComment) {
			continue
		}

		text := ""
		if cfg.Comment != nil {
			text = cfg.Comment.Text
		}

		targets[cfg.ConnectionUID] = text

		configs = append(configs, cfg)
	}

	return targets, configs
}

// attachChannel creates an enabled channel of the given type, wires it to the
// setup's check, and returns it.
func attachChannel(
	t *testing.T, s *resolveSetup, connType models.ConnectionType, name string, settings models.JSONMap,
) *models.Integration {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	conn := models.NewIntegration(s.org.UID, connType, name)
	conn.Enabled = true

	if settings != nil {
		conn.Settings = settings
	}

	r.NoError(s.dbSvc.CreateChannel(ctx, conn))
	r.NoError(s.dbSvc.CreateCheckConnection(ctx, models.NewCheckConnection(s.check.UID, conn.UID, s.org.UID)))

	return conn
}

// TestAddComment_FansOutToCheckChannels is the base case: a dashboard comment
// produces one notification job per attached channel, each carrying the
// comment body, plus the matching incident_notifications audit row.
func TestAddComment_FansOutToCheckChannels(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	// The fixture already attaches one webhook channel (s.connUID).
	email := attachChannel(t, s, models.ConnectionTypeEmail, "oncall-mail", nil)

	user := models.NewUser("responder@example.com")
	user.Name = "Ada Lovelace"
	r.NoError(s.dbSvc.CreateUser(ctx, user))

	_, err := s.svc.AddComment(ctx, s.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: s.incident.UID,
		Text:        "central DNS looks down",
		Source:      incidents.CommentSourceWeb,
		ActorUID:    user.UID,
	})
	r.NoError(err)

	targets, configs := commentJobTargets(t, s.dbSvc, s.org.UID)
	r.Len(targets, 2)
	r.Contains(targets, s.connUID)
	r.Contains(targets, email.UID)
	r.Equal("central DNS looks down", targets[s.connUID])

	// The author is resolved to a human label for the channel message.
	for _, cfg := range configs {
		r.NotNil(cfg.Comment)
		r.Equal("Ada Lovelace", cfg.Comment.AuthorName)
		r.Equal(incidents.CommentSourceWeb, cfg.Comment.Source)
	}

	// Audit rows exist for both, exactly as the lifecycle events produce.
	notifs, err := s.dbSvc.ListIncidentNotifications(ctx, s.org.UID, db.ListIncidentNotificationsFilter{
		IncidentUID: s.incident.UID,
	})
	r.NoError(err)

	audited := 0

	for _, n := range notifs {
		if n.EventType == string(models.EventTypeIncidentComment) {
			audited++
		}
	}

	r.Equal(2, audited)
}

// TestAddComment_EchoSuppressionSkipsOriginWorkspace proves the negative the
// spec calls out: a comment typed in Slack workspace A is NOT posted back to
// any connection of workspace A (the bot would repeat the author's own words
// into the thread they just typed them in), while workspace B and every other
// channel still receive it.
func TestAddComment_EchoSuppressionSkipsOriginWorkspace(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	slackA := attachChannel(t, s, models.ConnectionTypeSlack, "workspace-a", models.JSONMap{
		"team_id": "T_ORIGIN", "access_token": "xoxb-a", "channel_id": "C1",
	})
	slackB := attachChannel(t, s, models.ConnectionTypeSlack, "workspace-b", models.JSONMap{
		"team_id": "T_OTHER", "access_token": "xoxb-b", "channel_id": "C2",
	})

	_, err := s.svc.AddCommentFromSlack(
		ctx, s.org.UID, s.incident.UID, "I think the central DNS is down",
		"U123", "alice", "T_ORIGIN", "1700000000.000100",
	)
	r.NoError(err)

	targets, _ := commentJobTargets(t, s.dbSvc, s.org.UID)

	// The negative: the origin workspace is skipped.
	r.NotContains(targets, slackA.UID, "the origin Slack workspace must not receive its own comment")

	// …and everyone else still hears it.
	r.Contains(targets, slackB.UID)
	r.Contains(targets, s.connUID)
	r.Equal("I think the central DNS is down", targets[slackB.UID])
}

// TestAddComment_TwilioIsExcluded proves the registry opt-out: SMS/voice never
// receives a comment, while a channel with no opt-out does.
func TestAddComment_TwilioIsExcluded(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	twilio := attachChannel(t, s, models.ConnectionTypeTwilio, "pager-sms", models.JSONMap{
		"account_sid": "AC1", "auth_token": "tok", "from": "+15550001111", "to": "+15550002222",
	})

	_, err := s.svc.AddComment(ctx, s.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: s.incident.UID,
		Text:        "poking at it",
		Source:      incidents.CommentSourceWeb,
	})
	r.NoError(err)

	targets, _ := commentJobTargets(t, s.dbSvc, s.org.UID)

	r.NotContains(targets, twilio.UID, "twilio must not be paged for a comment")
	r.Contains(targets, s.connUID, "non-opted-out channels still receive it")
}

// TestAddComment_DisabledChannelIsSkipped keeps the comment fan-out honoring
// the same enabled flag the lifecycle fan-out does.
func TestAddComment_DisabledChannelIsSkipped(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	off := attachChannel(t, s, models.ConnectionTypeDiscord, "muted", nil)
	off.Enabled = false
	enabled := false
	r.NoError(s.dbSvc.UpdateChannel(ctx, off.UID, &models.IntegrationUpdate{Enabled: &enabled}))

	_, err := s.svc.AddComment(ctx, s.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: s.incident.UID,
		Text:        "still looking",
		Source:      incidents.CommentSourceWeb,
	})
	r.NoError(err)

	targets, _ := commentJobTargets(t, s.dbSvc, s.org.UID)
	r.NotContains(targets, off.UID)
}

// TestAddComment_TelegramSourceAttributesToUser covers the Telegram /comment
// path's attribution: the comment is owned by the linked contact's SolidPing
// user and labeled with the actor name the bot saw.
func TestAddComment_TelegramSourceAttributesToUser(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	user := models.NewUser("tg@example.com")
	r.NoError(s.dbSvc.CreateUser(ctx, user))

	event, err := s.svc.AddCommentFromTelegram(
		ctx, s.org.UID, s.incident.UID, "rebooting the box", user.UID, "Bob", "12345",
	)
	r.NoError(err)
	r.NotNil(event.ActorUID)
	r.Equal(user.UID, *event.ActorUID)
	r.Equal(incidents.CommentSourceTelegram, event.Payload["source"])
	r.Equal("12345", event.Payload["telegramChatId"])

	_, configs := commentJobTargets(t, s.dbSvc, s.org.UID)
	r.NotEmpty(configs)

	for _, cfg := range configs {
		r.NotNil(cfg.Comment)
		r.Equal("Bob", cfg.Comment.AuthorName)
		r.Equal(incidents.CommentSourceTelegram, cfg.Comment.Source)
	}
}
