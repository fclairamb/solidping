import { useMemo, useState } from "react";

import { Link, createFileRoute } from "@tanstack/react-router";
import { Inbox, MessageSquare, Search } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  SUPPORT_CHANNEL_LABELS,
  type SupportChannel,
  type SupportStatus,
  type SupportThread,
  useSupportThreads,
} from "@/api/support";
import { SupportGate } from "@/components/support/support-gate";
import { PageHeader } from "@/components/shared/page-header";
import { QueryErrorView } from "@/components/shared/error-views";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { TimeAgo } from "@/components/ui/time-ago";
import { useAuth } from "@/contexts/AuthContext";

/**
 * The instance support inbox (spec 2026-08-22-02).
 *
 * Deliberately UNLINKED: there is no navigation entry anywhere, so an
 * operator-only tool stays out of every customer's sidebar without needing a
 * separate admin app. That is discoverability only — SupportGate renders
 * Permission Denied for anyone else and the API is SuperAdmin on every
 * endpoint.
 */
export const Route = createFileRoute("/support/")({
  component: SupportInboxPage,
});

function SupportInboxPage() {
  return (
    <SupportGate>
      <div className="mx-auto w-full max-w-5xl space-y-6 p-4 sm:p-6">
        <SupportInbox />
      </div>
    </SupportGate>
  );
}

function SupportInbox() {
  const { t } = useTranslation("support");
  const [search, setSearch] = useState("");
  const [channel, setChannel] = useState<SupportChannel | "all">("all");
  const [status, setStatus] = useState<SupportStatus | "all">("all");

  const { data, isLoading, error, refetch } = useSupportThreads({
    channel: channel === "all" ? "" : channel,
    status: status === "all" ? "" : status,
    q: search.trim(),
  });

  const groups = useMemo(() => splitThreads(data ?? []), [data]);
  const { org } = useAuth();

  return (
    <>
      <PageHeader
        icon={Inbox}
        title={t("title")}
        description={t("subtitle")}
        docsHref="/docs/features/support-inbox"
      />

      <div className="flex flex-col gap-2 sm:flex-row">
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            className="pl-8"
            placeholder={t("search")}
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            data-testid="support-search"
          />
        </div>
        <Select
          value={channel}
          onValueChange={(value) => setChannel(value as SupportChannel | "all")}
        >
          <SelectTrigger className="sm:w-44" data-testid="support-channel-filter">
            <SelectValue placeholder={t("allChannels")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("allChannels")}</SelectItem>
            {(Object.keys(SUPPORT_CHANNEL_LABELS) as SupportChannel[]).map((key) => (
              <SelectItem key={key} value={key}>
                {SUPPORT_CHANNEL_LABELS[key]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={status}
          onValueChange={(value) => setStatus(value as SupportStatus | "all")}
        >
          <SelectTrigger className="sm:w-40" data-testid="support-status-filter">
            <SelectValue placeholder={t("allStatuses")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("allStatuses")}</SelectItem>
            <SelectItem value="open">{t("status.open")}</SelectItem>
            <SelectItem value="pending">{t("status.pending")}</SelectItem>
            <SelectItem value="closed">{t("status.closed")}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {error ? (
        <QueryErrorView
          error={error}
          org={org ?? "default"}
          onRetry={() => void refetch()}
        />
      ) : isLoading ? (
        <div className="space-y-2">
          {[0, 1, 2, 3].map((key) => (
            <Skeleton key={key} className="h-16 rounded-lg" />
          ))}
        </div>
      ) : (data ?? []).length === 0 ? (
        <Card>
          <CardContent className="py-10 text-center text-muted-foreground">
            {search || channel !== "all" || status !== "all"
              ? t("emptyFiltered")
              : t("empty")}
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-6" data-testid="support-thread-list">
          <ThreadSection
            title={t("sections.active")}
            help={t("sections.activeHelp")}
            testId="support-section-active"
            threads={groups.active}
          />
          <ThreadSection
            title={t("sections.expired")}
            help={t("sections.expiredHelp")}
            testId="support-section-expired"
            threads={groups.expired}
          />
          <ThreadSection
            title={t("sections.closed")}
            help={t("sections.closedHelp")}
            testId="support-section-closed"
            threads={groups.closed}
          />
        </div>
      )}
    </>
  );
}

/**
 * "Active / expired" is the REPLY WINDOW, not the thread status — two
 * independent axes, and conflating them produces a confusing UI. A thread can be
 * open (the customer's question is unanswered) and expired (WhatsApp will no
 * longer accept a free-form reply); that combination is precisely the state an
 * operator most needs to see, so it gets its own section rather than being
 * mixed in with the answerable ones.
 */
function splitThreads(threads: SupportThread[]) {
  const active: SupportThread[] = [];
  const expired: SupportThread[] = [];
  const closed: SupportThread[] = [];

  for (const thread of threads) {
    if (thread.status === "closed") {
      closed.push(thread);
    } else if (thread.replyWindow.open) {
      active.push(thread);
    } else {
      expired.push(thread);
    }
  }

  return { active, expired, closed };
}

function ThreadSection({
  title,
  help,
  threads,
  testId,
}: {
  title: string;
  help: string;
  threads: SupportThread[];
  testId: string;
}) {
  if (threads.length === 0) {
    return null;
  }

  return (
    <section className="space-y-2" data-testid={testId}>
      <div>
        <h2 className="text-sm font-semibold tracking-tight">
          {title}
          <Badge variant="secondary" className="ml-2">
            {threads.length}
          </Badge>
        </h2>
        <p className="text-xs text-muted-foreground">{help}</p>
      </div>
      <div className="space-y-2">
        {threads.map((thread) => (
          <ThreadRow key={thread.uid} thread={thread} />
        ))}
      </div>
    </section>
  );
}

function ThreadRow({ thread }: { thread: SupportThread }) {
  const { t } = useTranslation("support");

  return (
    <Link
      to="/support/$threadUid"
      params={{ threadUid: thread.uid }}
      className="block rounded-lg border bg-card p-3 transition-colors hover:bg-accent/40"
      data-testid="support-thread-row"
    >
      <div className="flex flex-wrap items-center gap-2">
        <MessageSquare className="h-4 w-4 shrink-0 text-muted-foreground" />
        <Badge variant="outline">{SUPPORT_CHANNEL_LABELS[thread.channel]}</Badge>
        <span className="font-mono text-xs text-muted-foreground">
          {thread.channelIdentity}
        </span>
        <Badge variant="secondary">{t(`status.${thread.status}`)}</Badge>
        {thread.unreadCount > 0 ? (
          <Badge data-testid="support-unread">
            {t("thread.unread", { count: thread.unreadCount })}
          </Badge>
        ) : null}
        <span className="ml-auto text-xs text-muted-foreground">
          <TimeAgo date={thread.lastMessageAt} />
        </span>
      </div>
      {/* Bodies are attacker-influenced: rendered as text, never as markup. */}
      <p className="mt-1 truncate text-sm">{thread.subject}</p>
    </Link>
  );
}
