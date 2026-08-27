import { useState } from "react";

import { Link, createFileRoute } from "@tanstack/react-router";
import { ArrowLeft, Ban, CircleDollarSign, Loader2, Send } from "lucide-react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  SUPPORT_CHANNEL_LABELS,
  type SupportMessage,
  type SupportThread,
  useResendSupportReply,
  useSendSupportReply,
  useSupportMessages,
  useSupportThread,
  useUpdateSupportThread,
} from "@/api/support";
import { ApiError } from "@/api/client";
import { SupportGate } from "@/components/support/support-gate";
import { SupportMessageBubble } from "@/components/support/message-bubble";
import { QueryErrorView } from "@/components/shared/error-views";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { useAuth } from "@/contexts/AuthContext";
import {
  supportReplyBlockReason,
  supportReplyBlockTitle,
} from "@/lib/support-reply-block";

export const Route = createFileRoute("/support/$threadUid")({
  component: SupportThreadPage,
});

function SupportThreadPage() {
  return (
    <SupportGate>
      <div className="mx-auto w-full max-w-3xl space-y-4 p-4 sm:p-6">
        <SupportThreadView />
      </div>
    </SupportGate>
  );
}

function SupportThreadView() {
  const { t } = useTranslation("support");
  const { threadUid } = Route.useParams();
  const { org } = useAuth();

  const thread = useSupportThread(threadUid);
  const messages = useSupportMessages(threadUid);

  if (thread.error) {
    return (
      <QueryErrorView
        error={thread.error}
        org={org ?? "default"}
        onRetry={() => void thread.refetch()}
      />
    );
  }

  if (thread.isLoading || !thread.data) {
    return <Skeleton className="h-64 rounded-lg" />;
  }

  return (
    <>
      <Link
        to="/support"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        data-testid="support-back"
      >
        <ArrowLeft className="h-4 w-4" />
        {t("backToInbox")}
      </Link>

      <ThreadHeader thread={thread.data} />

      <MessageList threadUid={threadUid} loading={messages.isLoading} messages={messages.data ?? []} />

      <ReplyBox thread={thread.data} />
    </>
  );
}

/**
 * The conversation, plus the way out of a reply that never left.
 *
 * A failed outbound message carries a Resend action: the server re-runs the
 * routing pre-flight and the send on that same row, so a reply queued while a
 * Slack workspace was unconnected goes out once it is connected instead of
 * staying stored-and-unsent forever.
 */
function MessageList({
  threadUid,
  loading,
  messages,
}: {
  threadUid: string;
  loading: boolean;
  messages: SupportMessage[];
}) {
  const { t } = useTranslation("support");
  const resend = useResendSupportReply(threadUid);
  const [resendingUid, setResendingUid] = useState<string | null>(null);

  const onResend = (messageUid: string) => {
    setResendingUid(messageUid);
    resend.mutate(messageUid, {
      onSuccess: (message) => {
        setResendingUid(null);

        // A 202 means the provider was tried and refused again — the row is
        // updated, but claiming success would be a lie.
        if ((message.delivery?.status as string | undefined) === "failed") {
          toast.error(t("reply.resendFailed"));

          return;
        }

        toast.success(t("reply.resent"));
      },
      onError: (error) => {
        setResendingUid(null);

        const detail =
          error instanceof ApiError ? error.detail || error.message : String(error);
        toast.error(`${t("reply.resendFailed")}: ${detail}`);
      },
    });
  };

  return (
    <Card>
      <CardContent className="space-y-3 py-4" data-testid="support-messages">
        {loading ? (
          <Skeleton className="h-24 rounded-lg" />
        ) : messages.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("thread.noMessages")}</p>
        ) : (
          messages.map((message) => (
            <SupportMessageBubble
              key={message.uid}
              message={message}
              onResend={onResend}
              resending={resendingUid === message.uid}
            />
          ))
        )}
      </CardContent>
    </Card>
  );
}

