package jobtypes

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// discordDetailCap bounds the free-text detail line in an escalation DM. Discord
// allows 4096 characters in an embed description; this keeps one alert
// glanceable on a phone notification.
const discordDetailCap = 200

// pageDiscord DMs an escalation alert to a member's Discord account through the
// INSTANCE bot. It mirrors pageTelegram:
//
//   - an unverified contact is never messaged (the binding IS the opt-in, so an
//     unverified discord contact means it was revoked);
//   - an instance with the Discord bot unconfigured degrades to an info-log skip;
//   - a send failure returns 0 so the escalation step falls through to the next
//     route, precisely as an SMS, WhatsApp or Telegram failure does.
//
// Two things are specific to this channel.
//
// The DM carries the SAME discord.IncidentActionRow the channel alert carries,
// so Acknowledge pressed inside a DM reaches the existing signature-verified
// interactions endpoint completely unchanged — there is no DM-specific
// acknowledge path to keep in step with the channel one.
//
// And Discord refusing the DM (error code 50007: DMs from server members off,
// bot blocked, or no shared server) does NOT clear the contact's VerifiedAt, in
// deliberate contrast to the Telegram path. A Telegram block is an explicit act
// by the user against the bot; 50007 is also what a member who simply has not
// joined the server yet gets, and un-verifying their contact would quietly
// demolish a working binding they are one server invite away from using.
func (r *EscalationStepJobRun) pageDiscord(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, route *models.UserNotificationRoute, filter map[string]bool,
) int {
	if !severityAllowsDiscord(filter) {
		return 0
	}

	contact := route.Contact

	if contact.VerifiedAt == nil {
		log.InfoContext(ctx, "discord contact not verified; skipping route",
			"contactUID", contact.UID, "userUID", route.UserUID)

		return 0
	}

	// BotConfigured(), not just a token: the DM carries the action row, and a
	// deployment with no public key would DM an Acknowledge button that does
	// nothing when pressed.
	if jctx.AppConfig == nil || !jctx.AppConfig.Discord.BotConfigured() {
		log.InfoContext(ctx, "discord bot not configured on this instance; skipping route",
			"contactUID", contact.UID, "orgUID", incident.OrganizationUID)

		return 0
	}

	if !r.reserveDiscord(contact.Value) {
		return 0
	}

	client := r.discordBotClient(jctx.AppConfig.Discord.BotToken)
	msg := escalationDiscordMessage(ctx, jctx, log, incident)

	result, err := discord.SendContactDM(ctx, client, jctx.DBService, contact, msg)
	if err != nil {
		reason := "discord_send_failed"
		if discord.IsCannotDMUser(err) {
			reason = "discord_dm_refused"
		}

		log.WarnContext(ctx, "failed to send escalation Discord DM",
			"contactUID", contact.UID, "reason", reason, "error", err)
		r.auditPhoneSkip(ctx, jctx, log, incident, reason)

		return 0
	}

	messageID := ""
	if result != nil {
		messageID = result.ID
	}

	r.auditPhoneSend(ctx, jctx, log, incident, route.UserUID,
		models.UserContactTypeDiscord, messageID)

	return 1
}

// reserveDiscord de-duplicates one Discord recipient within a single job run.
//
// The same human can be reached through two routes in one step (on-call by
// schedule and named on the policy), and two identical DMs from the same bot in
// the same second read as a bug. There is deliberately NO per-org runaway
// reservation, for the same reason Telegram has none: a Discord DM is free, and
// metering a free channel meters for its own sake.
func (r *EscalationStepJobRun) reserveDiscord(userID string) bool {
	key := models.UserContactTypeDiscord + ":" + userID
	if r.sentPhones[key] {
		return false
	}

	if r.sentPhones == nil {
		r.sentPhones = make(map[string]bool)
	}

	r.sentPhones[key] = true

	return true
}

// discordBotClient builds the bot client for this run.
//
// The factory is a PER-RUN field rather than a package-level variable so the
// tests that drive the real message-building code against an httptest stand-in
// can run in parallel: a package global would have them handing each other's
// fake server to each other, which is exactly the flake that produced this
// comment.
func (r *EscalationStepJobRun) discordBotClient(token string) *discord.BotClient {
	if r.discordClientFactory != nil {
		return r.discordClientFactory(token)
	}

	return discord.NewBotClient(token)
}

// escalationDiscordMessage renders the escalation DM: one embed plus the
// incident action row.
//
// No mention line: a DM already has exactly one recipient, so "<@id> — you are
// on call for this" would ping the person who is reading it in their own private
// conversation. That is the same reason a DM destination skips mention_on_call.
func escalationDiscordMessage(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, incident *models.Incident,
) *discord.Message {
	orgSlug := orgSlugForOrg(ctx, jctx, log, incident.OrganizationUID)
	checkName := incidentCheckName(ctx, jctx, incident)
	incidentURL := escalationIncidentURL(appBaseURL(jctx), orgSlug, incident)

	embed := discord.Embed{
		Title:       discordIncidentRef(incident) + " requires your attention",
		Description: "You are being paged for " + checkName + ".",
		URL:         incidentURL,
		Color:       discord.ColorRed,
		Fields: []discord.Field{
			{Name: viewModelKeyCheckName, Value: checkName, Inline: true},
			{Name: "Organization", Value: orgSlug, Inline: true},
		},
		Timestamp: incident.StartedAt.Format(time.RFC3339),
		Footer:    &discord.Footer{Text: "SolidPing escalation"},
	}

	if detail := discordEscalationDetail(incident); detail != "" {
		embed.Fields = append(embed.Fields,
			discord.Field{Name: "Detail", Value: detail})
	}

	return &discord.Message{
		Embeds:     []discord.Embed{embed},
		Components: []discord.Component{discord.IncidentActionRow(incident.UID)},
	}
}

// discordIncidentRef renders "Incident #42", or just "Incident" for an incident
// with no number yet.
func discordIncidentRef(incident *models.Incident) string {
	if incident != nil && incident.Number > 0 {
		return fmt.Sprintf("%s #%d", incidentRefWord, incident.Number)
	}

	return incidentRefWord
}

// discordEscalationDetail is the incident's headline, whitespace-collapsed and
// capped, falling back to how long it has been open — the same rule the Telegram
// alert uses, so the two channels say the same thing about one incident.
func discordEscalationDetail(incident *models.Incident) string {
	if incident == nil {
		return ""
	}

	detail := ""
	if incident.Title != nil {
		detail = strings.TrimSpace(*incident.Title)
	}

	if detail == "" {
		detail = fmt.Sprintf("open for %s", time.Since(incident.StartedAt).Round(time.Minute))
	}

	detail = strings.Join(strings.Fields(detail), " ")

	if len(detail) > discordDetailCap {
		detail = detail[:discordDetailCap-1] + "…"
	}

	return detail
}
