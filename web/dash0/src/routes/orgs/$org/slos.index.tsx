import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Flame, Layers, Pencil, Plus, Target, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { useDeleteSlo, useSloStatus, useSlos, type Slo } from "@/api/hooks";
import { PageHeader } from "@/components/shared/page-header";
import { QueryErrorView } from "@/components/shared/error-views";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
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

export const Route = createFileRoute("/orgs/$org/slos/")({
  component: SlosIndexPage,
});

/**
 * One row's live evaluation. Each objective fetches its own status: the scopes
 * are independent, and one slow group SLO must not hold up the whole table.
 */
function SloRow({ slo, org, onDelete }: { slo: Slo; org: string; onDelete: (slo: Slo) => void }) {
  const { t } = useTranslation("slos");
  const { data: status, isLoading } = useSloStatus(org, slo.uid);

  const current = status?.current;
  const attainment = formatAttainment(current?.attainmentPct ?? null);
  const state = current?.state ?? "unknown";

  return (
    <TableRow className="transition-colors hover:bg-muted/40" data-testid="slo-row">
      <TableCell>
        <Link
          to="/orgs/$org/slos/$uid"
          params={{ org, uid: slo.uid }}
          className="font-medium hover:underline"
          data-testid="slo-row-name"
        >
          {slo.name}
        </Link>
        <div className="text-xs text-muted-foreground">{slo.slug}</div>
      </TableCell>
      <TableCell>
        <span className="inline-flex items-center gap-1.5 text-sm">
          {slo.checkGroupUid ? (
            <Layers className="h-3.5 w-3.5 text-muted-foreground" />
          ) : (
            <Target className="h-3.5 w-3.5 text-muted-foreground" />
          )}
          <span className="truncate">
            {slo.checkName ?? slo.checkGroupName ?? t("scope.missing")}
          </span>
        </span>
      </TableCell>
      <TableCell className="whitespace-nowrap">{formatTarget(slo.targetPct)}</TableCell>
      <TableCell className="whitespace-nowrap" data-testid="slo-row-attainment">
        {isLoading ? (
          <Skeleton className="h-4 w-16" />
        ) : (
          // Null attainment renders as an em dash. It is NOT 100%.
          (attainment ?? <span className="text-muted-foreground">{t("detail.noData")}</span>)
        )}
      </TableCell>
      <TableCell className="min-w-[9rem]">
        {current ? (
          <div className="space-y-1">
            <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
              <div
                className={`h-full rounded-full ${sloBudgetBarClass(state)}`}
                style={{ width: `${budgetRemainingFraction(current) * 100}%` }}
              />
            </div>
            <div className="text-xs text-muted-foreground">
              {formatBudgetSeconds(current.budgetRemainingSeconds)}
            </div>
          </div>
        ) : (
          <Skeleton className="h-4 w-20" />
        )}
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge className={sloStateBadgeClass(state)} data-testid="slo-row-state">
            {t(`state.${state}`)}
          </Badge>
          {/* Burning is orthogonal to the window state: an objective can still
              be "healthy" on the month and be spending it far too fast today. */}
          {slo.burning && (
            <Badge variant="destructive" data-testid="slo-row-burning">
              <Flame className="mr-1 h-3 w-3" />
              {t("alerting.burning")}
            </Badge>
          )}
        </div>
      </TableCell>
      <TableCell className="text-right">
        <div className="flex justify-end gap-1">
          <Button asChild variant="ghost" size="icon" aria-label={t("detail.edit")}>
            <Link to="/orgs/$org/slos/$uid/edit" params={{ org, uid: slo.uid }}>
              <Pencil className="h-4 w-4" />
            </Link>
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="text-destructive"
            aria-label={t("delete.action")}
            data-testid="slo-row-delete"
            onClick={() => onDelete(slo)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

function SlosIndexPage() {
  const { t } = useTranslation("slos");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const { data: slos, isLoading, error, refetch } = useSlos(org);
  const deleteSlo = useDeleteSlo(org);
  const [pendingDelete, setPendingDelete] = useState<Slo | null>(null);

  if (error) {
    return <QueryErrorView error={error} org={org} onRetry={() => refetch()} />;
  }

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Target}
        title={t("layout.title")}
        description={t("layout.subtitle")}
        actions={
          <Button
            onClick={() => navigate({ to: "/orgs/$org/slos/new", params: { org } })}
            data-testid="slo-new"
          >
            <Plus className="mr-1.5 h-4 w-4" />
            {t("list.new")}
          </Button>
        }
      />

      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : !slos || slos.length === 0 ? (
        <div
          className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card"
          data-testid="slo-empty"
        >
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Target className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">{t("list.empty")}</p>
          <p className="mx-auto max-w-sm text-xs text-muted-foreground">
            {t("list.emptyHint")}
          </p>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <Table data-testid="slo-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t("list.columns.name")}</TableHead>
                <TableHead>{t("list.columns.scope")}</TableHead>
                <TableHead>{t("list.columns.target")}</TableHead>
                <TableHead>{t("list.columns.attainment")}</TableHead>
                <TableHead>{t("list.columns.budget")}</TableHead>
                <TableHead>{t("list.columns.state")}</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {slos.map((slo) => (
                <SloRow key={slo.uid} slo={slo} org={org} onDelete={setPendingDelete} />
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(open) => !open && setPendingDelete(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("delete.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("delete.description", { name: pendingDelete?.name ?? "" }).replace(
                /<\/?strong>/g,
                "",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("delete.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              data-testid="slo-delete-confirm"
              onClick={async () => {
                if (!pendingDelete) return;
                try {
                  await deleteSlo.mutateAsync(pendingDelete.uid);
                  toast.success(t("delete.deleted"));
                } catch (err) {
                  toast.error(err instanceof ApiError ? err.message : t("delete.failed"));
                } finally {
                  setPendingDelete(null);
                }
              }}
            >
              <Trash2 className="mr-1.5 h-4 w-4" />
              {t("delete.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
