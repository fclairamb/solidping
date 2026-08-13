import { useEffect, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Plus, Search, RefreshCw, Pencil, Trash2, Wrench } from "lucide-react";
import { toast } from "sonner";
import {
  useMaintenanceWindows,
  useMaintenanceWindowChecks,
  useDeleteMaintenanceWindow,
  type MaintenanceWindow,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
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
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Card, CardContent } from "@/components/ui/card";
import { QueryErrorView } from "@/components/shared/error-views";
import { PageHeader } from "@/components/shared/page-header";
import { ApiError } from "@/api/client";
import {
  computeMaintenanceStatus,
  maintenanceStatusBadgeVariant,
} from "@/lib/maintenance-window-status";
import { describeSchedule } from "@/lib/maintenance-window-schedule";
import { useDebounce } from "@/lib/use-debounce";

interface MaintenanceWindowsIndexSearch {
  q?: string;
}

export const Route = createFileRoute("/orgs/$org/maintenance-windows/")({
  component: MaintenanceWindowsIndexPage,
  validateSearch: (search: Record<string, unknown>): MaintenanceWindowsIndexSearch => ({
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  }),
});

function ChecksCountCell({ org, uid }: { org: string; uid: string }) {
  const { t } = useTranslation("maintenanceWindows");
  const { data: checks } = useMaintenanceWindowChecks(org, uid);
  const count = checks?.length ?? 0;
  return (
    <span className="text-muted-foreground">
      {t("checksCount", { count })}
    </span>
  );
}

