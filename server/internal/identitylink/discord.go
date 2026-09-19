package identitylink

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// DiscordIdentity is one resolved Discord account for a member.
type DiscordIdentity struct {
	// ExternalID is the Discord user id (a snowflake), the thing `<@…>` needs.
	ExternalID string
	// DisplayName is the provider-side label when the source knows one. Empty
	// for sources that only carry an id — the caller then keeps the SolidPing
	// display name, which is what it would have rendered anyway.
	DisplayName string
	// Source names where the id came from, for logs and tests: SourceContact or
	// SourceAuthProvider.
	Source string
}

// DeclaredDiscordIdentity returns the Discord user id this member declared for
// themselves, or nil.
//
// Resolution order:
//
//  1. a `discord` notification contact in this org — the member connected their
//     own account, so Discord itself attested the binding;
//  2. the member's Discord sign-in (`user_providers`).
//
// # There is deliberately NO guild scoping, unlike the Slack twin
//
// A Discord user id is GLOBAL. The same snowflake addresses the same human in
// every server on Discord, so there is no cross-workspace mistake to guard
// against and nothing to compare an integration's guild id to. That is why this
// function takes no team id, checks no `organization_providers` row, and does not
// care how many Discord integrations the org has — every one of the Slack
// resolver's three rules exists purely to prevent a mistake that cannot happen
// here.
//
// The consequence, which is the thing to know before reading a bug report about
// it: a mention only PINGS a member who is actually in the guild being posted
// to. For anyone else, Discord renders `<@id>` as an inert mention — visible
// text naming the right person, delivering no notification. That is a strictly
// better failure than Slack's would be (a wrong person pinged), which is why it
// is accepted rather than guarded, but it does mean "the mention rendered" is not
// the same claim as "the member was notified". The `AllowedMentions` allow-list
// is unaffected either way.
//
// Deliberately does NOT consider `user_integration_identities` — that row is the
// admin's answer and always outranks this one, so layering the two is the
// caller's job (and lets SyncIdentities use this as a pure fallback).
//
// Best-effort, nil on any doubt: a mention is a nicety and must never break a
// send.
func DeclaredDiscordIdentity(
	ctx context.Context, dbSvc db.Service, integration *models.Integration, userUID string,
) *DiscordIdentity {
	if dbSvc == nil || integration == nil || userUID == "" {
		return nil
	}

	if integration.Type != models.ConnectionTypeDiscord {
		return nil
	}

	orgUID := integration.OrganizationUID

	if id := discordContactIdentity(ctx, dbSvc, orgUID, userUID); id != nil {
		return id
	}

	return discordAuthProviderIdentity(ctx, dbSvc, userUID)
}

// discordContactIdentity implements rule 1.
func discordContactIdentity(
	ctx context.Context, dbSvc db.Service, orgUID, userUID string,
) *DiscordIdentity {
	routes, err := dbSvc.ListUserContactsWithRoutes(ctx, userUID, orgUID)
	if err != nil {
		return nil
	}

	for _, route := range routes {
		contact := route.Contact
		if contact == nil || contact.Type != models.UserContactTypeDiscord || contact.Value == "" {
			continue
		}

		return &DiscordIdentity{ExternalID: contact.Value, Source: SourceContact}
	}

	return nil
}

// discordAuthProviderIdentity implements rule 2.
func discordAuthProviderIdentity(
	ctx context.Context, dbSvc db.Service, userUID string,
) *DiscordIdentity {
	providers, err := dbSvc.ListUserProvidersByUser(ctx, userUID)
	if err != nil {
		return nil
	}

	for _, provider := range providers {
		if provider.ProviderType == models.ProviderTypeDiscord && provider.ProviderID != "" {
			return &DiscordIdentity{ExternalID: provider.ProviderID, Source: SourceAuthProvider}
		}
	}

	return nil
}