function ThreadHeader({ thread }: { thread: SupportThread }) {
  const { t } = useTranslation("support");
  const update = useUpdateSupportThread(thread.uid);

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline">{SUPPORT_CHANNEL_LABELS[thread.channel]}</Badge>
        <span className="font-mono text-sm">{thread.channelIdentity}</span>
        <Badge variant="secondary" data-testid="support-thread-status">
          {t(`status.${thread.status}`)}
        </Badge>
        <div className="ml-auto flex gap-2">
          {thread.status === "closed" ? (
            <Button
              size="sm"
              variant="outline"
              disabled={update.isPending}
              onClick={() => update.mutate({ status: "open" })}
              data-testid="support-reopen"
            >
              {t("actions.reopen")}
            </Button>
          ) : (
            <Button
              size="sm"
              variant="outline"
              disabled={update.isPending}
              onClick={() => update.mutate({ status: "closed" })}
              data-testid="support-close"
            >
              {t("actions.close")}
            </Button>
          )}
        </div>
      </div>

      <p className="text-sm text-muted-foreground">
        {thread.organizationUid ? (
          <>
            {t("thread.attribution")}: <span className="font-mono">{thread.organizationUid}</span>{" "}
            <span className="text-xs">({t("thread.attributionHelp")})</span>
          </>
        ) : (
          t("thread.noAttribution")
        )}
      </p>
    </div>
  );
}

/**
 * The reply box, and the one piece of UI the spec cares most about.
 *
 * When the thread cannot be answered the box is DISABLED WITH THE REASON SHOWN,
 * rather than left enabled to fail at send time. Both axes — is there a route
 * back to this specific conversation, and is the channel's free-form window
 * open — are resolved server-side per thread, so this cannot drift from what
 * the API will actually accept. The API enforces the same two checks itself:
 * this is a courtesy, not the gate.
 */
function ReplyBox({ thread }: { thread: SupportThread }) {
  const { t } = useTranslation("support");
  const [body, setBody] = useState("");
  const send = useSendSupportReply(thread.uid);

  const blockedReason = supportReplyBlockReason(thread, t);

  if (blockedReason) {
    return (
      <Alert variant="destructive" data-testid="support-reply-blocked">
        <Ban className="h-4 w-4" />
        <AlertTitle>{supportReplyBlockTitle(thread, t)}</AlertTitle>
        <AlertDescription>{blockedReason}</AlertDescription>
      </Alert>
    );
  }

  const submit = () => {
    const text = body.trim();
    if (!text) return;

    send.mutate(text, {
      onSuccess: () => {
        setBody("");
        toast.success(t("reply.sent"));
      },
      onError: (error) => {
        const detail =
          error instanceof ApiError ? error.detail || error.message : String(error);
        toast.error(`${t("reply.failed")}: ${detail}`);
      },
    });
  };

  return (
    <div className="space-y-2" data-testid="support-reply-box">
      {thread.replyWindow.costsMoney ? (
        <Alert>
          <CircleDollarSign className="h-4 w-4" />
          <AlertTitle>{t("window.costsMoney")}</AlertTitle>
          <AlertDescription>{t("reply.costWarning")}</AlertDescription>
        </Alert>
      ) : null}
      {thread.replyWindow.expires && thread.replyWindow.expiresAt ? (
        <p className="text-xs text-muted-foreground" data-testid="support-window-expiry">
          {t("window.expiresAt", {
            time: new Date(thread.replyWindow.expiresAt).toLocaleString(),
          })}
        </p>
      ) : null}
      <Textarea
        rows={3}
        value={body}
        placeholder={t("reply.placeholder")}
        onChange={(event) => setBody(event.target.value)}
        data-testid="support-reply-input"
      />
      <div className="flex justify-end">
        <Button
          onClick={submit}
          disabled={send.isPending || body.trim().length === 0}
          data-testid="support-reply-send"
        >
          {send.isPending ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <Send className="mr-2 h-4 w-4" />
          )}
          {send.isPending ? t("reply.sending") : t("reply.send")}
        </Button>
      </div>
    </div>
  );
}
