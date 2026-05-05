import { useState, useEffect } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  AlertTriangle,
  ArrowLeft,
  BellOff,
  CheckCircle,
  Clock,
  ExternalLink,
  Loader2,
  RefreshCw,
  RotateCcw,
} from "lucide-react";
import { toast } from "sonner";
import {
  useIncident,
  useIncidents,
  useAcknowledgeIncident,
  useUnacknowledgeIncident,
  useSnoozeIncident,
  useUnsnoozeIncident,
  useResolveIncident,
  useEvents,
  type IncidentDetail,
} from "@/api/hooks";
import { SnoozeDialog } from "@/components/incidents/snooze-dialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Trans } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { QueryErrorView } from "@/components/shared/error-views";

export const Route = createFileRoute("/orgs/$org/incidents/$incidentUid")({
  component: IncidentDetailPage,
});

function formatDuration(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (days > 0) return `${days}d ${hours % 24}h`;
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  if (minutes > 0) return `${minutes}m ${seconds % 60}s`;
  return `${seconds}s`;
}

function TotalDuration({
  startedAt,
  resolvedAt,
}: {
  startedAt?: string;
  resolvedAt?: string;
}) {
  const { t } = useTranslation("incidents");
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (startedAt && !resolvedAt) {
      const interval = setInterval(() => setNow(Date.now()), 1000);
      return () => clearInterval(interval);
    }
  }, [startedAt, resolvedAt]);

  if (!startedAt) return "-";

  if (resolvedAt) {
    return formatDuration(
      new Date(resolvedAt).getTime() - new Date(startedAt).getTime()
    );
  }
  return formatDuration(now - new Date(startedAt).getTime()) + " " + t("detail.ongoing");
}

function TimelineItem({
  label,
  timestamp,
  icon,
}: {
  label: string;
  timestamp?: string;
  icon: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-3">
      {icon}
      <div className="flex-1">
        <div className="font-medium">{label}</div>
        <div className="text-sm text-muted-foreground">
          {timestamp ? new Date(timestamp).toLocaleString() : "-"}
        </div>
      </div>
    </div>
  );
}

