package usernotifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/discordlink"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
)

// ErrDiscordNotEnabled is returned when the instance has no usable Discord bot
// configured, so no DM contact can be created and no link can be minted.
var ErrDiscordNotEnabled = errors.New("the Discord bot is not configured on this instance")

// ErrDiscordContactNotDirect is returned when someone tries to create a
// `discord` contact through the generic contact endpoint.
//
// This rule is load-bearing security, not tidiness. A discord contact's value is
// a Discord user id — a public number anyone can copy out of a Discord client in
// two clicks — and there is no verification round-trip that could catch a wrong
// one. Without this rejection, any user could type a STRANGER'S snowflake and
// have that person DMed with our incidents, indefinitely, with no way for them
// to find out where it came from.
//
// The only two accepted sources are therefore bindings Discord itself vouched
// for: a `discord` sign-in already on the account (`user_providers`), or the
// OAuth link-mode round trip.
var ErrDiscordContactNotDirect = errors.New(
	"discord contacts are created by connecting your Discord account, not through this endpoint",
)

// ErrDiscordNotSignedIn is returned by Connect when the member has no Discord
// sign-in on file. They are told to use the link flow instead.
var ErrDiscordNotSignedIn = errors.New(
	"no Discord account is linked to your SolidPing account yet",
)

// ErrDiscordContactExists is returned when the member already has a discord
// contact for that account in this org.
var ErrDiscordContactExists = errors.New("this Discord account is already connected")

// ErrDiscordDMRefused is what the Test button reports when Discord answers
// 50007. Its text is the member's actual remedy, which a generic failure is not.
var ErrDiscordDMRefused = errors.New(
	"Discord refused the DM — open your DMs for server members, " +
		"or join the server the bot is in",
)

// DiscordSuggestion carries the Discord DM hint when the member has a Discord
// sign-in on file and the instance bot is configured, but they have no Discord
// contact yet. The twin of SlackSuggestion.
type DiscordSuggestion struct {
	// DiscordUserID is the snowflake that would be bound, from the sign-in.
	DiscordUserID string `json:"discordUserId"`
}

// DiscordMentionIdentity tells a member how they will appear in the org's
// Discord CHANNEL alerts — a different question from "can SolidPing DM me".
//
// The twin of SlackMentionIdentity, with one structural difference: there is no
// Workspace field. A Discord user id is GLOBAL, not per-guild, so there is no
// workspace qualification to report — see identitylink.DeclaredDiscordIdentity
// for what that costs (a mention only pings a member who is in that guild).
type DiscordMentionIdentity struct {
	// Linked is false when nothing identifies this member on Discord, in which
	// case a channel alert names them in plain text and pings nobody.
	Linked bool `json:"linked"`
	// ExternalID is the Discord user id that would be mentioned.
	ExternalID string `json:"externalId,omitempty"`
	// Guild is the Discord server name, so a member in two orgs can tell which
	// one this is about.
	Guild string `json:"guild,omitempty"`
}

// DiscordLinkResponse is the body of POST /users/me/discord/link-start.
type DiscordLinkResponse struct {
	// URL is the Discord authorization URL the browser navigates to.
	URL string `json:"url"`
	// ExpiresAt is when the link round trip stops working.
	ExpiresAt time.Time `json:"expiresAt"`
}

// WithDiscordConfig supplies the instance Discord configuration.
func WithDiscordConfig(cfg *config.DiscordOAuthConfig) Option {
	return func(s *Service) {
		if cfg != nil {
			s.discordCfg = *cfg
		}
	}
}

// WithServerBaseURL supplies the public base URL, needed to build the Discord
// OAuth callback the link round trip returns to.
func WithServerBaseURL(baseURL string) Option {
	return func(s *Service) { s.serverBaseURL = baseURL }
}

// DiscordEnabled reports whether this instance can create Discord DM contacts.
func (s *Service) DiscordEnabled() bool {
	return s.discordCfg.BotConfigured()
}

// ConnectDiscord creates a `discord` contact from the Discord sign-in already on
// the member's account.
//
// This is one of exactly two paths that may create such a contact, and it is the
// cheap one: the `user_providers` row was written by a completed Discord OAuth
// login, so the snowflake is one Discord itself attested belongs to this member.
// The contact is therefore born VERIFIED — the binding is the proof, the same
// reasoning that makes a Telegram contact born verified after /start.
func (s *Service) ConnectDiscord(
	ctx context.Context, orgSlug string, user *models.User,
) (*RouteResponse, error) {
	if !s.DiscordEnabled() {
		return nil, ErrDiscordNotEnabled
	}

	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	discordUserID, err := s.discordProviderID(ctx, user.UID)
	if err != nil {
		return nil, err
	}

	return s.upsertDiscordContact(ctx, orgUID, user.UID, discordUserID)
}

