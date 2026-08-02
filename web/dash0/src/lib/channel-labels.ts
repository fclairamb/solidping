/**
 * Human-readable labels for the backend's notification channel tokens.
 *
 * Most channel names survive a plain CSS `capitalize` (slack → Slack), but a
 * few do not: "whatsapp" must render as "WhatsApp", not "Whatsapp". Anything
 * not listed falls back to first-letter capitalisation so a channel added on
 * the backend still renders sensibly without a frontend change.
 */
const CHANNEL_LABELS: Record<string, string> = {
  whatsapp: "WhatsApp",
  sms: "SMS",
  voice: "Voice call",
  webpush: "Browser push",
  msteams: "Microsoft Teams",
  "msteams-bot": "Microsoft Teams",
  googlechat: "Google Chat",
  ntfy: "ntfy",
  opsgenie: "Opsgenie",
  pushover: "Pushover",
};

/** Returns the display label for a notification channel token. */
export function channelTypeLabel(channelType: string | undefined | null): string {
  if (!channelType) return "";

  const known = CHANNEL_LABELS[channelType];
  if (known) return known;

  return channelType.charAt(0).toUpperCase() + channelType.slice(1);
}

/**
 * Human-readable text for the machine skip/failure reasons the backend records
 * on a notification audit row. Unknown codes are de-snake-cased rather than
 * hidden, so a reason added on the backend still reads acceptably.
 */
const FAILURE_REASONS: Record<string, string> = {
  whatsapp_template_unavailable:
    "WhatsApp message template unavailable — ask an admin to check the template's approval status",
  whatsapp_recipient_not_on_whatsapp: "Recipient is not on WhatsApp",
  whatsapp_rate_limited: "Rate limited by WhatsApp (messaging tier cap)",
  whatsapp_token_expired: "WhatsApp credentials expired — ask an admin to refresh them",
  whatsapp_not_configured: "WhatsApp is not configured on this instance",
  whatsapp_invalid_recipient: "WhatsApp number is not a valid E.164 number",
  whatsapp_send_failed: "WhatsApp delivery failed",
  whatsapp_quota_exhausted: "Monthly WhatsApp quota exhausted",
  sms_quota_exhausted: "Monthly SMS quota exhausted",
  voice_quota_exhausted: "Monthly voice-call quota exhausted",
};

/** Returns display text for a notification skip/failure reason code. */
export function failureReasonLabel(reason: string | undefined | null): string {
  if (!reason) return "";

  return FAILURE_REASONS[reason] ?? reason.replace(/_/g, " ");
}
