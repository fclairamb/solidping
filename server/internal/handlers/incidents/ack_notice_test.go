package incidents_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// pendingAckNoticeJobs returns the pending channel ack notices for an org,
// decoded, so a test can assert both HOW MANY went out and WHAT they say.
//
// Reads the jobs table directly for the same reason pendingNotificationJobs
// does: jobsvc.ListJobs uses a bun pattern that errors on the in-memory sqlite.
func pendingAckNoticeJobs(
	t *testing.T, s *resolveSetup, orgUID string,
) []jobtypes.NotificationJobConfig {
	t.Helper()

	var jobs []*models.Job

	err := s.dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeNotification)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	out := make([]jobtypes.NotificationJobConfig, 0, len(jobs))

	for _, job := range jobs {
		raw, marshalErr := json.Marshal(job.Config)
		require.NoError(t, marshalErr)

		var cfg jobtypes.NotificationJobConfig

		require.NoError(t, json.Unmarshal(raw, &cfg))

		if cfg.EventType == string(models.EventTypeIncidentAcknowledged) {
			out = append(out, cfg)
		}
	}

	return out
}

// pendingTelegramAckNoticeJobs counts the person-contact ack notices queued for
// an org — the Telegram counterpart of the resolution notice.
func pendingTelegramAckNoticeJobs(t *testing.T, s *resolveSetup, orgUID string) int {
	t.Helper()

	var jobs []*models.Job

	err := s.dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeIncidentAckNotice)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	return len(jobs)
}

// TestAckQueuesOneNoticePerDestination is the headline of spec 2026-08-24-01
// part B: acknowledging used to send NOTHING outbound, so everyone who was
// paged kept believing the incident was unclaimed.
func TestAckQueuesOneNoticePerDestination(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	second := attachChannel(t, s, models.ConnectionTypeSlack, "slack-ops", models.JSONMap{
		"team_id": "T0ACME", "access_token": "xoxb-test", "channel_id": "C1",
	})

	r.Empty(pendingAckNoticeJobs(t, s, s.org.UID))

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	notices := pendingAckNoticeJobs(t, s, s.org.UID)
	r.Len(notices, 2, "every channel the alert reached must hear the acknowledgment, exactly once")

	got := map[string]bool{}
	for _, cfg := range notices {
		r.False(got[cfg.ConnectionUID], "a destination must not be notified twice")
		got[cfg.ConnectionUID] = true
		r.Equal(s.incident.UID, cfg.IncidentUID)
	}

	r.True(got[s.connUID])
	r.True(got[second.UID])

	r.Equal(1, pendingTelegramAckNoticeJobs(t, s, s.org.UID),
		"the people paged over a person contact get their own notice job")
}

// The notice must name the acker — a channel message saying only "this was
// acknowledged" leaves the reader wondering whether to pick it up anyway.
func TestAckNoticeCarriesTheActor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	_, err := s.svc.AcknowledgeIncidentFromSlack(
		ctx, s.org.UID, s.incident.UID, "U0ALICE", "alice", "T0OTHER",
	)
	r.NoError(err)

	notices := pendingAckNoticeJobs(t, s, s.org.UID)
	r.Len(notices, 1)
	r.NotNil(notices[0].Acknowledgment)
	r.Equal("alice", notices[0].Acknowledgment.ActorName)
	r.Equal("slack", notices[0].Acknowledgment.Via)
}

// Idempotency: acking an already-acknowledged incident returns early, and must
// not fan out a second round. A retried Slack interaction or a double-clicked
// button would otherwise page every channel twice.
func TestSecondAckQueuesNoFurtherNotices(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	req := &incidents.AcknowledgeIncidentRequest{IncidentUID: s.incident.UID, Via: "web"}

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, req)
	r.NoError(err)
	r.Len(pendingAckNoticeJobs(t, s, s.org.UID), 1)
	r.Equal(1, pendingTelegramAckNoticeJobs(t, s, s.org.UID))

	_, err = s.svc.AcknowledgeIncident(ctx, s.org.Slug, req)
	r.NoError(err)

	r.Len(pendingAckNoticeJobs(t, s, s.org.UID), 1,
		"an idempotent re-ack must not fan out again")
	r.Equal(1, pendingTelegramAckNoticeJobs(t, s, s.org.UID))
}

// TestAckNoticeSurvivesItsOwnCancellationSweep pins the ORDERING that decides
// whether this feature works at all: acking cancels every pending job carrying
// the incident's UID, so a notice enqueued before that sweep deletes itself.
//
// The positive control is the escalation job queued beforehand: it MUST be
// swept, proving the sweep really ran and the notice's survival is ordering
// rather than a broken cancellation.
func TestAckNoticeSurvivesItsOwnCancellationSweep(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	// A pending paging job for this incident, standing in for an escalation
	// step scheduled when the incident opened.
	pendingCfg, err := json.Marshal(jobtypes.NotificationJobConfig{
		ConnectionUID: s.connUID,
		IncidentUID:   s.incident.UID,
		EventType:     string(models.EventTypeIncidentEscalated),
	})
	r.NoError(err)

	_, err = s.jobs.CreateJob(ctx, s.org.UID, string(jobdef.JobTypeNotification), pendingCfg, nil)
	r.NoError(err)
	r.Equal(1, pendingNotificationJobs(t, s.dbSvc, s.org.UID))

	_, err = s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	r.Len(pendingAckNoticeJobs(t, s, s.org.UID), 1,
		"the ack notice is queued AFTER the sweep, so it must still be pending")
	r.Equal(1, pendingNotificationJobs(t, s.dbSvc, s.org.UID),
		"the pre-existing escalation job must have been swept — only the notice remains")
	r.Equal(1, pendingTelegramAckNoticeJobs(t, s, s.org.UID))
}

