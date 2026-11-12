package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Sign in with Telegram errors.
var (
	// ErrTelegramAuthInvalid means the login result failed the signature
	// check, or is missing the fields the check needs.
	ErrTelegramAuthInvalid = errors.New("telegram login data failed verification")
	// ErrTelegramAuthExpired means the login result is older than
	// telegramAuthMaxAge (or dated in the future): a replay, or a tab left
	// open far too long.
	ErrTelegramAuthExpired = errors.New("telegram login data expired")
	// ErrTelegramNotLinked means no SolidPing user has this Telegram account
	// as a verified contact. Telegram returns no email address, so there is
	// nothing to match or create an account with.
	ErrTelegramNotLinked = errors.New("telegram account not linked to any user")
	// ErrTelegramAmbiguous means several SolidPing users linked this same
	// Telegram account and the login named no org that would pick one.
	ErrTelegramAmbiguous = errors.New("telegram account linked to several users")
)

const (
	telegramOAuthStatePrefix = "oauth_state:telegram:"
	telegramOAuthStateTTL    = 10 * time.Minute
	// telegramAuthMaxAge bounds how old a signed login result may be. The
	// result is a bearer credential for as long as it verifies, so it must be
	// short-lived: the user clicks "Log in" on Telegram's page and is sent back
	// within seconds.
	telegramAuthMaxAge = 5 * time.Minute
	// telegramAuthClockSkew tolerates an auth_date slightly ahead of our clock.
	telegramAuthClockSkew = time.Minute

	telegramFieldHash     = "hash"
	telegramFieldID       = "id"
	telegramFieldAuthDate = "auth_date"
)

// TelegramIdentity is a verified Telegram login result.
type TelegramIdentity struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	PhotoURL  string
	AuthDate  time.Time
}

// VerifyTelegramLogin checks a Telegram login result (the key/value pairs the
// Login Widget or oauth.telegram.org hands back) against the bot token.
//
// The hash is HMAC-SHA256 of the data-check string — every received field but
// `hash`, as `key=value`, sorted by key and joined with '\n' — keyed by the
// SHA-256 of the bot token. Every received field is covered, not just the ones
// we read, so a field Telegram adds later does not break the check and a field
// an attacker adds does.
//
// auth_date must be within telegramAuthMaxAge of now: a captured result must
// not sign anyone in later.
func VerifyTelegramLogin(fields map[string]string, botToken string, now time.Time) (*TelegramIdentity, error) {
	received := fields[telegramFieldHash]
	if received == "" || fields[telegramFieldID] == "" || fields[telegramFieldAuthDate] == "" || botToken == "" {
		return nil, ErrTelegramAuthInvalid
	}

	if !hmac.Equal([]byte(telegramLoginHash(fields, botToken)), []byte(strings.ToLower(received))) {
		return nil, ErrTelegramAuthInvalid
	}

	id, err := strconv.ParseInt(fields[telegramFieldID], 10, 64)
	if err != nil || id <= 0 {
		return nil, ErrTelegramAuthInvalid
	}

	authUnix, err := strconv.ParseInt(fields[telegramFieldAuthDate], 10, 64)
	if err != nil {
		return nil, ErrTelegramAuthInvalid
	}

	authDate := time.Unix(authUnix, 0)
	if now.Sub(authDate) > telegramAuthMaxAge || authDate.Sub(now) > telegramAuthClockSkew {
		return nil, ErrTelegramAuthExpired
	}

	return &TelegramIdentity{
		ID:        id,
		FirstName: fields["first_name"],
		LastName:  fields["last_name"],
		Username:  fields["username"],
		PhotoURL:  fields["photo_url"],
		AuthDate:  authDate,
	}, nil
}

// telegramLoginHash computes the hex hash Telegram signs a login result with.
func telegramLoginHash(fields map[string]string, botToken string) string {
	keys := make([]string, 0, len(fields))

	for key := range fields {
		if key != telegramFieldHash {
			keys = append(keys, key)
		}
	}

	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+fields[key])
	}

	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write([]byte(strings.Join(lines, "\n")))

	return hex.EncodeToString(mac.Sum(nil))
}

// DecodeTelegramAuthResult decodes the `tgAuthResult` value oauth.telegram.org
// appends to the return URL: base64 (standard or URL-safe, padded or not) of a
// JSON object. Numbers keep their exact text so the data-check string matches
// what Telegram signed.
func DecodeTelegramAuthResult(encoded string) (map[string]string, error) {
	normalized := strings.TrimRight(strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimSpace(encoded)), "=")

	raw, err := base64.RawStdEncoding.DecodeString(normalized)
	if err != nil {
		return nil, ErrTelegramAuthInvalid
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, ErrTelegramAuthInvalid
	}

	fields := make(map[string]string, len(object))

	for key, value := range object {
		switch typed := value.(type) {
		case string:
			fields[key] = typed
		case json.Number:
			fields[key] = typed.String()
		case bool:
			fields[key] = strconv.FormatBool(typed)
		default:
			return nil, ErrTelegramAuthInvalid
		}
	}

	return fields, nil
}

