import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowLeft, CirclePlus, RefreshCw, StopCircle, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
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
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  useDiscoveryScan,
  useListCandidateHosts,
  useDismissCandidate,
  useCancelScan,
  type DiscoveredHost,
} from "@/api/hooks";
import { useState } from "react";

export const Route = createFileRoute("/orgs/$org/discovery/$jobUid/")({
  component: ScanDetailPage,
});

function statusBadgeVariant(status: string): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "success": return "default";
    case "running": return "secondary";
    case "failed": return "destructive";
    default: return "outline";
  }
}

function HostRow({
  host,
  org,
  jobUid,
  onDismiss,
}: {
  host: DiscoveredHost;
  org: string;
  jobUid: string;
  onDismiss: (host: DiscoveredHost) => void;
}) {
  const { t } = useTranslation("discovery");

  return (
    <TableRow>
      <TableCell className="font-mono text-sm">{host.ip}</TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {host.hostname || "—"}
      </TableCell>
      <TableCell>
        <Badge variant="outline">
          {t(host.source === "freebox" ? "sourceFreebox" : "sourceLan")}
        </Badge>
      </TableCell>
      <TableCell className="text-xs">
        {host.openPorts?.length > 0 ? host.openPorts.join(", ") : "—"}
      </TableCell>
      <TableCell>
        {host.icmpReachable ? (
          <Badge variant="default">✓</Badge>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        )}
      </TableCell>
      <TableCell>
        {host.promotedToCheckUid ? (
          <Badge variant="secondary">{t("promoted")}</Badge>
        ) : (
          <span className="text-xs text-muted-foreground">{t("pending")}</span>
        )}
      </TableCell>
      <TableCell className="text-right">
        <div className="flex justify-end gap-1">
          {!host.promotedToCheckUid && (
            <>
              <Button asChild variant="ghost" size="icon" title={t("promote")}>
                <Link
                  to="/orgs/$org/discovery/$jobUid/$hostUid/promote"
                  params={{ org, jobUid, hostUid: host.uid }}
                >
                  <CirclePlus className="h-4 w-4" />
                </Link>
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="text-destructive"
                title={t("dismiss")}
                onClick={() => onDismiss(host)}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </>
          )}
        </div>
      </TableCell>
    </TableRow>
  );
}

function ScanDetailPage() {
  const { t } = useTranslation("discovery");
  const { org, jobUid } = Route.useParams();
  const { data: scanData, isLoading: scanLoading, refetch: refetchScan } = useDiscoveryScan(org, jobUid);
  const scan = scanData?.scan;
  const progress = scanData?.progress;

  // The displayed status prefers the fan-out derived status, falling back to the
  // plan job's own status for legacy/standalone scans.
  const displayStatus = progress?.derivedStatus ?? scan?.status ?? "pending";
  const isRefreshing = displayStatus === "pending" || displayStatus === "running";

  const { data: hosts, isLoading: hostsLoading, refetch: refetchHosts } = useListCandidateHosts(
    org,
    { jobUid },
    isRefreshing, // poll the host list while the scan is active so chunks stream in
  );
  const dismiss = useDismissCandidate(org);
  const cancelScan = useCancelScan(org);
  const [pendingDismiss, setPendingDismiss] = useState<DiscoveredHost | null>(null);
  const [confirmStop, setConfirmStop] = useState(false);

  const isLoading = scanLoading || hostsLoading;
  const canStop = isRefreshing && scan?.type === "network_discovery_plan";

  const handleDismiss = () => {
    if (!pendingDismiss) return;
    dismiss.mutate(pendingDismiss.uid, {
      onSuccess: () => {
        toast.success(t("dismissed"));
        setPendingDismiss(null);
        void refetchHosts();
      },
      onError: () => toast.error(t("dismissFailed")),
    });
  };

  const handleStop = () => {
    cancelScan.mutate(jobUid, {
      onSuccess: () => {
        toast.success(t("scanStopped"));
        setConfirmStop(false);
        void refetchScan();
      },
      onError: () => toast.error(t("stopScanFailed")),
    });
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex items-center gap-3">
          <Button asChild variant="ghost" size="icon">
            <Link to="/orgs/$org/discovery" params={{ org }}>
              <ArrowLeft className="h-4 w-4" />
            </Link>
          </Button>
          <div>
            <h1 className="text-2xl font-bold tracking-tight">{t("scanDetails")}</h1>
            {scan && (
              <div className="flex items-center gap-2 mt-1">
                <Badge variant={statusBadgeVariant(displayStatus)}>
                  {t(`scanStatus.${displayStatus}`, displayStatus)}
                </Badge>
                <span className="text-xs text-muted-foreground font-mono">{scan.uid}</span>
              </div>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2">
          {canStop && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setConfirmStop(true)}
              disabled={cancelScan.isPending}
            >
              <StopCircle className="h-4 w-4 mr-1" />
              {t("stopScan")}
            </Button>
          )}
          <Button
            variant="outline"
            size="icon"
            onClick={() => { void refetchScan(); void refetchHosts(); }}
            disabled={isLoading}
          >
            <RefreshCw className={`h-4 w-4 ${isRefreshing ? "animate-spin" : ""}`} />
          </Button>
        </div>
      </div>

      {progress && progress.totalChunks > 0 && (
        <Card>
          <CardHeader className="pb-2">
            <div className="flex items-center justify-between gap-2">
              <CardTitle className="text-sm">{t("progress")}</CardTitle>
              <span className="text-xs text-muted-foreground">
                {t("chunksProgress", {
                  completed: progress.completedChunks + progress.failedChunks,
                  total: progress.totalChunks,
                })}
              </span>
            </div>
          </CardHeader>
          <CardContent className="space-y-2">
            <Progress
              value={progress.completedChunks + progress.failedChunks}
              max={progress.totalChunks}
            />
            <p className="text-xs text-muted-foreground">
              {t("hostsFound", { count: progress.hostCount })}
            </p>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle>{t("hosts")}</CardTitle>
          {!isLoading && (!hosts || hosts.length === 0) && (
            <CardDescription>{t("noHostsDescription")}</CardDescription>
          )}
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="space-y-2">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : hosts && hosts.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("ip")}</TableHead>
                  <TableHead>{t("hostname")}</TableHead>
                  <TableHead>{t("source")}</TableHead>
                  <TableHead>{t("openPorts")}</TableHead>
                  <TableHead>{t("icmpReachable")}</TableHead>
                  <TableHead>{t("status")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {hosts.map((host) => (
                  <HostRow
                    key={host.uid}
                    host={host}
                    org={org}
                    jobUid={jobUid}
                    onDismiss={setPendingDismiss}
                  />
                ))}
              </TableBody>
            </Table>
          ) : (
            <p className="text-sm text-muted-foreground">{t("noHosts")}</p>
          )}
        </CardContent>
      </Card>

      <AlertDialog open={!!pendingDismiss} onOpenChange={(open) => { if (!open) setPendingDismiss(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("dismiss")}?</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDismiss?.ip}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={handleDismiss}>
              {t("dismiss")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={confirmStop} onOpenChange={setConfirmStop}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("stopScan")}?</AlertDialogTitle>
            <AlertDialogDescription>{t("stopScanConfirm")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={handleStop}>
              {t("stopScan")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
