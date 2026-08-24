import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Mail, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  useDeleteReportSchedule,
  useReportSchedules,
  type ReportSchedule,
} from "@/api/hooks";
import { PageHeader } from "@/components/shared/page-header";
import { QueryErrorView } from "@/components/shared/error-views";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { TimeAgo } from "@/components/ui/time-ago";
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

export const Route = createFileRoute("/orgs/$org/organization/report-schedules/")({
  component: ReportSchedulesIndexPage,
});

function ReportSchedulesIndexPage() {
  const { t } = useTranslation(["slos", "common"]);
  const { org } = Route.useParams();
  const { data: schedules, isLoading, error, refetch } = useReportSchedules(org);
  const deleteSchedule = useDeleteReportSchedule(org);
  const [pendingDelete, setPendingDelete] = useState<ReportSchedule | null>(null);

  if (error) {
    return <QueryErrorView error={error} org={org} onRetry={() => refetch()} />;
  }

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Mail}
        title={t("reports.title")}
        description={t("reports.subtitle")}
        docsHref="/docs/features/slos#uptime-reports"
        actions={
          <Button asChild data-testid="report-new">
            <Link to="/orgs/$org/organization/report-schedules/new" params={{ org }}>
              <Plus className="mr-1 h-4 w-4" />
              {t("reports.new")}
            </Link>
          </Button>
        }
      />

      {isLoading ? (
        <div className="space-y-2">
          {[...Array(4)].map((_, i) => (
            <Skeleton key={i} className="h-14 rounded-lg" />
          ))}
        </div>
      ) : !schedules || schedules.length === 0 ? (
        <div
          className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card"
          data-testid="report-empty"
        >
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Mail className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">{t("reports.empty")}</p>
          <p className="mx-auto max-w-sm text-xs text-muted-foreground">
            {t("reports.emptyHint")}
          </p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card shadow-card">
          {/* The card clips to its radius, so the table needs its own scroll
              container — without it the trailing columns (and the row actions)
              are unreachable on a narrow viewport rather than merely offscreen. */}
          <div className="overflow-x-auto">
          <Table data-testid="report-table">
            <TableHeader className="bg-muted/30">
              <TableRow>
                <TableHead>{t("reports.columns.name")}</TableHead>
                <TableHead>{t("reports.columns.frequency")}</TableHead>
                <TableHead>{t("reports.columns.recipients")}</TableHead>
                <TableHead>{t("reports.columns.scope")}</TableHead>
                <TableHead>{t("reports.columns.lastRun")}</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {schedules.map((schedule) => (
                <TableRow
                  key={schedule.uid}
                  data-testid="report-row"
                  className="transition-colors hover:bg-muted/40"
                >
                  <TableCell>
                    <div className="flex items-center gap-3">
                      <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-muted/60">
                        <Mail className="h-3.5 w-3.5 text-muted-foreground/70" />
                      </span>
                      <Link
                        to="/orgs/$org/organization/report-schedules/$uid"
                        params={{ org, uid: schedule.uid }}
                        className="font-medium text-foreground transition-colors hover:text-primary hover:underline"
                      >
                        {schedule.name}
                      </Link>
                      {!schedule.enabled && (
                        <Badge variant="secondary" className="font-normal">
                          {t("common:disabled")}
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>{t(`reports.frequency.${schedule.frequency}`)}</TableCell>
                  <TableCell>
                    {/*
                      Deliberately a COUNT, not the addresses: recipients are
                      PII and a list page is the easiest place for them to be
                      shoulder-surfed or screenshotted. The edit page shows them.
                    */}
                    {t("reports.recipientCount", { count: schedule.recipients.length })}
                  </TableCell>
                  <TableCell>
                    {schedule.checkUids.length === 0 && schedule.checkGroupUids.length === 0
                      ? t("reports.scopeOrgWide")
                      : t("reports.scopeCounts", {
                          checks: schedule.checkUids.length,
                          groups: schedule.checkGroupUids.length,
                        })}
                  </TableCell>
                  <TableCell>
                    {schedule.lastRunAt ? (
                      <TimeAgo date={schedule.lastRunAt} />
                    ) : (
                      <span className="text-muted-foreground">{t("reports.never")}</span>
                    )}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button asChild variant="ghost" size="icon" aria-label={t("common:edit")}>
                        <Link
                          to="/orgs/$org/organization/report-schedules/$uid"
                          params={{ org, uid: schedule.uid }}
                        >
                          <Pencil className="h-4 w-4" />
                        </Link>
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="text-destructive hover:text-destructive"
                        aria-label={t("common:delete")}
                        data-testid="report-row-delete"
                        onClick={() => setPendingDelete(schedule)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          </div>
        </div>
      )}

      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(open) => !open && setPendingDelete(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("reports.delete.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("reports.delete.description", { name: pendingDelete?.name ?? "" }).replace(
                /<\/?strong>/g,
                "",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("reports.delete.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              data-testid="report-delete-confirm"
              onClick={async () => {
                if (!pendingDelete) return;
                try {
                  await deleteSchedule.mutateAsync(pendingDelete.uid);
                  toast.success(t("reports.delete.deleted"));
                } catch (err) {
                  toast.error(
                    err instanceof ApiError ? err.message : t("reports.delete.failed"),
                  );
                } finally {
                  setPendingDelete(null);
                }
              }}
            >
              <Trash2 className="mr-1.5 h-4 w-4" />
              {t("reports.delete.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
