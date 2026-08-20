import { useEffect, useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  Megaphone,
  Plus,
  Search,
  RefreshCw,
  Pencil,
  Trash2,
  ExternalLink,
} from "lucide-react";
import { toast } from "sonner";
import {
  useStatusUpdates,
  useStatusPages,
  useStatusPage,
  useDeleteStatusUpdate,
  type StatusUpdate,
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { useDebounce } from "@/lib/use-debounce";
import { cn } from "@/lib/utils";
import { STATUS_UPDATE_KINDS, statusUpdateKindTone } from "@/lib/status-update-kind";

interface StatusUpdatesIndexSearch {
  q?: string;
}

export const Route = createFileRoute("/orgs/$org/status-updates/")({
  component: StatusUpdatesIndexPage,
  validateSearch: (search: Record<string, unknown>): StatusUpdatesIndexSearch => ({
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  }),
});

function KindBadge({ kind }: { kind: string }) {
  const { t } = useTranslation("statusUpdates");
  return (
    <Badge
      variant="outline"
      className={cn("font-medium capitalize", statusUpdateKindTone(kind))}
    >
      {t(`kinds.${kind}`, kind)}
    </Badge>
  );
}

function StatusUpdateRow({
  update,
  org,
  publicSlug,
  onDelete,
}: {
  update: StatusUpdate;
  org: string;
  publicSlug?: string;
  onDelete: (uid: string) => void;
}) {
  const { t } = useTranslation(["statusUpdates", "common"]);
  return (
    <TableRow
      className="hover:bg-muted/40 transition-colors"
      data-testid="status-update-row"
    >
      <TableCell>
        <Link
          to="/orgs/$org/status-updates/$updateUid/edit"
          params={{ org, updateUid: update.uid }}
          className="font-medium text-foreground hover:text-primary hover:underline transition-colors"
          data-testid="status-update-row-title"
        >
          {update.title}
        </Link>
      </TableCell>
      <TableCell>
        <KindBadge kind={update.kind} />
      </TableCell>
      <TableCell>
        <span className="text-muted-foreground whitespace-nowrap">
          {new Date(update.publishedAt).toLocaleString()}
        </span>
      </TableCell>
      <TableCell>
        <div className="flex items-center justify-end gap-1">
          {publicSlug && (
            <Button
              asChild
              variant="ghost"
              size="icon"
              className="h-8 w-8"
              aria-label={t("statusUpdates:viewPublic")}
              title={t("statusUpdates:viewPublic")}
            >
              <a
                href={`/status0/${org}/${publicSlug}#update-${update.uid}`}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="status-update-row-view"
              >
                <ExternalLink className="h-4 w-4" />
              </a>
            </Button>
          )}
          <Button
            asChild
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label={t("common:edit")}
            title={t("common:edit")}
          >
            <Link
              to="/orgs/$org/status-updates/$updateUid/edit"
              params={{ org, updateUid: update.uid }}
              data-testid="status-update-row-edit"
            >
              <Pencil className="h-4 w-4" />
            </Link>
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 text-destructive hover:text-destructive"
            onClick={() => onDelete(update.uid)}
            aria-label={t("common:delete")}
            title={t("common:delete")}
            data-testid="status-update-row-delete"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

function StatusUpdatesIndexPage() {
  const { t } = useTranslation(["statusUpdates", "common"]);
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
  const [filterPageUid, setFilterPageUid] = useState<string>("all");
  const [filterSectionUid, setFilterSectionUid] = useState<string>("all");
  const [filterCheckUid, setFilterCheckUid] = useState<string>("all");
  const [filterKind, setFilterKind] = useState<string>("all");
  const [deleteUid, setDeleteUid] = useState<string | null>(null);

  const { data: pages } = useStatusPages(org);

  // Resolve a status-page slug from its uid once, so each row can build the
  // public deep-link (/status0/{org}/{slug}#update-{uid}) without an extra fetch.
  const pageSlugByUid = useMemo(
    () => new Map((pages ?? []).map((p) => [p.uid, p.slug] as const)),
    [pages],
  );

  // Fetch selected page with sections for section/check filters
  const { data: selectedPage } = useStatusPage(
    org,
    filterPageUid !== "all" ? filterPageUid : "",
    { with: "sections" },
  );

  const sections = selectedPage?.sections ?? [];

  // A status update pins to an INDIVIDUAL check, so group-targeting resources
  // (which have no checkUid) are not selectable here — see spec 2026-08-01-03.
  const checkOptions = (
    filterSectionUid !== "all"
      ? (sections.find((s) => s.uid === filterSectionUid)?.resources ?? [])
      : sections.flatMap((s) => s.resources ?? [])
  ).filter((r): r is typeof r & { checkUid: string } => Boolean(r.checkUid));

  const queryParams = {
    ...(filterPageUid !== "all" ? { statusPage: filterPageUid } : {}),
    ...(filterSectionUid !== "all" ? { section: filterSectionUid } : {}),
    ...(filterCheckUid !== "all" ? { check: filterCheckUid } : {}),
    limit: 50,
  };

  const {
    data: updates,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useStatusUpdates(org, queryParams);

  const deleteMutation = useDeleteStatusUpdate(org);

  const filtered = (updates ?? []).filter((u) => {
    if (filterKind !== "all" && u.kind !== filterKind) return false;
    if (search && !u.title.toLowerCase().includes(search.toLowerCase()))
      return false;
    return true;
  });

  const handlePageChange = (v: string) => {
    setFilterPageUid(v);
    setFilterSectionUid("all");
    setFilterCheckUid("all");
  };

  const handleSectionChange = (v: string) => {
    setFilterSectionUid(v);
    setFilterCheckUid("all");
  };

  const handleDelete = async () => {
    if (!deleteUid) return;
    try {
      await deleteMutation.mutateAsync(deleteUid);
      toast.success(t("statusUpdates:toast.deleted"));
    } catch {
      toast.error(t("statusUpdates:toast.deleteFailed"));
    } finally {
      setDeleteUid(null);
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Megaphone}
        title={t("statusUpdates:title")}
        description={t("statusUpdates:subtitle")}
        docsHref="/docs/features/status-pages"
        actions={
          <Link to="/orgs/$org/status-updates/new" params={{ org }}>
            <Button data-testid="status-updates-new">
              <Plus className="mr-2 h-4 w-4" />
              {t("statusUpdates:newStatusUpdate")}
            </Button>
          </Link>
        }
        className="flex-wrap"
      />

      <div className="flex flex-wrap items-center gap-2">
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("statusUpdates:searchPlaceholder")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9 w-48"
          />
        </div>

        <Select value={filterPageUid} onValueChange={handlePageChange}>
          <SelectTrigger
            className="w-44"
            data-testid="status-updates-page-filter"
          >
            <SelectValue placeholder={t("statusUpdates:filters.allPages")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("statusUpdates:filters.allPages")}</SelectItem>
            {(pages ?? []).map((p) => (
              <SelectItem key={p.uid} value={p.uid}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        {filterPageUid !== "all" && (
          <Select value={filterSectionUid} onValueChange={handleSectionChange}>
            <SelectTrigger
              className="w-44"
              data-testid="status-updates-section-filter"
            >
              <SelectValue placeholder={t("statusUpdates:filters.allSections")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("statusUpdates:filters.allSections")}</SelectItem>
              {sections.map((s) => (
                <SelectItem key={s.uid} value={s.uid}>
                  {s.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}

        {filterPageUid !== "all" && (
          <Select value={filterCheckUid} onValueChange={setFilterCheckUid}>
            <SelectTrigger
              className="w-44"
              data-testid="status-updates-check-filter"
            >
              <SelectValue placeholder={t("statusUpdates:filters.allChecks")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("statusUpdates:filters.allChecks")}</SelectItem>
              {checkOptions.map((r) => (
                <SelectItem key={r.checkUid} value={r.checkUid}>
                  {r.check?.name ?? r.checkUid.slice(0, 8)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}

        <Select value={filterKind} onValueChange={setFilterKind}>
          <SelectTrigger
            className="w-36"
            data-testid="status-updates-kind-filter"
          >
            <SelectValue placeholder={t("statusUpdates:filters.allKinds")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("statusUpdates:filters.allKinds")}</SelectItem>
            {STATUS_UPDATE_KINDS.map((kind) => (
              <SelectItem key={kind} value={kind}>
                {t(`statusUpdates:kinds.${kind}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

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
      ) : filtered.length > 0 ? (
        <div className="rounded-xl border bg-card shadow-card overflow-hidden">
          <Table>
            <TableHeader className="bg-muted/30">
              <TableRow>
                <TableHead>{t("statusUpdates:table.title")}</TableHead>
                <TableHead>{t("statusUpdates:table.kind")}</TableHead>
                <TableHead>{t("statusUpdates:table.date")}</TableHead>
                <TableHead className="w-[100px] text-right" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((u) => (
                <StatusUpdateRow
                  key={u.uid}
                  update={u}
                  org={org}
                  publicSlug={pageSlugByUid.get(u.statusPageUid)}
                  onDelete={setDeleteUid}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      ) : updates && updates.length > 0 ? (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Search className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">
            {t("statusUpdates:noMatch")}
          </p>
        </div>
      ) : (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Megaphone className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">
            {t("statusUpdates:noStatusUpdates")}
          </p>
          <p className="mx-auto max-w-sm text-xs text-muted-foreground">
            {t("statusUpdates:noStatusUpdatesHint")}
          </p>
          <Button asChild size="sm">
            <Link to="/orgs/$org/status-updates/new" params={{ org }}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              {t("statusUpdates:newStatusUpdate")}
            </Link>
          </Button>
        </div>
      )}

      <AlertDialog
        open={!!deleteUid}
        onOpenChange={(o) => !o && setDeleteUid(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("statusUpdates:deleteDialog.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("statusUpdates:deleteDialog.description")}
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
