package discord

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ContactDMStore is the narrow slice of the database a DM send needs: somewhere
// to cache the channel Discord hands back. Declared here rather than taking a
// db.Service so this package keeps no dependency on the full store interface —
// and so tests can drive the caching behavior without a database.
type ContactDMStore interface {
	SetUserContactDMChannel(ctx context.Context, uid, channelID string) error
}

// ErrNotDiscordContact is returned when SendContactDM is handed a contact that
// is not a `discord` one. A programming error, surfaced rather than ignored:
// silently DMing whatever string happened to be in Value is how a Slack user id
// would end up addressed to Discord.
var ErrNotDiscordContact = errors.New("contact is not a discord contact")

// SendContactDM posts msg to a member's Discord DM, opening the DM channel if
// it has not been opened yet and caching it on the contact.
//
// Three cases, in order:
//
//  1. the contact already carries a DM channel id — post straight into it, no
//     extra round trip;
//  2. it does not — CreateDM, cache the channel id, post;
//  3. it did, but Discord has since 404'd that channel — forget the cache,
//     CreateDM again, and retry ONCE. A DM channel id is not supposed to expire,
//     but an id that came out of a restored database backup or a since-deleted
//     account does, and a permanently un-pageable contact is the worst possible
//     way to find that out.
//
// Caching failures are deliberately NOT fatal: a message that went out must not
// be reported as failed because a bookkeeping write lost a race. The next send
// simply pays for the open again.
//
// A refusal by the recipient (APIError code 50007, IsCannotDMUser) comes back
// unchanged for the caller to classify — it is not a fault and must not be
// retried.
func SendContactDM(
	ctx context.Context, client *BotClient, store ContactDMStore,
	contact *models.UserContact, msg *Message,
) (*MessageResult, error) {
	if contact == nil || contact.Type != models.UserContactTypeDiscord {
		return nil, ErrNotDiscordContact
	}

	if client == nil {
		return nil, ErrBotTokenMissing
	}

	cached := ""
	if contact.DMChannelID != nil {
		cached = *contact.DMChannelID
	}

	if cached != "" {
		result, err := client.CreateMessage(ctx, cached, msg)
		if err == nil {
			return result, nil
		}

		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}

		// The cached channel is gone. Drop it before re-opening so a second
		// failure cannot leave a known-bad id on the row.
		cacheDMChannel(ctx, store, contact, "")
	}

	channel, err := client.CreateDM(ctx, contact.Value)
	if err != nil {
		return nil, fmt.Errorf("opening discord dm: %w", err)
	}

	if channel.ID == "" {
		return nil, ErrEmptyRecipient
	}

	cacheDMChannel(ctx, store, contact, channel.ID)

	result, err := client.CreateMessage(ctx, channel.ID, msg)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// cacheDMChannel writes the channel id onto the contact, best-effort, and keeps
// the in-memory contact in step so a caller that sends twice in one run does not
// re-open the DM.
func cacheDMChannel(
	ctx context.Context, store ContactDMStore, contact *models.UserContact, channelID string,
) {
	if channelID == "" {
		contact.DMChannelID = nil
	} else {
		value := channelID
		contact.DMChannelID = &value
	}

	if store == nil {
		return
	}

	_ = store.SetUserContactDMChannel(ctx, contact.UID, channelID)
}
