import type { TFunction } from "i18next";

import type { SupportThread } from "@/api/support";

/**
 * Why the reply box is disabled for a thread, or "" when it is answerable.
 *
 * Two independent axes, checked in the order the server checks them:
 *
 *  1. `canReply` — is there a ROUTE back to this specific conversation? Resolved
 *     per thread from local state (a stored Slack connection for the thread's
 *     workspace, a Discord channel id, an SMS sender). This used to be a
 *     channel-level answer to a per-thread question, so every Slack thread said
 *     yes and the operator discovered otherwise only after typing a reply that
 *     was stored and never sent (spec 2026-08-27-03).
 *  2. `replyWindow.open` — will the channel accept a free-form reply right now?
 *     Only WhatsApp's 24-hour window ever closes.
 *
 * The server's own words are preferred over anything we could phrase here: it
 * knows whether the workspace was never connected, the thread lost its channel
 * id, or the instance has no SMS at all — three different things to go and fix.
 * The translated strings are fallbacks for a server that sent no reason.
 */
export function supportReplyBlockReason(thread: SupportThread, t: TFunction): string {
  if (!thread.canReply) {
    return thread.canReplyReason || t("reply.disabledNoRoute");
  }

  if (!thread.replyWindow.open) {
    return thread.replyWindow.reason || t("window.expired");
  }

  return "";
}

/**
 * The heading above a blocked reply box.
 *
 * A lapsed window and a missing route are not the same problem and must not
 * share a title: "Reply window closed" in front of an unconnected Slack
 * workspace would send an operator looking for a timer that does not exist.
 */
export function supportReplyBlockTitle(thread: SupportThread, t: TFunction): string {
  return thread.canReply ? t("window.expired") : t("window.cannotReply");
}
