package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/discordlink"
)

// Link-mode failures. All of them end as a redirect back to the dashboard with
// an error code, never as a session.
var (
	// ErrDiscordLinkStateInvalid covers an unknown, expired or already-redeemed
	// link token. One message for all three, so the public callback cannot be
	// used as an oracle for probing live tokens.
	ErrDiscordLinkStateInvalid = errors.New("this Discord link has expired")
	// ErrDiscordLinkClaimedByOther is returned when the Discord account is
	// already bound to a DIFFERENT SolidPing account. Two accounts cannot share
	// one snowflake: whoever got there first keeps it, because the alternative is
	// that completing this flow silently steals another member's paging route.
	ErrDiscordLinkClaimedByOther = errors.New("this Discord account is already linked to another user")
)

// Link-mode redirect parameters, read by the dashboard's account page.
const (
	// discordLinkedParam tells the account page the link succeeded, so it can
	// create the contact. Contact creation deliberately stays on the dashboard's
	// authenticated call rather than happening here: the callback is a PUBLIC
	// route, and the less it is allowed to write the smaller the blast radius of
	// anything wrong with it.
	discordLinkedParam = "discord_linked"
	// discordLinkErrorCode is the error code the account page renders.
	discordLinkErrorCode = "DISCORD_LINK_FAILED"
)

// isDiscordLinkState reports whether a callback state belongs to a link round
// trip rather than a login.
func isDiscordLinkState(state string) bool {
	return strings.HasPrefix(state, discordlink.StatePrefix)
}

// handleLinkCallback completes an OAuth LINK round trip.
//
// The whole point of this function is what it does NOT do. It does not call
// findOrCreateOrganization, so no organization can be created or joined; it does
// not call CompleteOrgLogin, so no session, access token or refresh token is
// minted; and it never touches the auth cookie. All it writes is one
// `user_providers` row for the user the link token named — a user who is already
// signed in, which is why no session needs minting in the first place.
//
// That matters because the LOGIN path deliberately does the opposite: it resolves
// an org from the caller's guild list and mints a session. Reusing it for link
// mode is how a "link my account" button would quietly become "create an org and
// sign me in as somebody" — the 2026-08-24 capture on the login path is exactly
// that shape of bug. Link mode asks for `identify email` only, without `guilds`,
// so there is not even a guild list here for an org-resolution step to read.
func (h *DiscordOAuthHandler) handleLinkCallback(
	writer http.ResponseWriter, req *http.Request, code, stateParam string,
) error {
	token := strings.TrimPrefix(stateParam, discordlink.StatePrefix)

	payload, err := discordlink.Consume(req.Context(), h.svc.db, token)
	if err != nil || payload == nil {
		return h.redirectWithError(writer, req, "/",
			discordLinkErrorCode, ErrDiscordLinkStateInvalid.Error())
	}

	userInfo, err := h.svc.exchangeAndIdentify(req.Context(), code)
	if err != nil {
		return h.redirectWithError(writer, req, payload.RedirectURI,
			discordLinkErrorCode, "Discord did not confirm your account")
	}

	if err := h.svc.LinkDiscordProvider(req.Context(), payload.UserUID, userInfo.ID); err != nil {
		return h.redirectWithError(writer, req, payload.RedirectURI,
			discordLinkErrorCode, err.Error())
	}

	return redirectLinked(writer, req, payload.RedirectURI)
}

// redirectLinked sends the browser back to the page that started the flow, with
// the success marker the account page acts on.
func redirectLinked(writer http.ResponseWriter, req *http.Request, redirectURI string) error {
	target := redirectURI
	if target == "" {
		target = "/"
	}

	parsed, err := url.Parse(target)
	if err != nil {
		parsed, _ = url.Parse("/")
	}

	query := parsed.Query()
	query.Set(discordLinkedParam, "1")
	parsed.RawQuery = query.Encode()

	http.Redirect(writer, req, parsed.String(), http.StatusFound)

	return nil
}

// exchangeAndIdentify is the token exchange plus the profile fetch, and nothing
// else.
//
// Deliberately NOT HandleCallback: that one also fetches guilds, resolves an
// organization and mints a session. This is the half link mode is allowed to do.
func (s *DiscordOAuthService) exchangeAndIdentify(
	ctx context.Context, code string,
) (*DiscordUserInfo, error) {
	tokenResp, err := s.exchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscordTokenExchange, err)
	}

	userInfo, err := s.fetchUserProfile(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrDiscordAPI, err)
	}

	if userInfo.ID == "" {
		return nil, ErrDiscordAPI
	}

	return userInfo, nil
}

// LinkDiscordProvider binds a Discord snowflake to an existing SolidPing user,
// idempotently.
//
// Already bound to this user — nothing to do. Already bound to someone else —
// refused: the first binding wins, because overwriting it would move another
// member's Discord paging route onto this account without telling either of them.
func (s *DiscordOAuthService) LinkDiscordProvider(
	ctx context.Context, userUID, discordUserID string,
) error {
	if userUID == "" || discordUserID == "" {
		return ErrDiscordLinkStateInvalid
	}

	existing, err := s.db.GetUserProviderByProviderID(
		ctx, models.ProviderTypeDiscord, discordUserID)
	if err == nil && existing != nil {
		if existing.UserUID == userUID {
			return nil
		}

		return ErrDiscordLinkClaimedByOther
	}

	provider := models.NewUserProvider(userUID, models.ProviderTypeDiscord, discordUserID)
	if err := s.db.CreateUserProvider(ctx, provider); err != nil {
		return fmt.Errorf("link discord provider: %w", err)
	}

	return nil
}