// Unacknowledging must not leave a notice in flight: a channel hearing "Alice
// took it" seconds after the ack was withdrawn stops anyone else from picking
// the incident up, which is worse than never hearing it.
func TestUnackCancelsAPendingAckNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)
	r.Len(pendingAckNoticeJobs(t, s, s.org.UID), 1)
	r.Equal(1, pendingTelegramAckNoticeJobs(t, s, s.org.UID))

	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, "", "web")
	r.NoError(err)

	r.Empty(pendingAckNoticeJobs(t, s, s.org.UID),
		"a withdrawn acknowledgment must not still announce itself")
	r.Equal(0, pendingTelegramAckNoticeJobs(t, s, s.org.UID))
}

// B.5, implemented deliberately: the Slack workspace whose Acknowledge button
// was pressed already shows "acknowledged by @them" on the very message the
// acker is looking at, so a second message there is noise. Every OTHER
// destination must still be told.
func TestAckSkipsTheOriginatingSlackWorkspace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	origin := attachChannel(t, s, models.ConnectionTypeSlack, "slack-origin", models.JSONMap{
		"team_id": "T0ACME", "access_token": "xoxb-a", "channel_id": "C1",
	})
	other := attachChannel(t, s, models.ConnectionTypeSlack, "slack-other", models.JSONMap{
		"team_id": "T0OTHER", "access_token": "xoxb-b", "channel_id": "C2",
	})

	_, err := s.svc.AcknowledgeIncidentFromSlack(
		ctx, s.org.UID, s.incident.UID, "U0ALICE", "alice", "T0ACME",
	)
	r.NoError(err)

	got := map[string]bool{}
	for _, cfg := range pendingAckNoticeJobs(t, s, s.org.UID) {
		got[cfg.ConnectionUID] = true
	}

	r.False(got[origin.UID], "the workspace whose button was pressed must not be told twice")
	r.True(got[other.UID], "a different workspace has seen nothing and must be told")
	r.True(got[s.connUID], "the webhook destination is unrelated to Slack and must be told")
}

// The Discord counterpart of the workspace skip, matched by guild.
func TestAckSkipsTheOriginatingDiscordGuild(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	origin := attachChannel(t, s, models.ConnectionTypeDiscord, "discord-origin", models.JSONMap{
		"guild_id": "G0ACME", "channel_id": "C1",
	})
	other := attachChannel(t, s, models.ConnectionTypeDiscord, "discord-other", models.JSONMap{
		"guild_id": "G0OTHER", "channel_id": "C2",
	})

	_, err := s.svc.AcknowledgeIncidentFromDiscord(
		ctx, s.org.UID, s.incident.UID, "D0BOB", "bob", "G0ACME",
	)
	r.NoError(err)

	got := map[string]bool{}
	for _, cfg := range pendingAckNoticeJobs(t, s, s.org.UID) {
		got[cfg.ConnectionUID] = true
	}

	r.False(got[origin.UID])
	r.True(got[other.UID])
	r.True(got[s.connUID])
}

// A dashboard ack carries no echo origin, so even a Slack workspace that
// happens to be attached hears about it. This is the other half of the
// origin-skip decision: the skip must be driven by where the ack HAPPENED, not
// by the channel type.
func TestWebAckStillNotifiesEverySlackWorkspace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	slack := attachChannel(t, s, models.ConnectionTypeSlack, "slack-ops", models.JSONMap{
		"team_id": "T0ACME", "access_token": "xoxb-a", "channel_id": "C1",
	})

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	got := map[string]bool{}
	for _, cfg := range pendingAckNoticeJobs(t, s, s.org.UID) {
		got[cfg.ConnectionUID] = true
	}

	r.True(got[slack.UID])
	r.True(got[s.connUID])
}

// SMS and voice opt out of acknowledgments in the sender registry: a paid text
// per "someone took it" is worse than silence, and it would usually reach the
// very person who acked.
func TestAckNoticeSkipsPagingCostChannels(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	twilio := attachChannel(t, s, models.ConnectionTypeTwilio, "sms", models.JSONMap{
		"account_sid": "AC0", "auth_token": "t", "from_number": "+33100000000",
	})

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	for _, cfg := range pendingAckNoticeJobs(t, s, s.org.UID) {
		r.NotEqual(twilio.UID, cfg.ConnectionUID, "SMS must not be billed for an acknowledgment")
	}

	r.Len(pendingAckNoticeJobs(t, s, s.org.UID), 1, "the webhook destination is still told")
}

// A rolled-up child was never paged, so announcing its acknowledgment would be
// the first thing its channels ever heard about it.
func TestSuppressedIncidentQueuesNoAckNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	suppressed := true
	r.NoError(s.dbSvc.UpdateIncident(ctx, s.incident.UID, &models.IncidentUpdate{
		PagingSuppressed: &suppressed,
	}))

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	r.Empty(pendingAckNoticeJobs(t, s, s.org.UID))
	r.Equal(0, pendingTelegramAckNoticeJobs(t, s, s.org.UID))
}
