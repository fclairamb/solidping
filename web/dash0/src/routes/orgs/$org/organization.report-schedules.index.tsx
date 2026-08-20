import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Mail, Pencil, Plus, Send, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  useDeleteReportSchedule,
  useReportSchedules,
  useTestReportSchedule,
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
  const { t } = useTranslation("slos");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const { data: schedules, isLoading, error, refetch } = useReportSchedules(org);
  const deleteSchedule = useDeleteReportSchedule(org);
  const testSchedule = useTestReportSchedule(org);
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
        actions={
          <Button
            data-testid="report-new"
            onClick={() =>
              navigate({
                to: "/orgs/$org/organization/report-schedules/new",
                params: { org },
              })
            }
          >
            <Plus className="mr-1.5 h-4 w-4" />
            {t("reports.new")}
          </Button>
        }
      />

      {isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : !schedules || schedules.length === 0 ? (
        <div
          className="rounded-lg border border-dashed p-8 text-center"
          data-testid="report-empty"
        >
          <p className="font-medium">{t("reports.empty")}</p>
          <p className="mt-1 text-sm text-muted-foreground">{t("reports.emptyHint")}</p>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <Table data-testid="report-table">
            <TableHeader>
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
                <TableRow key={schedule.uid} data-testid="report-row">
                  <TableCell>
                    <Link
                      to="/orgs/$org/organization/report-schedules/$uid"
                      params={{ org, uid: schedule.uid }}
                      className="font-medium hover:underline"
                    >
                      {schedule.name}
                    </Link>
                    {!schedule.enabled && (
                      <Badge variant="secondary" className="ml-2">
                        {t("reports.form.enabled")}: —
                      </Badge>
                    )}
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
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={t("reports.test")}
                        title={t("reports.test")}
                        data-testid="report-row-test"
                        onClick={async () => {
                          try {
                            await testSchedule.mutateAsync(schedule.uid);
                            toast.success(t("reports.testSent"));
                          } catch (err) {
                            toast.error(
                              err instanceof ApiError ? err.message : t("reports.testFailed"),
                            );
                          }
                        }}
                      >
                        <Send className="h-4 w-4" />
                      </Button>
                      <Button asChild variant="ghost" size="icon" aria-label={t("form.save")}>
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
                        className="text-destructive"
                        aria-label={t("delete.action")}
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
              className="bg-destructive text-white hover:bg-destructive/90"
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
