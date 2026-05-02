import { useState, useEffect, useMemo, useRef } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import type { IncidentDetail } from "@/api/hooks";
import {
  ArrowLeft,
  Check as CheckIcon,
  Clock,
  Copy,
  ExternalLink,
  Loader2,
  Pencil,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { toast } from "sonner";
import {
  useCheck,
  useDeleteCheck,
  useUpdateCheck,
  useResults,
  useIncidents,
  useRegions,
} from "@/api/hooks";
import { useEmailAddressDomain, emailCheckAddress } from "@/api/email-inbox";
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
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { QueryErrorView } from "@/components/shared/error-views";
import { CheckSummaryCards } from "@/components/checks/check-summary-cards";
import { ResponseTimeChart } from "@/components/checks/response-time-chart";
import { AvailabilityTable } from "@/components/checks/availability-table";

export const Route = createFileRoute("/orgs/$org/checks/$checkUid/")({
  validateSearch: (search: Record<string, unknown>) => ({
    graphPeriod: (["hour", "day", "week", "month"].includes(search.graphPeriod as string)
      ? search.graphPeriod
      : undefined) as "hour" | "day" | "week" | "month" | undefined,
    graphFull: search.graphFull === "true" ? true : undefined,
  }),
  component: CheckDetailPage,
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

function IncidentDuration({ incident }: { incident: IncidentDetail }) {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (incident.state === "active" && !incident.resolvedAt) {
      const interval = setInterval(() => setNow(Date.now()), 1000);
      return () => clearInterval(interval);
    }
  }, [incident.state, incident.resolvedAt]);

  if (incident.startedAt && incident.resolvedAt) {
    return formatDuration(
      new Date(incident.resolvedAt).getTime() -
        new Date(incident.startedAt).getTime()
    );
  }
  if (incident.startedAt) {
    return formatDuration(now - new Date(incident.startedAt).getTime()) + " (ongoing)";
  }
  return "-";
}

/** Parse HH:MM:SS period string to milliseconds */
function parsePeriodMs(period?: string): number | undefined {
  if (!period) return undefined;
  const parts = period.split(":").map(Number);
  if (parts.length !== 3 || parts.some(isNaN)) return undefined;
  const [h, m, s] = parts;
  const ms = (h * 3600 + m * 60 + s) * 1000;
  return ms > 0 ? ms : undefined;
}

function HeartbeatEndpoint({ org, check }: { org: string; check: { slug?: string; uid: string; config?: Record<string, unknown> } }) {
  const token = check.config?.token as string;
  const identifier = check.slug || check.uid;
  const heartbeatUrl = `${window.location.origin}/api/v1/heartbeat/${org}/${identifier}?token=${token}`;
  const curlCommand = `curl "${heartbeatUrl}"`;

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text);
    toast.success("Copied to clipboard");
  };

  return (
    <div>
      <div className="text-sm font-medium text-muted-foreground mb-2">
        Heartbeat Endpoint
      </div>
      <div className="space-y-3">
        <div className="bg-muted rounded-md p-3 text-sm font-mono break-all flex items-start gap-2">
          <span className="flex-1">{heartbeatUrl}</span>
          <button
            type="button"
            onClick={() => copyToClipboard(heartbeatUrl)}
            className="text-muted-foreground hover:text-foreground p-0.5 rounded shrink-0"
          >
            <Copy className="h-4 w-4" />
          </button>
        </div>
        <div>
          <div className="text-xs text-muted-foreground mb-1">Sample curl command:</div>
          <div className="bg-muted rounded-md p-3 text-sm font-mono break-all flex items-start gap-2">
            <span className="flex-1">{curlCommand}</span>
            <button
              type="button"
              onClick={() => copyToClipboard(curlCommand)}
              className="text-muted-foreground hover:text-foreground p-0.5 rounded shrink-0"
            >
              <Copy className="h-4 w-4" />
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function EmailEndpoint({ check }: { check: { config?: Record<string, unknown> } }) {
  const token = check.config?.token as string;
  const { data: domain } = useEmailAddressDomain();
  const [showHelp, setShowHelp] = useState(false);

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text);
    toast.success("Copied to clipboard");
  };

  if (!domain) {
    return (
      <div>
        <div className="text-sm font-medium text-muted-foreground mb-2">
          Email Endpoint
        </div>
        <div className="bg-muted rounded-md p-3 text-sm text-muted-foreground">
          Email inbox not configured. The check is created but cannot receive pings until an administrator configures the shared inbox.
        </div>
      </div>
    );
  }

  const address = emailCheckAddress(token, domain);
  const mailto = `mailto:${address}?subject=Test`;

  return (
    <div>
      <div className="text-sm font-medium text-muted-foreground mb-2">
        Email Endpoint
      </div>
      <div className="space-y-3">
        <div className="bg-muted rounded-md p-3 text-sm font-mono break-all flex items-start gap-2">
          <span className="flex-1" data-testid="email-check-address">{address}</span>
          <button
            type="button"
            data-testid="email-check-copy-btn"
            onClick={() => copyToClipboard(address)}
            className="text-muted-foreground hover:text-foreground p-0.5 rounded shrink-0"
          >
            <Copy className="h-4 w-4" />
          </button>
        </div>
        <div>
          <a href={mailto} className="text-sm text-primary hover:underline inline-flex items-center gap-1">
            Send test email
            <ExternalLink className="h-3 w-3" />
          </a>
        </div>
        <button
          type="button"
          onClick={() => setShowHelp((v) => !v)}
          className="text-sm text-muted-foreground hover:text-foreground"
        >
          {showHelp ? "Hide" : "Show"} reporting failure options
        </button>
        {showHelp && (
          <div className="bg-muted rounded-md p-3 text-sm space-y-2">
            <p>To report a failure or non-default status, use any of:</p>
            <ul className="list-disc pl-5 space-y-1 text-muted-foreground">
              <li>Plus-addressing: send to <code className="font-mono">{token}+down@{domain}</code> (or <code className="font-mono">+error</code>, <code className="font-mono">+running</code>).</li>
              <li>Header: include <code className="font-mono">X-SolidPing-Status: down</code>.</li>
              <li>Subject prefix: start the subject with <code className="font-mono">[DOWN]</code>, <code className="font-mono">[ERROR]</code>, or <code className="font-mono">[RUNNING]</code>.</li>
            </ul>
            <p className="text-xs text-muted-foreground">
              Resolution priority: plus-address &gt; header &gt; subject &gt; default <code className="font-mono">up</code>.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}

function CheckDetailPage() {
  const { org, checkUid } = Route.useParams();
  const { graphPeriod, graphFull } = Route.useSearch();
  const navigate = useNavigate();
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [editingSlug, setEditingSlug] = useState(false);
  const [slugValue, setSlugValue] = useState("");
  const slugInputRef = useRef<HTMLInputElement>(null);

  const {
    data: check,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useCheck(org, checkUid, { with: "last_result,last_status_change" });

  const refetchInterval = useMemo(
    () => parsePeriodMs(check?.period),
    [check?.period]
  );

  // Re-fetch check (with lastResult) at the same interval
  useCheck(org, checkUid, {
    with: "last_result,last_status_change",
    refetchInterval,
  });

  const { data: results } = useResults(org, {
    checkUid,
    size: 10,
    with: "durationMs",
    refetchInterval,
  });

  const { data: incidents } = useIncidents(org, { checkUid, size: 100 });

  const { data: regionsData } = useRegions(org);
  const deleteCheck = useDeleteCheck(org);
  const updateCheck = useUpdateCheck(org, checkUid);

  const startEditingSlug = () => {
    setSlugValue(check?.slug || "");
    setEditingSlug(true);
    setTimeout(() => slugInputRef.current?.focus(), 0);
  };

  const saveSlug = async () => {
    const trimmed = slugValue.trim();
    if (trimmed === (check?.slug || "")) {
      setEditingSlug(false);
      return;
    }
    try {
      await updateCheck.mutateAsync({ slug: trimmed });
      toast.success("Slug updated");
      setEditingSlug(false);
      navigate({
        to: "/orgs/$org/checks/$checkUid",
        params: { org, checkUid: trimmed },
        search: { graphPeriod: undefined, graphFull: undefined },
        replace: true,
      });
    } catch {
      toast.error("Failed to update slug");
    }
  };

  const cancelEditingSlug = () => {
    setEditingSlug(false);
    setSlugValue(check?.slug || "");
  };

  const handleDelete = async () => {
    try {
      await deleteCheck.mutateAsync(checkUid);
      toast.success("Check deleted successfully");
      navigate({ to: "/orgs/$org/checks", params: { org } });
    } catch {
      toast.error("Failed to delete check");
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
        <Skeleton className="h-64 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource="Check"
        backTo="/orgs/$org/checks"
        backLabel="Back to Checks"
        onRetry={() => refetch()}
      />
    );
  }

  if (!check) {
    return (
      <div className="text-center py-12">
        <p className="text-muted-foreground mb-4">Check not found</p>
        <Link to="/orgs/$org/checks" params={{ org }}>
          <Button variant="outline">Back to Checks</Button>
        </Link>
      </div>
    );
  }

  const statusColor =
    check.lastResult?.status === "up"
      ? "bg-green-500"
      : check.lastResult?.status === "down" ||
          check.lastResult?.status === "error"
        ? "bg-red-500"
        : check.lastResult?.status === "timeout"
          ? "bg-yellow-500"
          : "bg-muted-foreground";

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <Button
            variant="ghost"
            size="icon"
            onClick={() =>
              navigate({ to: "/orgs/$org/checks", params: { org } })
            }
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <div className="flex items-center gap-3">
            <div className={`h-3 w-3 rounded-full ${statusColor}`} />
            <div>
              <h1 className="text-3xl font-bold tracking-tight">
                {check.name || check.slug || check.uid?.slice(0, 8)}
              </h1>
              {check.slug && !editingSlug && (
                <div className="flex items-center gap-1 mt-1">
                  <Link
                    to="/orgs/$org/checks/$checkUid"
                    params={{ org, checkUid: check.slug }}
                    search={{ graphPeriod: undefined, graphFull: undefined }}
                    className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-xs font-mono text-muted-foreground hover:text-foreground transition-colors"
                  >
                    <span>🔗</span>
                    {check.slug}
                  </Link>
                  <button
                    type="button"
                    onClick={startEditingSlug}
                    className="text-muted-foreground hover:text-foreground p-0.5 rounded"
                  >
                    <Pencil className="h-3 w-3" />
                  </button>
                </div>
              )}
              {editingSlug && (
                <div className="flex items-center gap-1 mt-1">
                  <span className="text-xs">🔗</span>
                  <input
                    ref={slugInputRef}
                    value={slugValue}
                    onChange={(e) => setSlugValue(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") saveSlug();
                      if (e.key === "Escape") cancelEditingSlug();
                    }}
                    className="h-6 rounded border bg-background px-1.5 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-ring"
                    disabled={updateCheck.isPending}
                  />
                  <button
                    type="button"
                    onClick={saveSlug}
                    disabled={updateCheck.isPending}
                    className="text-muted-foreground hover:text-green-500 p-0.5 rounded"
                  >
                    {updateCheck.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <CheckIcon className="h-3 w-3" />}
                  </button>
                  <button
                    type="button"
                    onClick={cancelEditingSlug}
                    disabled={updateCheck.isPending}
                    className="text-muted-foreground hover:text-red-500 p-0.5 rounded"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </div>
              )}
              {check.uid && checkUid !== check.uid && (
                <div className="flex items-center gap-1 mt-1">
                  <Link
                    to="/orgs/$org/checks/$checkUid"
                    params={{ org, checkUid: check.uid }}
                    search={{ graphPeriod: undefined, graphFull: undefined }}
                    className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-xs font-mono text-muted-foreground hover:text-foreground transition-colors"
                  >
                    uid: {check.uid.slice(0, 8)}...
                  </Link>
                </div>
              )}
            </div>
            <Badge variant="outline">{check.type}</Badge>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Link
            to="/orgs/$org/checks/$checkUid/edit"
            params={{ org, checkUid }}
          >
            <Button variant="outline" size="icon">
              <Pencil className="h-4 w-4" />
            </Button>
          </Link>
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
          <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
            <AlertDialogTrigger asChild>
              <Button variant="destructive" size="icon">
                <Trash2 className="h-4 w-4" />
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Delete Check</AlertDialogTitle>
                <AlertDialogDescription>
                  Are you sure you want to delete this check? This action cannot
                  be undone.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  onClick={handleDelete}
                  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                >
                  {deleteCheck.isPending ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      Deleting...
                    </>
                  ) : (
                    "Delete"
                  )}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>

      {/* Summary cards */}
      <CheckSummaryCards
        check={check}
        totalIncidents={incidents?.total ?? incidents?.data?.length ?? 0}
      />

      {/* Response time chart */}
      <ResponseTimeChart
        org={org}
        checkUid={checkUid}
        refetchInterval={refetchInterval}
        initialPeriod={graphPeriod}
        initialFullRange={graphFull}
        onSettingsChange={(period, full) =>
          navigate({
            to: ".",
            search: {
              graphPeriod: period !== "day" ? period : undefined,
              graphFull: full ? true : undefined,
            },
            replace: true,
          })
        }
      />

      {/* Availability table */}
      <AvailabilityTable org={org} checkUid={checkUid} refetchInterval={refetchInterval} />

      <div className="grid gap-6 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Configuration</CardTitle>
            <CardDescription>Check settings and parameters</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {check.description && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  Description
                </div>
                <div>{check.description}</div>
              </div>
            )}
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                Type
              </div>
              <div className="capitalize">{check.type}</div>
            </div>
            {check.period && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  Check Interval
                </div>
                <div>{check.period}</div>
              </div>
            )}
            {check.regions && check.regions.length > 0 && (
              <div>
                <div className="text-sm font-medium text-muted-foreground mb-1">
                  Regions
                </div>
                <div className="flex gap-1 flex-wrap">
                  {check.regions.map((slug) => {
                    const region = regionsData?.regions?.find((r) => r.slug === slug);
                    return (
                      <Badge key={slug} variant="outline">
                        {region ? `${region.emoji} ${region.name}` : slug}
                      </Badge>
                    );
                  })}
                </div>
              </div>
            )}
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                Status
              </div>
              <div className="flex items-center gap-2">
                <Badge
                  variant="secondary"
                  className={
                    check.lastResult?.status === "up"
                      ? "bg-green-500/10 text-green-500"
                      : check.lastResult?.status === "down" ||
                          check.lastResult?.status === "error"
                        ? "bg-red-500/10 text-red-500"
                        : ""
                  }
                >
                  {check.lastResult?.status || "unknown"}
                </Badge>
                {check.enabled === false && (
                  <Badge variant="outline">Disabled</Badge>
                )}
              </div>
            </div>
            {check.config && Object.keys(check.config).length > 0 && (
              <div>
                <div className="text-sm font-medium text-muted-foreground mb-2">
                  Configuration
                </div>
                <div className="bg-muted rounded-md p-3 text-sm font-mono">
                  {Object.entries(check.config).map(([key, value]) => (
                    <div key={key} className="flex gap-2">
                      <span className="text-muted-foreground">{key}:</span>
                      <span>
                        {typeof value === "string" ? (
                          value.startsWith("http") ? (
                            <a
                              href={value}
                              target="_blank"
                              rel="noopener noreferrer"
                              className="text-primary hover:underline inline-flex items-center gap-1"
                            >
                              {value}
                              <ExternalLink className="h-3 w-3" />
                            </a>
                          ) : (
                            value
                          )
                        ) : (
                          JSON.stringify(value)
                        )}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}
            {check.labels && Object.keys(check.labels).length > 0 && (
              <div>
                <div className="text-sm font-medium text-muted-foreground mb-2">
                  Labels
                </div>
                <div className="flex gap-1 flex-wrap">
                  {Object.entries(check.labels).map(([key, value]) => (
                    <Badge key={key} variant="outline">
                      {key}: {value}
                    </Badge>
                  ))}
                </div>
              </div>
            )}
            {check.type === "heartbeat" && (check.config?.token as string) && (
              <HeartbeatEndpoint org={org} check={check} />
            )}
            {check.type === "email" && (check.config?.token as string) && (
              <EmailEndpoint check={check} />
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Last Result</CardTitle>
            <CardDescription>Most recent check execution</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {check.lastResult ? (
              <>
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Clock className="h-4 w-4" />
                  {check.lastResult.timestamp
                    ? new Date(check.lastResult.timestamp).toLocaleString()
                    : "Unknown"}
                </div>
                {check.lastResult.metrics && (
                  <div>
                    <div className="text-sm font-medium text-muted-foreground mb-2">
                      Metrics
                    </div>
                    <div className="bg-muted rounded-md p-3 text-sm font-mono">
                      {Object.entries(check.lastResult.metrics).map(
                        ([key, value]) => (
                          <div key={key} className="flex gap-2">
                            <span className="text-muted-foreground">
                              {key}:
                            </span>
                            <span>
                              {typeof value === "number"
                                ? Math.round(value * 100) / 100
                                : JSON.stringify(value)}
                            </span>
                          </div>
                        )
                      )}
                    </div>
                  </div>
                )}
                {check.lastResult.output &&
                  Object.keys(check.lastResult.output).length > 0 && (
                    <div>
                      <div className="text-sm font-medium text-muted-foreground mb-2">
                        Output
                      </div>
                      <div className="bg-muted rounded-md p-3 text-sm font-mono max-h-32 overflow-auto">
                        {Object.entries(check.lastResult.output).map(
                          ([key, value]) => (
                            <div key={key} className="flex gap-2">
                              <span className="text-muted-foreground">
                                {key}:
                              </span>
                              <span>
                                {typeof value === "string"
                                  ? value
                                  : JSON.stringify(value)}
                              </span>
                            </div>
                          )
                        )}
                      </div>
                    </div>
                  )}
              </>
            ) : (
              <p className="text-muted-foreground">No results yet</p>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Recent Results</CardTitle>
          <CardDescription>History of check executions</CardDescription>
        </CardHeader>
        <CardContent>
          {results?.data && results.data.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Time</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Duration</TableHead>
                  <TableHead>Region</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {results.data.map((result) => (
                  <TableRow key={result.uid}>
                    <TableCell className="text-sm">
                      {result.periodStart
                        ? new Date(result.periodStart).toLocaleString()
                        : "-"}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant="secondary"
                        className={
                          result.status === "up"
                            ? "bg-green-500/10 text-green-500"
                            : result.status === "down"
                              ? "bg-red-500/10 text-red-500"
                              : result.status === "created"
                                ? "bg-blue-500/10 text-blue-500"
                                : ""
                        }
                      >
                        {result.status}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-sm">
                      {result.durationMs !== undefined
                        ? `${Math.round(result.durationMs)}ms`
                        : "-"}
                    </TableCell>
                    <TableCell className="text-sm">
                      {result.region || "-"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : (
            <p className="text-center py-6 text-muted-foreground">
              No results available
            </p>
          )}
        </CardContent>
      </Card>

      {incidents?.data && incidents.data.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>Recent Incidents</CardTitle>
            <CardDescription>Issues related to this check</CardDescription>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Started</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Duration</TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {incidents.data.map((incident) => (
                  <TableRow key={incident.uid}>
                    <TableCell className="text-sm">
                      {incident.startedAt
                        ? new Date(incident.startedAt).toLocaleString()
                        : "-"}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={
                          incident.state === "active"
                            ? "destructive"
                            : "secondary"
                        }
                      >
                        {incident.state}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-sm">
                      <IncidentDuration incident={incident} />
                    </TableCell>
                    <TableCell>
                      <Link
                        to="/orgs/$org/incidents/$incidentUid"
                        params={{ org, incidentUid: incident.uid! }}
                      >
                        <Button variant="ghost" size="sm">
                          View
                        </Button>
                      </Link>
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
