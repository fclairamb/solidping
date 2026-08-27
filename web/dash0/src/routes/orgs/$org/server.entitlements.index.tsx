import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { AlertTriangle, Loader2, Search } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { TimeAgo } from "@/components/ui/time-ago";
import { useAdminEntitlementsList } from "@/api/hooks";
import type { AdminEntitlementsRow } from "@/api/hooks";
import { formatCheckRateDemand } from "@/lib/check-rate-limit";
import { formatLimit, provenanceOf } from "@/lib/entitlements-admin";
import { useDebounce } from "@/lib/use-debounce";

export const Route = createFileRoute("/orgs/$org/server/entitlements/")({
  component: EntitlementsListPage,
});

function EntitlementsListPage() {
  const { t } = useTranslation("server");
  const { org } = Route.useParams();
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebounce(search, 300);

  const { data, isLoading, error } = useAdminEntitlementsList({
    q: debouncedSearch,
    limit: 100,
  });

  const rows = data?.data ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("entitlements.title")}</CardTitle>
        <p className="text-sm text-muted-foreground">
          {t("entitlements.description")}
        </p>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-4">
          <div className="relative min-w-[200px] max-w-sm flex-1">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="pl-9"
              placeholder={t("entitlements.search")}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              data-testid="entitlements-search"
            />
          </div>
        </div>

        {isLoading ? (
          <div className="flex items-center justify-center py-8">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          </div>
        ) : error ? (
          <p className="text-sm text-destructive">
            {t("entitlements.loadError")}
          </p>
        ) : rows.length === 0 ? (
          <p className="py-6 text-sm text-muted-foreground">
            {t("entitlements.empty")}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("entitlements.columns.organization")}</TableHead>
                  <TableHead>{t("entitlements.columns.source")}</TableHead>
                  <TableHead className="text-right">
                    {t("entitlements.columns.maxChecks")}
                  </TableHead>
                  <TableHead className="text-right">
                    {t("entitlements.columns.maxUsers")}
                  </TableHead>
                  <TableHead className="text-right">
                    {t("entitlements.columns.maxChecksPerMinute")}
                  </TableHead>
                  <TableHead>{t("entitlements.columns.override")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((row) => (
                  <OrgRow key={row.organizationUid} org={org} row={row} />
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function OrgRow({ org, row }: { org: string; row: AdminEntitlementsRow }) {
  const { t } = useTranslation("server");
  const unlimited = t("entitlements.unlimited");
  const provenance = provenanceOf({
    source: row.source,
    displayName: row.displayName,
    stored: row.adminOverrideSince
      ? { source: "admin", updatedAt: row.adminOverrideSince }
      : undefined,
  });

  return (
    <TableRow data-testid={`entitlements-row-${row.slug}`}>
      <TableCell>
        <Link
          to="/orgs/$org/server/entitlements/$targetOrg"
          params={{ org, targetOrg: row.slug }}
          className="font-medium text-primary hover:underline"
        >
          {row.name || row.slug}
        </Link>
        <div className="text-xs text-muted-foreground">{row.slug}</div>
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-2">
          <SourceBadge kind={provenance.kind} />
          {row.displayName ? (
            <span className="text-xs text-muted-foreground">
              {row.displayEmoji ? `${row.displayEmoji} ` : ""}
              {row.displayName}
            </span>
          ) : null}
        </div>
      </TableCell>
      <TableCell className="text-right text-sm">
        {formatLimit(row.limits.maxChecks, unlimited)}
      </TableCell>
      <TableCell className="text-right text-sm">
        {formatLimit(row.limits.maxUsers, unlimited)}
      </TableCell>
      <TableCell className="text-right text-sm">
        <div className="flex items-center justify-end gap-2">
          {row.overCheckRate && row.checksPerMinute ? (
            <Badge
              variant="warning"
              className="gap-1"
              title={t("entitlements.overLimit", {
                demand: formatCheckRateDemand(row.checksPerMinute.demand),
                limit: row.checksPerMinute.limit ?? 0,
              })}
              data-testid={`entitlements-over-${row.slug}`}
            >
              <AlertTriangle className="h-3 w-3" />
              {t("entitlements.overLimitBadge")}
            </Badge>
          ) : null}
          <span>{formatLimit(row.limits.maxChecksPerMinute, unlimited)}</span>
        </div>
      </TableCell>
      <TableCell className="text-sm">
        {row.adminOverrideSince ? (
          <TimeAgo date={row.adminOverrideSince} />
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </TableCell>
    </TableRow>
  );
}

function SourceBadge({ kind }: { kind: string }) {
  const { t } = useTranslation("server");

  if (kind === "admin") {
    return (
      <Badge variant="secondary">{t("entitlements.provenance.adminPlain")}</Badge>
    );
  }

  if (kind === "billing") {
    return (
      <Badge variant="outline">
        {t("entitlements.provenance.billingPlain")}
      </Badge>
    );
  }

  return <Badge variant="outline">{t("entitlements.provenance.default")}</Badge>;
}
