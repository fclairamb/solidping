package incidents

import (
	"context"
	"log/slog"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Acknowledgment event payload keys.
//
// snake_case, unlike the camelCase comment keys above: these predate the
// comment convention and are already written to production event rows, so
// renaming them would silently orphan every historical acknowledgment. They
// are named here — rather than spelled inline at the two places that read and
// write them — because the writer (buildAcknowledgmentEvent), the reader
// (resolveAckActor) and the dashboard's extraction must agree exactly.
const (
	payloadKeyAckSlackUserID   = "slack_user_id"
	payloadKeyAckSlackUsername = "slack_username"
	payloadKeyAckDiscordUserID = "discord_user_id"
	payloadKeyAckDiscordUser   = "discord_username"
	payloadKeyAckEmail         = "acknowledged_by_email"
	payloadKeyAckPhone         = "acknowledged_by_phone"
	payloadKeyAckTelegram      = "acknowledged_by_telegram"
	payloadKeyAckNote          = "note"
)

// IncidentActorResponse is the display-oriented identity of whoever performed
// an incident action.
//
// It exists because `incidents.acknowledged_by` is a FK to `users.uid` and
// therefore CANNOT name the acker for the channels where most acks actually
// happen: a Slack, Discord or phone acker is not a SolidPing user, so the
// column stays NULL and their identity survives only on the acknowledgment
// event's payload. A consumer that reads the column alone renders "acked by
// nobody" for exactly the cases operators care most about.
type IncidentActorResponse struct {
	// Name is the human label to render: the SolidPing user's name or email
	// when the acker maps to one, otherwise the chat username / email address
	// / phone number recorded on the event. Never empty — falls back to a
	// neutral label rather than rendering a blank attribution.
	Name string `json:"name"`
	// Via is the channel the action came from ("web", "slack", "discord",
	// "telegram", "email", "phone"). Empty for an acknowledgment recorded
	// before the `via` key existed.
	Via string `json:"via,omitempty"`
	// UserUID is the SolidPing user the action is credited to, when there is
	// one. Absent for chat/phone ackers with no platform account.
	UserUID string `json:"userUid,omitempty"`
}

// ackActorUnknownName is what an acknowledgment with no recoverable identity
// renders as. A neutral word beats an empty string: the dashboard's "Acked by
// ___" must never read as a rendering bug.
const ackActorUnknownName = "Unknown"

// resolveAckActor builds the display identity of an incident's acker.
//
// Three sources, and the ORDER between them is deliberate:
//
//  1. the chat identity on the latest `incident.acknowledged` event payload —
//     the ONLY record of a Slack, Discord or phone acker, and also the only
//     record of WHICH person pressed the button in a shared Telegram group;
//  2. the `users` row behind `acknowledged_by`, authoritative but populated
//     only for the dash0 UI, a magic link whose email matches a user, and a
//     linked Telegram chat;
//  3. the payload's weaker identifiers (email, phone, raw chat user ids).
//
// A Telegram ack is credited to the account the chat is LINKED to, so putting
// the user row first would report the linked account for a button a colleague
// pressed — the one thing the payload exists to disambiguate.
//
// Best-effort throughout: a missing event or a deleted user degrades the
// attribution, never the incident response.
func (s *Service) resolveAckActor(
	ctx context.Context, orgUID string, incident *models.Incident,
) *IncidentActorResponse {
	if incident == nil || incident.AcknowledgedAt == nil {
		return nil
	}

	actor := &IncidentActorResponse{}

	if incident.AcknowledgedBy != nil && *incident.AcknowledgedBy != "" {
		actor.UserUID = *incident.AcknowledgedBy
	}

	payload := s.latestAckEventPayload(ctx, orgUID, incident.UID)
	actor.Via = payloadString(payload, payloadKeyVia)
	actor.Name = ackChatIdentity(payload)

	if actor.Name == "" && actor.UserUID != "" {
		actor.Name = s.lookupUserDisplayName(ctx, actor.UserUID)
	}

	if actor.Name == "" {
		actor.Name = ackWeakIdentity(payload)
	}

	if actor.Name == "" {
		actor.Name = ackActorUnknownName
	}

	return actor
}

// ackChatIdentity returns the chat username the acknowledgment was performed
// under, or "" when the ack did not come from a chat platform.
func ackChatIdentity(payload models.JSONMap) string {
	for _, key := range []string{payloadKeyAckSlackUsername, payloadKeyAckDiscordUser} {
		if v := payloadString(payload, key); v != "" {
			return v
		}
	}

	return telegramActorName(payloadString(payload, payloadKeyAckTelegram))
}

// ackWeakIdentity returns the last-resort identifiers: an address, a number,
// or a raw platform user id. Each at least distinguishes two people, which a
// blank attribution does not.
func ackWeakIdentity(payload models.JSONMap) string {
	for _, key := range []string{
		payloadKeyAckEmail,
		payloadKeyAckPhone,
		payloadKeyAckSlackUserID,
		payloadKeyAckDiscordUserID,
	} {
		if v := payloadString(payload, key); v != "" {
			return v
		}
	}

	return ""
}

// telegramActorName extracts the bare first name out of the Telegram
// attribution label, which is stored as the whole phrase `via Telegram (Carol)`
// (telegram.ActorLabel).
//
// Necessary because that label is a SENTENCE FRAGMENT, not a name: rendering it
// as one produces "Acknowledged by via Telegram (Carol) via Telegram". A label
// carrying no name ("via Telegram") yields "" so the caller falls through to
// the linked account instead.
func telegramActorName(label string) string {
	open := strings.Index(label, "(")
	closing := strings.LastIndex(label, ")")

	if open < 0 || closing <= open+1 {
		return ""
	}

	return strings.TrimSpace(label[open+1 : closing])
}

// payloadString reads a trimmed string off an event payload, tolerating both
// an absent key and a non-string value.
func payloadString(payload models.JSONMap, key string) string {
	if payload == nil {
		return ""
	}

	v, ok := payload[key].(string)
	if !ok {
		return ""
	}

	return strings.TrimSpace(v)
}

// lookupUserDisplayName resolves a user UID to the label a human reads. Name
// first, email second — an operator recognizes "Alice" faster than
// "alice@acme.com", but either beats a UUID.
func (s *Service) lookupUserDisplayName(ctx context.Context, userUID string) string {
	user, err := s.db.GetUser(ctx, userUID)
	if err != nil || user == nil {
		return ""
	}

	if name := strings.TrimSpace(user.Name); name != "" {
		return name
	}

	return strings.TrimSpace(user.Email)
}

// latestAckEventPayload returns the payload of the most recent
// `incident.acknowledged` event on an incident, or nil.
//
// Most recent rather than first: an incident can be acked, unacked and acked
// again, and the attribution the API reports must be the ack that is currently
// in force.
func (s *Service) latestAckEventPayload(
	ctx context.Context, orgUID, incidentUID string,
) models.JSONMap {
	events, err := s.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: orgUID,
		IncidentUID:     &incidentUID,
		EventTypes:      []models.EventType{models.EventTypeIncidentAcknowledged},
		Limit:           1,
	})
	if err != nil {
		slog.WarnContext(ctx, "Failed to load acknowledgment event for actor resolution",
			"incidentUid", incidentUID, "error", err)

		return nil
	}

	if len(events) == 0 || events[0] == nil {
		return nil
	}

	return events[0].Payload
}

// IncidentResponseWithAckActor renders an incident for the API with its
// acknowledgment attribution resolved.
//
// Used by the ack/unack endpoints so the response they hand back matches what
// a refetch of the detail endpoint would return — a dashboard that renders the
// POST result directly must not show "Acked by Unknown" for the second it
// takes the query cache to catch up.
func (s *Service) IncidentResponseWithAckActor(
	ctx context.Context, orgSlug string, incident *models.Incident,
) IncidentResponse {
	response := incidentToResponse(incident)

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return response
	}

	response.AcknowledgedByActor = s.resolveAckActor(ctx, org.UID, incident)

	return response
}
