import { useState, useEffect } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  AlertTriangle,
  ArrowLeft,
  Bell,
  BellOff,
  CheckCircle,
  Eye,
  EyeOff,
  ExternalLink,
  Loader2,
  MessageSquare,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import {
  useIncident,
  useIncidents,
  useAcknowledgeIncident,
  useUnacknowledgeIncident,
  useSnoozeIncident,
  useUnsnoozeIncident,
  useResolveIncident,
  useEvents,
  useIncidentNotifications,
  useStatusUpdates,
  useStatusPages,
  useCreateStatusUpdate,
  useUpdateStatusUpdate,
  useDeleteStatusUpdate,
  useMembers,
  useAddComment,
  type Event,
  type IncidentDetail,
  type IncidentResultSnapshot,
  type IncidentFailureResponse,
  type IncidentAttachment,
  type StatusUpdate,
  type CreateStatusUpdateRequest,
} from "@/api/hooks";
import {
  EventTypeBadge,
  getCommentSource,
  getCommentText,
  getCommentSlackAuthor,
  getEventIcon,
} from "@/components/dashboard/event-display";
import { useLiveSubscription } from "@/contexts/LiveEventsContext";
import { SnoozeDialog } from "@/components/incidents/snooze-dialog";
import { IncidentPublicationsPanel } from "@/components/incidents/incident-publications-panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Trans } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { TimeAgo } from "@/components/ui/time-ago";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { CollapsibleCode } from "@/components/shared/copyable-code";
import { QueryErrorView } from "@/components/shared/error-views";
import { notificationStatusVariant, sourceLabel } from "@/lib/notifications";
import { channelTypeLabel } from "@/lib/channel-labels";
import { cn } from "@/lib/utils";
import { statusUpdateKindTone } from "@/lib/status-update-kind";

export const Route = createFileRoute("/orgs/$org/incidents/$incidentUid")({
  component: IncidentDetailPage,
});

function formatDuration(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (days > 0) return `${days}d ${hours % 24}h`;
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  if (minutes > 0) return `${minutes}m ${seconds % 60}s`;
  return `${seconds}s`;
}

function TotalDuration({
  startedAt,
  resolvedAt,
}: {
  startedAt?: string;
  resolvedAt?: string;
}) {
  const { t } = useTranslation("incidents");
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (startedAt && !resolvedAt) {
      const interval = setInterval(() => setNow(Date.now()), 1000);
      return () => clearInterval(interval);
    }
  }, [startedAt, resolvedAt]);

  if (!startedAt) return "-";

  if (resolvedAt) {
    return formatDuration(
      new Date(resolvedAt).getTime() - new Date(startedAt).getTime(),
    );
  }
  return (
    formatDuration(now - new Date(startedAt).getTime()) +
    " " +
    t("detail.ongoing")
  );
}

function TimelineItem({
  label,
  timestamp,
  icon,
}: {
  label: string;
  timestamp?: string;
  icon: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-3">
      {icon}
      <div className="flex-1">
        <div className="font-medium">{label}</div>
        <div className="text-sm text-muted-foreground">
          {timestamp ? (
            <TimeAgo
              date={timestamp}
              variant="inline"
              data-testid="incident-timeline-time"
            />
          ) : (
            "-"
          )}
        </div>
      </div>
    </div>
  );
}

// --- Status Updates Panel ---

const STATUS_UPDATE_KINDS = [
  { value: "investigating", label: "Investigating" },
  { value: "identified", label: "Identified" },
  { value: "monitoring", label: "Monitoring" },
  { value: "resolved", label: "Resolved" },
  { value: "maintenance", label: "Maintenance" },
  { value: "info", label: "Info" },
];

function KindBadgeInline({ kind }: { kind: string }) {
  return (
    <Badge
      variant="outline"
      className={cn("font-medium capitalize", statusUpdateKindTone(kind))}
    >
      {kind}
    </Badge>
  );
}

interface IncidentStatusUpdateDialogProps {
  org: string;
  open: boolean;
  onClose: () => void;
  incidentUid: string;
  editTarget?: StatusUpdate;
}

