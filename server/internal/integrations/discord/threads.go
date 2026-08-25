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
