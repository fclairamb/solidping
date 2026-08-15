import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { z } from "zod";
import {
  ArrowLeft,
  Ban,
  CheckCircle2,
  Clock,
  Timer,
  XCircle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
import {
  CollapsibleCode,
  CopyableInline,
} from "@/components/shared/copyable-code";
import {
  useOrgNotification,
  type IncidentNotification,
} from "@/api/hooks";
import {
  notificationStatusVariant,
  sourceLabel,
} from "@/lib/notifications";
import { useTranslation } from "react-i18next";
import { channelTypeLabel, failureReasonLabel } from "@/lib/channel-labels";
import { EventTypeBadge } from "@/components/dashboard/event-display";

export const Route = createFileRoute(
  "/orgs/$org/notifications/$notificationUid",
)({
  validateSearch: z.object({
    from: z.string().optional(),
  }),
  component: NotificationDetailPage,
});

/** Parses the `?from=` search param. Returns `{ type, uid }` or `null`. */
function parseFrom(
  from: string | undefined,
): { type: "incident" | "integration"; uid: string } | null {
  if (!from) return null;
  const colonIdx = from.indexOf(":");
  if (colonIdx === -1) return null;
  const type = from.slice(0, colonIdx);
  const uid = from.slice(colonIdx + 1);
  if (!uid) return null;
  if (type === "incident" || type === "integration") {
    return { type, uid };
  }
  return null;
}

/** Renders an ISO timestamp in the user's locale, or "—" when absent. */
function formatDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

/**
 * Human-readable elapsed time between two ISO timestamps (e.g. "350ms",
 * "1.2s", "2m 5s"). Returns null when either timestamp is absent, invalid,
 * or the interval is negative.
 */
function formatElapsed(fromIso?: string, toIso?: string): string | null {
  if (!fromIso || !toIso) return null;
  const from = new Date(fromIso).getTime();
  const to = new Date(toIso).getTime();
  if (Number.isNaN(from) || Number.isNaN(to)) return null;
  const ms = to - from;
  if (ms < 0) return null;
  if (ms < 1000) return `${ms}ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)}s`;
  const totalSeconds = Math.floor(seconds);
  const minutes = Math.floor(totalSeconds / 60);
  const hours = Math.floor(minutes / 60);
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  return `${minutes}m ${totalSeconds % 60}s`;
}

/** A single timeline entry. Returns null when the timestamp is absent. */
function TimelineRow({
  icon,
  label,
  iso,
  tone,
  delta,
}: {
  icon: React.ReactNode;
  label: string;
  iso?: string;
  tone?: string;
  delta?: string | null;
}) {
  if (!iso) return null;
  return (
    <div className="flex items-center gap-3 text-sm">
      <span className={tone}>{icon}</span>
      <span className="text-muted-foreground w-24 shrink-0">{label}</span>
      <code className="font-mono">{formatDate(iso)}</code>
      {delta && (
        <span className="text-xs text-muted-foreground">+{delta}</span>
      )}
    </div>
  );
}

function TargetSection({
  org,
  notif,
}: {
  org: string;
  notif: IncidentNotification;
}) {
  if (notif.user) {
    return (
      <Link
        to="/orgs/$org/organization/members"
        params={{ org }}
        className="font-medium text-primary hover:underline"
      >
        {notif.user.name || notif.user.uid}
      </Link>
    );
  }

  if (notif.connection) {
    return (
      <span className="flex flex-wrap items-center gap-1.5">
        <span className="text-muted-foreground text-xs capitalize">
          {notif.connection.type}
        </span>
        <Link
          to="/orgs/$org/integrations/$integrationUid"
          params={{ org, integrationUid: notif.connection.uid }}
          className="font-medium text-primary hover:underline"
        >
          {notif.connection.name}
        </Link>
      </span>
    );
  }

  return <span className="text-muted-foreground">—</span>;
}

/** Maps an HTTP status code to a Badge variant. */
function statusCodeVariant(
  code: number,
): "default" | "secondary" | "destructive" | "outline" {
  if (code >= 200 && code < 300) return "default";
  if (code >= 400) return "destructive";
  return "secondary";
}

/** Delivery section: HTTP status badge, duration, request URL, bodies. */
function DeliverySection({ notif }: { notif: IncidentNotification }) {
  const d = notif.deliveryDetails;
  if (!d) return null;

  // Email is SMTP, not HTTP: the captured "response body" is the per-recipient
  // transcript of SMTP server replies, so we relabel and add the honest caveat
  // that acceptance by the relay is not proof of inbox delivery.
  const isEmail = notif.channelType === "email";

  const hasAny =
    d.httpStatusCode !== undefined ||
    d.durationMs !== undefined ||
    Boolean(d.requestUrl) ||
    Boolean(d.requestBody) ||
    Boolean(d.responseBody) ||
    (d.responseHeaders && Object.keys(d.responseHeaders).length > 0);

  if (!hasAny) return null;

  const headerEntries = d.responseHeaders
    ? Object.entries(d.responseHeaders)
    : [];

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Delivery</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          {d.httpStatusCode !== undefined && d.httpStatusCode > 0 && (
            <span className="flex items-center gap-2">
              <span className="text-muted-foreground">Status:</span>
              <Badge variant={statusCodeVariant(d.httpStatusCode)}>
                {d.httpStatusCode}
              </Badge>
            </span>
          )}
          {d.durationMs !== undefined && (
            <span className="flex items-center gap-1.5 text-muted-foreground">
              <Timer className="h-4 w-4" />
              <span>{d.durationMs} ms</span>
            </span>
          )}
        </div>

        {d.requestUrl && (
          <div className="space-y-1">
            <div className="text-muted-foreground text-xs">Request URL</div>
            <CopyableInline value={d.requestUrl} label="request URL" />
          </div>
        )}

        {d.requestBody && (
          <CollapsibleCode label="Request payload" value={d.requestBody} />
        )}

        {d.responseBody && (
          <CollapsibleCode
            label={isEmail ? "SMTP server response" : "Response body"}
            value={d.responseBody}
            defaultOpen={notif.status === "failed"}
          />
        )}

        {headerEntries.length > 0 && (
          <div className="space-y-1">
            <div className="text-muted-foreground text-xs">Response headers</div>
            <div className="space-y-1">
              {headerEntries.map(([name, val]) => (
                <div key={name} className="flex flex-wrap gap-x-2 font-mono text-xs">
                  <span className="text-muted-foreground">{name}:</span>
                  <span className="break-all">{val}</span>
                </div>
              ))}
            </div>
          </div>
        )}

        {isEmail && (
          <p className="text-muted-foreground text-xs">
            “Sent” means the mail server accepted the message for relay — it does
            not confirm the message reached the recipient&apos;s inbox.
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function NotificationDetailPage() {
  const { t } = useTranslation("common");
  const { t: tEvents } = useTranslation("events");
  const navigate = useNavigate();
  const { org, notificationUid } = Route.useParams();
  const { from } = Route.useSearch();
  const { data, isLoading, error, refetch } = useOrgNotification(
    org,
    notificationUid,
  );

  const fromParsed = parseFrom(from);

  const goBack = () => {
    if (fromParsed?.type === "incident") {
      void navigate({
        to: "/orgs/$org/incidents/$incidentUid",
        params: { org, incidentUid: fromParsed.uid },
      });
    } else if (fromParsed?.type === "integration") {
      void navigate({
        to: "/orgs/$org/integrations/$integrationUid",
        params: { org, integrationUid: fromParsed.uid },
      });
    } else {
      void navigate({
        to: "/orgs/$org/incidents",
        params: { org },
        search: { state: "all" as const, showSuppressed: undefined },
      });
    }
  };

  const backLabel = fromParsed?.type === "incident"
    ? "Back to incident"
    : fromParsed?.type === "integration"
      ? "Back to integration"
      : "Back to incidents";

  const errorBackTo = fromParsed?.type === "incident"
    ? `/orgs/${org}/incidents/${fromParsed.uid}` as const
    : `/orgs/${org}/incidents` as const;

  if (isLoading) {
    return (
      <div className="space-y-6 max-w-3xl">
        <div className="flex items-center gap-4">
          <Skeleton className="h-10 w-10 rounded" />
          <Skeleton className="h-8 w-64" />
        </div>
        <Skeleton className="h-32 rounded-lg" />
        <Skeleton className="h-48 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource="Notification"
        backTo={errorBackTo}
        backLabel={backLabel}
        onRetry={() => void refetch()}
      />
    );
  }

  if (!data) return null;

  const hasIdentifiers = Boolean(data.messageId || data.jobUid);

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="ghost" size="icon" onClick={goBack} aria-label={backLabel}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-2xl font-semibold">Notification</h1>
        <Badge
          variant={notificationStatusVariant(data.status)}
          className="capitalize"
        >
          {data.status}
        </Badge>
        {data.channelType && data.channelType !== "none" && (
          <Badge variant="outline">{channelTypeLabel(t, data.channelType)}</Badge>
        )}
        <EventTypeBadge eventType={data.eventType} t={tEvents} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Delivery timeline</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          <TimelineRow
            icon={<Clock className="h-4 w-4" />}
            label="Created"
            iso={data.createdAt}
            tone="text-muted-foreground"
          />
          <TimelineRow
            icon={<CheckCircle2 className="h-4 w-4" />}
            label="Sent"
            iso={data.sentAt}
            tone="text-green-600 dark:text-green-500"
            delta={formatElapsed(data.createdAt, data.sentAt)}
          />
          <TimelineRow
            icon={<XCircle className="h-4 w-4" />}
            label="Failed"
            iso={data.failedAt}
            tone="text-destructive"
          />
          <TimelineRow
            icon={<Ban className="h-4 w-4" />}
            label="Cancelled"
            iso={data.cancelledAt}
            tone="text-muted-foreground"
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Target</CardTitle>
        </CardHeader>
        <CardContent className="text-sm">
          <TargetSection org={org} notif={data} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Escalation context</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2 text-sm">
          <div className="flex flex-wrap gap-x-2">
            <span className="text-muted-foreground">Source:</span>
            <span>{sourceLabel(data.source, data.repeatIndex)}</span>
          </div>
          {data.stepUid && (
            <div className="flex flex-wrap items-center gap-x-2">
              <span className="text-muted-foreground">Step:</span>
              <code className="font-mono text-xs break-all">{data.stepUid}</code>
            </div>
          )}
          {data.repeatIndex !== undefined && (
            <div className="flex flex-wrap gap-x-2">
              <span className="text-muted-foreground">Escalation cycle:</span>
              <span>{data.repeatIndex + 1}</span>
            </div>
          )}
          {data.skipReason && (
            <div className="flex flex-wrap gap-x-2">
              <span className="text-muted-foreground">Skip reason:</span>
              <span>{failureReasonLabel(t, data.skipReason)}</span>
            </div>
          )}
        </CardContent>
      </Card>

      <DeliverySection notif={data} />

      {hasIdentifiers && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Identifiers</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            {data.messageId && (
              <div className="space-y-1">
                <div className="text-muted-foreground text-xs">Message ID</div>
                <CopyableInline value={data.messageId} label="message ID" />
              </div>
            )}
            {data.jobUid && (
              <div className="space-y-1">
                <div className="text-muted-foreground text-xs">Job UID</div>
                <CopyableInline value={data.jobUid} label="job UID" />
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {data.error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base text-destructive">Error</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex items-start gap-2">
              <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-words rounded-md bg-muted p-3 font-mono text-xs text-destructive">
                {data.error}
              </pre>
              <CopyableInline
                value={data.error}
                label="error"
                inline={false}
                size="md"
              />
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
