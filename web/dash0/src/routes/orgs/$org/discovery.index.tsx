import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Network, Plus, RefreshCw, Scan, Wifi } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { PageHeader } from "@/components/shared/page-header";
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  useChannels,
  useListDiscoveryScans,
  useStartFreeboxScan,
  canSource,
  type DiscoveryScan,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/discovery/")({
  component: DiscoveryIndexPage,
});

function statusBadgeVariant(status: string): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "success": return "default";
    case "running": return "secondary";
    case "failed": return "destructive";
    default: return "outline";
  }
}

// scanSource derives the discovery source from the underlying job type.
function scanSource(type: string): "lan" | "freebox" {
  return type === "freebox_lan_discovery" ? "freebox" : "lan";
}

function ScanRow({ scan, org }: { scan: DiscoveryScan; org: string }) {
  const { t } = useTranslation("discovery");
  const statusLabel = t(`scanStatus.${scan.status}`, scan.status);
  const source = scanSource(scan.type);

  return (
    <TableRow>
      <TableCell>
        <Badge variant="outline">
          {t(source === "freebox" ? "sourceFreebox" : "sourceLan")}
        </Badge>
      </TableCell>
      <TableCell>
        <Badge variant={statusBadgeVariant(scan.status)}>{statusLabel}</Badge>
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {Array.isArray((scan.config as { cidrs?: string[] })?.cidrs)
          ? ((scan.config as { cidrs?: string[] }).cidrs ?? []).join(", ")
          : "—"}
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {new Date(scan.createdAt).toLocaleString()}
      </TableCell>
      <TableCell className="text-right">
        <Button asChild variant="ghost" size="sm">
          <Link to="/orgs/$org/discovery/$jobUid" params={{ org, jobUid: scan.uid }}>
            {t("hosts")}
          </Link>
        </Button>
      </TableCell>
    </TableRow>
  );
}

function FreeboxLauncher({ org }: { org: string }) {
  const { t } = useTranslation("discovery");
  const { data: channels } = useChannels(org);
  const startFreebox = useStartFreeboxScan(org);

  // Source integrations (canSource) that can drive a discovery scan. Today the
  // active-scan launcher is Freebox-only; capability filtering keeps it open to
  // future source types without another type-literal carve-out.
  const freeboxChannels = useMemo(
    () => (channels ?? []).filter((c) => canSource(c.type)),
    [channels],
  );

  const handleSelect = (channelUid: string) => {
    startFreebox.mutate(channelUid, {
      onSuccess: () => toast.success(t("freeboxScanStarted")),
      onError: () => toast.error(t("freeboxScanFailed")),
    });
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" disabled={startFreebox.isPending}>
          <Wifi className="h-4 w-4 mr-1" />
          {t("discoverViaFreebox")}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {freeboxChannels.length === 0 ? (
          <DropdownMenuItem disabled>{t("noFreeboxChannels")}</DropdownMenuItem>
        ) : (
          freeboxChannels.map((c) => (
            <DropdownMenuItem key={c.uid} onSelect={() => handleSelect(c.uid)}>
              {c.name}
            </DropdownMenuItem>
          ))
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function DiscoveryIndexPage() {
  const { t } = useTranslation("discovery");
  const { org } = Route.useParams();
  const { data: scans, isLoading, isRefetching, refetch } = useListDiscoveryScans(org);
  const [sourceFilter, setSourceFilter] = useState<"all" | "lan" | "freebox">("all");

  const filteredScans = useMemo(() => {
    if (!scans) return scans;
    if (sourceFilter === "all") return scans;
    return scans.filter((s) => scanSource(s.type) === sourceFilter);
  }, [scans, sourceFilter]);

  return (
    <div className="space-y-4">
      <PageHeader
        icon={Network}
        title={t("title")}
        description={t("subtitle")}
        className="flex-wrap"
        actions={
          <>
            <Button
              variant="outline"
              size="icon"
              onClick={() => void refetch()}
              disabled={isRefetching}
              aria-label="Refresh"
            >
              <RefreshCw className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />
            </Button>
            <FreeboxLauncher org={org} />
            <Button asChild>
              <Link to="/orgs/$org/discovery/new" params={{ org }}>
                <Plus className="h-4 w-4 mr-1" />
                {t("newScan")}
              </Link>
            </Button>
          </>
        }
      />

      <Card>
        <CardHeader>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <CardTitle className="flex items-center gap-2">
              <Scan className="h-5 w-5" />
              {t("scans")}
            </CardTitle>
            <Select
              value={sourceFilter}
              onValueChange={(v) => setSourceFilter(v as "all" | "lan" | "freebox")}
            >
              <SelectTrigger className="w-40" aria-label={t("filterBySource")}>
                <SelectValue placeholder={t("filterBySource")} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t("allSources")}</SelectItem>
                <SelectItem value="lan">{t("sourceLan")}</SelectItem>
                <SelectItem value="freebox">{t("sourceFreebox")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {!isLoading && (!filteredScans || filteredScans.length === 0) && (
            <CardDescription>{t("noScansDescription")}</CardDescription>
          )}
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="space-y-2">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : filteredScans && filteredScans.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("source")}</TableHead>
                  <TableHead>{t("status")}</TableHead>
                  <TableHead>{t("cidrs")}</TableHead>
                  <TableHead>{t("startedAt")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {filteredScans.map((scan) => (
                  <ScanRow key={scan.uid} scan={scan} org={org} />
                ))}
              </TableBody>
            </Table>
          ) : (
            <p className="text-sm text-muted-foreground">{t("noScans")}</p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
