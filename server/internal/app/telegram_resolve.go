package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

const (
	// TelegramBotUsernameParam holds the bot's @username, derived from getMe on
	// the first boot that does not already know it. Public: the browser needs it
	// to build the t.me connect link.
	TelegramBotUsernameParam = "telegram.bot_username"

	// TelegramWebhookSecretParam holds the shared string Telegram echoes in the
	// X-Telegram-Bot-Api-Secret-Token header. SECRET: it is the ONLY authenticity
	// gate on the public webhook, since Telegram does not sign payloads.
	TelegramWebhookSecretParam = "telegram.webhook_secret"

	// telegramSecretBytes is the entropy of a generated webhook secret. 32 bytes
	// because this single value is the whole lock on a public endpoint.
	telegramSecretBytes = 32

	// telegramGetMeTimeout bounds the ONE synchronous network call the startup
	// path may make (only on a first boot with no username known from env or
	// DB). An unreachable Telegram must delay startup by at most this, once, and
	// must never fail it.
	telegramGetMeTimeout = 3 * time.Second
)

// errTelegramNoUsername means getMe answered without a username, which a bot
// account cannot legitimately do.
var errTelegramNoUsername = errors.New("telegram getMe returned no username")

// resolveTelegramSettings hydrates cfg.Telegram with the values the operator
// did not have to supply: the bot @username and the webhook secret.
//
// Resolution order for both, and it matters:
//
//	env/YAML  >  persisted system parameter  >  derived (getMe / crypto-rand)
//
// Env keeps priority so an operator fronting the webhook with a proxy that must
// know the secret — or running declarative/GitOps config — is never fought by
// the server, and a configured value is never overwritten in the DB.
//
// WHY SYNCHRONOUS, AND WHY HERE: cfg is read concurrently by every request
// handler and nothing mutates it after startup. Hydrating it from the async
// bootstrapTelegram goroutine would be a genuine data race. So this runs at the
// top of SetupRoutes — before any handler exists to read cfg, and strictly
// before the async bootstrap pushes the secret to Telegram with setWebhook
// (persist first: the reverse order can leave Telegram holding a secret no pod
// in the fleet knows).
//
// Nothing here can fail startup. A DB or Bot API problem degrades the connect
// surface and logs; it never returns an error.
func resolveTelegramSettings(ctx context.Context, dbSvc db.Service, cfg *config.Config) {
	if cfg == nil || dbSvc == nil || !cfg.Telegram.Configured() {
		return
	}

	resolveTelegramWebhookSecret(ctx, dbSvc, cfg)
	resolveTelegramBotUsername(ctx, dbSvc, cfg)
}

// resolveTelegramWebhookSecret fills cfg.Telegram.WebhookSecret from the DB, or
// generates and persists one. Local and fast: no network call ever.
func resolveTelegramWebhookSecret(ctx context.Context, dbSvc db.Service, cfg *config.Config) {
	if strings.TrimSpace(cfg.Telegram.WebhookSecret) != "" {
		// Explicitly configured: use it verbatim and leave any stored parameter
		// untouched, so switching back to a derived secret is a config change
		// away rather than a data-loss event.
		return
	}

	candidate, err := generateTelegramWebhookSecret()
	if err != nil {
		slog.ErrorContext(ctx, "could not generate a Telegram webhook secret; "+
			"the webhook will reject every update until one is configured", "error", err)

		return
	}

	// The RETURNED value is what we adopt: a pod that loses the insert race must
	// use the winner's secret, never the one it just generated.
	param, created, err := dbSvc.GetOrCreateSystemParameter(
		ctx, TelegramWebhookSecretParam, candidate, true,
	)
	if err != nil {
		slog.ErrorContext(ctx, "could not resolve the Telegram webhook secret",
			"parameter", TelegramWebhookSecretParam, "error", err)

		return
	}

	stored, ok := parameterStringValue(param)
	if !ok || stored == "" {
		slog.ErrorContext(ctx, "the stored Telegram webhook secret is not a string",
			"parameter", TelegramWebhookSecretParam)

		return
	}

	cfg.Telegram.WebhookSecret = stored

	// Deliberately never logs the value itself.
	slog.InfoContext(ctx, "Telegram webhook secret resolved",
		"parameter", TelegramWebhookSecretParam, "generated", created)
}

// resolveTelegramBotUsername fills cfg.Telegram.BotUsername from the DB, or —
// only on a boot where neither env nor DB knows it — from a single bounded
// getMe call, persisting the answer so no later boot needs the network.
func resolveTelegramBotUsername(ctx context.Context, dbSvc db.Service, cfg *config.Config) {
	if strings.TrimSpace(cfg.Telegram.BotUsername) != "" {
		return
	}

	param, err := dbSvc.GetSystemParameter(ctx, TelegramBotUsernameParam)
	if err != nil {
		slog.WarnContext(ctx, "could not read the stored Telegram bot username",
			"parameter", TelegramBotUsernameParam, "error", err)
	}

	if stored, ok := parameterStringValue(param); ok && stored != "" {
		cfg.Telegram.BotUsername = stored

		return
	}

	// First boot: ask Telegram who this token belongs to. Bounded, and a failure
	// is a warning — the connect surface simply stays off until the next boot.
	username, err := fetchTelegramBotUsername(ctx, &cfg.Telegram)
	if err != nil {
		slog.WarnContext(ctx, "could not derive the Telegram bot username from getMe; "+
			"the connect surface stays off until a later boot succeeds "+
			"(set SP_TELEGRAM_BOT_USERNAME to skip this call entirely)",
			"error", err)

		return
	}

	persisted, _, err := dbSvc.GetOrCreateSystemParameter(
		ctx, TelegramBotUsernameParam, username, false,
	)
	if err != nil {
		slog.WarnContext(ctx, "could not persist the derived Telegram bot username",
			"parameter", TelegramBotUsernameParam, "error", err)

		// Still usable for this process's lifetime.
		cfg.Telegram.BotUsername = username

		return
	}

	if stored, ok := parameterStringValue(persisted); ok && stored != "" {
		username = stored
	}

	cfg.Telegram.BotUsername = username

	slog.InfoContext(ctx, "Telegram bot username derived from getMe",
		"username", username, "parameter", TelegramBotUsernameParam)
}

// fetchTelegramBotUsername performs the one bounded getMe call.
func fetchTelegramBotUsername(ctx context.Context, cfg *config.TelegramConfig) (string, error) {
	client, err := telegram.NewClientFromConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("build telegram client: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, telegramGetMeTimeout)
	defer cancel()

	bot, err := client.GetMe(callCtx)
	if err != nil {
		return "", fmt.Errorf("telegram getMe: %w", err)
	}

	username := strings.TrimPrefix(strings.TrimSpace(bot.Username), "@")
	if username == "" {
		return "", errTelegramNoUsername
	}

	return username, nil
}

// generateTelegramWebhookSecret mints a high-entropy, URL-safe secret.
// Unpadded base64url so it is safe in any header, URL or shell one-liner an
// operator might paste it into.
func generateTelegramWebhookSecret() (string, error) {
	buf := make([]byte, telegramSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate telegram webhook secret: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// parameterStringValue unwraps the {"value": …} envelope parameters are stored
// in, when it holds a string.
func parameterStringValue(param *models.Parameter) (string, bool) {
	if param == nil || param.Value == nil {
		return "", false
	}

	value, ok := param.Value[models.ParameterValueKey].(string)

	return value, ok
}
