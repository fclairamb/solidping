import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Separator } from "@/components/ui/separator";
import {
  CheckCircle,
  AlertTriangle,
  XCircle,
  Clock,
  Globe,
} from "lucide-react";
import { StatusTimeline } from "./status-timeline";
import { cn } from "@/lib/utils";

interface LastResult {
  uid: string;
  status: string;
  timestamp: string;
  durationMs?: number;
}

interface Check {
  uid: string;
  name: string;
  slug: string;
  lastResult?: LastResult;
  latency_ms?: number;
  last_check_at?: string;
  uptime_24h?: number;
}

interface ChecksResponse {
  data: Check[];
}

async function fetchChecks(org: string): Promise<ChecksResponse> {
  const response = await fetch(`/api/v1/orgs/${org}/checks?with=last_result`);
  if (!response.ok) {
    throw new Error("Failed to fetch checks");
  }
  return response.json();
}

function getCheckStatus(check: Check): "ok" | "warning" | "error" | "unknown" {
  if (!check.lastResult) return "unknown";
  switch (check.lastResult.status) {
    case "up":
      return "ok";
    case "down":
    case "error":
      return "error";
    case "timeout":
      return "warning";
    default:
      return "unknown";
  }
}

function StatusIcon({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  switch (status) {
    case "ok":
      return <CheckCircle className={cn("text-green-500", className)} />;
    case "warning":
      return <AlertTriangle className={cn("text-yellow-500", className)} />;
    case "error":
      return <XCircle className={cn("text-red-500", className)} />;
    default:
      return <Clock className={cn("text-muted-foreground", className)} />;
  }
}

function StatusBadge({ status }: { status: string }) {
  const variants: Record<
    string,
    "success" | "warning" | "destructive" | "secondary"
  > = {
    ok: "success",
    warning: "warning",
    error: "destructive",
    unknown: "secondary",
  };

  const labels: Record<string, string> = {
    ok: "Operational",
    warning: "Degraded",
    error: "Outage",
    unknown: "Unknown",
  };

  return (
    <Badge variant={variants[status] || "secondary"}>
      {labels[status] || status}
    </Badge>
  );
}

function CheckCard({ check, org }: { check: Check; org: string }) {
  const status = getCheckStatus(check);
  const latencyMs = check.lastResult?.durationMs;
  return (
    <Link
      to="/orgs/$org/checks/$checkUid"
      params={{ org, checkUid: check.uid }}
      search={{ graphPeriod: undefined, graphFull: undefined }}
      className="block no-underline"
    >
      <Card className="hover:shadow-md transition-shadow cursor-pointer">
        <CardContent className="p-3">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <StatusIcon status={status} className="h-5 w-5" />
              <div>
                <h3 className="font-medium">{check.name}</h3>
                {latencyMs !== undefined && (
                  <p className="text-sm text-muted-foreground">
                    {Math.round(latencyMs)}ms latency
                  </p>
                )}
              </div>
            </div>
            <StatusBadge status={status} />
          </div>
          <div className="mt-2">
            <StatusTimeline org={org} checkUid={check.uid} />
          </div>
          {check.uptime_24h !== undefined && (
            <p className="text-xs text-muted-foreground mt-2">
              {(check.uptime_24h * 100).toFixed(2)}% uptime (24h)
            </p>
          )}
        </CardContent>
      </Card>
    </Link>
  );
}

function CheckCardSkeleton() {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <Skeleton className="h-5 w-5 rounded-full" />
            <div>
              <Skeleton className="h-4 w-32" />
              <Skeleton className="h-3 w-20 mt-1" />
            </div>
          </div>
          <Skeleton className="h-5 w-20" />
        </div>
        <Skeleton className="h-8 w-full mt-4" />
      </CardContent>
    </Card>
  );
}

function OverallStatus({ checks }: { checks: Check[] }) {
  const hasError = checks.some((c) => getCheckStatus(c) === "error");
  const hasWarning = checks.some((c) => getCheckStatus(c) === "warning");

  const status = hasError ? "error" : hasWarning ? "warning" : "ok";
  const message = hasError
    ? "Some systems are experiencing issues"
    : hasWarning
      ? "Some systems are degraded"
      : "All systems operational";

  const bgClass =
    status === "ok"
      ? "bg-green-50 dark:bg-green-950 border-green-200 dark:border-green-800"
      : status === "warning"
        ? "bg-yellow-50 dark:bg-yellow-950 border-yellow-200 dark:border-yellow-800"
        : "bg-red-50 dark:bg-red-950 border-red-200 dark:border-red-800";

  return (
    <Card className={cn("border-2", bgClass)}>
      <CardContent className="py-3 px-4">
        <div className="flex items-center gap-3">
          <StatusIcon status={status} className="h-6 w-6" />
          <div>
            <h2 className="text-lg font-semibold">{message}</h2>
            <p className="text-xs text-muted-foreground">
              Last updated: {new Date().toLocaleString()}
            </p>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

interface StatusDashboardProps {
  org: string;
}

export function StatusDashboard({ org }: StatusDashboardProps) {
  const {
    data: checksData,
    isLoading,
    error,
  } = useQuery({
    queryKey: ["checks", org],
    queryFn: () => fetchChecks(org),
    refetchInterval: 30000, // Refresh every 30 seconds
  });

  const checks = checksData?.data || [];

  return (
    <div className="min-h-full bg-background">
      {/* Main Content */}
      <div className="container mx-auto px-4 py-8 space-y-8">
        {/* Overall Status */}
        {isLoading ? (
          <Card>
            <CardContent className="p-6">
              <div className="flex items-center gap-4">
                <Skeleton className="h-8 w-8 rounded-full" />
                <div>
                  <Skeleton className="h-6 w-64" />
                  <Skeleton className="h-4 w-40 mt-1" />
                </div>
              </div>
            </CardContent>
          </Card>
        ) : error ? (
          <Card className="border-2 border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-950">
            <CardContent className="p-6">
              <div className="flex items-center gap-4">
                <XCircle className="h-8 w-8 text-red-500" />
                <div>
                  <h2 className="text-xl font-semibold">
                    Unable to load status
                  </h2>
                  <p className="text-sm text-muted-foreground">
                    Please try again later
                  </p>
                </div>
              </div>
            </CardContent>
          </Card>
        ) : (
          <OverallStatus checks={checks} />
        )}

        <Separator />

        {/* Services Section */}
        <section>
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Globe className="h-5 w-5" />
                Checks
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
                {isLoading
                  ? Array.from({ length: 6 }).map((_, i) => (
                      <CheckCardSkeleton key={i} />
                    ))
                  : checks.map((check) => (
                      <CheckCard key={check.uid} check={check} org={org} />
                    ))}
              </div>
              {!isLoading && checks.length === 0 && (
                <p className="text-center text-muted-foreground py-8">
                  No services configured
                </p>
              )}
            </CardContent>
          </Card>
        </section>
      </div>
    </div>
  );
}
