package incidents

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// queueAckNotifications tells everyone who was paged about an incident that a
// teammate has taken it.
//
// Without this, an acknowledgment is visible ONLY on the surface it happened
// on: the escalation machinery stops, the dashboard updates, and every other
// channel that shouted about the outage stays silent — so the four people who
// were woken up have no way to learn that the fifth already picked it up. That
// is the exact situation acknowledgments exist to prevent.
//
// The destination set is the one the incident's own alerts reached
// (commentFanoutConnections), for the same reason a comment uses it: an
// acknowledgment is a fact ABOUT an alert, so it belongs wherever that alert
// landed and nowhere else.
//
// Two filters apply on top, both reused rather than reinvented:
//
//   - notifications.AcceptsEventType — the registry's per-channel opt-out. SMS
//     and voice do not receive acknowledgments: a paid text saying "someone
//     took it" is worse than silence.
//   - isAckEchoOrigin — the Slack workspace / Discord guild whose button was
//     pressed is skipped, because that message is rewritten in place with the
//     acknowledgment already. See the type doc on AcknowledgeIncidentRequest's
//     EchoOrigin fields.
//
// ALWAYS-ON by decision (spec 2026-08-24-01): there is no per-destination
// opt-out setting, no new column and no new API field. The only opt-out is the
// per-channel-type one above, which predates this.
//
// CALL ORDER IS LOAD-BEARING. This must run AFTER cancelPendingNotifications:
// that sweep soft-deletes every pending job carrying the incident's UID, so an
// ack notice enqueued before it would cancel itself.
func (s *Service) queueAckNotifications(
	ctx context.Context, orgUID string, incident *models.Incident, req *AcknowledgeIncidentRequest,
) {
	// A rolled-up child was never paged, and a RESOLVED incident's ack is a
	// stale button press on a message the resolution notice has already
	// answered — announcing "Alice took it" for something that is over would
	// make the channel read backwards. Neither is a destination problem, so
	// both are decided here rather than per channel.
	if incident.PagingSuppressed || incident.State == models.IncidentStateResolved {
		slog.DebugContext(ctx, "Skipping ack notice fan-out",
			"incidentUid", incident.UID,
			"pagingSuppressed", incident.PagingSuppressed,
			"state", incident.State)

		return
	}

	actorName := s.ackActorName(ctx, req)

	// People paged over a person contact (Telegram today) are reached by their
	// own job, exactly as the resolution notice does: the escalation step is
	// the only thing that ever messages them, and it has just been canceled.
	s.queueTelegramAckNotice(ctx, orgUID, incident.UID, actorName, req.Via)

	ack := &notifications.AckInfo{
		ActorName: actorName,
		Via:       req.Via,
	}

	for _, conn := range s.commentFanoutConnections(ctx, incident) {
		if !conn.Enabled {
			continue
		}

		if !notifications.AcceptsEventType(conn.Type, string(models.EventTypeIncidentAcknowledged)) {
			continue
		}

		if isAckEchoOrigin(conn, req) {
			slog.DebugContext(ctx, "Skipping ack notice to the connection the ack came from",
				"connectionUid", conn.UID, "incidentUid", incident.UID)

			continue
		}

		s.enqueueNotificationJob(ctx, orgUID, conn, incident.UID,
			models.EventTypeIncidentAcknowledged, &notificationExtras{Acknowledgment: ack})
	}
}

// ackActorName resolves the human label for the person who acknowledged,
// straight from the request that carried the ack.
//
// Deliberately NOT read back off the event row: the fan-out already holds
// every field the event was built from, and a re-read would be one query per
// acknowledgment for information we just wrote. Precedence matches
// ackNameFromPayload, so the channel message and the API response name the
// same person.
func (s *Service) ackActorName(ctx context.Context, req *AcknowledgeIncidentRequest) string {
	for _, candidate := range []string{
		req.SlackUsername,
		req.DiscordUsername,
		// The bare first name, not the whole `via Telegram (Carol)` label —
		// otherwise the notice reads "via Telegram (Carol) acknowledged this
		// incident via Telegram".
		telegramActorName(req.TelegramActor),
	} {
		if candidate != "" {
			return candidate
		}
	}

	// The platform user comes AFTER the chat identities on purpose: a Telegram
	// ack is credited to the linked account, but the person who pressed the
	// button in a group chat may be someone else entirely, and TelegramActor is
	// the only record of who that was.
	if req.AcknowledgedBy != "" {
		if name := s.lookupUserDisplayName(ctx, req.AcknowledgedBy); name != "" {
			return name
		}
	}

	for _, candidate := range []string{
		req.AcknowledgedByEmail,
		req.PhoneNumber,
		req.SlackUserID,
		req.DiscordUserID,
	} {
		if candidate != "" {
			return candidate
		}
	}

	return ""
}

// isAckEchoOrigin reports whether this connection is the one the
// acknowledgment was performed on.
//
// Matched at WORKSPACE level for Slack and GUILD level for Discord, not per
// connection row — identical reasoning to isCommentEchoOrigin: the incident's
// thread is stored once per incident, so any connection in that workspace or
// guild would post into the very thread the acker is looking at, under a
// message that already says "Acknowledged by @them".
func isAckEchoOrigin(conn *models.Integration, req *AcknowledgeIncidentRequest) bool {
	switch {
	case req.EchoOriginTeamID != "" && conn.Type == models.ConnectionTypeSlack:
		settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
		if err != nil || settings == nil {
			return false
		}

		return settings.TeamID == req.EchoOriginTeamID
	case req.EchoOriginGuildID != "" && conn.Type == models.ConnectionTypeDiscord:
		settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)
		if err != nil || settings == nil {
			return false
		}

		return settings.GuildID == req.EchoOriginGuildID
	default:
		return false
	}
}

// queueTelegramAckNotice enqueues the job that closes the loop with the person
// contacts (Telegram today) paged for this incident.
//
// Same contract as queueNotifications: a failure to enqueue is logged and
// never fails the acknowledgment. An acknowledgment whose notice job could not
// be created is a missed message; an acknowledgment that failed is an
// escalation that keeps waking people up.
func (s *Service) queueTelegramAckNotice(ctx context.Context, orgUID, incidentUID, actorName, via string) {
	config, err := json.Marshal(jobtypes.IncidentAckNoticeJobConfig{
		OrganizationUID: orgUID,
		IncidentUID:     incidentUID,
		ActorName:       actorName,
		Via:             via,
	})
	if err != nil {
		slog.WarnContext(ctx, "Failed to marshal incident ack notice config",
			"incidentUid", incidentUID, "error", err)

		return
	}

	if _, err := s.jobsSvc.CreateJob(
		ctx, orgUID, string(jobdef.JobTypeIncidentAckNotice), config, nil,
	); err != nil {
		slog.WarnContext(ctx, "Failed to create incident ack notice job",
			"incidentUid", incidentUID, "error", err)
	}
}
