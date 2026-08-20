import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  useSlo,
  useSloHistory,
  useSloStatus,
  useUpdateSlo,
  type SloStatusRow,
} from "@/api/hooks";
import { QueryErrorView } from "@/components/shared/error-views";
import { StatTile } from "@/components/shared/stat-tile";
import { SloForm } from "@/components/slos/slo-form";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  budgetRemainingFraction,
  formatAttainment,
  formatBudgetSeconds,
  formatTarget,
  sloBudgetBarClass,
  sloStateBadgeClass,
} from "@/lib/slo-format";

export const Route = createFileRoute("/orgs/$org/slos/$uid")({
  component: SloDetailPage,
});

function BudgetBar({ row }: { row: SloStatusRow }) {
  const { t } = useTranslation("slos");
  const fraction = budgetRemainingFraction(row);

  return (
    <div className="space-y-1.5" data-testid="slo-budget-bar">
      <div className="h-2.5 w-full overflow-hidden rounded-full bg-muted">
        <div
          className={`h-full rounded-full ${sloBudgetBarClass(row.state)}`}
          style={{ width: `${fraction * 100}%` }}
        />
      </div>
      <p className="text-xs text-muted-foreground">
        {t("detail.budgetLabel", {
          remaining: formatBudgetSeconds(row.budgetRemainingSeconds),
          total: formatBudgetSeconds(row.budgetTotalSeconds),
        })}
      </p>
    </div>
  );
}

function SloDetailPage() {
  const { t } = useTranslation("slos");
  const { org, uid } = Route.useParams();
  const navigate = useNavigate();

  const { data: slo, isLoading, error, refetch } = useSlo(org, uid);
  const { data: status } = useSloStatus(org, uid);
  const { data: history } = useSloHistory(org, uid, 12);
  const updateSlo = useUpdateSlo(org, uid);

  if (error) {
    return <QueryErrorView error={error} onRetry={() => refetch()} />;
  }

  if (isLoading || !slo) {
    return <Skeleton className="h-64 w-full" />;
  }

  const current = status?.current;
  const attainment = formatAttainment(current?.attainmentPct ?? null);

  return (
    <div className="space-y-6">
      {current && (
        <Card data-testid="slo-status-card">
          <CardHeader className="flex flex-row items-start justify-between gap-3 space-y-0">
            <div className="min-w-0">
              <CardTitle className="truncate">{slo.name}</CardTitle>
              <p className="mt-1 text-sm text-muted-foreground">
                {t("detail.currentWindow")}: {current.window.label} ({slo.timezone})
                {current.partial ? ` · ${t("detail.partial")}` : ""}
              </p>
            </div>
            <Badge className={sloStateBadgeClass(current.state)} data-testid="slo-state">
              {t(`state.${current.state}`)}
            </Badge>
          </CardHeader>
          <CardContent className="space-y-5">
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              <StatTile
                label={t("detail.attainment")}
                // Never render null as 100%: no data means nobody was watching.
                value={attainment ?? t("detail.noData")}
                tone={current.state === "breached" ? "destructive" : "default"}
              />
              <StatTile label={t("detail.target")} value={formatTarget(current.targetPct)} />
              <StatTile
                label={t("detail.budgetRemaining")}
                value={formatBudgetSeconds(current.budgetRemainingSeconds)}
              />
              <StatTile
                label={t("detail.burnRate")}
                value={current.burnRate === null ? "—" : `${current.burnRate.toFixed(2)}x`}
              />
            </div>

            <BudgetBar row={current} />

            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              <StatTile
                label={t("detail.budgetConsumed")}
                value={formatBudgetSeconds(current.budgetConsumedSeconds)}
              />
              <StatTile
                label={t("detail.excludedMaintenance")}
                value={formatBudgetSeconds(current.excludedMaintenanceSeconds)}
              />
              <StatTile label={t("detail.incidents")} value={status?.incidents.count ?? 0} />
              <StatTile
                label={t("detail.projectedExhaustion")}
                value={
                  current.projectedExhaustionAt
                    ? new Date(current.projectedExhaustionAt).toLocaleString()
                    : t("detail.never")
                }
              />
            </div>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("detail.history")}</CardTitle>
        </CardHeader>
        <CardContent>
          {!history || history.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("detail.historyEmpty")}</p>
          ) : (
            <div className="overflow-x-auto">
              <Table data-testid="slo-history-table">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("detail.currentWindow")}</TableHead>
                    <TableHead>{t("detail.attainment")}</TableHead>
                    <TableHead>{t("detail.budgetRemaining")}</TableHead>
                    <TableHead>{t("list.columns.state")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {history.map((row) => (
                    <TableRow key={row.window.label}>
                      <TableCell className="whitespace-nowrap">{row.window.label}</TableCell>
                      <TableCell className="whitespace-nowrap">
                        {formatAttainment(row.attainmentPct) ?? (
                          <span className="text-muted-foreground">{t("detail.noData")}</span>
                        )}
                      </TableCell>
                      <TableCell className="whitespace-nowrap">
                        {formatBudgetSeconds(row.budgetRemainingSeconds)}
                      </TableCell>
                      <TableCell>
                        <Badge className={sloStateBadgeClass(row.state)}>
                          {t(`state.${row.state}`)}
                        </Badge>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      <SloForm
        key={slo.uid}
        org={org}
        mode="edit"
        initial={slo}
        isPending={updateSlo.isPending}
        onCancel={() => navigate({ to: "/orgs/$org/slos", params: { org } })}
        onSubmit={async (request) => {
          try {
            await updateSlo.mutateAsync(request);
            toast.success(t("form.updated"));
          } catch (err) {
            toast.error(err instanceof ApiError ? err.message : t("form.saveFailed"));
          }
        }}
      />
    </div>
  );
}
