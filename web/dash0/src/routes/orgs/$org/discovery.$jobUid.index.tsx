import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowLeft, CirclePlus, RefreshCw, StopCircle, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { Checkbox } from "@/components/ui/checkbox";
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
} from "@/components/ui/alert-dialog";
import { CheckTypeBadge } from "@/components/shared/check-type-identity";
import {
  useDiscoveryScan,
  useListDiscoveredChecks,
  useDismissCheck,
  useDismissGroup,
  usePromoteChecks,
  useCancelScan,
  type DiscoveredCheck,
} from "@/api/hooks";

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

// configHint summarizes a discovered check's config into a short string
// (url / host:port / host) for display.
function configHint(check: DiscoveredCheck): string {
  const cfg = check.config ?? {};
  if (typeof cfg.url === "string") return cfg.url;
  if (cfg.host !== undefined && cfg.port !== undefined) {
    return `${String(cfg.host)}:${String(cfg.port)}`;
  }
  if (cfg.host !== undefined) return String(cfg.host);
  return "";
}

// CheckGroup is the render-time grouping of discovered checks by groupKey.
interface CheckGroup {
  key: string;
  label: string;
  source: string;
  metadata?: Record<string, unknown>;
  checks: DiscoveredCheck[];
}

// groupChecks groups a flat discovered-check list by groupKey, preserving order.
function groupChecks(checks: DiscoveredCheck[]): CheckGroup[] {
  const byKey = new Map<string, CheckGroup>();
  for (const c of checks) {
    let g = byKey.get(c.groupKey);
    if (!g) {
      g = {
        key: c.groupKey,
        label: c.groupLabel || c.groupKey,
        source: c.source,
        metadata: c.metadata,
        checks: [],
      };
      byKey.set(c.groupKey, g);
    }
    g.checks.push(c);
  }
  return Array.from(byKey.values());
}

// healthBadgeVariant maps a container health word to a badge variant.
function healthBadgeVariant(
  health: string,
): "default" | "secondary" | "destructive" | "outline" {
  switch (health) {
    case "healthy":
      return "default";
    case "unhealthy":
      return "destructive";
    default:
      return "outline";
  }
}

// replicaBadgeVariant maps a ready/desired ratio to a badge variant: all ready
// → default (green), some ready → secondary, none ready → destructive.
function replicaBadgeVariant(
  ready: number,
  desired: number,
): "default" | "secondary" | "destructive" | "outline" {
  if (desired === 0) return "outline";
  if (ready >= desired) return "default";
  if (ready === 0) return "destructive";
  return "secondary";
}

// KubernetesBadges renders the k8s-specific group hints: a kind badge, a
// ready/desired replica badge, and one badge per externally-reachable endpoint.
function KubernetesBadges({ metadata }: { metadata: Record<string, unknown> }) {
  const { t } = useTranslation("discovery");

  const kind = typeof metadata.kind === "string" ? metadata.kind : "";
  const desired =
    typeof metadata.desiredReplicas === "number" ? metadata.desiredReplicas : 0;
  const ready =
    typeof metadata.readyReplicas === "number" ? metadata.readyReplicas : 0;
  const endpoints = Array.isArray(metadata.endpoints)
    ? (metadata.endpoints as Record<string, unknown>[])
    : [];

  return (
    <div className="flex flex-wrap items-center gap-1">
      {kind && <Badge variant="secondary">{kind}</Badge>}
      <Badge variant={replicaBadgeVariant(ready, desired)}>
        {t("replicaCount", { ready, desired })}
      </Badge>
      {endpoints.map((ep, i) => {
        const addr = typeof ep.address === "string" ? ep.address : "";
        const port = typeof ep.port === "number" ? ep.port : undefined;
        const label = addr
          ? port !== undefined
            ? `${addr}:${port}`
            : addr
          : typeof ep.source === "string"
            ? ep.source
            : "";
        if (!label) return null;
        return (
          <Badge key={i} variant="outline" className="font-mono text-xs">
            {label}
          </Badge>
        );
      })}
    </div>
  );
}

