import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
  ArrowLeft,
  Check,
  Copy,
  Ban,
  CheckCircle2,
  Clock,
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
  useIncidentNotification,
  type IncidentNotification,
} from "@/api/hooks";
import {
  notificationStatusVariant,
  sourceLabel,
} from "@/lib/notifications";

export const Route = createFileRoute(
  "/orgs/$org/incidents/$incidentUid/notifications/$notificationUid",
)({
  component: NotificationDetailPage,
});

/** Renders an ISO timestamp in the user's locale, or "—" when absent. Never
 * shows "Invalid Date" or an epoch for a missing value. */
function formatDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

/** Inline copyable value: monospace text plus a copy-to-clipboard button.
 * Mirrors the design-reference CodeSnippet copy affordance. */
function CopyableValue({ value, label }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard requires a secure context; the value stays visible to copy by hand.
    }
  };

  return (
    <div className="flex items-start gap-2">
      <code className="min-w-0 break-all font-mono text-sm">{value}</code>
      <button
        type="button"
        onClick={onCopy}
        aria-label={copied ? "Copied" : `Copy ${label ?? "value"}`}
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
      >
        {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
      </button>
    </div>
  );
}

/** A single timeline entry: an icon, a label and a formatted timestamp.
 * Returns null when the timestamp is absent so unfired stages stay hidden. */
function TimelineRow({
  icon,
  label,
  iso,
  tone,
}: {
  icon: React.ReactNode;
  label: string;
  iso?: string;
  tone?: string;
}) {
  if (!iso) return null;
  return (
    <div className="flex items-center gap-3 text-sm">
      <span className={tone}>{icon}</span>
      <span className="text-muted-foreground w-24 shrink-0">{label}</span>
      <code className="font-mono">{formatDate(iso)}</code>
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

function NotificationDetailPage() {
  const navigate = useNavigate();
  const { org, incidentUid, notificationUid } = Route.useParams();
  const { data, isLoading, error, refetch } = useIncidentNotification(
    org,
    incidentUid,
    notificationUid,
  );

  const goBack = () =>
    navigate({
      to: "/orgs/$org/incidents/$incidentUid",
      params: { org, incidentUid },
    });

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
        backTo="/orgs/$org/incidents/$incidentUid"
        backLabel="Back to incident"
        onRetry={() => refetch()}
      />
    );
  }

  if (!data) return null;

  const hasIdentifiers = Boolean(data.messageId || data.jobUid);

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="ghost" size="icon" onClick={goBack} aria-label="Back to incident">
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
          <Badge variant="outline" className="capitalize">
            {data.channelType}
          </Badge>
        )}
        <code className="font-mono text-sm text-muted-foreground">
          {data.eventType}
        </code>
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
              <span>{data.skipReason}</span>
            </div>
          )}
        </CardContent>
      </Card>

      {hasIdentifiers && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Identifiers</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            {data.messageId && (
              <div className="space-y-1">
                <div className="text-muted-foreground text-xs">Message ID</div>
                <CopyableValue value={data.messageId} label="message ID" />
              </div>
            )}
            {data.jobUid && (
              <div className="space-y-1">
                <div className="text-muted-foreground text-xs">Job UID</div>
                <CopyableValue value={data.jobUid} label="job UID" />
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
              <CopyErrorButton value={data.error} />
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function CopyErrorButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard requires a secure context; the error stays visible to copy by hand.
    }
  };

  return (
    <button
      type="button"
      onClick={onCopy}
      aria-label={copied ? "Copied" : "Copy error"}
      className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
    >
      {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
    </button>
  );
}
