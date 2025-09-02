import { useState, useEffect } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
  AlertTriangle,
  ArrowLeft,
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
  useAcknowledgeIncident,
  useResolveIncident,
  useEvents,
} from "@/api/hooks";
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
  return formatDuration(now - new Date(startedAt).getTime()) + " (ongoing)";
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
  const resolveIncident = useResolveIncident(org);

  const handleAcknowledge = async () => {
    try {
      await acknowledgeIncident.mutateAsync(incidentUid);
      toast.success("Incident acknowledged");
      refetch();
    } catch {
      toast.error("Failed to acknowledge incident");
    }
  };

  const handleResolve = async () => {
    try {
      await resolveIncident.mutateAsync(incidentUid);
      toast.success("Incident resolved");
      refetch();
    } catch {
      toast.error("Failed to resolve incident");
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
        resource="Incident"
        backTo="/orgs/$org/incidents"
        backLabel="Back to Incidents"
        onRetry={() => refetch()}
      />
    );
  }

  if (!incident) {
    return (
      <div className="text-center py-12">
        <p className="text-muted-foreground mb-4">Incident not found</p>
        <Link to="/orgs/$org/incidents" params={{ org }} search={{ state: "all" as const }}>
          <Button variant="outline">Back to Incidents</Button>
        </Link>
      </div>
    );
  }

  const isActive = incident.state === "active";

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <Button
            variant="ghost"
            size="icon"
            onClick={() =>
              navigate({ to: "/orgs/$org/incidents", params: { org }, search: { state: "all" as const } })
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
                "Incident"}
            </h1>
            <Badge variant={isActive ? "destructive" : "secondary"}>
              {incident.state}
            </Badge>
            {(incident.relapseCount ?? 0) > 0 && (
              <Badge variant="outline">Reopened ({incident.relapseCount} {incident.relapseCount === 1 ? "time" : "times"})</Badge>
            )}
            {incident.escalatedAt && <Badge variant="outline">Escalated</Badge>}
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
          {isActive && !incident.acknowledgedAt && (
            <Button
              variant="outline"
              onClick={handleAcknowledge}
              disabled={acknowledgeIncident.isPending}
            >
              {acknowledgeIncident.isPending ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : null}
              Acknowledge
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
              Resolve
            </Button>
          )}
        </div>
      </div>

      <div className="grid gap-6 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Incident Details</CardTitle>
            <CardDescription>Information about this incident</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {incident.description && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  Description
                </div>
                <div>{incident.description}</div>
              </div>
            )}
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                Check
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
                  Check Type
                </div>
                <div className="capitalize">{incident.check.type}</div>
              </div>
            )}
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                Failure Count
              </div>
              <div>{incident.failureCount ?? 0}</div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Timeline</CardTitle>
            <CardDescription>Key events in this incident</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-3">
              <TimelineItem
                label="Started"
                timestamp={incident.startedAt}
                icon={<AlertTriangle className="h-4 w-4 text-yellow-500" />}
              />
              {incident.acknowledgedAt && (
                <TimelineItem
                  label="Acknowledged"
                  timestamp={incident.acknowledgedAt}
                  icon={<Clock className="h-4 w-4 text-blue-400" />}
                />
              )}
              {incident.escalatedAt && (
                <TimelineItem
                  label="Escalated"
                  timestamp={incident.escalatedAt}
                  icon={<AlertTriangle className="h-4 w-4 text-red-500" />}
                />
              )}
              {incident.lastReopenedAt && (
                <TimelineItem
                  label={`Reopened (relapse #${incident.relapseCount})`}
                  timestamp={incident.lastReopenedAt}
                  icon={<RotateCcw className="h-4 w-4 text-orange-500" />}
                />
              )}
              {incident.resolvedAt && (
                <TimelineItem
                  label="Resolved"
                  timestamp={incident.resolvedAt}
                  icon={<CheckCircle className="h-4 w-4 text-green-500" />}
                />
              )}
            </div>
            {incident.startedAt && (
              <div className="pt-4 border-t">
                <div className="text-sm font-medium text-muted-foreground">
                  Total Duration
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

      {events?.data && events.data.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>Event Log</CardTitle>
            <CardDescription>
              Detailed history of incident events
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Time</TableHead>
                  <TableHead>Event Type</TableHead>
                  <TableHead>Actor</TableHead>
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
    </div>
  );
}