function IncidentStatusUpdateDialog({
  org,
  open,
  onClose,
  incidentUid,
  editTarget,
}: IncidentStatusUpdateDialogProps) {
  const { data: pages } = useStatusPages(org);
  const createMutation = useCreateStatusUpdate(org);
  const updateMutation = useUpdateStatusUpdate(org, editTarget?.uid ?? "");

  const defaultPageUid =
    pages?.find((p) => p.isDefault)?.uid ?? pages?.[0]?.uid ?? "";

  const [form, setForm] = useState<{
    statusPageUid: string;
    kind: string;
    title: string;
    bodyMarkdown: string;
    linkUrl: string;
    publishedAt: string;
  }>({
    statusPageUid: editTarget?.statusPageUid ?? defaultPageUid,
    kind: editTarget?.kind ?? "investigating",
    title: editTarget?.title ?? "",
    bodyMarkdown: editTarget?.bodyMarkdown ?? "",
    linkUrl: editTarget?.linkUrl ?? "",
    publishedAt: editTarget
      ? new Date(editTarget.publishedAt).toISOString().slice(0, 16)
      : new Date().toISOString().slice(0, 16),
  });

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      if (editTarget) {
        await updateMutation.mutateAsync({
          kind: form.kind,
          title: form.title,
          bodyMarkdown: form.bodyMarkdown,
          linkUrl: form.linkUrl || undefined,
          publishedAt: form.publishedAt
            ? new Date(form.publishedAt).toISOString()
            : undefined,
        });
        toast.success("Status update saved");
      } else {
        const req: CreateStatusUpdateRequest = {
          statusPageUid: form.statusPageUid,
          incidentUid,
          kind: form.kind,
          title: form.title,
          bodyMarkdown: form.bodyMarkdown,
          linkUrl: form.linkUrl || undefined,
          publishedAt: form.publishedAt
            ? new Date(form.publishedAt).toISOString()
            : undefined,
        };
        await createMutation.mutateAsync(req);
        toast.success("Status update added");
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
          <DialogTitle>
            {editTarget ? "Edit status update" : "Add status update"}
          </DialogTitle>
          <DialogDescription>
            This update will be linked to this incident on the status page.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          {!editTarget && (
            <div className="space-y-1">
              <Label htmlFor="su-page">Status page</Label>
              <Select
                value={form.statusPageUid}
                onValueChange={(v) =>
                  setForm((f) => ({ ...f, statusPageUid: v }))
                }
              >
                <SelectTrigger id="su-page">
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
            <Label htmlFor="su-kind">Kind</Label>
            <Select
              value={form.kind}
              onValueChange={(v) => setForm((f) => ({ ...f, kind: v }))}
            >
              <SelectTrigger id="su-kind">
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
            <Label htmlFor="su-title">Title</Label>
            <Input
              id="su-title"
              value={form.title}
              onChange={(e) =>
                setForm((f) => ({ ...f, title: e.target.value }))
              }
              maxLength={200}
              required
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="su-body">Body</Label>
            <Textarea
              id="su-body"
              value={form.bodyMarkdown}
              onChange={(e) =>
                setForm((f) => ({ ...f, bodyMarkdown: e.target.value }))
              }
              required
              rows={3}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="su-link">Link URL (optional)</Label>
            <Input
              id="su-link"
              type="url"
              value={form.linkUrl}
              onChange={(e) =>
                setForm((f) => ({ ...f, linkUrl: e.target.value }))
              }
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="su-pub">Published at</Label>
            <Input
              id="su-pub"
              type="datetime-local"
              value={form.publishedAt}
              onChange={(e) =>
                setForm((f) => ({ ...f, publishedAt: e.target.value }))
              }
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={isLoading}>
              {isLoading
                ? "Saving…"
                : editTarget
                  ? "Save changes"
                  : "Add update"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function StatusUpdatesPanel({
  org,
  incidentUid,
}: {
  org: string;
  incidentUid: string;
}) {
  const { data: updates, isLoading } = useStatusUpdates(org, {
    incident: incidentUid,
    limit: 50,
  });
  const deleteMutation = useDeleteStatusUpdate(org);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<StatusUpdate | undefined>();
  const [deleteUid, setDeleteUid] = useState<string | null>(null);

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
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <MessageSquare className="h-4 w-4 text-muted-foreground" />
            <CardTitle>Status updates</CardTitle>
          </div>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setEditTarget(undefined);
              setDialogOpen(true);
            }}
          >
            <Plus className="h-3 w-3 mr-1" />
            Add update
          </Button>
        </div>
        <CardDescription>
          Narrative updates published to your status page for this incident.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        ) : !updates || updates.length === 0 ? (
          <p className="text-sm text-muted-foreground text-center py-4">
            No status updates yet for this incident.
          </p>
        ) : (
          <div className="space-y-3">
            {updates.map((u) => (
              <div
                key={u.uid}
                className="flex items-start justify-between gap-3 pb-3 border-b last:border-0 last:pb-0"
              >
                <div className="flex items-start gap-2 min-w-0">
                  <KindBadgeInline kind={u.kind} />
                  <div className="min-w-0">
                    <div className="font-medium text-sm truncate">
                      {u.title}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      <TimeAgo
                        date={u.publishedAt}
                        variant="inline"
                        data-testid="status-update-time"
                      />
                    </div>
                  </div>
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => {
                      setEditTarget(u);
                      setDialogOpen(true);
                    }}
                    aria-label="Edit"
                  >
                    <Pencil className="h-3 w-3" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-destructive hover:text-destructive"
                    onClick={() => setDeleteUid(u.uid)}
                    aria-label="Delete"
                  >
                    <Trash2 className="h-3 w-3" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>

      {dialogOpen && (
        <IncidentStatusUpdateDialog
          org={org}
          open={dialogOpen}
          onClose={() => {
            setDialogOpen(false);
            setEditTarget(undefined);
          }}
          incidentUid={incidentUid}
          editTarget={editTarget}
        />
      )}

      <AlertDialog
        open={!!deleteUid}
        onOpenChange={(o) => !o && setDeleteUid(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete status update?</AlertDialogTitle>
            <AlertDialogDescription>
              This will remove the update from the status page.
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
    </Card>
  );
}

// --- Comments (discussion) panel ---

function CommentsCard({
  org,
  incidentUid,
}: {
  org: string;
  incidentUid: string;
}) {
  const { t } = useTranslation("incidents");
  const { data: members } = useMembers(org);
  const { data: comments, isLoading } = useEvents(org, {
    incidentUid,
    eventType: "incident.comment",
    size: 100,
  });
  const addComment = useAddComment(org);
  const [text, setText] = useState("");

  const authorName = (event: Event): string => {
    if (getCommentSource(event) === "slack") {
      return getCommentSlackAuthor(event) ?? t("comments.slackUser");
    }
    const authored = event.payload?.authorName;
    if (typeof authored === "string" && authored.length > 0) return authored;
    const member = members?.data?.find((m) => m.userUid === event.actorUid);
    return member?.name || member?.email || t("comments.teamMember");
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = text.trim();
    if (!trimmed) return;
    try {
      await addComment.mutateAsync({ uid: incidentUid, text: trimmed });
      setText("");
    } catch {
      toast.error(t("comments.addFailed"));
    }
  };

  // Oldest-first so the discussion reads top-to-bottom like a chat thread.
  const items = (comments?.data ?? [])
    .slice()
    .sort(
      (a, b) =>
        new Date(a.createdAt ?? 0).getTime() -
        new Date(b.createdAt ?? 0).getTime(),
    );

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center gap-2">
          <MessageSquare className="h-4 w-4 text-muted-foreground" />
          <CardTitle>{t("comments.title")}</CardTitle>
        </div>
        <CardDescription>{t("comments.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-14 w-full" />
            <Skeleton className="h-14 w-full" />
          </div>
        ) : items.length === 0 ? (
          <p className="text-sm text-muted-foreground text-center py-4">
            {t("comments.empty")}
          </p>
        ) : (
          <ul className="space-y-3">
            {items.map((c) => (
              <li
                key={c.uid}
                className="flex flex-col gap-1 rounded-md border bg-muted/30 p-3"
                data-testid="incident-comment"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">{authorName(c)}</span>
                  {getCommentSource(c) === "slack" && (
                    <Badge variant="outline" className="text-xs">
                      {t("comments.viaSlack")}
                    </Badge>
                  )}
                  {getCommentSource(c) === "telegram" && (
                    <Badge variant="outline" className="text-xs">
                      {t("comments.viaTelegram")}
                    </Badge>
                  )}
                  <span className="text-xs text-muted-foreground">
                    {c.createdAt ? (
                      <TimeAgo
                        date={c.createdAt}
                        variant="inline"
                        data-testid="comment-time"
                      />
                    ) : (
                      ""
                    )}
                  </span>
                </div>
                <p className="whitespace-pre-wrap break-words text-sm">
                  {getCommentText(c)}
                </p>
              </li>
            ))}
          </ul>
        )}
        <form onSubmit={handleSubmit} className="space-y-2 border-t pt-4">
          <Label htmlFor="incident-comment-input" className="sr-only">
            {t("comments.placeholder")}
          </Label>
          <Textarea
            id="incident-comment-input"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={t("comments.placeholder")}
            rows={3}
            maxLength={4096}
            data-testid="comment-input"
          />
          <div className="flex justify-end">
            <Button
              type="submit"
              disabled={!text.trim() || addComment.isPending}
              data-testid="comment-submit"
            >
              {addComment.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
              ) : (
                <MessageSquare className="h-4 w-4 sm:mr-2" />
              )}
              <span>{t("comments.submit")}</span>
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function IncidentDetailPage() {
  const { t } = useTranslation("incidents");
  const { t: tEvents } = useTranslation("events");
  const { org, incidentUid } = Route.useParams();
  const navigate = useNavigate();

  const {
    data: incident,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useIncident(org, incidentUid);

  const { data: events } = useEvents(org, { incidentUid, size: 20 });

  // Stream new timeline events (comments included) live: the backend publishes
  // an `events` hint on comment creation, which invalidates the events queries
  // on this page — covering Slack- and remote-authored comments without a
  // manual refresh. Falls back to plain polling when live updates are off.
  useLiveSubscription({ entity: "events" });

  const acknowledgeIncident = useAcknowledgeIncident(org);
  const unacknowledgeIncident = useUnacknowledgeIncident(org);
  const snoozeIncident = useSnoozeIncident(org);
  const unsnoozeIncident = useUnsnoozeIncident(org);
  const resolveIncident = useResolveIncident(org);
  const [snoozeOpen, setSnoozeOpen] = useState(false);

  const handleAcknowledge = async () => {
    try {
      await acknowledgeIncident.mutateAsync({ uid: incidentUid });
      toast.success(t("actions.acknowledged"));
      refetch();
    } catch {
      toast.error(t("actions.acknowledgeFailed"));
    }
  };

  const handleUnacknowledge = async () => {
    try {
      await unacknowledgeIncident.mutateAsync({ uid: incidentUid });
      toast.success(t("actions.unacknowledged"));
      refetch();
    } catch {
      toast.error(t("actions.unacknowledgeFailed"));
    }
  };

  const handleSnooze = async (payload: {
    duration?: string;
    until?: string;
    reason?: string;
  }) => {
    try {
      await snoozeIncident.mutateAsync({ uid: incidentUid, body: payload });
      toast.success(t("actions.snoozed"));
      setSnoozeOpen(false);
      refetch();
    } catch {
      toast.error(t("actions.snoozeFailed"));
    }
  };

  const handleUnsnooze = async () => {
    try {
      await unsnoozeIncident.mutateAsync({ uid: incidentUid });
      toast.success(t("actions.unsnoozed"));
      refetch();
    } catch {
      toast.error(t("actions.unsnoozeFailed"));
    }
  };

  const handleResolve = async () => {
    try {
      await resolveIncident.mutateAsync({ uid: incidentUid });
      toast.success(t("actions.resolved"));
      refetch();
    } catch {
      toast.error(t("actions.resolveFailed"));
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-4">
          <Skeleton className="h-10 w-10 rounded" />
          <Skeleton className="h-8 w-48" />
        </div>
        <Skeleton className="h-48 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("fallbackTitle")}
        backTo="/orgs/$org/incidents"
        backLabel={t("backToIncidents")}
        onRetry={() => refetch()}
      />
    );
  }

  if (!incident) {
    return (
      <div className="text-center py-12">
        <p className="text-muted-foreground mb-4">{t("incidentNotFound")}</p>
        <Link
          to="/orgs/$org/incidents"
          params={{ org }}
          search={{ state: "all" as const, showSuppressed: undefined }}
        >
          <Button variant="outline">{t("backToIncidents")}</Button>
        </Link>
      </div>
    );
  }

  const isActive = incident.state === "active";
  const isSnoozed =
    !!incident.snoozedUntil &&
    new Date(incident.snoozedUntil).getTime() > Date.now();
  const relapseCount = incident.relapseCount ?? 0;

  return (
    <div className="space-y-6">
      <CausedByBanner org={org} incident={incident} />
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <div className="flex items-center gap-3">
            {isActive ? (
              <AlertTriangle className="h-6 w-6 text-yellow-500" />
            ) : (
              <CheckCircle className="h-6 w-6 text-green-500" />
            )}
            <h1 className="text-3xl font-bold tracking-tight">
              {incident.number ? (
                <span className="text-muted-foreground tabular-nums mr-2">
                  #{incident.number}
                </span>
              ) : null}
              {incident.title ||
                incident.checkName ||
                incident.checkSlug ||
                t("fallbackTitle")}
            </h1>
            <Badge variant={isActive ? "destructive" : "secondary"}>
              {isActive ? t("active") : t("resolved")}
            </Badge>
            {isActive && isSnoozed && incident.snoozedUntil && (
              <Badge variant="outline">
                {t("stateBadges.snoozedUntil", {
                  time: new Date(incident.snoozedUntil).toLocaleString(),
                })}
              </Badge>
            )}
            {isActive && !isSnoozed && incident.acknowledgedAt && (
              <Badge variant="outline">{t("stateBadges.acked")}</Badge>
            )}
            {relapseCount > 0 && (
              <Badge variant="outline">
                {t("reopenedTimes", {
                  count: relapseCount,
                  unit:
                    relapseCount === 1
                      ? t("timeUnit.time")
                      : t("timeUnit.times"),
                })}
              </Badge>
            )}
            {incident.escalatedAt && (
              <Badge variant="outline">{t("escalated")}</Badge>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("actions.back")}
            onClick={() =>
              navigate({
                to: "/orgs/$org/incidents",
                params: { org },
                search: { state: "all" as const, showSuppressed: undefined },
              })
            }
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <Button
            variant="outline"
            aria-label={t("actions.refresh")}
            onClick={() => refetch()}
            disabled={isRefetching}
          >
            <RefreshCw
              className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`}
            />
            <span className="hidden sm:inline">{t("actions.refresh")}</span>
          </Button>
          {isActive && !incident.acknowledgedAt && !isSnoozed && (
            <Button
              variant="outline"
              aria-label={t("actions.acknowledge")}
              onClick={handleAcknowledge}
              disabled={acknowledgeIncident.isPending}
            >
              {acknowledgeIncident.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
              ) : (
                <Eye className="h-4 w-4 sm:mr-2" />
              )}
              <span className="hidden sm:inline">
                {t("actions.acknowledge")}
              </span>
            </Button>
          )}
          {isActive && incident.acknowledgedAt && !isSnoozed && (
            <Button
              variant="outline"
              aria-label={t("actions.unacknowledge")}
              onClick={handleUnacknowledge}
              disabled={unacknowledgeIncident.isPending}
            >
              {unacknowledgeIncident.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
              ) : (
                <EyeOff className="h-4 w-4 sm:mr-2" />
              )}
              <span className="hidden sm:inline">
                {t("actions.unacknowledge")}
              </span>
            </Button>
          )}
          {isActive && !isSnoozed && (
            <Button
              variant="outline"
              aria-label={t("actions.snooze")}
              onClick={() => setSnoozeOpen(true)}
              disabled={snoozeIncident.isPending}
            >
              {snoozeIncident.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
              ) : (
                <BellOff className="h-4 w-4 sm:mr-2" />
              )}
              <span className="hidden sm:inline">{t("actions.snooze")}</span>
            </Button>
          )}
          {isActive && isSnoozed && (
            <Button
              variant="outline"
              aria-label={t("actions.wakeUp")}
              onClick={handleUnsnooze}
              disabled={unsnoozeIncident.isPending}
            >
              {unsnoozeIncident.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
              ) : (
                <Bell className="h-4 w-4 sm:mr-2" />
              )}
              <span className="hidden sm:inline">{t("actions.wakeUp")}</span>
            </Button>
          )}
          {isActive && (
            <Button
              aria-label={t("actions.resolve")}
              onClick={handleResolve}
              disabled={resolveIncident.isPending}
            >
              {resolveIncident.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
              ) : (
                <CheckCircle className="h-4 w-4 sm:mr-2" />
              )}
              <span className="hidden sm:inline">{t("actions.resolve")}</span>
            </Button>
          )}
        </div>
      </div>

      <div className="grid gap-6 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>{t("detail.incidentDetails")}</CardTitle>
            <CardDescription>
              {t("detail.incidentDetailsDescription")}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {incident.description && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  {t("detail.descriptionLabel")}
                </div>
                <div>{incident.description}</div>
              </div>
            )}
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                {t("detail.checkLabel")}
              </div>
              <Link
                to="/orgs/$org/checks/$checkUid"
                params={{ org, checkUid: incident.checkUid! }}
                search={{
                  graphPeriod: undefined,
                  graphFull: undefined,
                  region: undefined,
                }}
                className="text-primary hover:underline inline-flex items-center gap-1"
              >
                {incident.checkName || incident.checkSlug || incident.checkUid}
                <ExternalLink className="h-3 w-3" />
              </Link>
            </div>
            {incident.check?.type && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  {t("detail.checkTypeLabel")}
                </div>
                <div className="capitalize">{incident.check.type}</div>
              </div>
            )}
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                {t("detail.failureCount")}
              </div>
              <div>{incident.failureCount ?? 0}</div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t("timeline.title")}</CardTitle>
            <CardDescription>{t("timeline.description")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-3">
              <TimelineItem
                label={t("timeline.started")}
                timestamp={incident.startedAt}
                icon={getEventIcon("incident.created")}
              />
              {incident.acknowledgedAt && (
                <TimelineItem
                  label={t("timeline.acknowledged")}
                  timestamp={incident.acknowledgedAt}
                  icon={getEventIcon("incident.acknowledged")}
                />
              )}
              {incident.escalatedAt && (
                <TimelineItem
                  label={t("timeline.escalated")}
                  timestamp={incident.escalatedAt}
                  icon={getEventIcon("incident.escalated")}
                />
              )}
              {incident.lastReopenedAt && (
                <TimelineItem
                  label={t("timeline.reopenedRelapse", { count: relapseCount })}
                  timestamp={incident.lastReopenedAt}
                  icon={getEventIcon("incident.reopened")}
                />
              )}
              {incident.resolvedAt && (
                <TimelineItem
                  label={t("timeline.resolved")}
                  timestamp={incident.resolvedAt}
                  icon={getEventIcon("incident.resolved")}
                />
              )}
            </div>
            {incident.startedAt && (
              <div className="pt-4 border-t">
                <div className="text-sm font-medium text-muted-foreground">
                  {t("detail.totalDuration")}
                </div>
                <div className="text-lg font-semibold">
                  <TotalDuration
                    startedAt={incident.startedAt}
                    resolvedAt={incident.resolvedAt}
                  />
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <FailureDetailsCard incident={incident} />

      <ProbeResponseCard incident={incident} />

      <ProbeScreenshotCard incident={incident} />

      <StatusUpdatesPanel org={org} incidentUid={incidentUid} />

      <IncidentPublicationsPanel org={org} incidentUid={incidentUid} />

      <CommentsCard org={org} incidentUid={incidentUid} />

      <BlastRadiusCard org={org} incident={incident} />

      {events?.data && (
        <EscalationTimelineCard
          events={events.data.filter(
            (e) =>
              e.eventType === "incident.escalated" ||
              e.eventType === "incident.escalation_failed",
          )}
        />
      )}

      <NotificationsCard org={org} incidentUid={incidentUid} />

      {events?.data &&
        events.data.some((e) => e.eventType !== "incident.comment") && (
          <Card>
            <CardHeader>
              <CardTitle>{t("eventLog.title")}</CardTitle>
              <CardDescription>{t("eventLog.description")}</CardDescription>
            </CardHeader>
            <CardContent>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("eventLog.time")}</TableHead>
                    <TableHead>{t("eventLog.eventType")}</TableHead>
                    <TableHead>{t("eventLog.actor")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {/* Comments render in their own card, not the raw event log. */}
                  {events.data
                    .filter((e) => e.eventType !== "incident.comment")
                    .map((event) => (
                      <TableRow key={event.uid}>
                        <TableCell className="text-sm">
                          {event.createdAt
                            ? new Date(event.createdAt).toLocaleString()
                            : "-"}
                        </TableCell>
                        <TableCell>
                          <EventTypeBadge
                            eventType={event.eventType}
                            t={tEvents}
                          />
                        </TableCell>
                        <TableCell className="text-sm capitalize">
                          {event.actorType || "-"}
                        </TableCell>
                      </TableRow>
                    ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        )}

      <SnoozeDialog
        open={snoozeOpen}
        onOpenChange={setSnoozeOpen}
        isPending={snoozeIncident.isPending}
        onSubmit={handleSnooze}
      />
    </div>
  );
}

function CausedByBanner({
  org,
  incident,
}: {
  org: string;
  incident: IncidentDetail;
}) {
  const { t } = useTranslation("incidents");
  const { data: parent } = useIncident(org, incident.causedByIncidentUid ?? "");

  if (!incident.causedByIncidentUid) return null;

  const parentName =
    parent?.checkName || parent?.checkSlug || t("rollup.parentLoading");

  if (incident.pagingSuppressed) {
    return (
      <Alert className="border-yellow-500/50 bg-yellow-500/10 text-yellow-900 dark:text-yellow-100">
        <AlertTriangle className="h-4 w-4" />
        <AlertDescription>
          <Trans
            t={t}
            i18nKey="rollup.causedByActive"
            values={{ parent: parentName }}
            components={{
              strong: (
                <Link
                  to="/orgs/$org/incidents/$incidentUid"
                  params={{ org, incidentUid: incident.causedByIncidentUid }}
                  className="font-semibold underline"
                />
              ),
            }}
          />
        </AlertDescription>
      </Alert>
    );
  }

  return (
    <Alert className="border-green-500/50 bg-green-500/10 text-green-900 dark:text-green-100">
      <CheckCircle className="h-4 w-4" />
      <AlertDescription>
        {t("rollup.causedByPast", {
          parent: parentName,
          resolvedAt: parent?.resolvedAt
            ? new Date(parent.resolvedAt).toLocaleString()
            : "",
        })}
      </AlertDescription>
    </Alert>
  );
}

// --- Failure Details Card ---

// isPrimitive reports whether a value can be rendered directly as text
// without a JSON fallback (compact key-value list vs. nested-object dump).
function isPrimitive(value: unknown): value is string | number | boolean {
  return (
    typeof value === "string" ||
    typeof value === "number" ||
    typeof value === "boolean"
  );
}

function FailureSnapshotBlock({
  title,
  snapshot,
}: {
  title: string;
  snapshot: IncidentResultSnapshot;
}) {
  const { t } = useTranslation("incidents");
  const output = snapshot.output ?? {};
  const outputEntries = Object.entries(output).filter(
    ([key]) => key !== "error",
  );
  const errorText = typeof output.error === "string" ? output.error : undefined;

  return (
    <div className="space-y-3 rounded-lg border border-destructive/30 bg-destructive/5 p-4">
      <div className="text-sm font-medium text-muted-foreground">{title}</div>
      {errorText && (
        <div className="break-words font-mono text-sm text-destructive">
          {errorText}
        </div>
      )}
      <div className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm sm:grid-cols-4">
        <div>
          <div className="text-xs text-muted-foreground">
            {t("detail.failureDetails.status")}
          </div>
          <div>{snapshot.status || "-"}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">
            {t("detail.failureDetails.region")}
          </div>
          <div>{snapshot.region || "-"}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">
            {t("detail.failureDetails.duration")}
          </div>
          <div>
            {typeof snapshot.duration === "number"
              ? `${snapshot.duration.toFixed(2)}s`
              : "-"}
          </div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">
            {t("detail.failureDetails.time")}
          </div>
          <div>
            {snapshot.periodStart
              ? new Date(snapshot.periodStart).toLocaleString()
              : "-"}
          </div>
        </div>
      </div>
      {outputEntries.length > 0 && (
        <div className="space-y-1 border-t pt-3">
          {outputEntries.map(([key, value]) => (
            <div
              key={key}
              className="flex flex-wrap items-baseline gap-2 text-sm"
            >
              <span className="text-muted-foreground">{key}:</span>
              <span className="break-all font-mono">
                {isPrimitive(value) ? String(value) : JSON.stringify(value)}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function FailureDetailsCard({ incident }: { incident: IncidentDetail }) {
  const { t } = useTranslation("incidents");
  const details = incident.details;
  const firstResult = details?.first_result;

  if (!firstResult) return null;

  return (
    <Card data-testid="failure-details-card">
      <CardHeader>
        <CardTitle>{t("detail.failureDetails.title")}</CardTitle>
        <CardDescription>
          {t("detail.failureDetails.description")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="text-base font-semibold">
          {details?.failure_reason || t("detail.failureDetails.unknown")}
        </div>
        <FailureSnapshotBlock
          title={t("detail.failureDetails.firstFailure")}
          snapshot={firstResult}
        />
        {details?.last_failure && (
          <FailureSnapshotBlock
            title={t("detail.failureDetails.latestRelapse")}
            snapshot={details.last_failure}
          />
        )}
      </CardContent>
    </Card>
  );
}

// formatAttachmentBytes renders an attachment's size. Unlike a text capture,
// a screenshot is routinely hundreds of KB, so MB has to be reachable.
function formatAttachmentBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// ProbeScreenshotCard renders the browser check's failure screenshot next to
// "What the probe saw".
//
// The caption is deliberately precise about WHEN the shot was taken: the
// capture happens after the checker has already decided the target failed, so
// it is "shortly after failure detection", never "the failure frame". A page
// that recovered in the intervening second would photograph as healthy, and
// presenting that as the moment of failure would be a lie the reader cannot
// detect.
//
// The image is fetched through a short-lived signed URL minted for this
// response — it expires, so nothing here caches or persists it.
function ProbeScreenshotCard({ incident }: { incident: IncidentDetail }) {
  const { t } = useTranslation("incidents");

  const shot: IncidentAttachment | undefined = incident.attachments?.find(
    (attachment) => attachment.kind === "screenshot" && attachment.downloadUrl,
  );

  if (!shot) return null;

  const capturedAt = shot.details?.capturedAt ?? shot.createdAt;
  const region = shot.details?.region;

  return (
    <Card data-testid="probe-screenshot-card">
      <CardHeader>
        <CardTitle>{t("detail.screenshot.title")}</CardTitle>
        <CardDescription>{t("detail.screenshot.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {/* Plain <img>: the bytes come from our own origin behind a signed
            URL, and a max-w-full image is what keeps this usable on a phone. */}
        <a
          href={shot.downloadUrl}
          target="_blank"
          rel="noreferrer"
          className="block overflow-hidden rounded-md border bg-muted"
          data-testid="probe-screenshot-link"
        >
          <img
            src={shot.downloadUrl}
            alt={t("detail.screenshot.alt")}
            loading="lazy"
            className="h-auto max-h-[32rem] w-full object-contain"
            data-testid="probe-screenshot-image"
          />
        </a>
        <div
          className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground"
          data-testid="probe-screenshot-caption"
        >
          <span>
            {t("detail.screenshot.capturedAt", {
              when: capturedAt
                ? new Date(capturedAt).toLocaleString()
                : t("detail.screenshot.unknownTime"),
            })}
          </span>
          {region && (
            <span>{t("detail.screenshot.region", { region })}</span>
          )}
          {typeof shot.size === "number" && (
            <span>{formatAttachmentBytes(shot.size)}</span>
          )}
        </div>
        <p className="text-xs text-muted-foreground">
          {t("detail.screenshot.disclaimer")}
        </p>
      </CardContent>
    </Card>
  );
}

// formatCaptureBytes renders a byte count compactly (the capture cap is 16 KB,
// so KB is the largest unit that ever matters).
function formatCaptureBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(1)} KB`;
}

// ProbeResponseCard renders "What the probe saw": the opt-in capture of the
// response that opened (or reopened) this incident.
//
// Deliberately conservative in what it claims: this is what the probe received
// at failure DETECTION, from one region, at one instant — never presented as
// the target's current state. The capture timestamp and probing region sit
// next to the content for exactly that reason.
function ProbeResponseCard({ incident }: { incident: IncidentDetail }) {
  const { t } = useTranslation("incidents");
  const capture: IncidentFailureResponse | undefined =
    incident.details?.failureResponse;

  if (!capture) return null;

  const headerEntries = Object.entries(capture.headers ?? {}).sort(([a], [b]) =>
    a.localeCompare(b),
  );

  return (
    <Card data-testid="probe-response-card">
      <CardHeader>
        <CardTitle>{t("detail.probeResponse.title")}</CardTitle>
        <CardDescription>
          {t("detail.probeResponse.description")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <span
            className="rounded-md bg-muted px-2 py-1 font-mono text-sm"
            data-testid="probe-response-status-line"
          >
            {capture.statusLine || capture.statusCode || "-"}
          </span>
          {capture.url && (
            <span className="break-all font-mono text-xs text-muted-foreground">
              {capture.url}
            </span>
          )}
        </div>

        <div
          className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm sm:grid-cols-4"
          data-testid="probe-response-meta"
        >
          <div>
            <div className="text-xs text-muted-foreground">
              {t("detail.probeResponse.capturedAt")}
            </div>
            <div>
              {capture.capturedAt
                ? new Date(capture.capturedAt).toLocaleString()
                : "-"}
            </div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">
              {t("detail.probeResponse.region")}
            </div>
            <div>{capture.region || "-"}</div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">
              {t("detail.probeResponse.remoteAddr")}
            </div>
            <div className="break-all font-mono text-xs">
              {capture.remoteAddr || "-"}
            </div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">
              {t("detail.probeResponse.contentType")}
            </div>
            <div className="break-all text-xs">
              {capture.contentType || "-"}
            </div>
          </div>
        </div>

        {capture.truncated && (
          <Alert data-testid="probe-response-truncated-notice">
            <AlertTriangle className="h-4 w-4" />
            <AlertDescription>
              {t("detail.probeResponse.truncated", {
                shown: formatCaptureBytes(16 * 1024),
                total:
                  typeof capture.contentLength === "number" &&
                  capture.contentLength > 0
                    ? formatCaptureBytes(capture.contentLength)
                    : t("detail.probeResponse.unknownSize"),
              })}
            </AlertDescription>
          </Alert>
        )}

        {capture.binary && (
          <Alert data-testid="probe-response-binary-notice">
            <AlertTriangle className="h-4 w-4" />
            <AlertDescription>
              {t("detail.probeResponse.binary", {
                size:
                  typeof capture.bodyBytes === "number"
                    ? formatCaptureBytes(capture.bodyBytes)
                    : t("detail.probeResponse.unknownSize"),
                hash: capture.bodySha256 ?? "-",
              })}
            </AlertDescription>
          </Alert>
        )}

        {capture.body !== undefined && capture.body !== "" && (
          <CollapsibleCode
            label={t("detail.probeResponse.bodyLabel")}
            value={capture.body}
          />
        )}

        {headerEntries.length > 0 && (
          <div className="overflow-x-auto">
            <Table data-testid="probe-response-headers-table">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("detail.probeResponse.headerName")}</TableHead>
                  <TableHead>{t("detail.probeResponse.headerValue")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {headerEntries.map(([name, value]) => (
                  <TableRow key={name}>
                    <TableCell className="whitespace-nowrap font-mono text-xs">
                      {name}
                    </TableCell>
                    <TableCell className="break-all font-mono text-xs">
                      {value}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <p className="pt-2 text-xs text-muted-foreground">
              {t("detail.probeResponse.redactionNote")}
            </p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function BlastRadiusCard({
  org,
  incident,
}: {
  org: string;
  incident: IncidentDetail;
}) {
  const { t } = useTranslation("incidents");
  const { data: children } = useIncidents(org, {
    causedByIncidentUid: incident.uid,
    size: 50,
    with: "check",
    refetchInterval: incident.state === "active" ? 30_000 : undefined,
  });

  const items = children?.data ?? [];
  if (items.length === 0) return null;

  return (
    <Card data-testid="blast-radius-card">
      <CardHeader>
        <CardTitle>
          {t("rollup.blastRadiusTitle", { count: items.length })}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead data-testid="blast-radius-header-check">
                {t("detail.checkLabel")}
              </TableHead>
              <TableHead
                className="whitespace-nowrap"
                data-testid="blast-radius-header-state"
              >
                {t("detail.state")}
              </TableHead>
              <TableHead
                className="whitespace-nowrap"
                data-testid="blast-radius-header-paging"
              >
                {t("rollup.pagingColumn")}
              </TableHead>
              <TableHead className="whitespace-nowrap px-2">
                <span className="sr-only">{t("rollup.checkLink")}</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((child) => {
              const displayName =
                child.checkName || child.checkSlug || child.checkUid;
              // checkUid alone is NOT "the check still exists" — it is an
              // always-present historical FK on the incident row (backend
              // IncidentResponse.CheckUID is a plain non-omitempty string,
              // never cleared when the check is later hard-deleted). The
              // only signal that hydration (`with=check`) actually found a
              // live check is checkName/checkSlug being populated — the same
              // signal displayName's fallback to the raw UID already relies
              // on. Guarding the check-page link on checkUid alone would
              // link to a check that 404s once it's gone. Bind the narrowed
              // value (not just a boolean) so the Link below gets a `string`,
              // not `string | undefined`.
              const liveCheckUid =
                child.checkUid && (child.checkName || child.checkSlug)
                  ? child.checkUid
                  : undefined;
              return (
                <TableRow key={child.uid} data-testid="blast-radius-row">
                  <TableCell className="max-w-0">
                    {child.uid ? (
                      <Link
                        to="/orgs/$org/incidents/$incidentUid"
                        params={{ org, incidentUid: child.uid }}
                        title={displayName}
                        data-testid="blast-radius-check-link"
                        className="block truncate font-medium text-primary hover:underline"
                      >
                        {displayName}
                      </Link>
                    ) : (
                      <span
                        title={displayName}
                        className="block truncate font-medium"
                      >
                        {displayName}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="whitespace-nowrap">
                    <Badge
                      variant={
                        child.state === "active" ? "destructive" : "secondary"
                      }
                    >
                      {child.state === "active" ? t("active") : t("resolved")}
                    </Badge>
                  </TableCell>
                  <TableCell className="whitespace-nowrap">
                    {child.pagingSuppressed && (
                      <Badge variant="outline">
                        {t("rollup.rolledUpBadge")}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="whitespace-nowrap px-2 text-right">
                    {liveCheckUid && (
                      <Link
                        to="/orgs/$org/checks/$checkUid"
                        params={{ org, checkUid: liveCheckUid }}
                        search={{
                          graphPeriod: undefined,
                          graphFull: undefined,
                          region: undefined,
                        }}
                        aria-label={t("rollup.checkLink")}
                        data-testid="blast-radius-open-check"
                        className="inline-flex text-muted-foreground hover:text-foreground"
                      >
                        <ExternalLink className="h-3.5 w-3.5" />
                      </Link>
                    )}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
        <p className="mt-3 text-xs text-muted-foreground">
          {t("rollup.blastRadiusFooter")}
        </p>
      </CardContent>
    </Card>
  );
}

// ─── Notifications card ───────────────────────────────────────────────────────

function NotificationsCard({
  org,
  incidentUid,
}: {
  org: string;
  incidentUid: string;
}) {
  const { t } = useTranslation("common");
  const { t: tEvents } = useTranslation("events");
  const navigate = useNavigate();
  const { data: rows, isLoading } = useIncidentNotifications(org, incidentUid);

  const hasErrors = rows?.some((r) => r.error);

  const openNotification = (notifUid: string) =>
    navigate({
      to: "/orgs/$org/notifications/$notificationUid",
      params: { org, notificationUid: notifUid },
      search: { from: `incident:${incidentUid}` },
    });

  return (
    <Card data-testid="notifications-card">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          Notifications
          {rows && rows.length > 0 && (
            <Badge variant="outline" className="text-xs">
              {rows.length}
            </Badge>
          )}
        </CardTitle>
        <CardDescription>
          Who was notified and the delivery status.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading && <Skeleton className="h-24 w-full" />}
        {!isLoading && (!rows || rows.length === 0) && (
          <p className="text-sm text-muted-foreground py-4 text-center">
            No notifications recorded for this incident yet.
          </p>
        )}
        {!isLoading && rows && rows.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>Event</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Target</TableHead>
                <TableHead>Source</TableHead>
                <TableHead>Channel</TableHead>
                {hasErrors && <TableHead>Error</TableHead>}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow
                  key={row.uid}
                  role="link"
                  tabIndex={0}
                  className="cursor-pointer hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:outline-none"
                  onClick={() => openNotification(row.uid)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      openNotification(row.uid);
                    }
                  }}
                  data-testid="notification-row"
                >
                  <TableCell
                    className="text-sm whitespace-nowrap"
                    title={row.createdAt}
                  >
                    {new Date(row.createdAt).toLocaleString()}
                  </TableCell>
                  <TableCell>
                    <EventTypeBadge eventType={row.eventType} t={tEvents} />
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant={notificationStatusVariant(row.status)}
                      className="text-xs capitalize"
                    >
                      {row.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-sm">
                    {row.user ? (
                      <span className="font-medium">
                        {row.user.name || row.user.uid}
                      </span>
                    ) : row.connection ? (
                      <span className="flex items-center gap-1">
                        <span className="capitalize text-muted-foreground text-xs">
                          {row.connection.type}
                        </span>
                        {row.connection.name}
                      </span>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    {sourceLabel(row.source, row.repeatIndex)}
                  </TableCell>
                  <TableCell className="text-sm">
                    {channelTypeLabel(t, row.channelType)}
                  </TableCell>
                  {hasErrors && (
                    <TableCell
                      className="text-sm text-destructive max-w-[200px] truncate"
                      title={row.error ?? undefined}
                    >
                      {row.error ?? ""}
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

// ─── Escalation timeline card ─────────────────────────────────────────────────

interface EscalationTimelineCardProps {
  events: Event[];
}

function EscalationTimelineCard({ events }: EscalationTimelineCardProps) {
  const { t } = useTranslation(["escalation"]);

  if (!events || events.length === 0) {
    return null;
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("escalation:timeline.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {events.map((event) => {
          const failed = event.eventType === "incident.escalation_failed";
          const stepPos = event.payload?.step_position as number | undefined;
          const repeatIdx = event.payload?.repeat_index as number | undefined;
          return (
            <div
              key={event.uid}
              className="flex items-center gap-3 text-sm py-1 border-b last:border-0"
            >
              <span
                className={
                  failed
                    ? "text-red-500 font-medium"
                    : "text-green-600 font-medium"
                }
              >
                {failed
                  ? t("escalation:timeline.failed")
                  : t("escalation:timeline.fired")}
              </span>
              <span className="text-muted-foreground">
                {event.createdAt
                  ? new Date(event.createdAt).toLocaleString()
                  : "-"}
              </span>
              {stepPos !== undefined && <span>· step {stepPos + 1}</span>}
              {repeatIdx !== undefined && repeatIdx > 0 && (
                <span>· cycle {repeatIdx + 1}</span>
              )}
              {failed && typeof event.payload?.reason === "string" && (
                <span className="text-red-500">· {event.payload.reason}</span>
              )}
            </div>
          );
        })}
      </CardContent>
    </Card>
  );
}