// MetadataBadges renders a group's metadata hints as small badges. It handles
// the LAN/Freebox shape (open ports, ICMP), the container shape (image, state,
// health status), and the kubernetes shape (kind, replicas, endpoints).
function MetadataBadges({ metadata }: { metadata?: Record<string, unknown> }) {
  const { t } = useTranslation("discovery");
  if (!metadata) return null;

  // Kubernetes metadata: kind + replicas + endpoints (distinct keys).
  if (metadata.kind !== undefined || metadata.desiredReplicas !== undefined) {
    return <KubernetesBadges metadata={metadata} />;
  }

  // Container metadata: image / state / health.
  const image = typeof metadata.image === "string" ? metadata.image : "";
  const state = typeof metadata.state === "string" ? metadata.state : "";
  const health =
    typeof metadata.healthStatus === "string" ? metadata.healthStatus : "";

  if (image || state || health) {
    return (
      <div className="flex flex-wrap items-center gap-1">
        {image && (
          <Badge variant="outline" className="font-mono text-xs">
            {image}
          </Badge>
        )}
        {state && <Badge variant="secondary">{state}</Badge>}
        {health && <Badge variant={healthBadgeVariant(health)}>{health}</Badge>}
      </div>
    );
  }

  // LAN / Freebox metadata: open ports + ICMP.
  const ports = Array.isArray(metadata.openPorts)
    ? (metadata.openPorts as number[])
    : [];
  const icmp = metadata.icmpReachable === true;

  return (
    <div className="flex flex-wrap items-center gap-1">
      {icmp && <Badge variant="outline">{t("icmpReachable")}</Badge>}
      {ports.length > 0 && (
        <Badge variant="outline" className="font-mono text-xs">
          {ports.join(", ")}
        </Badge>
      )}
    </div>
  );
}

