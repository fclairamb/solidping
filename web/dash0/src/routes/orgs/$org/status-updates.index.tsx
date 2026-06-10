import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Megaphone, Plus, Search, RefreshCw, Pencil, Trash2 } from "lucide-react";
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

export const Route = createFileRoute("/orgs/$org/status-updates/")({
  component: StatusUpdatesIndexPage,
});

const STATUS_UPDATE_KINDS = [
  { value: "investigating", label: "Investigating" },
  { value: "identified", label: "Identified" },
  { value: "monitoring", label: "Monitoring" },
  { value: "resolved", label: "Resolved" },
  { value: "maintenance", label: "Maintenance" },
  { value: "info", label: "Info" },
];

const KIND_COLORS: Record<string, string> = {
  investigating: "bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200",
  identified: "bg-orange-100 text-orange-800 dark:bg-orange-900 dark:text-orange-200",
  monitoring: "bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200",
  resolved: "bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200",
  maintenance: "bg-purple-100 text-purple-800 dark:bg-purple-900 dark:text-purple-200",
  info: "bg-gray-100 text-gray-800 dark:bg-gray-900 dark:text-gray-200",
};

function KindBadge({ kind }: { kind: string }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium capitalize ${KIND_COLORS[kind] ?? "bg-gray-100 text-gray-800"}`}
    >
      {kind}
    </span>
  );
}

function StatusUpdateRow({
  update,
  org,
  onDelete,
}: {
  update: StatusUpdate;
  org: string;
  onDelete: (uid: string) => void;
}) {
  return (
    <TableRow data-testid="status-update-row">
      <TableCell>
        <KindBadge kind={update.kind} />
      </TableCell>
      <TableCell>
        <span className="font-medium">{update.title}</span>
      </TableCell>
      <TableCell>
        <span className="text-muted-foreground whitespace-nowrap">
          {new Date(update.publishedAt).toLocaleString()}
        </span>
      </TableCell>
      <TableCell>
        <div className="flex items-center justify-end gap-1">
          <Button
            asChild
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label="Edit"
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
            aria-label="Delete"
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
  const { org } = Route.useParams();
  const [search, setSearch] = useState("");
  const [filterPageUid, setFilterPageUid] = useState<string>("all");
  const [filterSectionUid, setFilterSectionUid] = useState<string>("all");
  const [filterCheckUid, setFilterCheckUid] = useState<string>("all");
  const [filterKind, setFilterKind] = useState<string>("all");
  const [deleteUid, setDeleteUid] = useState<string | null>(null);

  const { data: pages } = useStatusPages(org);

  // Fetch selected page with sections for section/check filters
  const { data: selectedPage } = useStatusPage(
    org,
    filterPageUid !== "all" ? filterPageUid : "",
    { with: "sections" }
  );

  const sections = selectedPage?.sections ?? [];

  const checkOptions =
    filterSectionUid !== "all"
      ? (sections.find((s) => s.uid === filterSectionUid)?.resources ?? [])
      : sections.flatMap((s) => s.resources ?? []);

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
    if (search && !u.title.toLowerCase().includes(search.toLowerCase())) return false;
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
      toast.success("Status update deleted");
    } catch {
      toast.error("Failed to delete status update");
    } finally {
      setDeleteUid(null);
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
            <Megaphone className="h-7 w-7 text-muted-foreground" />
            Status updates
          </h1>
          <p className="text-muted-foreground">
            Publish narrative updates on your status pages.
          </p>
        </div>
        <Button asChild>
          <Link
            to="/orgs/$org/status-updates/new"
            params={{ org }}
            data-testid="status-updates-new"
          >
            <Plus className="mr-2 h-4 w-4" />
            New update
          </Link>
        </Button>
      </div>

      {/* Filters row */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder="Search by title…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9 w-48"
          />
        </div>

        <Select
          value={filterPageUid}
          onValueChange={handlePageChange}
        >
          <SelectTrigger className="w-44" data-testid="status-updates-page-filter">
            <SelectValue placeholder="All pages" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All pages</SelectItem>
            {(pages ?? []).map((p) => (
              <SelectItem key={p.uid} value={p.uid}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        {filterPageUid !== "all" && (
          <Select
            value={filterSectionUid}
            onValueChange={handleSectionChange}
          >
            <SelectTrigger className="w-44" data-testid="status-updates-section-filter">
              <SelectValue placeholder="All sections" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All sections</SelectItem>
              {sections.map((s) => (
                <SelectItem key={s.uid} value={s.uid}>
                  {s.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}

        {filterPageUid !== "all" && (
          <Select
            value={filterCheckUid}
            onValueChange={setFilterCheckUid}
          >
            <SelectTrigger className="w-44" data-testid="status-updates-check-filter">
              <SelectValue placeholder="All checks" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All checks</SelectItem>
              {checkOptions.map((r) => (
                <SelectItem key={r.checkUid} value={r.checkUid}>
                  {r.check?.name ?? r.checkUid.slice(0, 8)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}

        <Select
          value={filterKind}
          onValueChange={setFilterKind}
        >
          <SelectTrigger className="w-36" data-testid="status-updates-kind-filter">
            <SelectValue placeholder="All kinds" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All kinds</SelectItem>
            {STATUS_UPDATE_KINDS.map((k) => (
              <SelectItem key={k.value} value={k.value}>
                {k.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Button
          variant="outline"
          size="icon"
          onClick={() => refetch()}
          disabled={isRefetching}
          aria-label="Refresh"
        >
          <RefreshCw className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />
        </Button>
      </div>

      {error && <QueryErrorView error={error} org={org} onRetry={() => refetch()} />}

      {isLoading ? (
        <div className="rounded-md border">
          <div className="space-y-2 p-4">
            {[...Array(3)].map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        </div>
      ) : filtered.length > 0 ? (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Kind</TableHead>
                <TableHead>Title</TableHead>
                <TableHead>Date</TableHead>
                <TableHead className="w-[100px]" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((u) => (
                <StatusUpdateRow
                  key={u.uid}
                  update={u}
                  org={org}
                  onDelete={setDeleteUid}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        <div className="text-center py-12 text-muted-foreground">
          <Megaphone className="h-8 w-8 mx-auto mb-2 opacity-50" />
          <p className="mb-2">No status updates yet.</p>
          <Button asChild>
            <Link
              to="/orgs/$org/status-updates/new"
              params={{ org }}
            >
              <Plus className="mr-2 h-4 w-4" />
              New update
            </Link>
          </Button>
        </div>
      )}

      <AlertDialog open={!!deleteUid} onOpenChange={(o) => !o && setDeleteUid(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete status update?</AlertDialogTitle>
            <AlertDialogDescription>
              This will remove the update from the status page. This action cannot be
              undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
