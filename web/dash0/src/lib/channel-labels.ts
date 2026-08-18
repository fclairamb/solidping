import type { TFunction } from "i18next";

/**
 * Human-readable labels for the backend's notification channel tokens and for
 * the machine skip/failure reasons recorded on a notification audit row.
 *
 * Both are translated: they surface in the delivery history, which an fr/de/es
 * user reads in their own language everywhere else. The `t` function is passed
 * in (from the `common` namespace) rather than resolved from a module-level
 * i18next instance, so components re-render when the language changes.
 *
 * The fallbacks matter as much as the translations: channels and reasons are
 * added on the backend without a frontend release, so an unknown token must
 * still render sensibly (capitalised / de-snake-cased) rather than leaking a
 * raw i18n key into the UI. Hence the explicit allow-lists — `t()` returns the
 * key itself for a miss, which would be worse than the fallback.
 */

/** Channel tokens that have an explicit translated label. */
const TRANSLATED_CHANNELS = new Set([
  "whatsapp",
  "telegram",
  "sms",
  "voice",
  "webpush",
  "email",
  "msteams",
  "msteams-bot",
  "googlechat",
  "mattermost",
  "ntfy",
  "matrix",
  "opsgenie",
  "pushover",
  "slack",
  "discord",
  "webhook",
]);

/** Skip/failure reason codes that have a translated explanation. */
const TRANSLATED_REASONS = new Set([
  "whatsapp_template_unavailable",
  "whatsapp_recipient_not_on_whatsapp",
  "whatsapp_rate_limited",
  "whatsapp_unsupported_message",
  "whatsapp_session_window_closed",
  "whatsapp_token_expired",
  "whatsapp_not_configured",
  "whatsapp_invalid_recipient",
  "whatsapp_send_failed",
  "whatsapp_quota_exhausted",
  "sms_quota_exhausted",
  "voice_quota_exhausted",
  "telegram_unauthorized",
  "telegram_bot_blocked",
  "telegram_chat_not_found",
  "telegram_reply_target_missing",
  "telegram_rate_limited",
  "telegram_not_configured",
  "telegram_invalid_chat",
  "telegram_send_failed",
  "telegram_runaway_guard",
]);

/**
 * Returns the display label for a notification channel token.
 *
 * `t` must come from the `common` namespace (`useTranslation("common")`).
 */
export function channelTypeLabel(
  t: TFunction,
  channelType: string | undefined | null,
): string {
  if (!channelType) return "";

  if (TRANSLATED_CHANNELS.has(channelType)) {
    return t(`channels.${channelType}`);
  }

  return channelType.charAt(0).toUpperCase() + channelType.slice(1);
}

/**
 * Returns display text for a notification skip/failure reason code. Unknown
 * codes are de-snake-cased rather than hidden, so a reason added on the backend
 * still reads acceptably before the frontend catches up.
 *
 * `t` must come from the `common` namespace (`useTranslation("common")`).
 */
export function failureReasonLabel(
  t: TFunction,
  reason: string | undefined | null,
): string {
  if (!reason) return "";

  if (TRANSLATED_REASONS.has(reason)) {
    return t(`notificationFailureReasons.${reason}`);
  }

  return reason.replace(/_/g, " ");
}
