package discord

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/identitylink"
)

// ErrDiscordUserNotResolved is returned when OpenDMDestination is asked for a
// user the org cannot vouch for.
//
// Only a member with a RESOLVED Discord identity may be picked as a destination.
// Accepting an arbitrary snowflake here would be the same hole the `discord`
// contact type closes at the other end: an admin could point a check's alerts at
// any Discord account on earth, and that person would start receiving a
// stranger's incidents with nothing naming where they came from.
var ErrDiscordUserNotResolved = errors.New(
	"that Discord user is not a member of this organization with a linked Discord account",
)

// DiscordDestinationUser is an org member who can be picked as a DM destination.
type DiscordDestinationUser struct { //nolint:revive // prefix is intentional disambiguation
	// ID is the Discord user id (snowflake) the DM would be opened with.
	ID string `json:"id"`
	// Name is the SolidPing display name, which is the only name we have — the
	// bot cannot read a guild member list without the privileged GUILD_MEMBERS
	// intent, and that list would not say which SolidPing account a member is
	// anyway.
	Name string `json:"name"`
	// UserUID is the SolidPing member, so the picker can show who this is rather
	// than an opaque snowflake.
	UserUID string `json:"userUid"`
}

// DiscordDMResponse is returned by OpenDMDestination: the DM channel the sender
// will address, plus the user it belongs to.
type DiscordDMResponse struct { //nolint:revive // prefix is intentional disambiguation
	ChannelID string `json:"channelId"`
	UserID    string `json:"userId"`
	Name      string `json:"name"`
}

// listDestinationUsers returns the org members with a resolved Discord identity.
//
// Resolution is exactly the same as an on-call mention's: the admin's
// `user_integration_identities` mapping first, then
// identitylink.DeclaredDiscordIdentity (a `discord` contact, then a Discord
// sign-in). Sharing that definition is the point — a picker that listed people
// the sender cannot address, or hid people it can, is worse than no picker.
//
// Deliberately NOT the guild member list: that needs the privileged
// GUILD_MEMBERS intent, and it would tell us who is in the server without
// telling us which SolidPing account any of them is.
//
// Best-effort: a member whose lookups fail is left out rather than failing the
// whole picker.
func (s *Service) listDestinationUsers(
	ctx context.Context, conn *models.Integration,
) []DiscordDestinationUser {
	members, err := s.db.ListMembersByOrg(ctx, conn.OrganizationUID)
	if err != nil {
		return nil
	}

	users := make([]DiscordDestinationUser, 0, len(members))
	seen := make(map[string]bool, len(members))

	for _, member := range members {
		if member == nil || member.User == nil {
			continue
		}

		user := member.User

		externalID := ""

		identity, idErr := s.db.GetUserIntegrationIdentity(ctx, conn.UID, user.UID)
		if idErr == nil && identity != nil && identity.ExternalID != "" {
			externalID = identity.ExternalID
		}

		if externalID == "" {
			if declared := identitylink.DeclaredDiscordIdentity(ctx, s.db, conn, user.UID); declared != nil {
				externalID = declared.ExternalID
			}
		}

		if externalID == "" || seen[externalID] {
			continue
		}

		seen[externalID] = true

		name := strings.TrimSpace(user.Name)
		if name == "" {
			name = user.Email
		}

		users = append(users, DiscordDestinationUser{
			ID:      externalID,
			Name:    name,
			UserUID: user.UID,
		})
	}

	slices.SortFunc(users, func(a, b DiscordDestinationUser) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	return users
}

// OpenDMDestination opens (or reuses) the DM channel for one org member and
// returns its channel id, for the picker to store as the integration's
// destination.
//
// The DM is opened HERE, at pick time, rather than lazily at send time, for one
// reason: an admin who picks a destination must find out immediately whether it
// works. Discord answers 50007 for a member who has DMs from server members off
// or who is not in the server, and discovering that during the first real
// incident is how a check ends up routed to nobody.
func (s *Service) OpenDMDestination(
	ctx context.Context, orgSlug, channelUID, discordUserID string,
) (*DiscordDMResponse, error) {
	conn, err := s.loadConnection(ctx, orgSlug, channelUID)
	if err != nil {
		return nil, err
	}

	if s.botToken() == "" {
		return nil, ErrBotNotConfigured
	}

	var picked *DiscordDestinationUser

	for _, candidate := range s.listDestinationUsers(ctx, conn) {
		if candidate.ID == discordUserID {
			user := candidate
			picked = &user

			break
		}
	}

	if picked == nil {
		return nil, ErrDiscordUserNotResolved
	}

	channel, err := s.newBotClient(s.botToken()).CreateDM(ctx, discordUserID)
	if err != nil {
		return nil, fmt.Errorf("opening discord dm: %w", err)
	}

	return &DiscordDMResponse{
		ChannelID: channel.ID,
		UserID:    picked.ID,
		Name:      picked.Name,
	}, nil
}
