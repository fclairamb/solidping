import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Plus, RefreshCw, Scan } from "lucide-react";

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
import { useListDiscoveryScans, type DiscoveryScan } from "@/api/hooks";

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

function ScanRow({ scan, org }: { scan: DiscoveryScan; org: string }) {
  const { t } = useTranslation("discovery");
  const statusLabel = t(`scanStatus.${scan.status}`, scan.status);

  return (
    <TableRow>
      <TableCell className="font-mono text-xs text-muted-foreground">
        {scan.uid.slice(0, 8)}…
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

function DiscoveryIndexPage() {
  const { t } = useTranslation("discovery");
  const { org } = Route.useParams();
  const { data: scans, isLoading, isRefetching, refetch } = useListDiscoveryScans(org);

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">{t("title")}</h1>
          <p className="text-sm text-muted-foreground">{t("subtitle")}</p>
        </div>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="icon"
            onClick={() => void refetch()}
            disabled={isRefetching}
            aria-label="Refresh"
          >
            <RefreshCw className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />
          </Button>
          <Button asChild>
            <Link to="/orgs/$org/discovery/new" params={{ org }}>
              <Plus className="h-4 w-4 mr-1" />
              {t("newScan")}
            </Link>
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Scan className="h-5 w-5" />
            {t("scans")}
          </CardTitle>
          {!isLoading && (!scans || scans.length === 0) && (
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
          ) : scans && scans.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("ip")}</TableHead>
                  <TableHead>{t("status")}</TableHead>
                  <TableHead>{t("cidrs")}</TableHead>
                  <TableHead>{t("startedAt")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {scans.map((scan) => (
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
