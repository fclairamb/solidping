import { useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  useMaintenanceWindow,
  useMaintenanceWindowChecks,
  useDeleteMaintenanceWindow,
  useChecks,
  useCheckGroups,
} from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { QueryErrorView } from "@/components/shared/error-views";
import { MaintenanceScheduleSummary } from "@/components/shared/maintenance-schedule-summary";
import { ApiError } from "@/api/client";
import {
  computeMaintenanceStatus,
  maintenanceStatusBadgeVariant,
} from "@/lib/maintenance-window-status";
import {
  formatOccurrenceDate,
  nextOccurrences,
} from "@/lib/maintenance-window-schedule";

export const Route = createFileRoute(
  "/orgs/$org/maintenance-windows/$maintenanceWindowUid/",
)({
  component: MaintenanceWindowDetailPage,
});

// A concrete instant (one-time start/end): browser local time, zone shown.
function formatDateTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZoneName: "short",
  });
}

// A recurrence boundary (recurrenceEnd): UTC-defined, render the UTC date so it
// matches what the form set. No zone label needed on a bare date.
function formatRecurrenceEnd(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  });
}

function MaintenanceWindowDetailPage() {
  const { t } = useTranslation(["maintenanceWindows", "common"]);
  const { org, maintenanceWindowUid } = Route.useParams();
  const navigate = useNavigate();
  const [deleteOpen, setDeleteOpen] = useState(false);

  const {
    data: window,
    isLoading,
    error,
    refetch,
  } = useMaintenanceWindow(org, maintenanceWindowUid);
  const { data: checkAssocs } = useMaintenanceWindowChecks(
    org,
    maintenanceWindowUid,
  );
  // Resolve associated UIDs to display names. Both lists are small per org and
  // already cached by other views.
  const { data: checks } = useChecks(org);
  const { data: groups } = useCheckGroups(org);

  const deleteWindow = useDeleteMaintenanceWindow(org);

  const checkNameByUid = useMemo(() => {
    const map = new Map<string, string>();
    for (const c of checks ?? []) {
      if (c.uid) map.set(c.uid, c.name || c.slug || c.uid);
    }
    return map;
  }, [checks]);

  const groupNameByUid = useMemo(() => {
    const map = new Map<string, string>();
    for (const g of groups ?? []) map.set(g.uid, g.name);
    return map;
  }, [groups]);

  const affectedChecks = (checkAssocs ?? []).filter((a) => a.checkUid);
  const affectedGroups = (checkAssocs ?? []).filter((a) => a.checkGroupUid);

  const handleDelete = async () => {
    try {
      await deleteWindow.mutateAsync(maintenanceWindowUid);
      toast.success(t("maintenanceWindows:toast.deleted"));
      navigate({ to: "/orgs/$org/maintenance-windows", params: { org } });
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("maintenanceWindows:toast.deleteFailed"),
      );
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-40 rounded-lg" />
        <Skeleton className="h-40 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("maintenanceWindows:title")}
        backTo="/orgs/$org/maintenance-windows"
        backLabel={t("maintenanceWindows:backToList")}
        onRetry={() => refetch()}
      />
    );
  }

  if (!window) return null;

  // Prefer the server-computed lifecycle and occurrences when present (newer
  // backends), falling back to the client port otherwise.
  const status = window.status ?? computeMaintenanceStatus(window);
  const occurrences =
    window.nextOccurrences ?? nextOccurrences(window, new Date(), 3);

  return (
    <div className="space-y-6">
      <div className="flex items-start gap-3 justify-between">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <h1 className="text-2xl sm:text-3xl font-bold tracking-tight truncate">
              {window.title}
            </h1>
            <Badge variant={maintenanceStatusBadgeVariant(status)}>
              {t(`maintenanceWindows:status.${status}`)}
            </Badge>
          </div>
          {window.description && (
            <p className="text-muted-foreground mt-1">{window.description}</p>
          )}
        </div>
        <div className="flex gap-2 shrink-0">
          <Link to="/orgs/$org/maintenance-windows" params={{ org }}>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t("maintenanceWindows:backToList")}
            >
              <ArrowLeft className="h-4 w-4" />
            </Button>
          </Link>
          <Tooltip>
            <TooltipTrigger asChild>
              <Link
                to="/orgs/$org/maintenance-windows/$maintenanceWindowUid/edit"
                params={{ org, maintenanceWindowUid }}
              >
                <Button
                  variant="outline"
                  size="sm"
                  aria-label={t("maintenanceWindows:edit")}
                  data-testid="mw-detail-edit-button"
                >
                  <Pencil className="sm:mr-2 h-4 w-4" />
                  <span className="hidden sm:inline">
                    {t("maintenanceWindows:edit")}
                  </span>
                </Button>
              </Link>
            </TooltipTrigger>
            <TooltipContent className="sm:hidden">
              {t("maintenanceWindows:edit")}
            </TooltipContent>
          </Tooltip>
          <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="outline"
                  size="sm"
                  className="text-destructive hover:text-destructive"
                  onClick={() => setDeleteOpen(true)}
                  aria-label={t("common:delete")}
                  data-testid="mw-detail-delete-button"
                >
                  <Trash2 className="sm:mr-2 h-4 w-4" />
                  <span className="hidden sm:inline">{t("common:delete")}</span>
                </Button>
              </TooltipTrigger>
              <TooltipContent className="sm:hidden">
                {t("common:delete")}
              </TooltipContent>
            </Tooltip>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  {t("maintenanceWindows:delete.confirmTitle")}
                </AlertDialogTitle>
                <AlertDialogDescription>
                  {t("maintenanceWindows:delete.confirmBody")}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
                <AlertDialogAction
                  onClick={handleDelete}
                  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                >
                  {t("common:delete")}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("maintenanceWindows:detail.schedule")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-6">
          {/* Plain-language summary + next-occurrence preview (prefers server
              data via the occurrences passed in). */}
          <MaintenanceScheduleSummary
            window={window}
            occurrences={occurrences}
          />

          <div>
            <p className="text-sm font-medium mb-2">
              {t("maintenanceWindows:detail.nextOccurrences")}
            </p>
            {occurrences.length > 0 ? (
              <ul
                className="space-y-1 text-sm"
                data-testid="mw-detail-next-occurrences"
              >
                {occurrences.map((o) => (
                  <li key={o.startAt} className="text-muted-foreground">
                    {formatOccurrenceDate(o.startAt)} –{" "}
                    {formatOccurrenceDate(o.endAt)}
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-sm text-muted-foreground">
                {t("maintenanceWindows:detail.noNextOccurrences")}
              </p>
            )}
          </div>

          {/* Raw fields kept below the summary as detail. */}
          <div className="grid gap-4 sm:grid-cols-2 border-t pt-4">
            <div>
              <p className="text-sm text-muted-foreground">
                {t("maintenanceWindows:detail.start")}
              </p>
              <p className="font-medium">{formatDateTime(window.startAt)}</p>
            </div>
            <div>
              <p className="text-sm text-muted-foreground">
                {t("maintenanceWindows:detail.end")}
              </p>
              <p className="font-medium">{formatDateTime(window.endAt)}</p>
            </div>
            <div>
              <p className="text-sm text-muted-foreground">
                {t("maintenanceWindows:detail.recurrence")}
              </p>
              <p className="font-medium">
                {t(`maintenanceWindows:recurrence.${window.recurrence}`)}
              </p>
            </div>
            {window.recurrence !== "none" && (
              <div>
                <p className="text-sm text-muted-foreground">
                  {t("maintenanceWindows:detail.recurrenceEnd")}
                </p>
                <p className="font-medium">
                  {window.recurrenceEnd
                    ? formatRecurrenceEnd(window.recurrenceEnd)
                    : "—"}
                </p>
              </div>
            )}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("maintenanceWindows:detail.affectedChecks")}</CardTitle>
        </CardHeader>
        <CardContent>
          {affectedChecks.length === 0 && affectedGroups.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t("maintenanceWindows:detail.noAffected")}
            </p>
          ) : (
            <div className="flex flex-wrap gap-2">
              {affectedChecks.map((a) => (
                <Link
                  key={a.uid}
                  to="/orgs/$org/checks/$checkUid"
                  params={{ org, checkUid: a.checkUid! }}
                  search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
                >
                  <Badge variant="secondary" className="hover:bg-secondary/70">
                    {checkNameByUid.get(a.checkUid!) ?? a.checkUid!.slice(0, 8)}
                  </Badge>
                </Link>
              ))}
              {affectedGroups.map((a) => (
                <Badge key={a.uid} variant="outline">
                  {groupNameByUid.get(a.checkGroupUid!) ??
                    a.checkGroupUid!.slice(0, 8)}
                </Badge>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