function MaintenanceWindowRow({
  window,
  org,
  onDelete,
}: {
  window: MaintenanceWindow;
  org: string;
  onDelete: (uid: string) => void;
}) {
  const { t } = useTranslation("maintenanceWindows");
  // Prefer the server-computed status when present (newer backends).
  const status = window.status ?? computeMaintenanceStatus(window);
  return (
    <TableRow>
      <TableCell>
        <Link
          to="/orgs/$org/maintenance-windows/$maintenanceWindowUid"
          params={{ org, maintenanceWindowUid: window.uid }}
          className="font-medium hover:underline"
          data-testid={`mw-row-view-${window.uid}`}
        >
          {window.title}
        </Link>
      </TableCell>
      <TableCell
        className="text-muted-foreground max-w-[18rem] sm:max-w-none truncate"
        data-testid={`mw-row-schedule-${window.uid}`}
      >
        {describeSchedule(window, t)}
      </TableCell>
      <TableCell>
        <Badge variant={maintenanceStatusBadgeVariant(status)}>
          {t(`status.${status}`)}
        </Badge>
      </TableCell>
      <TableCell>
        <ChecksCountCell org={org} uid={window.uid} />
      </TableCell>
      <TableCell>
        <div className="flex items-center justify-end gap-1">
          <Button
            asChild
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label={t("edit")}
            title={t("edit")}
          >
            <Link
              to="/orgs/$org/maintenance-windows/$maintenanceWindowUid/edit"
              params={{ org, maintenanceWindowUid: window.uid }}
              data-testid={`mw-row-edit-${window.uid}`}
            >
              <Pencil className="h-4 w-4" />
            </Link>
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 text-destructive hover:text-destructive"
            aria-label={t("delete.confirmTitle")}
            title={t("delete.confirmTitle")}
            onClick={() => onDelete(window.uid)}
            data-testid={`mw-row-delete-${window.uid}`}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

function MaintenanceWindowsIndexPage() {
  const { t } = useTranslation(["maintenanceWindows", "common"]);
  const { org } = Route.useParams();
  const { q: qParam } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  // Seeded from the URL once on mount via a lazy initializer — the layout
  // route otherwise races a second search-validation pass that can drop the
  // param on a cold load (known dash0 pitfall, see checks.index.tsx). The
  // debounced value is written back below so the URL stays in sync.
  const [search, setSearch] = useState(() => qParam ?? "");
  const debouncedSearch = useDebounce(search, 300);
  useEffect(() => {
    const next = debouncedSearch || undefined;
    // Skip the no-op write-back that would otherwise fire on every mount.
    // `navigate` is anchored to this route (`from`), so a redundant call
    // re-targets it from wherever the router has since moved — on a
    // logged-out deep link the guard has already bounced to /login, and
    // navigating back re-enters the guard with login's own search params
    // (session_expired/returnTo) folded in. That nests returnTo one level
    // deeper on every pass: an unbounded redirect loop that grows the URL
    // until the renderer hangs. Only write when the URL is actually stale.
    if (next === qParam) return;
    void navigate({
      search: (prev) => ({ ...prev, q: next }),
      replace: true,
    });
  }, [debouncedSearch, qParam, navigate]);
  const [deleteUid, setDeleteUid] = useState<string | null>(null);

  const {
    data: windows,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useMaintenanceWindows(org);

  const deleteWindow = useDeleteMaintenanceWindow(org);

  const filtered =
    windows?.filter((w) => {
      const q = search.toLowerCase();
      return (
        w.title.toLowerCase().includes(q) ||
        (w.description?.toLowerCase().includes(q) ?? false)
      );
    }) ?? [];

  const handleDelete = async () => {
    if (!deleteUid) return;
    try {
      await deleteWindow.mutateAsync(deleteUid);
      toast.success(t("maintenanceWindows:toast.deleted"));
      setDeleteUid(null);
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("maintenanceWindows:toast.deleteFailed"),
      );
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Wrench}
        title={t("maintenanceWindows:title")}
        description={t("maintenanceWindows:subtitle")}
        docsHref="/docs/features/maintenance-windows"
        actions={
          <>
            <Button
              variant="outline"
              onClick={() => refetch()}
              disabled={isRefetching}
              aria-label={t("common:refresh")}
            >
              <RefreshCw
                className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`}
              />
              <span className="hidden sm:inline">{t("common:refresh")}</span>
            </Button>
            <Link to="/orgs/$org/maintenance-windows/new" params={{ org }}>
              <Button
                data-testid="mw-new-button"
                aria-label={t("maintenanceWindows:new")}
              >
                <Plus className="sm:mr-2 h-4 w-4" />
                <span className="hidden sm:inline">
                  {t("maintenanceWindows:new")}
                </span>
              </Button>
            </Link>
          </>
        }
        className="flex-wrap"
      />

      <div className="relative max-w-sm">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
        <Input
          placeholder={t("maintenanceWindows:searchPlaceholder")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="pl-9"
        />
      </div>

      {error ? (
        <QueryErrorView error={error} org={org} onRetry={() => refetch()} />
      ) : isLoading ? (
        <div className="space-y-2">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-lg" />
          ))}
        </div>
      ) : filtered.length > 0 ? (
        <div className="rounded-md border overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("maintenanceWindows:table.title")}</TableHead>
                <TableHead>{t("maintenanceWindows:table.schedule")}</TableHead>
                <TableHead>{t("maintenanceWindows:table.status")}</TableHead>
                <TableHead>{t("maintenanceWindows:table.checks")}</TableHead>
                <TableHead className="w-[50px]" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((window) => (
                <MaintenanceWindowRow
                  key={window.uid}
                  window={window}
                  org={org}
                  onDelete={setDeleteUid}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      ) : windows && windows.length > 0 ? (
        <div className="text-center py-12 text-muted-foreground">
          <Search className="h-8 w-8 mx-auto mb-2 opacity-50" />
          <p>{t("maintenanceWindows:noMatch")}</p>
        </div>
      ) : (
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            <Wrench className="h-8 w-8 mx-auto mb-2 opacity-50" />
            <p>{t("maintenanceWindows:empty")}</p>
          </CardContent>
        </Card>
      )}

      <AlertDialog
        open={!!deleteUid}
        onOpenChange={(open) => !open && setDeleteUid(null)}
      >
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
  );
}
