import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Network, Plus, RefreshCw, Scan } from "lucide-react";

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
  useDiscoveryTypes,
  useListDiscoveryScans,
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
function scanSource(type: string): string {
  switch (type) {
    case "freebox_lan_discovery":
      return "freebox";
    case "container_discovery":
      return "container";
    case "kubernetes_discovery":
      return "kubernetes";
    default:
      return "lan";
  }
}

// scanDetails returns a generic, source-aware one-line summary of a scan's
// config, replacing the old LAN-only "CIDRs" cell: CIDRs for LAN, hosts for
// container, namespaces for kubernetes (empty ⇒ "All namespaces"), "—" otherwise.
function scanDetails(
  source: string,
  config: Record<string, unknown> | undefined,
  allNamespacesLabel: string,
): string {
  const cfg = config ?? {};
  const joinList = (key: string) =>
    Array.isArray(cfg[key]) ? (cfg[key] as unknown[]).map(String).join(", ") : "";
  switch (source) {
    case "lan":
      return joinList("cidrs") || "—";
    case "container":
      return joinList("hosts") || "—";
    case "kubernetes":
      return joinList("namespaces") || allNamespacesLabel;
    default:
      return "—";
  }
}

function ScanRow({ scan, org }: { scan: DiscoveryScan; org: string }) {
  const { t } = useTranslation("discovery");
  const navigate = useNavigate();
  const statusLabel = t(`scanStatus.${scan.status}`, scan.status);
  const source = scanSource(scan.type);
  const details = scanDetails(
    source,
    scan.config as Record<string, unknown> | undefined,
    t("allNamespaces"),
  );

  const open = () =>
    void navigate({
      to: "/orgs/$org/discovery/$jobUid",
      params: { org, jobUid: scan.uid },
    });

  return (
    <TableRow
      className="cursor-pointer hover:bg-muted/50"
      role="link"
      tabIndex={0}
      onClick={open}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          open();
        }
      }}
    >
      <TableCell>
        <Badge variant="outline">{t(`sourceLabel.${source}`, source)}</Badge>
      </TableCell>
      <TableCell>
        <Badge variant={statusBadgeVariant(scan.status)}>{statusLabel}</Badge>
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">{details}</TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {new Date(scan.createdAt).toLocaleString()}
      </TableCell>
    </TableRow>
  );
}

function DiscoveryIndexPage() {
  const { t } = useTranslation("discovery");
  const { org } = Route.useParams();
  const { data: scans, isLoading, isRefetching, refetch } = useListDiscoveryScans(org);
  const { data: types } = useDiscoveryTypes(org);
  const [sourceFilter, setSourceFilter] = useState<string>("all");

  // Source filter options are driven by the registry (sources of registered
  // types), falling back to the known sources so the filter is never empty.
  const sourceOptions = useMemo(() => {
    const fromRegistry = Array.from(new Set((types ?? []).map((d) => d.source)));
    return fromRegistry.length > 0 ? fromRegistry : ["lan", "freebox"];
  }, [types]);

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
        docsHref="/docs/features/discovery"
        className="flex-wrap"
        actions={
          <>
            <Button
              variant="outline"
              onClick={() => void refetch()}
              disabled={isRefetching}
              aria-label={t("refresh")}
            >
              <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
              <span className="hidden sm:inline">{t("refresh")}</span>
            </Button>
            <Button asChild>
              <Link
                to="/orgs/$org/discovery/new"
                params={{ org }}
                search={{ method: "lan" }}
                aria-label={t("newScan")}
              >
                <Plus className="sm:mr-2 h-4 w-4" />
                <span className="hidden sm:inline">{t("newScan")}</span>
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
            <Select value={sourceFilter} onValueChange={setSourceFilter}>
              <SelectTrigger className="w-40" aria-label={t("filterBySource")}>
                <SelectValue placeholder={t("filterBySource")} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t("allSources")}</SelectItem>
                {sourceOptions.map((src) => (
                  <SelectItem key={src} value={src}>
                    {t(`sourceLabel.${src}`, src)}
                  </SelectItem>
                ))}
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
                  <TableHead>{t("details")}</TableHead>
                  <TableHead>{t("startedAt")}</TableHead>
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
