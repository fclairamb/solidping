package app

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
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
//  1. getMe, to confirm the configured BotUsername actually matches the token —
//     and, when nothing knew the username yet, to PERSIST the answer.
//     A mismatch is a LOUD WARNING, never a crash: a stale username produces
//     connect links pointing at the wrong bot, which is otherwise a silent and
//     utterly baffling failure ("the link opens a bot that does nothing").
//  2. setWebhook, ALWAYS, so a deploy to a new hostname — or a rotated webhook
//     secret — self-heals instead of needing a manual curl. Skipped only when
//     the instance has no public base URL to register.
//
// Nothing here can fail startup. An instance whose Telegram bot is temporarily
// unreachable must still serve every other feature.
func bootstrapTelegram(ctx context.Context, dbSvc db.Service, cfg *config.Config) {
	// Only API nodes serve the webhook, and an inactive config has nothing to
	// check. Both guards live here rather than at the call site so Start stays
	// a list of unconditional startup steps.
	if cfg == nil || !cfg.ShouldRunAPI() || !cfg.Telegram.Configured() {
		return
	}

	client, err := telegram.NewClientFromConfig(&cfg.Telegram)
	if err != nil {
		slog.WarnContext(ctx, "telegram is enabled but no client could be built", "error", err)

		return
	}

	callCtx, cancel := context.WithTimeout(ctx, telegramBootstrapTimeout)
	defer cancel()

	verifyTelegramIdentity(callCtx, dbSvc, client, &cfg.Telegram)
	ensureTelegramWebhook(callCtx, client, cfg)
}

// verifyTelegramIdentity warns when the configured username does not match the
// bot the token actually belongs to, and persists the username when nothing
// knew it yet.
func verifyTelegramIdentity(
	ctx context.Context, dbSvc db.Service, client *telegram.Client, cfg *config.TelegramConfig,
) {
	bot, err := client.GetMe(ctx)
	if err != nil {
		slog.WarnContext(ctx, "could not verify the Telegram bot identity",
			"reason", telegram.FailureReason(err), "error", err)

		return
	}

	// Only an EXPLICITLY configured username can disagree with the token. When
	// it was derived from getMe (the default now) there is by construction
	// nothing to disagree with, so warning would be pure noise.
	configured := cfg.ResolvedBotUsername()
	if configured != "" && !strings.EqualFold(configured, bot.Username) {
		slog.WarnContext(ctx,
			"SP_TELEGRAM_BOT_USERNAME does not match the bot this token belongs to; "+
				"connect links will point at the wrong bot",
			"configured", configured, "actual", bot.Username)

		return
	}

	if configured == "" {
		persistTelegramBotUsername(ctx, dbSvc, bot.Username)
	}

	slog.InfoContext(ctx, "Telegram bot ready", "username", bot.Username)
}

// persistTelegramBotUsername stores a username this boot derived only here, so
// the NEXT boot resolves it from the DB instead of the network.
//
// Why this exists: the startup resolution in resolveTelegramSettings makes one
// getMe bounded to telegramGetMeTimeout, because it runs synchronously before
// any handler exists and must not delay boot. When that call loses a race — a
// cold DNS cache on the first pod of a cluster is enough — the username stays
// empty and NOTHING is written down. Active() is then false forever, so
// POST /users/me/telegram/link answers "telegram is not configured" on every
// later request even though this bootstrap's own getMe, running with a far more
// generous budget, has just succeeded. The connect surface stayed dead until an
// operator noticed and set SP_TELEGRAM_BOT_USERNAME by hand.
//
// This deliberately does NOT write cfg.Telegram.BotUsername. By now the router
// is live and every request handler reads cfg concurrently; mutating it here
// would be a real data race, which is exactly why the synchronous resolver
// exists in the first place. Persisting is enough: one restart away from a
// self-heal beats a permanent manual fix.
func persistTelegramBotUsername(ctx context.Context, dbSvc db.Service, username string) {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if dbSvc == nil || username == "" {
		return
	}

	if _, _, err := dbSvc.GetOrCreateSystemParameter(
		ctx, TelegramBotUsernameParam, username, false,
	); err != nil {
		slog.WarnContext(ctx, "could not persist the Telegram bot username derived at bootstrap",
			"parameter", TelegramBotUsernameParam, "username", username, "error", err)

		return
	}

	slog.InfoContext(ctx, "Telegram bot username persisted; "+
		"the connect link surface becomes available after the next restart",
		"parameter", TelegramBotUsernameParam, "username", username)
}

// ensureTelegramWebhook registers our webhook URL and secret with Telegram.
//
// The setWebhook call is UNCONDITIONAL, and that is the whole point. Telegram's
// getWebhookInfo returns the registered `url` but NEVER the `secret_token` —
// that field is write-only in the Bot API. So a URL comparison can only detect
// half of a divergence: a secret that changes at a constant URL would never be
// pushed, Telegram would keep echoing the old X-Telegram-Bot-Api-Secret-Token,
// and the handler would 403 every single update — the connect flow, in-chat
// /stop and block notifications all dying silently, with the only trace being a
// last_error_message surfaced at the NEXT boot.
//
// setWebhook is idempotent, so re-registering costs exactly one API call per
// boot and buys convergence by construction for a value that cannot be read
// back. getWebhookInfo is still called, but purely for the
// last_error_message / pending_update_count diagnostics: it no longer gates
// anything, and a failure to read it does not stop the registration.
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

	// Diagnostics only — deliberately BEFORE the registration so the reported
	// state is the one Telegram held on arrival, and deliberately non-fatal.
	previous := reportTelegramWebhookState(ctx, client, want)

	if err := client.SetWebhook(ctx, want, cfg.Telegram.WebhookSecret); err != nil {
		slog.WarnContext(ctx, "could not register the Telegram webhook",
			"url", want, "reason", telegram.FailureReason(err), "error", err)

		return
	}

	slog.InfoContext(ctx, "Telegram webhook registered", "url", want, "previous", previous)
}

// reportTelegramWebhookState logs whatever Telegram reports about the current
// registration and returns the URL it holds (empty when unknown). Purely
// informational: every failure path here is a warning, never a reason to skip
// the setWebhook that follows.
func reportTelegramWebhookState(ctx context.Context, client *telegram.Client, want string) string {
	info, err := client.GetWebhookInfo(ctx)
	if err != nil {
		slog.WarnContext(ctx, "could not read the current Telegram webhook registration",
			"reason", telegram.FailureReason(err), "error", err)

		return ""
	}

	if info.LastErrorMessage != "" {
		slog.WarnContext(ctx, "Telegram reports a recent webhook delivery error",
			"url", info.URL, "want", want, "lastError", info.LastErrorMessage,
			"pendingUpdates", info.PendingUpdateCount)
	}

	return info.URL
}
