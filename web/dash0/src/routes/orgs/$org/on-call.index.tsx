import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  CalendarClock,
  Globe,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  Users,
} from "lucide-react";
import { toast } from "sonner";

import {
  useDeleteOnCallSchedule,
  useOnCallSchedules,
  type OnCallSchedule,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
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
          <>
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
            <Button asChild aria-label={t("oncall:list.create")}>
              <Link to="/orgs/$org/on-call/new" params={{ org }}>
                <Plus />
                <span className="hidden sm:inline">{t("oncall:list.create")}</span>
              </Link>
            </Button>
          </>
        }
        className="flex-wrap"
      />

      <div className="rounded-xl border bg-card shadow-card overflow-hidden">
        {isLoading ? (
          <div className="space-y-2 p-4">
            {[...Array(3)].map((_, i) => (
              <Skeleton key={i} className="h-14 rounded-lg" />
            ))}
          </div>
        ) : isEmpty ? (
          <div className="py-16 text-center space-y-3">
            <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
              <Users className="h-6 w-6 text-muted-foreground" />
            </div>
            <div className="space-y-1">
              <h3 className="font-semibold text-sm">{t("oncall:list.empty")}</h3>
              <p className="text-xs text-muted-foreground max-w-sm mx-auto">
                {t("oncall:list.emptyHint")}
              </p>
            </div>
            <Button asChild size="sm">
              <Link to="/orgs/$org/on-call/new" params={{ org }}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                {t("oncall:list.create")}
              </Link>
            </Button>
          </div>
        ) : (
          <Table>
            <TableHeader className="bg-muted/30">
              <TableRow>
                <TableHead>{t("oncall:list.col.name")}</TableHead>
                <TableHead>{t("oncall:list.col.timezone")}</TableHead>
                <TableHead>{t("oncall:list.col.rotation")}</TableHead>
                <TableHead>{t("oncall:list.col.currentlyOnCall")}</TableHead>
                <TableHead className="w-[100px] text-right" />
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

  const initials = schedule.currentlyOnCall?.name
    ? schedule.currentlyOnCall.name
        .split(" ")
        .map((p) => p[0])
        .join("")
        .toUpperCase()
        .slice(0, 2)
    : "—";

  return (
    <TableRow data-testid="oncall-row" className="hover:bg-muted/40 transition-colors">
      <TableCell>
        <Link
          to="/orgs/$org/on-call/$uid"
          params={{ org, uid: schedule.uid }}
          className="font-medium text-foreground hover:text-primary hover:underline transition-colors"
        >
          {schedule.name}
        </Link>
      </TableCell>
      <TableCell className="text-sm text-muted-foreground">
        <div className="flex items-center gap-1.5 font-mono text-xs">
          <Globe className="h-3.5 w-3.5 text-muted-foreground/70" />
          {schedule.timezone}
        </div>
      </TableCell>
      <TableCell>
        <Badge variant="outline" className="text-xs font-normal">
          {rotationLabel}
        </Badge>
      </TableCell>
      <TableCell>
        {schedule.currentlyOnCall ? (
          <div className="flex items-center gap-2">
            <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-[10px] font-bold text-primary">
              {initials}
            </span>
            <div className="flex flex-col">
              <span className="font-medium text-sm text-foreground">{schedule.currentlyOnCall.name}</span>
            </div>
            <span className="flex h-2 w-2 rounded-full bg-emerald-500" title="Active on duty" />
          </div>
        ) : (
          <span className="text-muted-foreground text-xs">
            {t("oncall:list.noOneOnCall")}
          </span>
        )}
      </TableCell>
      <TableCell className="text-right">
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