function GroupCard({
  group,
  selected,
  onToggleCheck,
  onToggleGroup,
  onDismissGroup,
  onDismissCheck,
}: {
  group: CheckGroup;
  selected: Record<string, boolean>;
  onToggleCheck: (uid: string) => void;
  onToggleGroup: (group: CheckGroup, checked: boolean) => void;
  onDismissGroup: (group: CheckGroup) => void;
  onDismissCheck: (check: DiscoveredCheck) => void;
}) {
  const { t } = useTranslation("discovery");
  // "Select all" only governs promotable rows: promoted checks can never be
  // (re-)selected, so counting them would leave the header checkbox stuck
  // unchecked forever once any check in the group has been promoted.
  const promotable = group.checks.filter((c) => !c.promotedToCheckUid);
  const allSelected =
    promotable.length > 0 && promotable.every((c) => selected[c.uid]);

  return (
    <Card data-testid="discovery-group">
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <Checkbox
              checked={allSelected}
              disabled={promotable.length === 0}
              onCheckedChange={(v) => onToggleGroup(group, !!v)}
              aria-label={t("selectAllInGroup")}
            />
            <CardTitle className="text-base font-mono">{group.label}</CardTitle>
            <Badge variant="outline">{t(`sourceLabel.${group.source}`, group.source)}</Badge>
            <MetadataBadges metadata={group.metadata} />
          </div>
          <Button
            variant="ghost"
            size="icon"
            className="text-destructive"
            title={t("dismissGroup")}
            aria-label={t("dismissGroup")}
            onClick={() => onDismissGroup(group)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-2">
        {group.checks.map((check) => (
          <div
            key={check.uid}
            className="flex items-center justify-between gap-3 rounded-md border p-2"
            data-testid="discovery-check-row"
          >
            <label className="flex flex-1 items-center gap-3 text-sm">
              <Checkbox
                checked={!!selected[check.uid]}
                onCheckedChange={() => onToggleCheck(check.uid)}
                aria-label={check.name}
                disabled={!!check.promotedToCheckUid}
              />
              <span className="font-medium">{check.name}</span>
              <CheckTypeBadge type={check.type} />
              {configHint(check) && (
                <span className="font-mono text-xs text-muted-foreground">
                  {configHint(check)}
                </span>
              )}
            </label>
            <div className="flex items-center gap-1">
              {check.promotedToCheckUid ? (
                <Badge variant="secondary">{t("promoted")}</Badge>
              ) : (
                <Button
                  variant="ghost"
                  size="icon"
                  className="text-destructive"
                  title={t("dismiss")}
                  aria-label={t("dismiss")}
                  onClick={() => onDismissCheck(check)}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              )}
            </div>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function ScanDetailPage() {
  const { t } = useTranslation("discovery");
  const { org, jobUid } = Route.useParams();
  const { data: scanData, isLoading: scanLoading, refetch: refetchScan } = useDiscoveryScan(org, jobUid);
  const scan = scanData?.scan;
  const progress = scanData?.progress;

  const displayStatus = progress?.derivedStatus ?? scan?.status ?? "pending";
  const isRefreshing = displayStatus === "pending" || displayStatus === "running";

  const { data: checks, isLoading: checksLoading, refetch: refetchChecks } =
    useListDiscoveredChecks(org, { jobUid }, isRefreshing);
  const dismissCheck = useDismissCheck(org);
  const dismissGroup = useDismissGroup(org);
  const promote = usePromoteChecks(org);
  const cancelScan = useCancelScan(org);

  const [selected, setSelected] = useState<Record<string, boolean>>({});
  const [pendingDismissCheck, setPendingDismissCheck] = useState<DiscoveredCheck | null>(null);
  const [pendingDismissGroup, setPendingDismissGroup] = useState<CheckGroup | null>(null);
  const [confirmStop, setConfirmStop] = useState(false);

  const groups = useMemo(() => groupChecks(checks ?? []), [checks]);
  const selectedUids = useMemo(
    () => Object.keys(selected).filter((uid) => selected[uid]),
    [selected],
  );

  const isLoading = scanLoading || checksLoading;
  const canStop = isRefreshing && scan?.type === "network_discovery_plan";

  const toggleCheck = (uid: string) =>
    setSelected((prev) => ({ ...prev, [uid]: !prev[uid] }));

  const toggleGroup = (group: CheckGroup, checked: boolean) =>
    setSelected((prev) => {
      const next = { ...prev };
      for (const c of group.checks) {
        if (!c.promotedToCheckUid) next[c.uid] = checked;
      }
      return next;
    });

  const handlePromote = () => {
    if (selectedUids.length === 0) return;
    promote.mutate(
      { uids: selectedUids },
      {
        onSuccess: () => {
          toast.success(t("promoted_success"));
          setSelected({});
          void refetchChecks();
        },
        onError: () => toast.error(t("promoted_failed")),
      },
    );
  };

  const handleDismissCheck = () => {
    if (!pendingDismissCheck) return;
    dismissCheck.mutate(pendingDismissCheck.uid, {
      onSuccess: () => {
        toast.success(t("dismissed"));
        setPendingDismissCheck(null);
        void refetchChecks();
      },
      onError: () => toast.error(t("dismissFailed")),
    });
  };

  const handleDismissGroup = () => {
    if (!pendingDismissGroup) return;
    dismissGroup.mutate(
      { jobUid, group: pendingDismissGroup.key },
      {
        onSuccess: () => {
          toast.success(t("dismissed"));
          setPendingDismissGroup(null);
          void refetchChecks();
        },
        onError: () => toast.error(t("dismissFailed")),
      },
    );
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
          <Button asChild variant="ghost" size="icon" aria-label={t("back")}>
            <Link to="/orgs/$org/discovery" params={{ org }}>
              <ArrowLeft className="h-4 w-4" />
            </Link>
          </Button>
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
            onClick={() => { void refetchScan(); void refetchChecks(); }}
            disabled={isLoading}
            aria-label={t("refresh")}
          >
            <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefreshing ? "animate-spin" : ""}`} />
            <span className="hidden sm:inline">{t("refresh")}</span>
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
              {t("groupsFound", { count: progress.groupCount })} ·{" "}
              {t("checksFound", { count: progress.checkCount })}
            </p>
          </CardContent>
        </Card>
      )}

      <div className="flex items-center justify-between gap-2">
        <h2 className="text-lg font-semibold">{t("suggestedChecks")}</h2>
        <Button
          onClick={handlePromote}
          disabled={selectedUids.length === 0 || promote.isPending}
        >
          <CirclePlus className="h-4 w-4 mr-1" />
          {t("promoteSelected", { count: selectedUids.length })}
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-24 w-full" />
          ))}
        </div>
      ) : groups.length > 0 ? (
        <div className="space-y-3">
          {groups.map((group) => (
            <GroupCard
              key={group.key}
              group={group}
              selected={selected}
              onToggleCheck={toggleCheck}
              onToggleGroup={toggleGroup}
              onDismissGroup={setPendingDismissGroup}
              onDismissCheck={setPendingDismissCheck}
            />
          ))}
        </div>
      ) : (
        <Card>
          <CardHeader>
            <CardTitle>{t("suggestedChecks")}</CardTitle>
            <CardDescription>{t("noChecksDescription")}</CardDescription>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">{t("noChecks")}</p>
          </CardContent>
        </Card>
      )}

      <AlertDialog
        open={!!pendingDismissCheck}
        onOpenChange={(open) => { if (!open) setPendingDismissCheck(null); }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("dismiss")}?</AlertDialogTitle>
            <AlertDialogDescription>{pendingDismissCheck?.name}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={handleDismissCheck}>{t("dismiss")}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={!!pendingDismissGroup}
        onOpenChange={(open) => { if (!open) setPendingDismissGroup(null); }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("dismissGroup")}?</AlertDialogTitle>
            <AlertDialogDescription>{pendingDismissGroup?.label}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={handleDismissGroup}>{t("dismiss")}</AlertDialogAction>
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
            <AlertDialogAction onClick={handleStop}>{t("stopScan")}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
