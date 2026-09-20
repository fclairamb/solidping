// Package discordlink owns the "link my Discord account to the SolidPing
// account I am already signed in to" round trip.
//
// It exists as its own leaf package because both halves of that round trip live
// in packages that must not import each other: the account page mints the state
// (handlers/usernotifications) and the public OAuth callback redeems it
// (handlers/auth). Keeping the token format, its TTL and its single-use
// semantics in one file is what stops the two halves drifting.
//
// The state is deliberately NOT an auth.OAuthState. A login state carries a
// redirect and an org and feeds findOrCreateOrganization plus session minting; a
// link state must do NONE of that. Giving link mode its own token type makes
// "link mode never mints a session and never creates an organization" a
// structural property of the code rather than a branch somebody has to remember
// not to break.
package discordlink

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// StatePrefix is the marker that tells the shared /auth/discord/callback
	// handler this callback is a LINK, not a login. Callback state values are
	// opaque to Discord, which hands back whatever we sent.
	StatePrefix = "link:"
	// statePrefix namespaces the single-use link tokens in state_entries.
	statePrefix = "discord_link:"
	// TTL is how long a link round trip stays valid. Short on purpose: the token
	// is a bearer credential that binds whichever Discord account completes the
	// flow to a SolidPing account, so one left in a browser tab overnight must
	// not still work.
	TTL = 15 * time.Minute
	// tokenBytes is the entropy of a link token — far past the ≥128 bits
	// required, which is what lets the public callback trust it with no other
	// authentication.
	tokenBytes = 32
	// Scopes are the ONLY scopes link mode asks for. Deliberately without
	// `guilds`: link mode has no business knowing which servers the member is
	// in, and not holding that data is what makes it structurally impossible for
	// this path to resolve (or create) an organization.
	Scopes = "identify email"
)

// Payload is what a link token stands for: the SolidPing account the resulting
// Discord identity is bound to, and where to send the browser afterwards.
type Payload struct {
	// UserUID is the already-signed-in member. The callback writes the
	// user_providers row for THIS user and nobody else.
	UserUID string
	// OrgUID is the org whose account page started the flow, so the contact can
	// be created in the right one.
	OrgUID string
	// RedirectURI is the dashboard page to return to.
	RedirectURI string
}

// Mint stores a single-use link token and returns it.
//
// Stored as a GLOBAL state entry (organization_uid NULL) even though the payload
// is org-scoped, for the same reason the Telegram connect token is: the OAuth
// callback is instance-level and carries no org at all, so an org-scoped row
// would be unfindable at redemption time. The org lives in the value and the
// token's entropy is what protects it.
func Mint(ctx context.Context, dbSvc db.Service, payload Payload) (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate discord link token: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(buf)
	ttl := TTL
	value := &models.JSONMap{
		"userUid":     payload.UserUID,
		"orgUid":      payload.OrgUID,
		"redirectUri": payload.RedirectURI,
	}

	if err := dbSvc.SetStateEntry(ctx, nil, statePrefix+token, value, &ttl); err != nil {
		return "", fmt.Errorf("store discord link token: %w", err)
	}

	return token, nil
}

// Consume resolves a link token and deletes it in the same breath, so it can
// never be redeemed twice.
//
// Returns (nil, nil) for an unknown, expired or already-redeemed token. The
// caller answers the same way either way, which keeps the public callback from
// becoming an oracle for probing live tokens.
func Consume(ctx context.Context, dbSvc db.Service, token string) (*Payload, error) {
	if token == "" {
		return nil, nil //nolint:nilnil // "no live token" is not an error
	}

	key := statePrefix + token

	entry, err := dbSvc.GetStateEntry(ctx, nil, key)
	if err != nil {
		return nil, fmt.Errorf("load discord link token: %w", err)
	}

	if entry == nil || entry.Value == nil {
		return nil, nil //nolint:nilnil // unknown or expired token
	}

	// Single use: delete before acting on it. The returned bool is the
	// compare-and-set that makes a replay racing this redemption a no-op.
	deleted, delErr := dbSvc.DeleteStateEntry(ctx, nil, key)
	if delErr != nil {
		return nil, fmt.Errorf("consume discord link token: %w", delErr)
	}

	if !deleted {
		return nil, nil //nolint:nilnil // lost the race: treat as already used
	}

	userUID, _ := (*entry.Value)["userUid"].(string)
	orgUID, _ := (*entry.Value)["orgUid"].(string)
	redirectURI, _ := (*entry.Value)["redirectUri"].(string)

	if userUID == "" || orgUID == "" {
		return nil, nil //nolint:nilnil // malformed payload: same polite failure
	}

	return &Payload{UserUID: userUID, OrgUID: orgUID, RedirectURI: redirectURI}, nil
}

// AuthorizeURL builds the Discord authorization URL for a link round trip.
//
// Exported so the account page, the callback and the tests share one spelling of
// the scopes and of the `link:`-prefixed state.
func AuthorizeURL(authorizeBase, clientID, callbackURL, token string) string {
	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("redirect_uri", callbackURL)
	params.Set("response_type", "code")
	params.Set("scope", Scopes)
	params.Set("state", StatePrefix+token)

	return authorizeBase + "?" + params.Encode()
}