function IncidentDetailPage() {
  const { t } = useTranslation("incidents");
  const { org, incidentUid } = Route.useParams();
  const navigate = useNavigate();

  const {
    data: incident,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useIncident(org, incidentUid);

  const { data: events } = useEvents(org, { incidentUid, size: 20 });

  const acknowledgeIncident = useAcknowledgeIncident(org);
  const unacknowledgeIncident = useUnacknowledgeIncident(org);
  const snoozeIncident = useSnoozeIncident(org);
  const unsnoozeIncident = useUnsnoozeIncident(org);
  const resolveIncident = useResolveIncident(org);
  const [snoozeOpen, setSnoozeOpen] = useState(false);

  const handleAcknowledge = async () => {
    try {
      await acknowledgeIncident.mutateAsync({ uid: incidentUid });
      toast.success(t("actions.acknowledged"));
      refetch();
    } catch {
      toast.error(t("actions.acknowledgeFailed"));
    }
  };

  const handleUnacknowledge = async () => {
    try {
      await unacknowledgeIncident.mutateAsync({ uid: incidentUid });
      toast.success(t("actions.unacknowledged"));
      refetch();
    } catch {
      toast.error(t("actions.unacknowledgeFailed"));
    }
  };

  const handleSnooze = async (
    payload: { duration?: string; until?: string; reason?: string },
  ) => {
    try {
      await snoozeIncident.mutateAsync({ uid: incidentUid, body: payload });
      toast.success(t("actions.snoozed"));
      setSnoozeOpen(false);
      refetch();
    } catch {
      toast.error(t("actions.snoozeFailed"));
    }
  };

  const handleUnsnooze = async () => {
    try {
      await unsnoozeIncident.mutateAsync({ uid: incidentUid });
      toast.success(t("actions.unsnoozed"));
      refetch();
    } catch {
      toast.error(t("actions.unsnoozeFailed"));
    }
  };

  const handleResolve = async () => {
    try {
      await resolveIncident.mutateAsync({ uid: incidentUid });
      toast.success(t("actions.resolved"));
      refetch();
    } catch {
      toast.error(t("actions.resolveFailed"));
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-4">
          <Skeleton className="h-10 w-10 rounded" />
          <Skeleton className="h-8 w-48" />
        </div>
        <Skeleton className="h-48 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("fallbackTitle")}
        backTo="/orgs/$org/incidents"
        backLabel={t("backToIncidents")}
        onRetry={() => refetch()}
      />
    );
  }

  if (!incident) {
    return (
      <div className="text-center py-12">
        <p className="text-muted-foreground mb-4">{t("incidentNotFound")}</p>
        <Link to="/orgs/$org/incidents" params={{ org }} search={{ state: "all" as const, showSuppressed: undefined }}>
          <Button variant="outline">{t("backToIncidents")}</Button>
        </Link>
      </div>
    );
  }

  const isActive = incident.state === "active";
  const isSnoozed =
    !!incident.snoozedUntil && new Date(incident.snoozedUntil).getTime() > Date.now();
  const relapseCount = incident.relapseCount ?? 0;

  return (
    <div className="space-y-6">
      <CausedByBanner org={org} incident={incident} />
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <Button
            variant="ghost"
            size="icon"
            onClick={() =>
              navigate({ to: "/orgs/$org/incidents", params: { org }, search: { state: "all" as const, showSuppressed: undefined } })
            }
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <div className="flex items-center gap-3">
            {isActive ? (
              <AlertTriangle className="h-6 w-6 text-yellow-500" />
            ) : (
              <CheckCircle className="h-6 w-6 text-green-500" />
            )}
            <h1 className="text-3xl font-bold tracking-tight">
              {incident.title ||
                incident.checkName ||
                incident.checkSlug ||
                t("fallbackTitle")}
            </h1>
            <Badge variant={isActive ? "destructive" : "secondary"}>
              {isActive ? t("active") : t("resolved")}
            </Badge>
            {isActive && isSnoozed && incident.snoozedUntil && (
              <Badge variant="outline">
                {t("stateBadges.snoozedUntil", {
                  time: new Date(incident.snoozedUntil).toLocaleString(),
                })}
              </Badge>
            )}
            {isActive && !isSnoozed && incident.acknowledgedAt && (
              <Badge variant="outline">{t("stateBadges.acked")}</Badge>
            )}
            {relapseCount > 0 && (
              <Badge variant="outline">
                {t("reopenedTimes", {
                  count: relapseCount,
                  unit: relapseCount === 1 ? t("timeUnit.time") : t("timeUnit.times"),
                })}
              </Badge>
            )}
            {incident.escalatedAt && <Badge variant="outline">{t("escalated")}</Badge>}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="icon"
            onClick={() => refetch()}
            disabled={isRefetching}
          >
            <RefreshCw
              className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`}
            />
          </Button>
          {isActive && !incident.acknowledgedAt && !isSnoozed && (
            <Button
              variant="outline"
              onClick={handleAcknowledge}
              disabled={acknowledgeIncident.isPending}
            >
              {acknowledgeIncident.isPending ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : null}
              {t("actions.acknowledge")}
            </Button>
          )}
          {isActive && incident.acknowledgedAt && !isSnoozed && (
            <Button
              variant="outline"
              onClick={handleUnacknowledge}
              disabled={unacknowledgeIncident.isPending}
            >
              {unacknowledgeIncident.isPending ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : null}
              {t("actions.unacknowledge")}
            </Button>
          )}
          {isActive && !isSnoozed && (
            <Button
              variant="outline"
              onClick={() => setSnoozeOpen(true)}
              disabled={snoozeIncident.isPending}
            >
              {snoozeIncident.isPending ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <BellOff className="mr-2 h-4 w-4" />
              )}
              {t("actions.snooze")}
            </Button>
          )}
          {isActive && isSnoozed && (
            <Button
              variant="outline"
              onClick={handleUnsnooze}
              disabled={unsnoozeIncident.isPending}
            >
              {unsnoozeIncident.isPending ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : null}
              {t("actions.wakeUp")}
            </Button>
          )}
          {isActive && (
            <Button
              onClick={handleResolve}
              disabled={resolveIncident.isPending}
            >
              {resolveIncident.isPending ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <CheckCircle className="mr-2 h-4 w-4" />
              )}
              {t("actions.resolve")}
            </Button>
          )}
        </div>
      </div>

      <div className="grid gap-6 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>{t("detail.incidentDetails")}</CardTitle>
            <CardDescription>{t("detail.incidentDetailsDescription")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {incident.description && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  {t("detail.descriptionLabel")}
                </div>
                <div>{incident.description}</div>
              </div>
            )}
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                {t("detail.checkLabel")}
              </div>
              <Link
                to="/orgs/$org/checks/$checkUid"
                params={{ org, checkUid: incident.checkUid! }}
                search={{ graphPeriod: undefined, graphFull: undefined }}
                className="text-primary hover:underline inline-flex items-center gap-1"
              >
                {incident.checkName ||
                  incident.checkSlug ||
                  incident.checkUid}
                <ExternalLink className="h-3 w-3" />
              </Link>
            </div>
            {incident.check?.type && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  {t("detail.checkTypeLabel")}
                </div>
                <div className="capitalize">{incident.check.type}</div>
              </div>
            )}
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                {t("detail.failureCount")}
              </div>
              <div>{incident.failureCount ?? 0}</div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t("timeline.title")}</CardTitle>
            <CardDescription>{t("timeline.description")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-3">
              <TimelineItem
                label={t("timeline.started")}
                timestamp={incident.startedAt}
                icon={<AlertTriangle className="h-4 w-4 text-yellow-500" />}
              />
              {incident.acknowledgedAt && (
                <TimelineItem
                  label={t("timeline.acknowledged")}
                  timestamp={incident.acknowledgedAt}
                  icon={<Clock className="h-4 w-4 text-blue-400" />}
                />
              )}
              {incident.escalatedAt && (
                <TimelineItem
                  label={t("timeline.escalated")}
                  timestamp={incident.escalatedAt}
                  icon={<AlertTriangle className="h-4 w-4 text-red-500" />}
                />
              )}
              {incident.lastReopenedAt && (
                <TimelineItem
                  label={t("timeline.reopenedRelapse", { count: relapseCount })}
                  timestamp={incident.lastReopenedAt}
                  icon={<RotateCcw className="h-4 w-4 text-orange-500" />}
                />
              )}
              {incident.resolvedAt && (
                <TimelineItem
                  label={t("timeline.resolved")}
                  timestamp={incident.resolvedAt}
                  icon={<CheckCircle className="h-4 w-4 text-green-500" />}
                />
              )}
            </div>
            {incident.startedAt && (
              <div className="pt-4 border-t">
                <div className="text-sm font-medium text-muted-foreground">
                  {t("detail.totalDuration")}
                </div>
                <div className="text-lg font-semibold">
                  <TotalDuration
                    startedAt={incident.startedAt}
                    resolvedAt={incident.resolvedAt}
                  />
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <BlastRadiusCard org={org} incident={incident} />

      {events?.data && events.data.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>{t("eventLog.title")}</CardTitle>
            <CardDescription>{t("eventLog.description")}</CardDescription>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("eventLog.time")}</TableHead>
                  <TableHead>{t("eventLog.eventType")}</TableHead>
                  <TableHead>{t("eventLog.actor")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {events.data.map((event) => (
                  <TableRow key={event.uid}>
                    <TableCell className="text-sm">
                      {event.createdAt
                        ? new Date(event.createdAt).toLocaleString()
                        : "-"}
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline" className="text-xs">
                        {event.eventType}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-sm capitalize">
                      {event.actorType || "-"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      <SnoozeDialog
        open={snoozeOpen}
        onOpenChange={setSnoozeOpen}
        isPending={snoozeIncident.isPending}
        onSubmit={handleSnooze}
      />
    </div>
  );
}

function CausedByBanner({
  org,
  incident,
}: {
  org: string;
  incident: IncidentDetail;
}) {
  const { t } = useTranslation("incidents");
  const { data: parent } = useIncident(org, incident.causedByIncidentUid ?? "");

  if (!incident.causedByIncidentUid) return null;

  const parentName =
    parent?.checkName || parent?.checkSlug || t("rollup.parentLoading");

  if (incident.pagingSuppressed) {
    return (
      <Alert className="border-yellow-500/50 bg-yellow-500/10 text-yellow-900 dark:text-yellow-100">
        <AlertTriangle className="h-4 w-4" />
        <AlertDescription>
          <Trans
            t={t}
            i18nKey="rollup.causedByActive"
            values={{ parent: parentName }}
            components={{
              strong: (
                <Link
                  to="/orgs/$org/incidents/$incidentUid"
                  params={{ org, incidentUid: incident.causedByIncidentUid }}
                  className="font-semibold underline"
                />
              ),
            }}
          />
        </AlertDescription>
      </Alert>
    );
  }

  return (
    <Alert className="border-green-500/50 bg-green-500/10 text-green-900 dark:text-green-100">
      <CheckCircle className="h-4 w-4" />
      <AlertDescription>
        {t("rollup.causedByPast", {
          parent: parentName,
          resolvedAt: parent?.resolvedAt
            ? new Date(parent.resolvedAt).toLocaleString()
            : "",
        })}
      </AlertDescription>
    </Alert>
  );
}

function BlastRadiusCard({
  org,
  incident,
}: {
  org: string;
  incident: IncidentDetail;
}) {
  const { t } = useTranslation("incidents");
  const { data: children } = useIncidents(org, {
    causedByIncidentUid: incident.uid,
    size: 50,
    refetchInterval: incident.state === "active" ? 30_000 : undefined,
  });

  const items = children?.data ?? [];
  if (items.length === 0) return null;

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {t("rollup.blastRadiusTitle", { count: items.length })}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("detail.checkLabel")}</TableHead>
              <TableHead>{t("detail.state", { defaultValue: "State" })}</TableHead>
              <TableHead>{t("rollup.rolledUpBadge")}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((child) => (
              <TableRow key={child.uid}>
                <TableCell>
                  {child.checkName || child.checkSlug || child.checkUid}
                </TableCell>
                <TableCell>
                  <Badge variant={child.state === "active" ? "destructive" : "secondary"}>
                    {child.state === "active" ? t("active") : t("resolved")}
                  </Badge>
                </TableCell>
                <TableCell>
                  {child.pagingSuppressed && (
                    <Badge variant="outline">{t("rollup.rolledUpBadge")}</Badge>
                  )}
                </TableCell>
                <TableCell>
                  {child.uid && (
                    <Link
                      to="/orgs/$org/incidents/$incidentUid"
                      params={{ org, incidentUid: child.uid }}
                      className="text-primary hover:underline"
                    >
                      <ExternalLink className="h-3.5 w-3.5" />
                    </Link>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <p className="mt-3 text-xs text-muted-foreground">
          {t("rollup.blastRadiusFooter")}
        </p>
      </CardContent>
    </Card>
  );
}
