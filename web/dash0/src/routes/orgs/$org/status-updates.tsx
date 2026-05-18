import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { formatDistanceToNow } from "date-fns";
import { Plus, Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  useStatusUpdates,
  useStatusPages,
  useCreateStatusUpdate,
  useUpdateStatusUpdate,
  useDeleteStatusUpdate,
  type StatusUpdate,
  type CreateStatusUpdateRequest,
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
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { QueryErrorView } from "@/components/shared/error-views";

export const Route = createFileRoute("/orgs/$org/status-updates")({
  component: StatusUpdatesPage,
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

interface StatusUpdateFormData {
  statusPageUid: string;
  kind: string;
  title: string;
  bodyMarkdown: string;
  linkUrl: string;
  publishedAt: string;
}

function emptyForm(defaultPageUid: string): StatusUpdateFormData {
  return {
    statusPageUid: defaultPageUid,
    kind: "info",
    title: "",
    bodyMarkdown: "",
    linkUrl: "",
    publishedAt: new Date().toISOString().slice(0, 16),
  };
}

interface StatusUpdateDialogProps {
  org: string;
  open: boolean;
  onClose: () => void;
  defaultPageUid?: string;
  editTarget?: StatusUpdate;
}

function StatusUpdateDialog({
  org,
  open,
  onClose,
  defaultPageUid = "",
  editTarget,
}: StatusUpdateDialogProps) {
  const { data: pages } = useStatusPages(org);
  const createMutation = useCreateStatusUpdate(org);
  const updateMutation = useUpdateStatusUpdate(org, editTarget?.uid ?? "");

  const [form, setForm] = useState<StatusUpdateFormData>(() =>
    editTarget
      ? {
          statusPageUid: editTarget.statusPageUid,
          kind: editTarget.kind,
          title: editTarget.title,
          bodyMarkdown: editTarget.bodyMarkdown,
          linkUrl: editTarget.linkUrl ?? "",
          publishedAt: new Date(editTarget.publishedAt).toISOString().slice(0, 16),
        }
      : emptyForm(defaultPageUid)
  );

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    const publishedAt = form.publishedAt
      ? new Date(form.publishedAt).toISOString()
      : undefined;

    try {
      if (editTarget) {
        await updateMutation.mutateAsync({
          kind: form.kind,
          title: form.title,
          bodyMarkdown: form.bodyMarkdown,
          linkUrl: form.linkUrl || undefined,
          publishedAt,
        });
        toast.success("Status update saved");
      } else {
        const req: CreateStatusUpdateRequest = {
          statusPageUid: form.statusPageUid,
          kind: form.kind,
          title: form.title,
          bodyMarkdown: form.bodyMarkdown,
          linkUrl: form.linkUrl || undefined,
          publishedAt,
        };
        await createMutation.mutateAsync(req);
        toast.success("Status update created");
      }
      onClose();
    } catch {
      toast.error("Failed to save status update");
    }
  };

  const isLoading = createMutation.isPending || updateMutation.isPending;

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{editTarget ? "Edit status update" : "New status update"}</DialogTitle>
          <DialogDescription>
            Publish a narrative update visible on your status page.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          {!editTarget && (
            <div className="space-y-1">
              <Label htmlFor="statusPage">Status page</Label>
              <Select
                value={form.statusPageUid}
                onValueChange={(v) => setForm((f) => ({ ...f, statusPageUid: v }))}
              >
                <SelectTrigger id="statusPage">
                  <SelectValue placeholder="Select a status page" />
                </SelectTrigger>
                <SelectContent>
                  {(pages ?? []).map((p) => (
                    <SelectItem key={p.uid} value={p.uid}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          <div className="space-y-1">
            <Label htmlFor="kind">Kind</Label>
            <Select
              value={form.kind}
              onValueChange={(v) => setForm((f) => ({ ...f, kind: v }))}
            >
              <SelectTrigger id="kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {STATUS_UPDATE_KINDS.map((k) => (
                  <SelectItem key={k.value} value={k.value}>
                    {k.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-1">
            <Label htmlFor="title">Title</Label>
            <Input
              id="title"
              value={form.title}
              onChange={(e) => setForm((f) => ({ ...f, title: e.target.value }))}
              maxLength={200}
              required
              placeholder="Investigating elevated error rates"
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="body">Body</Label>
            <Textarea
              id="body"
              value={form.bodyMarkdown}
              onChange={(e) => setForm((f) => ({ ...f, bodyMarkdown: e.target.value }))}
              required
              rows={4}
              placeholder="We are investigating an issue with..."
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="linkUrl">Link URL (optional)</Label>
            <Input
              id="linkUrl"
              type="url"
              value={form.linkUrl}
              onChange={(e) => setForm((f) => ({ ...f, linkUrl: e.target.value }))}
              placeholder="https://status.example.com/incident/123"
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="publishedAt">Published at</Label>
            <Input
              id="publishedAt"
              type="datetime-local"
              value={form.publishedAt}
              onChange={(e) => setForm((f) => ({ ...f, publishedAt: e.target.value }))}
            />
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={isLoading}>
              {isLoading ? "Saving…" : editTarget ? "Save changes" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function StatusUpdateRow({
  update,
  onEdit,
  onDelete,
}: {
  update: StatusUpdate;
  onEdit: (u: StatusUpdate) => void;
  onDelete: (uid: string) => void;
}) {
  return (
    <tr className="border-b last:border-0">
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
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            onClick={() => onEdit(update)}
            aria-label="Edit"
          >
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 text-destructive hover:text-destructive"
            onClick={() => onDelete(update.uid)}
            aria-label="Delete"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </td>
    </tr>
  );
}

function StatusUpdatesPage() {
  const { org } = Route.useParams();
  const { data: pages } = useStatusPages(org);
  const [filterPageUid, setFilterPageUid] = useState<string>("");
  const [filterKind, setFilterKind] = useState<string>("");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<StatusUpdate | undefined>();
  const [deleteUid, setDeleteUid] = useState<string | null>(null);

  const deleteMutation = useDeleteStatusUpdate(org);

  const queryParams = {
    ...(filterPageUid ? { statusPage: filterPageUid } : {}),
    limit: 50,
  };

  const { data: updates, isLoading, error } = useStatusUpdates(org, queryParams);

  const defaultPageUid = pages?.find((p) => p.isDefault)?.uid ?? pages?.[0]?.uid ?? "";

  const filtered = (updates ?? []).filter(
    (u) => !filterKind || u.kind === filterKind
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
        <Button onClick={() => { setEditTarget(undefined); setDialogOpen(true); }}>
          <Plus className="h-4 w-4 mr-1" />
          New update
        </Button>
      </div>

      {/* Filter chips */}
      <div className="flex flex-wrap gap-2">
        <Select value={filterPageUid} onValueChange={setFilterPageUid}>
          <SelectTrigger className="w-48">
            <SelectValue placeholder="All status pages" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">All status pages</SelectItem>
            {(pages ?? []).map((p) => (
              <SelectItem key={p.uid} value={p.uid}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select value={filterKind} onValueChange={setFilterKind}>
          <SelectTrigger className="w-36">
            <SelectValue placeholder="All kinds" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">All kinds</SelectItem>
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
                    onEdit={(target) => {
                      setEditTarget(target);
                      setDialogOpen(true);
                    }}
                    onDelete={setDeleteUid}
                  />
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      {dialogOpen && (
        <StatusUpdateDialog
          org={org}
          open={dialogOpen}
          onClose={() => { setDialogOpen(false); setEditTarget(undefined); }}
          defaultPageUid={defaultPageUid}
          editTarget={editTarget}
        />
      )}

      <AlertDialog open={!!deleteUid} onOpenChange={(o) => !o && setDeleteUid(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete status update?</AlertDialogTitle>
            <AlertDialogDescription>
              This will remove the update from the status page. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete} className="bg-destructive text-destructive-foreground hover:bg-destructive/90">
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
