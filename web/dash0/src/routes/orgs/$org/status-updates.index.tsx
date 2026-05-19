import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { formatDistanceToNow } from "date-fns";
import { Plus, Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  useStatusUpdates,
  useStatusPages,
  useDeleteStatusUpdate,
  type StatusUpdate,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
  investigating:
    "bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200",
  identified:
    "bg-orange-100 text-orange-800 dark:bg-orange-900 dark:text-orange-200",
  monitoring:
    "bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200",
  resolved:
    "bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200",
  maintenance:
    "bg-purple-100 text-purple-800 dark:bg-purple-900 dark:text-purple-200",
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
    <tr className="border-b last:border-0" data-testid="status-update-row">
      <td className="py-3 pr-4">
        <KindBadge kind={update.kind} />
      </td>
      <td className="py-3 pr-4 text-sm text-muted-foreground whitespace-nowrap">
        {formatDistanceToNow(new Date(update.publishedAt), { addSuffix: true })}
      </td>
      <td className="py-3 pr-4 font-medium">{update.title}</td>
      <td className="py-3 text-right">
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
      </td>
    </tr>
  );
}

function StatusUpdatesIndexPage() {
  const { org } = Route.useParams();
  const { data: pages } = useStatusPages(org);
  const [filterPageUid, setFilterPageUid] = useState<string>("all");
  const [filterKind, setFilterKind] = useState<string>("all");
  const [deleteUid, setDeleteUid] = useState<string | null>(null);

  const deleteMutation = useDeleteStatusUpdate(org);

  const queryParams = {
    ...(filterPageUid !== "all" ? { statusPage: filterPageUid } : {}),
    limit: 200,
  };

  const { data: updates, isLoading, error } = useStatusUpdates(org, queryParams);

  const filtered = (updates ?? []).filter(
    (u) => filterKind === "all" || u.kind === filterKind
  );

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
    <div className="container mx-auto p-6 space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Status updates</h1>
          <p className="text-muted-foreground text-sm mt-1">
            Publish narrative updates on your status pages.
          </p>
        </div>
        <Link
          to="/orgs/$org/status-updates/new"
          params={{ org }}
          data-testid="status-updates-new"
        >
          <Button>
            <Plus className="h-4 w-4 mr-1" />
            New update
          </Button>
        </Link>
      </div>

      <div className="flex flex-wrap gap-2">
        <Select value={filterPageUid} onValueChange={setFilterPageUid}>
          <SelectTrigger
            className="w-48"
            data-testid="status-updates-page-filter"
          >
            <SelectValue placeholder="All status pages" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All status pages</SelectItem>
            {(pages ?? []).map((p) => (
              <SelectItem key={p.uid} value={p.uid}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select value={filterKind} onValueChange={setFilterKind}>
          <SelectTrigger
            className="w-36"
            data-testid="status-updates-kind-filter"
          >
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
      </div>

      {error && <QueryErrorView error={error} org={org} />}

      <Card>
        <CardHeader>
          <CardTitle>Updates</CardTitle>
          <CardDescription>
            {filtered.length} update{filtered.length !== 1 ? "s" : ""}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="space-y-2">
              {[...Array(3)].map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : filtered.length === 0 ? (
            <p className="text-muted-foreground text-sm text-center py-8">
              No status updates yet. Click "+ New update" to publish one.
            </p>
          ) : (
            <table className="w-full">
              <tbody>
                {filtered.map((u) => (
                  <StatusUpdateRow
                    key={u.uid}
                    update={u}
                    org={org}
                    onDelete={setDeleteUid}
                  />
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      <AlertDialog open={!!deleteUid} onOpenChange={(o) => !o && setDeleteUid(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete status update?</AlertDialogTitle>
            <AlertDialogDescription>
              This will remove the update from the status page. This action
              cannot be undone.
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
