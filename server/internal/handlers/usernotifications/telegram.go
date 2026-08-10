package usernotifications

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Telegram connect-flow constants.
const (
	// TelegramLinkStatePrefix namespaces the single-use connect tokens in the
	// state_entries table.
	TelegramLinkStatePrefix = "telegram_link:"
	// TelegramLinkTTL is how long a connect link stays valid. Short on purpose:
	// the token is a bearer credential that binds whichever Telegram chat
	// presents it to a SolidPing account, so a link left in a browser tab
	// overnight must not still work.
	TelegramLinkTTL = 15 * time.Minute
	// telegramLinkTokenBytes is the entropy of a connect token. 32 bytes is far
	// past the ≥128 bits the spec requires — the token is unguessable, which is
	// what lets the inbound webhook trust it with no other authentication.
	telegramLinkTokenBytes = 32
)

// ErrTelegramNotEnabled is returned when the instance has no usable Telegram
// bot configured, so no connect link can be minted.
var ErrTelegramNotEnabled = errors.New("telegram is not configured on this instance")

// ErrTelegramContactNotDirect is returned when someone tries to create a
// telegram contact through the generic contact endpoint.
//
// This rule is load-bearing security, not tidiness: a telegram contact's value
// is a chat id, and there is no verification round-trip that could catch a
// wrong one. Without this rejection any user could type a stranger's chat id
// and page them at will.
var ErrTelegramContactNotDirect = errors.New(
	"telegram contacts are created by connecting the bot, not through this endpoint",
)

// TelegramLinkResponse is the body of POST /users/me/telegram/link.
type TelegramLinkResponse struct {
	// URL is the t.me deep link the browser opens. Pressing Start in Telegram
	// delivers the token to our webhook, which is what actually creates the
	// contact.
	URL string `json:"url"`
	// ExpiresAt is when the token stops working.
	ExpiresAt time.Time `json:"expiresAt"`
}

// TelegramLinkPayload is the value stored under a connect token. It is what
// binds the eventual /start to a specific user in a specific org.
type TelegramLinkPayload struct {
	UserUID string
	OrgUID  string
}

// WithTelegramConfig supplies the instance Telegram configuration.
func WithTelegramConfig(cfg *config.TelegramConfig) Option {
	return func(s *Service) {
		if cfg != nil {
			s.telegramCfg = *cfg
		}
	}
}

// TelegramEnabled reports whether this instance can mint connect links.
func (s *Service) TelegramEnabled() bool {
	return s.telegramCfg.Active()
}

// CreateTelegramLink mints a single-use connect token and returns the t.me deep
// link that carries it.
//
// The token is stored as a GLOBAL state entry (organization_uid NULL) even
// though its payload is org-scoped. That is deliberate: the inbound Telegram
// webhook is instance-level and carries no org whatsoever, so an org-scoped row
// would be unfindable at redemption time. The org lives in the value, and the
// token's own entropy is what protects it.
func (s *Service) CreateTelegramLink(
	ctx context.Context, orgSlug string, user *models.User,
) (*TelegramLinkResponse, error) {
	if !s.telegramCfg.Active() {
		return nil, ErrTelegramNotEnabled
	}

	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	token, err := newTelegramLinkToken()
	if err != nil {
		return nil, err
	}

	ttl := TelegramLinkTTL
	value := &models.JSONMap{"userUid": user.UID, "orgUid": orgUID}

	if setErr := s.db.SetStateEntry(ctx, nil, TelegramLinkStatePrefix+token, value, &ttl); setErr != nil {
		return nil, fmt.Errorf("store telegram link token: %w", setErr)
	}

	return &TelegramLinkResponse{
		URL:       TelegramLinkURL(s.telegramCfg.ResolvedBotUsername(), token),
		ExpiresAt: s.now().Add(ttl),
	}, nil
}

// TelegramLinkURL builds the t.me deep link for a bot username and token.
// Exported so the handler, the tests and the docs share one spelling.
func TelegramLinkURL(botUsername, token string) string {
	return "https://t.me/" + botUsername + "?start=" + token
}

// newTelegramLinkToken mints a URL-safe, unpadded, high-entropy token.
// Unpadded because Telegram's /start payload allows only [A-Za-z0-9_-].
func newTelegramLinkToken() (string, error) {
	buf := make([]byte, telegramLinkTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate telegram link token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ConsumeTelegramLinkToken resolves a connect token and deletes it in the same
// breath, so it can never be redeemed twice.
//
// Returns (nil, nil) for an unknown, expired or already-redeemed token: the
// caller replies with the same polite "this link has expired" either way, which
// keeps the bot from becoming an oracle for probing live tokens.
//
// Lives here rather than in the webhook handler so the minting and the
// redemption of a token — the two halves of one security-critical protocol —
// stay in the same file and cannot drift apart.
func ConsumeTelegramLinkToken(
	ctx context.Context, dbSvc db.Service, token string,
) (*TelegramLinkPayload, error) {
	if token == "" {
		return nil, nil //nolint:nilnil // "no live token" is not an error
	}

	key := TelegramLinkStatePrefix + token

	entry, err := dbSvc.GetStateEntry(ctx, nil, key)
	if err != nil {
		return nil, fmt.Errorf("load telegram link token: %w", err)
	}

	if entry == nil || entry.Value == nil {
		return nil, nil //nolint:nilnil // unknown or expired token
	}

	// Single use: delete before acting on it. The returned bool is the
	// compare-and-set that makes a replay racing this redemption a no-op rather
	// than a second contact.
	deleted, delErr := dbSvc.DeleteStateEntry(ctx, nil, key)
	if delErr != nil {
		return nil, fmt.Errorf("consume telegram link token: %w", delErr)
	}

	if !deleted {
		return nil, nil //nolint:nilnil // lost the race: treat as already used
	}

	userUID, _ := (*entry.Value)["userUid"].(string)
	orgUID, _ := (*entry.Value)["orgUid"].(string)

	if userUID == "" || orgUID == "" {
		return nil, nil //nolint:nilnil // malformed payload: same polite failure
	}

	return &TelegramLinkPayload{UserUID: userUID, OrgUID: orgUID}, nil
}
