package app

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// telegramBootstrapTimeout bounds the boot-time Bot API round trips. Startup
// must never hang on a network problem at Telegram — the checks below are
// diagnostics and self-healing, not preconditions.
const telegramBootstrapTimeout = 15 * time.Second

// TelegramWebhookPath is the inbound webhook route. Declared once so the
// bootstrap registration and the router can never disagree about it.
const TelegramWebhookPath = "/api/v1/integrations/telegram/webhook"

// bootstrapTelegram performs two boot-time Bot API calls when Telegram is
// active, both strictly best-effort:
//
//  1. getMe, to confirm the configured BotUsername actually matches the token.
//     A mismatch is a LOUD WARNING, never a crash: a stale username produces
//     connect links pointing at the wrong bot, which is otherwise a silent and
//     utterly baffling failure ("the link opens a bot that does nothing").
//  2. setWebhook, when the registered URL differs from ours, so a deploy to a
//     new hostname self-heals instead of needing a manual curl. Skipped when
//     the instance has no public base URL to register.
//
// Nothing here can fail startup. An instance whose Telegram bot is temporarily
// unreachable must still serve every other feature.
func bootstrapTelegram(ctx context.Context, cfg *config.Config) {
	// Only API nodes serve the webhook, and an inactive config has nothing to
	// check. Both guards live here rather than at the call site so Start stays
	// a list of unconditional startup steps.
	if cfg == nil || !cfg.ShouldRunAPI() || !cfg.Telegram.Active() {
		return
	}

	client, err := telegram.NewClientFromConfig(&cfg.Telegram)
	if err != nil {
		slog.WarnContext(ctx, "telegram is enabled but no client could be built", "error", err)

		return
	}

	callCtx, cancel := context.WithTimeout(ctx, telegramBootstrapTimeout)
	defer cancel()

	verifyTelegramIdentity(callCtx, client, &cfg.Telegram)
	ensureTelegramWebhook(callCtx, client, cfg)
}

// verifyTelegramIdentity warns when the configured username does not match the
// bot the token actually belongs to.
func verifyTelegramIdentity(ctx context.Context, client *telegram.Client, cfg *config.TelegramConfig) {
	bot, err := client.GetMe(ctx)
	if err != nil {
		slog.WarnContext(ctx, "could not verify the Telegram bot identity",
			"reason", telegram.FailureReason(err), "error", err)

		return
	}

	configured := cfg.ResolvedBotUsername()
	if !strings.EqualFold(configured, bot.Username) {
		slog.WarnContext(ctx,
			"SP_TELEGRAM_BOT_USERNAME does not match the bot this token belongs to; "+
				"connect links will point at the wrong bot",
			"configured", configured, "actual", bot.Username)

		return
	}

	slog.InfoContext(ctx, "Telegram bot ready", "username", bot.Username)
}

// ensureTelegramWebhook registers our webhook URL when Telegram is pointing
// somewhere else (or nowhere).
func ensureTelegramWebhook(ctx context.Context, client *telegram.Client, cfg *config.Config) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.Server.BaseURL), "/")
	if baseURL == "" {
		slog.WarnContext(ctx,
			"Telegram is enabled but no server base URL is configured; "+
				"register the webhook manually (see the Telegram docs page)")

		return
	}

	if cfg.Telegram.WebhookSecret == "" {
		// Registering without a secret would leave the public webhook with no
		// authenticity gate at all, which the handler would then reject on
		// every delivery. Refuse loudly rather than half-configure.
		slog.WarnContext(ctx,
			"Telegram is enabled but SP_TELEGRAM_WEBHOOK_SECRET is empty; "+
				"the webhook will reject every update until one is set")

		return
	}

	want := baseURL + TelegramWebhookPath

	info, err := client.GetWebhookInfo(ctx)
	if err != nil {
		slog.WarnContext(ctx, "could not read the current Telegram webhook registration",
			"reason", telegram.FailureReason(err), "error", err)

		return
	}

	if info.URL == want {
		if info.LastErrorMessage != "" {
			slog.WarnContext(ctx, "Telegram reports a recent webhook delivery error",
				"url", want, "lastError", info.LastErrorMessage,
				"pendingUpdates", info.PendingUpdateCount)
		}

		return
	}

	if err := client.SetWebhook(ctx, want, cfg.Telegram.WebhookSecret); err != nil {
		slog.WarnContext(ctx, "could not register the Telegram webhook",
			"url", want, "reason", telegram.FailureReason(err), "error", err)

		return
	}

	slog.InfoContext(ctx, "Telegram webhook registered", "url", want, "previous", info.URL)
}