// discordProviderID returns the member's Discord sign-in snowflake.
func (s *Service) discordProviderID(ctx context.Context, userUID string) (string, error) {
	providers, err := s.db.ListUserProvidersByUser(ctx, userUID)
	if err != nil {
		return "", fmt.Errorf("list user providers: %w", err)
	}

	for _, provider := range providers {
		if provider.ProviderType == models.ProviderTypeDiscord && provider.ProviderID != "" {
			return provider.ProviderID, nil
		}
	}

	return "", ErrDiscordNotSignedIn
}

// upsertDiscordContact creates the contact + route for a proven snowflake.
//
// Shared by ConnectDiscord and by the OAuth link-mode callback, so the two
// verified sources cannot end up creating subtly different contacts.
func (s *Service) upsertDiscordContact(
	ctx context.Context, orgUID, userUID, discordUserID string,
) (*RouteResponse, error) {
	existing, listErr := s.db.ListUserContactsWithRoutes(ctx, userUID, orgUID)
	if listErr != nil {
		return nil, fmt.Errorf("list existing routes: %w", listErr)
	}

	for _, route := range existing {
		if route.Contact != nil &&
			route.Contact.Type == models.UserContactTypeDiscord &&
			route.Contact.Value == discordUserID {
			return toRouteResponse(route), nil
		}
	}

	contact := models.NewUserContact(
		userUID, orgUID, models.UserContactTypeDiscord, discordUserID, "Discord")

	// Born verified: the OAuth binding IS the proof of both reachability-intent
	// and opt-in. There is no code to exchange.
	now := s.now()
	contact.VerifiedAt = &now

	if err := s.db.UpsertUserContact(ctx, contact); err != nil {
		return nil, fmt.Errorf("create discord contact: %w", err)
	}

	route := models.NewUserNotificationRoute(userUID, orgUID, contact.UID, len(existing))
	if _, err := s.db.DB().NewInsert().Model(route).
		On("CONFLICT (contact_uid) DO NOTHING").
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("create discord route: %w", err)
	}

	routes, reloadErr := s.db.ListUserContactsWithRoutes(ctx, userUID, orgUID)
	if reloadErr != nil {
		return nil, fmt.Errorf("reload routes after create: %w", reloadErr)
	}

	for _, r := range routes {
		if r.Contact != nil &&
			r.Contact.Type == models.UserContactTypeDiscord &&
			r.Contact.Value == discordUserID {
			return toRouteResponse(r), nil
		}
	}

	return nil, ErrRouteNotFoundAfterCreate
}

// CreateDiscordLink mints a link-mode round trip for a member with no Discord
// sign-in, and returns the Discord authorization URL to send the browser to.
//
// The state is a discordlink token, NOT an auth.OAuthState: the callback it
// redeems must not mint a session and must not create an organization, and
// giving link mode its own token type makes that structural. It asks for
// `identify email` only — without the `guilds` scope there is literally no guild
// list for an org-resolution step to read.
func (s *Service) CreateDiscordLink(
	ctx context.Context, orgSlug string, user *models.User, redirectURI string,
) (*DiscordLinkResponse, error) {
	if !s.DiscordEnabled() {
		return nil, ErrDiscordNotEnabled
	}

	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	token, err := discordlink.Mint(ctx, s.db, discordlink.Payload{
		UserUID:     user.UID,
		OrgUID:      orgUID,
		RedirectURI: redirectURI,
	})
	if err != nil {
		return nil, err
	}

	return &DiscordLinkResponse{
		URL: discordlink.AuthorizeURL(
			discord.OAuthAuthorizeURL, s.discordCfg.ClientID, s.discordCallbackURL(), token),
		ExpiresAt: s.now().Add(discordlink.TTL),
	}, nil
}

// discordCallbackURL mirrors DiscordOAuthService.getCallbackURL. Duplicated
// rather than imported: handlers/auth is above this package in the import graph.
func (s *Service) discordCallbackURL() string {
	return s.serverBaseURL + "/api/v1/auth/discord/callback"
}

// dispatchTestDiscord DMs a test message through the instance bot.
//
// BotConfigured(), not just a token, for the same reason the escalation path
// needs it: a member pressing Test is checking the setup they just completed, and
// an instance that can post but whose buttons are dead is not a working setup.
func (s *Service) dispatchTestDiscord(ctx context.Context, contact *models.UserContact) error {
	if !s.DiscordEnabled() {
		return ErrDiscordNotEnabled
	}

	client := discord.NewBotClient(s.discordCfg.BotToken)

	msg := &discord.Message{
		Content: "Test alert from SolidPing. This is a test notification — " +
			"your Discord DMs are working.",
	}

	if _, err := discord.SendContactDM(ctx, client, s.db, contact, msg); err != nil {
		// 50007 is not a fault, and a generic "send failed" would leave the
		// member with nothing to act on. Say what they can actually change.
		if discord.IsCannotDMUser(err) {
			return ErrDiscordDMRefused
		}

		return fmt.Errorf("send discord test message: %w", err)
	}

	return nil
}
