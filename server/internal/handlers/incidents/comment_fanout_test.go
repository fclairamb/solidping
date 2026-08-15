package incidents_test

import (
	"encoding/json"
	"testing"
	"time"

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

// attachChannelToCheck creates an enabled channel and wires it to an arbitrary
// check (not just the setup's default one), returning it.
func attachChannelToCheck(
	t *testing.T, s *resolveSetup, checkUID string, connType models.ConnectionType, name string,
) *models.Integration {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	conn := models.NewIntegration(s.org.UID, connType, name)
	conn.Enabled = true
	r.NoError(s.dbSvc.CreateChannel(ctx, conn))
	r.NoError(s.dbSvc.CreateCheckConnection(ctx, models.NewCheckConnection(checkUID, conn.UID, s.org.UID)))

	return conn
}

// linkMember adds a check to a group incident's member list.
//
// currently_failing is set with a follow-up UPDATE rather than on the insert:
// the column is `notnull,default:true`, so bun sends the DEFAULT for a false
// zero value and a member asked to be "recovered" would silently come back
// failing — which would make the no-CurrentlyFailing-filter assertion below
// vacuous. The read-back is the guard against that regressing.
func linkMember(t *testing.T, s *resolveSetup, incidentUID, checkUID string, currentlyFailing bool) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()
	now := time.Now()

	r.NoError(s.dbSvc.UpsertIncidentMemberCheck(ctx, &models.IncidentMemberCheck{
		IncidentUID:    incidentUID,
		CheckUID:       checkUID,
		JoinedAt:       now,
		FirstFailureAt: now,
		LastFailureAt:  now,
		FailureCount:   1,
	}))

	r.NoError(s.dbSvc.UpdateIncidentMemberCheck(ctx, incidentUID, checkUID, &models.IncidentMemberUpdate{
		CurrentlyFailing: &currentlyFailing,
	}))

	member, err := s.dbSvc.GetIncidentMemberCheck(ctx, incidentUID, checkUID)
	r.NoError(err)
	r.Equal(currentlyFailing, member.CurrentlyFailing, "member state must be what the test asked for")
}

// TestAddComment_GroupIncidentFansOutToEveryMembersChannels covers the second
// half of commentFanoutConnections, which the per-check tests never reach.
//
// Three things are asserted at once because they are the three ways this
// branch can break:
//
//  1. UNION — a channel attached to member A and a different channel attached
//     to member B both get a job.
//  2. DE-DUPLICATION — a channel attached to BOTH members gets exactly one job,
//     not one per member.
//  3. NO `CurrentlyFailing` FILTER — a member that has already recovered still
//     contributes its channels. A comment is discussion about the group
//     incident as a whole (service.go, queueCommentNotifications), so an
//     "optimization" that copies queueGroupNotifications' mid-incident filter
//     must fail this test rather than silently muting a channel.
func TestAddComment_GroupIncidentFansOutToEveryMembersChannels(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	group := models.NewCheckGroup(s.org.UID, "API Group", "api-group")
	r.NoError(s.dbSvc.CreateCheckGroup(ctx, group))

	failing := models.NewCheck(s.org.UID, "group-failing", "http")
	failing.CheckGroupUID = &group.UID
	r.NoError(s.dbSvc.CreateCheck(ctx, failing))

	recovered := models.NewCheck(s.org.UID, "group-recovered", "http")
	recovered.CheckGroupUID = &group.UID
	r.NoError(s.dbSvc.CreateCheck(ctx, recovered))

	// The group incident itself hangs off the failing member's check.
	groupIncident := models.NewIncident(s.org.UID, failing.UID, time.Now().Add(-time.Minute), "group is down")
	groupIncident.CheckGroupUID = &group.UID
	r.NoError(s.dbSvc.CreateIncident(ctx, groupIncident))

	linkMember(t, s, groupIncident.UID, failing.UID, true)
	linkMember(t, s, groupIncident.UID, recovered.UID, false)

	onlyFailing := attachChannelToCheck(t, s, failing.UID, models.ConnectionTypeDiscord, "failing-only")
	onlyRecovered := attachChannelToCheck(t, s, recovered.UID, models.ConnectionTypeEmail, "recovered-only")

	// One channel attached to BOTH members — the de-dup case.
	shared := models.NewIntegration(s.org.UID, models.ConnectionTypeWebhook, "shared")
	shared.Enabled = true
	r.NoError(s.dbSvc.CreateChannel(ctx, shared))
	r.NoError(s.dbSvc.CreateCheckConnection(ctx, models.NewCheckConnection(failing.UID, shared.UID, s.org.UID)))
	r.NoError(s.dbSvc.CreateCheckConnection(ctx, models.NewCheckConnection(recovered.UID, shared.UID, s.org.UID)))

	_, err := s.svc.AddComment(ctx, s.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: groupIncident.UID,
		Text:        "both members are flapping",
		Source:      incidents.CommentSourceWeb,
	})
	r.NoError(err)

	targets, configs := commentJobTargets(t, s.dbSvc, s.org.UID)

	// (1) union across members.
	r.Contains(targets, onlyFailing.UID)
	// (3) a recovered member still contributes — no CurrentlyFailing filter.
	r.Contains(targets, onlyRecovered.UID, "a recovered group member must still receive comments")
	r.Contains(targets, shared.UID)

	// (2) exactly three deliveries: the shared channel was not double-sent, and
	// the setup's own unrelated check channel is not in a group member's set.
	//
	// Asserted on the AUDIT rows, not just the job rows: jobsvc.CreateJob
	// collapses two identical configs into one job by itself, so a lost
	// de-duplication shows up as a duplicated incident_notifications row (the
	// incident page listing the same channel twice) rather than a second send.
	r.Len(configs, 3, "the shared channel must produce exactly one job")
	r.Len(targets, 3)
	r.NotContains(targets, s.connUID, "a channel on a non-member check must not be pulled in")

	notifs, err := s.dbSvc.ListIncidentNotifications(ctx, s.org.UID, db.ListIncidentNotificationsFilter{
		IncidentUID: groupIncident.UID,
	})
	r.NoError(err)

	auditedConns := make(map[string]int, len(notifs))

	for _, n := range notifs {
		if n.EventType == string(models.EventTypeIncidentComment) && n.ConnectionUID != nil {
			auditedConns[*n.ConnectionUID]++
		}
	}

	r.Len(auditedConns, 3)
	r.Equal(1, auditedConns[shared.UID], "the shared channel must be audited exactly once")

	for _, cfg := range configs {
		r.NotNil(cfg.Comment)
		r.Equal("both members are flapping", cfg.Comment.Text)
	}
}
