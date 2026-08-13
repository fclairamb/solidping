import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { CalendarClock, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { toast } from "sonner";

import {
  useDeleteOnCallSchedule,
  useOnCallSchedules,
  type OnCallSchedule,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
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
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { QueryErrorView } from "@/components/shared/error-views";
import { PageHeader } from "@/components/shared/page-header";

export const Route = createFileRoute("/orgs/$org/on-call/")({
  component: OnCallListPage,
});

function OnCallListPage() {
  const { t } = useTranslation(["oncall", "common"]);
  const { org } = Route.useParams();
  const {
    data: schedules,
    isLoading,
    isRefetching,
    error,
    refetch,
  } = useOnCallSchedules(org);
  const deleteMutation = useDeleteOnCallSchedule(org);
  const [pendingDelete, setPendingDelete] = useState<OnCallSchedule | null>(null);

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("oncall:list.title")}
        onRetry={() => refetch()}
      />
    );
  }

  const onConfirmDelete = () => {
    if (!pendingDelete) return;
    deleteMutation.mutate(pendingDelete.uid, {
      onSuccess: () => {
        toast.success(t("oncall:detail.deleteConfirmAction"));
        setPendingDelete(null);
      },
      onError: () => toast.error(t("oncall:errors.generic")),
    });
  };

  const isEmpty = !isLoading && (!schedules || schedules.length === 0);

  return (
    <div className="space-y-6">
      <PageHeader
        icon={CalendarClock}
        title={t("oncall:list.title")}
        description={t("oncall:list.subtitle")}
        docsHref="/docs/features/on-call"
        actions={
          <Button asChild aria-label={t("oncall:list.create")}>
            <Link to="/orgs/$org/on-call/new" params={{ org }}>
              <Plus />
              <span className="hidden sm:inline">{t("oncall:list.create")}</span>
            </Link>
          </Button>
        }
        className="flex-wrap"
      />

      <div className="flex flex-wrap items-center justify-end gap-4">
        <Button
          variant="outline"
          onClick={() => refetch()}
          disabled={isRefetching}
          data-testid="oncall-refresh"
          aria-label={t("common:refresh")}
        >
          <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
          <span className="hidden sm:inline">{t("common:refresh")}</span>
        </Button>
      </div>

      <div className="rounded-md border">
        {isLoading ? (
          <div className="space-y-2 p-2">
            {[...Array(6)].map((_, i) => (
              <Skeleton key={i} className="h-12 rounded-lg" />
            ))}
          </div>
        ) : isEmpty ? (
          <p className="py-12 text-center text-sm text-muted-foreground">
            {t("oncall:list.empty")}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("oncall:list.col.name")}</TableHead>
                <TableHead>{t("oncall:list.col.timezone")}</TableHead>
                <TableHead>{t("oncall:list.col.rotation")}</TableHead>
                <TableHead>{t("oncall:list.col.currentlyOnCall")}</TableHead>
                <TableHead className="w-[100px]" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {schedules?.map((schedule) => (
                <ScheduleRow
                  key={schedule.uid}
                  org={org}
                  schedule={schedule}
                  onDelete={() => setPendingDelete(schedule)}
                />
              ))}
            </TableBody>
          </Table>
        )}
      </div>

      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(open) => (open ? null : setPendingDelete(null))}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingDelete
                ? t("oncall:detail.deleteConfirmTitle", {
                    name: pendingDelete.name,
                  })
                : ""}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("oncall:detail.deleteConfirmBody")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("oncall:form.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={onConfirmDelete}>
              {t("oncall:detail.deleteConfirmAction")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

interface ScheduleRowProps {
  org: string;
  schedule: OnCallSchedule;
  onDelete: () => void;
}

function ScheduleRow({ org, schedule, onDelete }: ScheduleRowProps) {
  const { t } = useTranslation(["oncall", "common"]);
  const rotationLabel =
    schedule.rotationType === "daily"
      ? t("oncall:detail.rotationDaily")
      : t("oncall:detail.rotationWeekly");

  return (
    <TableRow data-testid="oncall-row">
      <TableCell>
        <Link
          to="/orgs/$org/on-call/$uid"
          params={{ org, uid: schedule.uid }}
          className="font-medium hover:underline"
        >
          {schedule.name}
        </Link>
      </TableCell>
      <TableCell className="text-sm text-muted-foreground">
        {schedule.timezone}
      </TableCell>
      <TableCell className="text-sm text-muted-foreground">
        {rotationLabel}
      </TableCell>
      <TableCell className="text-sm">
        {schedule.currentlyOnCall ? (
          <span className="font-medium">{schedule.currentlyOnCall.name}</span>
        ) : (
          <span className="text-muted-foreground">
            {t("oncall:list.noOneOnCall")}
          </span>
        )}
      </TableCell>
      <TableCell>
        <div className="flex items-center justify-end gap-1">
          <Button asChild variant="ghost" size="icon" aria-label={t("oncall:detail.edit")}>
            <Link
              to="/orgs/$org/on-call/$uid/edit"
              params={{ org, uid: schedule.uid }}
            >
              <Pencil className="h-4 w-4" />
            </Link>
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="text-destructive hover:text-destructive"
            onClick={onDelete}
            aria-label={t("oncall:detail.delete")}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}