// TelegramLoginService handles Sign in with Telegram.
type TelegramLoginService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	now         func() time.Time
}

// NewTelegramLoginService creates a new Telegram login service.
func NewTelegramLoginService(dbService db.Service, cfg *config.Config, authService *Service) *TelegramLoginService {
	return &TelegramLoginService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		now:         time.Now,
	}
}

// GenerateOAuthState creates a single-use login state and stores it.
func (s *TelegramLoginService) GenerateOAuthState(
	ctx context.Context, redirectURI, orgSlug string,
) (string, error) {
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	stateJSON, err := json.Marshal(OAuthState{
		Nonce:       nonce,
		RedirectURI: redirectURI,
		OrgSlug:     orgSlug,
		CreatedAt:   s.now().Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal state: %w", err)
	}

	ttl := telegramOAuthStateTTL
	value := &models.JSONMap{keyState: string(stateJSON)}

	if err := s.db.SetStateEntry(ctx, nil, telegramOAuthStatePrefix+nonce, value, &ttl); err != nil {
		return "", fmt.Errorf("failed to store state: %w", err)
	}

	return nonce, nil
}

// ValidateOAuthState validates and consumes a login state.
func (s *TelegramLoginService) ValidateOAuthState(ctx context.Context, stateParam string) (*OAuthState, error) {
	if stateParam == "" {
		return nil, ErrInvalidOAuthState
	}

	entry, err := s.db.GetStateEntry(ctx, nil, telegramOAuthStatePrefix+stateParam)
	if err != nil || entry == nil || entry.Value == nil {
		return nil, ErrInvalidOAuthState
	}

	_, _ = s.db.DeleteStateEntry(ctx, nil, telegramOAuthStatePrefix+stateParam)

	stateJSON, ok := (*entry.Value)[keyState].(string)
	if !ok {
		return nil, ErrInvalidOAuthState
	}

	var state OAuthState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrInvalidOAuthState
	}

	if s.now().Unix()-state.CreatedAt > int64(telegramOAuthStateTTL.Seconds()) {
		return nil, ErrInvalidOAuthState
	}

	return &state, nil
}

// HandleCallback verifies a Telegram login result and signs in the user who
// linked that Telegram account.
//
// Only a verified `telegram` contact counts as a link: it is created when the
// user presses Start in a private chat with the instance bot, so its value
// (the private chat id) is the Telegram user id and the link proves the user
// controls that account. There is no auto-registration: Telegram returns no
// email address to create an account with.
func (s *TelegramLoginService) HandleCallback(
	ctx context.Context, fields map[string]string, orgSlug string,
) (*ProviderOutcome, error) {
	identity, err := VerifyTelegramLogin(fields, strings.TrimSpace(s.cfg.Telegram.BotToken), s.now())
	if err != nil {
		return nil, err
	}

	var requested *models.Organization

	if orgSlug != "" {
		requested, err = s.db.GetOrganizationBySlug(ctx, orgSlug)
		if err != nil || requested == nil {
			requested = nil
		}
	}

	contact, err := s.resolveContact(ctx, identity.ID, requested)
	if err != nil {
		return nil, err
	}

	user, err := s.db.GetUser(ctx, contact.UserUID)
	if err != nil || user == nil {
		return nil, ErrTelegramNotLinked
	}

	org := requested
	if org == nil {
		org, err = s.db.GetOrganization(ctx, contact.OrganizationUID)
		if err != nil || org == nil {
			return nil, ErrTelegramNotLinked
		}
	}

	login, err := s.authService.CompleteOrgLogin(ctx, org, user, WithLoginMethod(signupMethodTelegram))
	if err != nil {
		return nil, err
	}

	return login.Outcome(org.Slug, user.UID), nil
}

// resolveContact picks the verified telegram contact this login signs in as.
//
// One Telegram account can be linked in several orgs (one contact per org),
// and in theory by several SolidPing users. The contact in the requested org
// wins; otherwise the oldest link, provided every link belongs to one user —
// signing in as an arbitrary one of several users would be a guess.
func (s *TelegramLoginService) resolveContact(
	ctx context.Context, telegramID int64, requested *models.Organization,
) (*models.UserContact, error) {
	contacts, err := s.db.ListUserContactsByTypeValue(
		ctx, models.UserContactTypeTelegram, strconv.FormatInt(telegramID, 10),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list telegram contacts: %w", err)
	}

	verified := make([]*models.UserContact, 0, len(contacts))
	users := make(map[string]struct{})

	for _, contact := range contacts {
		if contact.VerifiedAt == nil {
			continue
		}

		if requested != nil && contact.OrganizationUID == requested.UID {
			return contact, nil
		}

		verified = append(verified, contact)
		users[contact.UserUID] = struct{}{}
	}

	switch {
	case len(verified) == 0:
		return nil, ErrTelegramNotLinked
	case len(users) > 1:
		return nil, ErrTelegramAmbiguous
	default:
		return verified[0], nil
	}
}

// getCallbackURL returns the URL Telegram sends the browser back to.
func (s *TelegramLoginService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + telegramLoginCallbackPath
}
