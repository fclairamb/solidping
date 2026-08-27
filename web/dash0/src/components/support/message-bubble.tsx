import { Loader2, RotateCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { SupportMessage } from "@/api/support";
import { Button } from "@/components/ui/button";
import { TimeAgo } from "@/components/ui/time-ago";
import { cn } from "@/lib/utils";

/**
 * One message in a support thread, styled as a chat bubble: inbound on the
 * left, our own replies on the right, so the thread reads the way the
 * WhatsApp/Telegram/SMS conversation the person is actually sitting in does.
 *
 * The body is rendered as TEXT, never as markup — these bodies arrive from
 * publicly reachable phone numbers and are attacker-influenced by definition.
 *
 * A failed outbound reply gets a Resend action: without one the operator's own
 * words are visibly stored and permanently unsent, which is the state spec
 * 2026-08-27-03 refuses to leave as a resting place. The server re-runs the
 * routing pre-flight before retrying, so a reply queued against a workspace
 * that has since been connected simply goes.
 */
export function SupportMessageBubble({
  message,
  onResend,
  resending = false,
}: {
  message: SupportMessage;
  onResend?: (messageUid: string) => void;
  resending?: boolean;
}) {
  const { t } = useTranslation("support");
  const outbound = message.direction === "outbound";
  const failed = (message.delivery?.status as string | undefined) === "failed";

  return (
    <div className={cn("flex", outbound ? "justify-end" : "justify-start")}>
      <div
        className={cn(
          "max-w-[85%] rounded-lg px-3 py-2 text-sm",
          outbound ? "bg-primary text-primary-foreground" : "bg-muted",
        )}
        data-testid={outbound ? "support-message-outbound" : "support-message-inbound"}
      >
        <div className="mb-1 flex items-center gap-2 text-xs opacity-80">
          <span>{outbound ? t("thread.outbound") : t("thread.inbound")}</span>
          <TimeAgo date={message.createdAt} />
        </div>
        {/*
          NEVER dangerouslySetInnerHTML here. These bodies arrive from publicly
          reachable phone numbers and are attacker-influenced by definition;
          React escapes text children, and that is the whole protection.
        */}
        <p className="whitespace-pre-wrap break-words">{message.body}</p>
        {message.truncated ? (
          <p className="mt-1 text-xs italic opacity-80">{t("thread.truncated")}</p>
        ) : null}
        {failed ? (
          <div className="mt-1 space-y-1">
            <p className="text-xs font-medium text-destructive">
              {t("thread.deliveryFailed")}
            </p>
            {onResend ? (
              <Button
                size="sm"
                variant="secondary"
                disabled={resending}
                onClick={() => onResend(message.uid)}
                data-testid="support-message-resend"
              >
                {resending ? (
                  <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
                ) : (
                  <RotateCw className="h-4 w-4 sm:mr-2" />
                )}
                <span className="hidden sm:inline">
                  {resending ? t("reply.resending") : t("reply.resend")}
                </span>
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>
    </div>
  );
}
