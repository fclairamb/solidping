package usernotifications

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func activeTelegramConfig() *config.TelegramConfig {
	return &config.TelegramConfig{
		Enabled:       true,
		BotToken:      "123456789:AAtest",
		BotUsername:   "solidping_test_bot",
		WebhookSecret: "a-long-random-webhook-secret",
	}
}

// setupTelegramService builds a service with Telegram active plus its DB, so
// tests can inspect the state entries the connect flow writes.
func setupTelegramService(t *testing.T) (*Service, db.Service, *models.Organization, *models.User) {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("tg-org", "TG Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("oncall@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, member))

	svc := NewService(dbSvc, nil, WithTelegramConfig(activeTelegramConfig()))

	return svc, dbSvc, org, user
}

// THE security rule of this feature: a telegram contact's value is a chat id
// and there is no verification round-trip that could catch a wrong one, so
// accepting one through the generic endpoint would let any user page a
// stranger by typing their chat id.
func TestCreateContact_RejectsTelegram(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)

	_, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypeTelegram,
		Value: "987654321",
		Label: "someone else's chat",
	})
	r.ErrorIs(err, ErrTelegramContactNotDirect)

	// Nothing was created — not even a soft-deleted row.
	routes, listErr := svc.db.ListUserContactsWithRoutes(ctx, user.UID, org.UID)
	r.NoError(listErr)

	for _, route := range routes {
		r.NotEqual(models.UserContactTypeTelegram, route.Contact.Type)
	}
}

// Positive control for the test above: the same call with a normally-creatable
// type succeeds, so the rejection is about `telegram` and not about the harness.
func TestCreateContact_StillAcceptsOtherTypes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)

	route, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypePhone,
		Value: "+15551230000",
	})
	r.NoError(err)
	r.Equal(models.UserContactTypePhone, route.Contact.Type)
}

// telegram must NOT be in VerifiableContactTypes: there is no code round-trip
// for it, and listing it there would make the verify endpoints offer a flow
// that cannot exist.
func TestTelegramIsNotAVerifiableContactType(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.False(models.ContactRequiresVerification(models.UserContactTypeTelegram))
	r.NotContains(models.VerifiableContactTypes(), models.UserContactTypeTelegram)
	// Positive control: the types that DO have a round-trip are still listed.
	r.True(models.ContactRequiresVerification(models.UserContactTypePhone))
	r.True(models.ContactRequiresVerification(models.UserContactTypeWhatsApp))
}

func TestCreateTelegramLink_MintsAConsumableToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, dbSvc, org, user := setupTelegramService(t)

	resp, err := svc.CreateTelegramLink(ctx, org.Slug, user)
	r.NoError(err)
	r.True(strings.HasPrefix(resp.URL, "https://t.me/solidping_test_bot?start="))
	r.WithinDuration(time.Now().Add(TelegramLinkTTL), resp.ExpiresAt, time.Minute)

	_, token, _ := strings.Cut(resp.URL, "?start=")
	// ≥128 bits of entropy, URL-safe and free of base64 padding (Telegram's
	// /start payload allows only [A-Za-z0-9_-]).
	r.GreaterOrEqual(len(token), 22)
	r.NotContains(token, "=")
	r.NotContains(token, "+")
	r.NotContains(token, "/")

	payload, err := ConsumeTelegramLinkToken(ctx, dbSvc, token)
	r.NoError(err)
	r.NotNil(payload)
	r.Equal(user.UID, payload.UserUID)
	r.Equal(org.UID, payload.OrgUID)
}

// Single use: the token is deleted the moment it is redeemed, so a replay is a
// no-op rather than a second contact.
func TestConsumeTelegramLinkToken_IsSingleUse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, dbSvc, org, user := setupTelegramService(t)

	resp, err := svc.CreateTelegramLink(ctx, org.Slug, user)
	r.NoError(err)

	_, token, _ := strings.Cut(resp.URL, "?start=")

	first, err := ConsumeTelegramLinkToken(ctx, dbSvc, token)
	r.NoError(err)
	r.NotNil(first)

	second, err := ConsumeTelegramLinkToken(ctx, dbSvc, token)
	r.NoError(err)
	r.Nil(second, "a replayed token must resolve to nothing")
}

// Two links minted back to back must be independent — a fixed or predictable
// token would make the whole connect flow forgeable.
func TestCreateTelegramLink_TokensAreUnique(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, _, org, user := setupTelegramService(t)

	seen := make(map[string]bool, 5)

	for range 5 {
		resp, err := svc.CreateTelegramLink(ctx, org.Slug, user)
		r.NoError(err)

		_, token, _ := strings.Cut(resp.URL, "?start=")
		r.False(seen[token], "connect tokens must never repeat")
		seen[token] = true
	}
}

func TestConsumeTelegramLinkToken_UnknownOrExpired(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	_, dbSvc, org, user := setupTelegramService(t)

	// Never existed.
	payload, err := ConsumeTelegramLinkToken(ctx, dbSvc, "never-minted")
	r.NoError(err)
	r.Nil(payload)

	// Empty.
	payload, err = ConsumeTelegramLinkToken(ctx, dbSvc, "")
	r.NoError(err)
	r.Nil(payload)

	// Expired: stored with an already-elapsed TTL.
	past := -time.Minute
	r.NoError(dbSvc.SetStateEntry(ctx, nil, TelegramLinkStatePrefix+"stale",
		&models.JSONMap{"userUid": user.UID, "orgUid": org.UID}, &past))

	payload, err = ConsumeTelegramLinkToken(ctx, dbSvc, "stale")
	r.NoError(err)
	r.Nil(payload, "an expired token must resolve to nothing")

	// Malformed payload (no user/org) fails the same, silent way.
	ttl := time.Hour
	r.NoError(dbSvc.SetStateEntry(ctx, nil, TelegramLinkStatePrefix+"junk",
		&models.JSONMap{"nonsense": true}, &ttl))

	payload, err = ConsumeTelegramLinkToken(ctx, dbSvc, "junk")
	r.NoError(err)
	r.Nil(payload)
}

func TestCreateTelegramLink_RefusedWhenInactive(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user := setupUserNotificationsService(t)
	r.False(svc.TelegramEnabled())

	_, err := svc.CreateTelegramLink(ctx, org.Slug, user)
	r.ErrorIs(err, ErrTelegramNotEnabled)
}

func TestTelegramLinkURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("https://t.me/solidping_bot?start=abc", TelegramLinkURL("solidping_bot", "abc"))
}
