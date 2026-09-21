package discord

import (
	"context"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db"
)

// LookupThreadIncident resolves a Discord thread back to the incident whose
// notification opened it, using the reverse mapping the sender writes.
//
// Returns (incidentUID, orgUID, found). `found` is false — with no error — for
// a thread we do not track, which is the common case: most threads in a guild
// have nothing to do with SolidPing.
func LookupThreadIncident(
	ctx context.Context, dbService db.Service, guildID, threadID string,
) (string, string, bool) {
	if guildID == "" || threadID == "" {
		return "", "", false
	}

	entry, err := dbService.GetStateEntry(ctx, nil, ReverseThreadStateKey(guildID, threadID))
	if err != nil {
		slog.WarnContext(ctx, "Failed to look up Discord thread mapping",
			"guild_id", guildID, "thread_id", threadID, "error", err)

		return "", "", false
	}

	if entry == nil || entry.Value == nil {
		return "", "", false
	}

	incidentUID, _ := (*entry.Value)[ThreadIncidentUIDKey].(string)
	orgUID, _ := (*entry.Value)[ThreadOrgUIDKey].(string)

	if incidentUID == "" || orgUID == "" {
		return "", "", false
	}

	return incidentUID, orgUID, true
}

// Keys inside the forward incident→message/thread state entry the notification
// sender writes when it posts an incident alert.
const (
	IncidentStateKeyChannelID = "channel_id"
	IncidentStateKeyMessageID = "message_id"
	IncidentStateKeyThreadID  = "thread_id"
)

// IncidentThreadStateKey is the forward incident→message/thread state entry
// key: written by the notification sender when it posts the alert (with the
// message id, the channel it landed in and the thread it opened), read by every
// consumer that needs to reach an incident's conversation later — follow-up
// replies, and the acknowledgment notice a button press posts.
func IncidentThreadStateKey(incidentUID string) string {
	return "incidents/" + incidentUID + "/discord/thread"
}
