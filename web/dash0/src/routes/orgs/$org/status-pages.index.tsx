import { useEffect, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  Plus,
  Search,
  RefreshCw,
  ExternalLink,
  Pencil,
  Trash2,
  Star,
  Globe,
} from "lucide-react";
import { toast } from "sonner";
import { useStatusPages, useDeleteStatusPage, type StatusPage } from "@/api/hooks";
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
import { QueryErrorView } from "@/components/shared/error-views";
import { PageHeader } from "@/components/shared/page-header";
import { ApiError } from "@/api/client";
import { useDebounce } from "@/lib/use-debounce";

interface StatusPagesIndexSearch {
  q?: string;
}

export const Route = createFileRoute("/orgs/$org/status-pages/")({
  component: StatusPagesIndexPage,
  validateSearch: (search: Record<string, unknown>): StatusPagesIndexSearch => ({
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  }),
});

function StatusPageRow({
  page,
  org,
  onDelete,
}: {
  page: StatusPage;
  org: string;
  onDelete: (uid: string) => void;
}) {
  const { t } = useTranslation("statusPages");
  return (
    <TableRow className="hover:bg-muted/40 transition-colors">
      <TableCell>
        <Link
          to="/orgs/$org/status-pages/$statusPageUid"
          params={{ org, statusPageUid: page.uid }}
          className="flex items-center gap-2 font-medium text-foreground hover:text-primary hover:underline transition-colors"
        >
          {page.name}
          {page.isDefault && <Star className="h-3.5 w-3.5 fill-amber-400 text-amber-500" />}
        </Link>
      </TableCell>
      <TableCell>
        <div className="flex items-center gap-1 font-mono text-xs text-muted-foreground">
          <Globe className="h-3 w-3 text-muted-foreground/70" />
          <span>/{page.slug}</span>
        </div>
      </TableCell>
      <TableCell>
        <Badge
          variant={page.visibility === "public" ? "default" : "secondary"}
          className={
            page.visibility === "public"
              ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400 border border-emerald-500/20 font-medium"
              : "font-normal"
          }
        >
          {page.visibility === "public"
            ? t("visibility.public")
            : t("visibility.restricted")}
        </Badge>
      </TableCell>
      <TableCell>
        <Badge variant={page.enabled ? "default" : "outline"} className="font-normal text-xs">
          {page.enabled ? t("enabled") : t("disabled")}
        </Badge>
      </TableCell>
      <TableCell>
        <div className="flex items-center justify-end gap-1">
          <Button
            asChild
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label={t("viewPublic")}
            title={t("viewPublic")}
          >
            <a
              href={`/status0/${org}/${page.slug}`}
              target="_blank"
              rel="noopener noreferrer"
              data-testid={`status-page-row-view-${page.slug}`}
            >
              <ExternalLink className="h-4 w-4" />
            </a>
          </Button>
          <Button
            asChild
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label={t("edit")}
            title={t("edit")}
          >
            <Link
              to="/orgs/$org/status-pages/$statusPageUid/edit"
              params={{ org, statusPageUid: page.uid }}
              data-testid={`status-page-row-edit-${page.slug}`}
            >
              <Pencil className="h-4 w-4" />
            </Link>
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 text-destructive hover:text-destructive"
            aria-label={t("delete")}
            title={t("delete")}
            onClick={() => onDelete(page.uid)}
            data-testid={`status-page-row-delete-${page.slug}`}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

function StatusPagesIndexPage() {
  const { t } = useTranslation(["statusPages", "common"]);
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
    data: pages,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useStatusPages(org);

  const deleteStatusPage = useDeleteStatusPage(org);

  const filteredPages =
    pages?.filter((page) => {
      const searchLower = search.toLowerCase();
      return (
        page.name.toLowerCase().includes(searchLower) ||
        page.slug.toLowerCase().includes(searchLower)
      );
    }) ?? [];

  const handleDelete = async () => {
    if (!deleteUid) return;
    try {
      await deleteStatusPage.mutateAsync(deleteUid);
      toast.success(t("statusPages:toast.deleted"));
      setDeleteUid(null);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("statusPages:toast.deleteFailed"));
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Globe}
        title={t("statusPages:title")}
        description={t("statusPages:subtitle")}
        docsHref="/docs/features/status-pages"
        actions={
          <Link to="/orgs/$org/status-pages/new" params={{ org }}>
            <Button>
              <Plus className="mr-2 h-4 w-4" />
              {t("statusPages:newStatusPage")}
            </Button>
          </Link>
        }
        className="flex-wrap"
      />

      <div className="flex flex-wrap items-center gap-4">
        <div className="relative flex-1 min-w-[200px] max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("statusPages:searchPlaceholder")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9"
          />
        </div>
        <Button
          variant="outline"
          onClick={() => refetch()}
          disabled={isRefetching}
          aria-label={t("common:refresh")}
        >
          <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
          <span className="hidden sm:inline">{t("common:refresh")}</span>
        </Button>
      </div>

      {error ? (
        <QueryErrorView error={error} org={org} onRetry={() => refetch()} />
      ) : isLoading ? (
        <div className="space-y-2">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-lg" />
          ))}
        </div>
      ) : filteredPages.length > 0 ? (
        <div className="rounded-xl border bg-card shadow-card overflow-hidden">
          <Table>
            <TableHeader className="bg-muted/30">
              <TableRow>
                <TableHead>{t("statusPages:table.name")}</TableHead>
                <TableHead>{t("statusPages:table.slug")}</TableHead>
                <TableHead>{t("statusPages:table.visibility")}</TableHead>
                <TableHead>{t("statusPages:table.status")}</TableHead>
                <TableHead className="w-[100px] text-right" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filteredPages.map((page) => (
                <StatusPageRow
                  key={page.uid}
                  page={page}
                  org={org}
                  onDelete={setDeleteUid}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      ) : pages && pages.length > 0 ? (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Search className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">
            {t("statusPages:noMatch")}
          </p>
        </div>
      ) : (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Globe className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">
            {t("statusPages:noStatusPages")}
          </p>
          <p className="mx-auto max-w-sm text-xs text-muted-foreground">
            {t("statusPages:noStatusPagesHint")}
          </p>
        </div>
      )}

      <AlertDialog open={!!deleteUid} onOpenChange={() => setDeleteUid(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("statusPages:deleteDialog.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("statusPages:deleteDialog.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {t("statusPages:delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
