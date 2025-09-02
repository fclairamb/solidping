import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  Plus,
  Search,
  RefreshCw,
  MoreVertical,
  Trash2,
  Loader2,
  ChevronRight,
  ChevronDown,
  Eye,
  FolderPlus,
  FolderSymlink,
  Pencil,
  ArrowUp,
  ArrowDown,
  Download,
  Upload,
} from "lucide-react";
import { toast } from "sonner";
import {
  useInfiniteChecks,
  useDeleteCheck,
  useCheckGroups,
  useCreateCheckGroup,
  useDeleteCheckGroup,
  useImportChecks,
  type Check,
  type CheckGroup,
  type ExportDocument,
  type ImportResult,
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { QueryErrorView } from "@/components/shared/error-views";
import { ApiError, apiFetch } from "@/api/client";
import { useQueryClient } from "@tanstack/react-query";

export const Route = createFileRoute("/orgs/$org/checks/")({
  component: ChecksIndexPage,
});

function StatusDot({ status }: { status?: string | null }) {
  const color =
    status === "up"
      ? "bg-green-500"
      : status === "down" || status === "error"
        ? "bg-red-500"
        : status === "timeout"
          ? "bg-yellow-500"
          : "bg-muted-foreground";

  return <div className={`h-2.5 w-2.5 rounded-full ${color}`} />;
}

function StatusBadge({ status }: { status?: string | null }) {
  const label = status || "unknown";
  const className =
    label === "up"
      ? "bg-green-500/10 text-green-500"
      : label === "down" || label === "error"
        ? "bg-red-500/10 text-red-500"
        : "";

  return (
    <Badge variant="secondary" className={className}>
      {label}
    </Badge>
  );
}

function CheckRow({
  check,
  org,
  onDelete,
  onChangeGroup,
  groups,
}: {
  check: Check;
  org: string;
  onDelete: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
}) {
  const { t } = useTranslation("checks");
  return (
    <TableRow>
      <TableCell>
        <Link
          to="/orgs/$org/checks/$checkUid"
          params={{ org, checkUid: check.uid }}
          search={{ graphPeriod: undefined, graphFull: undefined }}
          className="flex items-center gap-2 hover:underline font-medium"
        >
          <StatusDot status={check.lastResult?.status} />
          {check.name || check.slug || check.uid?.slice(0, 8)}
        </Link>
      </TableCell>
      <TableCell>
        <div className="flex items-center gap-1">
          <Badge variant="outline" className="text-xs">
            {check.type}
          </Badge>
          {check.internal && (
            <Badge variant="secondary" className="text-xs">
              {t("internal")}
            </Badge>
          )}
        </div>
      </TableCell>
      <TableCell className="text-muted-foreground">
        {check.type === "heartbeat"
          ? (check.slug || "heartbeat")
          : (check.config?.url as string) ||
            (check.config?.host as string) ||
            check.slug}
      </TableCell>
      <TableCell>
        <StatusBadge status={check.lastResult?.status} />
      </TableCell>
      <TableCell className="text-muted-foreground">
        {check.lastResult?.durationMs != null
          ? `${Math.round(check.lastResult.durationMs)}ms`
          : "\u2014"}
      </TableCell>
      <TableCell>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" className="h-8 w-8">
              <MoreVertical className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem asChild>
              <Link
                to="/orgs/$org/checks/$checkUid"
                params={{ org, checkUid: check.uid }}
                search={{ graphPeriod: undefined, graphFull: undefined }}
              >
                <Eye className="mr-2 h-4 w-4" />
                {t("menu.viewDetails")}
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem asChild>
              <Link
                to="/orgs/$org/checks/$checkUid/edit"
                params={{ org, checkUid: check.uid }}
              >
                <Pencil className="mr-2 h-4 w-4" />
                {t("menu.edit")}
              </Link>
            </DropdownMenuItem>
            {groups.length > 0 && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={() => onChangeGroup(check)}
                  data-testid="change-group-action"
                >
                  <FolderSymlink className="mr-2 h-4 w-4" />
                  {t("menu.changeGroup")}
                </DropdownMenuItem>
              </>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuItem
              className="text-destructive"
              onClick={() => onDelete(check.uid)}
            >
              <Trash2 className="mr-2 h-4 w-4" />
              {t("menu.delete")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </TableCell>
    </TableRow>
  );
}

function ChecksTable({
  checks,
  org,
  onDelete,
  onChangeGroup,
  groups,
}: {
  checks: Check[];
  org: string;
  onDelete: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
}) {
  const { t } = useTranslation("checks");
  return (
    <div className="rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("table.name")}</TableHead>
            <TableHead>{t("table.type")}</TableHead>
            <TableHead>{t("table.target")}</TableHead>
            <TableHead>{t("table.status")}</TableHead>
            <TableHead>{t("table.response")}</TableHead>
            <TableHead className="w-[50px]" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {checks.map((check) => (
            <CheckRow
              key={check.uid}
              check={check}
              org={org}
              onDelete={onDelete}
              onChangeGroup={onChangeGroup}
              groups={groups}
            />
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function CheckGroupSection({
  group,
  org,
  search,
  internalFilter,
  isFirst,
  isLast,
  onDelete,
  onRename,
  onMoveUp,
  onMoveDown,
  onDeleteCheck,
  onChangeGroup,
  groups,
}: {
  group: CheckGroup;
  org: string;
  search: string;
  internalFilter?: string;
  isFirst: boolean;
  isLast: boolean;
  onDelete: () => void;
  onRename: () => void;
  onMoveUp: () => void;
  onMoveDown: () => void;
  onDeleteCheck: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
}) {
  const { t } = useTranslation("checks");
  const [collapsed, setCollapsed] = useState(false);
  const sentinelRef = useRef<HTMLDivElement>(null);

  const {
    data,
    isLoading,
    error,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteChecks(org, {
    with: "last_result",
    checkGroupUid: group.uid,
    q: search || undefined,
    internal: internalFilter,
    limit: 20,
  });

  const checks = data?.pages.flatMap((page) => page.data || []) ?? [];

  // Expand when searching
  useEffect(() => {
    if (search) setCollapsed(false);
  }, [search]);

  const handleObserver = useCallback(
    (entries: IntersectionObserverEntry[]) => {
      const [entry] = entries;
      if (entry.isIntersecting && hasNextPage && !isFetchingNextPage) {
        fetchNextPage();
      }
    },
    [fetchNextPage, hasNextPage, isFetchingNextPage]
  );

  useEffect(() => {
    const el = sentinelRef.current;
    if (!el || collapsed) return;
    const observer = new IntersectionObserver(handleObserver, {
      threshold: 0.1,
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [handleObserver, collapsed]);

  return (
    <div className="border rounded-lg" data-testid="group-section">
      <div
        className="flex items-center justify-between px-4 py-3 cursor-pointer hover:bg-muted/50"
        onClick={() => setCollapsed(!collapsed)}
      >
        <div className="flex items-center gap-2">
          {collapsed ? (
            <ChevronRight className="h-4 w-4 text-muted-foreground" />
          ) : (
            <ChevronDown className="h-4 w-4 text-muted-foreground" />
          )}
          <span className="font-semibold" data-testid="group-name">{group.name}</span>
          <Badge variant="secondary" className="text-xs">
            {group.checkCount}
          </Badge>
        </div>
        <div onClick={(e) => e.stopPropagation()}>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="h-8 w-8" data-testid="group-menu-button">
                <MoreVertical className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={onRename} data-testid="group-rename-action">
                <Pencil className="mr-2 h-4 w-4" />
                {t("menu.rename")}
              </DropdownMenuItem>
              {!isFirst && (
                <DropdownMenuItem onClick={onMoveUp} data-testid="group-move-up-action">
                  <ArrowUp className="mr-2 h-4 w-4" />
                  {t("menu.moveUp")}
                </DropdownMenuItem>
              )}
              {!isLast && (
                <DropdownMenuItem onClick={onMoveDown} data-testid="group-move-down-action">
                  <ArrowDown className="mr-2 h-4 w-4" />
                  {t("menu.moveDown")}
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              <DropdownMenuItem
                className="text-destructive"
                onClick={onDelete}
                data-testid="group-delete-action"
              >
                <Trash2 className="mr-2 h-4 w-4" />
                {t("menu.deleteGroup")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {!collapsed && (
        <div className="border-t">
          {error ? (
            <div className="p-4 text-sm text-destructive">
              {t("failedToLoadChecks")}
            </div>
          ) : isLoading ? (
            <div className="p-4 space-y-2">
              {[...Array(3)].map((_, i) => (
                <Skeleton key={i} className="h-10 rounded-lg" />
              ))}
            </div>
          ) : checks.length > 0 ? (
            <>
              <ChecksTable
                checks={checks}
                org={org}
                onDelete={onDeleteCheck}
                onChangeGroup={onChangeGroup}
                groups={groups}
              />
              <div ref={sentinelRef} className="flex justify-center py-2">
                {isFetchingNextPage && (
                  <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                )}
              </div>
            </>
          ) : (
            <div className="p-4 text-center text-sm text-muted-foreground">
              {t("noChecks")}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function UngroupedChecksSection({
  org,
  search,
  internalFilter,
  onDeleteCheck,
  onChangeGroup,
  groups,
}: {
  org: string;
  search: string;
  internalFilter?: string;
  onDeleteCheck: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
}) {
  const { t } = useTranslation("checks");
  const sentinelRef = useRef<HTMLDivElement>(null);

  const {
    data,
    isLoading,
    error,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteChecks(org, {
    with: "last_result",
    checkGroupUid: "none",
    q: search || undefined,
    internal: internalFilter,
    limit: 20,
  });

  const checks = data?.pages.flatMap((page) => page.data || []) ?? [];
  const total = data?.pages[0]?.pagination?.total ?? 0;

  const handleObserver = useCallback(
    (entries: IntersectionObserverEntry[]) => {
      const [entry] = entries;
      if (entry.isIntersecting && hasNextPage && !isFetchingNextPage) {
        fetchNextPage();
      }
    },
    [fetchNextPage, hasNextPage, isFetchingNextPage]
  );

  useEffect(() => {
    const el = sentinelRef.current;
    if (!el) return;
    const observer = new IntersectionObserver(handleObserver, {
      threshold: 0.1,
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [handleObserver]);

  if (!isLoading && !error && total === 0 && !search) {
    return null;
  }

  return (
    <div>
      <h3 className="text-sm font-medium text-muted-foreground mb-2">
        {t("ungroupedChecks")}
      </h3>
      {error ? (
        <div className="p-4 text-sm text-destructive">
          {t("failedToLoadChecks")}
        </div>
      ) : isLoading ? (
        <div className="space-y-2">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-10 rounded-lg" />
          ))}
        </div>
      ) : checks.length > 0 ? (
        <>
          <ChecksTable checks={checks} org={org} onDelete={onDeleteCheck} onChangeGroup={onChangeGroup} groups={groups} />
          <div ref={sentinelRef} className="flex justify-center py-4">
            {isFetchingNextPage && (
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            )}
          </div>
        </>
      ) : search ? (
        <div className="text-center py-6 text-muted-foreground text-sm">
          {t("noUngroupedChecks")}
        </div>
      ) : null}
    </div>
  );
}

function useDebounce(value: string, delay: number) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay);
    return () => clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

function ChecksIndexPage() {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const { org } = Route.useParams();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [internalFilter, setInternalFilter] = useState<string>("false");
  const [deleteCheckUid, setDeleteCheckUid] = useState<string | null>(null);
  const [deleteGroupUid, setDeleteGroupUid] = useState<string | null>(null);
  const [showNewGroup, setShowNewGroup] = useState(false);
  const [newGroupName, setNewGroupName] = useState("");
  const [renameGroup, setRenameGroup] = useState<CheckGroup | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [changeGroupCheck, setChangeGroupCheck] = useState<Check | null>(null);
  const debouncedSearch = useDebounce(search, 300);

  const {
    data: groups,
    isLoading: groupsLoading,
    error: groupsError,
    refetch: refetchGroups,
    isRefetching,
  } = useCheckGroups(org);

  const [importPreview, setImportPreview] = useState<{
    doc: ExportDocument;
    result: ImportResult;
  } | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const deleteCheck = useDeleteCheck(org);
  const createGroup = useCreateCheckGroup(org);
  const deleteGroup = useDeleteCheckGroup(org);
  const importChecks = useImportChecks(org);

  const handleDeleteCheck = async () => {
    if (!deleteCheckUid) return;
    try {
      await deleteCheck.mutateAsync(deleteCheckUid);
      toast.success(t("toast.deleted"));
      setDeleteCheckUid(null);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("toast.deleteFailed"));
    }
  };

  const handleDeleteGroup = async () => {
    if (!deleteGroupUid) return;
    try {
      await deleteGroup.mutateAsync(deleteGroupUid);
      toast.success(t("toast.groupDeleted"));
      setDeleteGroupUid(null);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("toast.groupDeleteFailed"));
    }
  };

  const handleCreateGroup = async () => {
    if (!newGroupName.trim()) return;
    try {
      await createGroup.mutateAsync({ name: newGroupName.trim() });
      toast.success(t("toast.groupCreated"));
      setNewGroupName("");
      setShowNewGroup(false);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("toast.groupCreateFailed"));
    }
  };

  const handleRename = async () => {
    if (!renameGroup || !renameValue.trim()) return;
    try {
      await apiFetch(`/api/v1/orgs/${org}/check-groups/${renameGroup.uid}`, {
        method: "PATCH",
        body: JSON.stringify({ name: renameValue.trim() }),
      });
      queryClient.invalidateQueries({ queryKey: ["checkGroups", org] });
      toast.success(t("toast.groupRenamed"));
      setRenameGroup(null);
    } catch {
      toast.error(t("toast.groupRenameFailed"));
    }
  };

  const handleMoveGroup = async (group: CheckGroup, direction: "up" | "down") => {
    if (!groups) return;
    const idx = groups.findIndex((g) => g.uid === group.uid);
    const swapIdx = direction === "up" ? idx - 1 : idx + 1;
    if (swapIdx < 0 || swapIdx >= groups.length) return;

    // Set sort_order to the neighbor's value to trigger backend normalization
    const targetOrder = groups[swapIdx].sortOrder;
    const newOrder = direction === "up" ? targetOrder - 1 : targetOrder + 1;

    try {
      await apiFetch(`/api/v1/orgs/${org}/check-groups/${group.uid}`, {
        method: "PATCH",
        body: JSON.stringify({ sortOrder: newOrder }),
      });
      queryClient.invalidateQueries({ queryKey: ["checkGroups", org] });
    } catch {
      toast.error(t("toast.reorderFailed"));
    }
  };

  const handleExport = async () => {
    try {
      const token = localStorage.getItem("solidping_session_token");
      const response = await fetch(`/api/v1/orgs/${org}/checks/export`, {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      });
      if (!response.ok) throw new Error("Export failed");
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `solidping-checks-${org}.json`;
      a.click();
      URL.revokeObjectURL(url);
      toast.success(t("toast.exportSuccess"));
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("toast.exportFailed"));
    }
  };

  const handleImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    try {
      const text = await file.text();
      const doc = JSON.parse(text) as ExportDocument;
      const result = await importChecks.mutateAsync({ doc, dryRun: true });
      setImportPreview({ doc, result });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("toast.parseFailed"));
    }
    // Reset file input so the same file can be re-selected
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  const handleImportConfirm = async () => {
    if (!importPreview) return;
    try {
      const result = await importChecks.mutateAsync({ doc: importPreview.doc });
      toast.success(t("toast.importSuccess", { count: result.created + result.updated, created: result.created, updated: result.updated }));
      setImportPreview(null);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("toast.importFailed"));
    }
  };

  const handleRefresh = () => {
    refetchGroups();
    queryClient.invalidateQueries({ queryKey: ["checks", "infinite", org] });
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("title")}</h1>
          <p className="text-muted-foreground">{t("subtitle")}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={handleExport} data-testid="export-button">
            <Download className="mr-2 h-4 w-4" />
            {t("export")}
          </Button>
          <Button variant="outline" onClick={() => fileInputRef.current?.click()} data-testid="import-button">
            <Upload className="mr-2 h-4 w-4" />
            {t("import")}
          </Button>
          <input
            ref={fileInputRef}
            type="file"
            accept=".json"
            className="hidden"
            onChange={handleImportFile}
          />
          <Button variant="outline" onClick={() => setShowNewGroup(true)} data-testid="new-group-button">
            <FolderPlus className="mr-2 h-4 w-4" />
            {t("newGroup")}
          </Button>
          <Link to="/orgs/$org/checks/new" params={{ org }} search={{ checkType: undefined, checkPeriod: undefined, checkName: undefined, checkSlug: undefined, httpUrl: undefined, httpMethod: undefined, host: undefined, port: undefined, url: undefined, domain: undefined, username: undefined, database: undefined }}>
            <Button data-testid="new-check-button">
              <Plus className="mr-2 h-4 w-4" />
              {t("newCheck")}
            </Button>
          </Link>
        </div>
      </div>

      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("searchChecks")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9"
          />
        </div>
        <Select value={internalFilter} onValueChange={setInternalFilter}>
          <SelectTrigger className="w-[160px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="false">{t("userChecks")}</SelectItem>
            <SelectItem value="true">{t("internalOnly")}</SelectItem>
            <SelectItem value="all">{t("allChecks")}</SelectItem>
          </SelectContent>
        </Select>
        <Button
          variant="outline"
          size="icon"
          onClick={handleRefresh}
          disabled={isRefetching}
        >
          <RefreshCw
            className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`}
          />
        </Button>
      </div>

      {groupsError ? (
        <QueryErrorView error={groupsError} org={org} onRetry={() => refetchGroups()} />
      ) : groupsLoading ? (
        <div className="space-y-2">
          {[...Array(6)].map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-lg" />
          ))}
        </div>
      ) : (
        <div className="space-y-4">
          {(groups || []).map((group, idx) => (
            <CheckGroupSection
              key={group.uid}
              group={group}
              org={org}
              search={debouncedSearch}
              internalFilter={internalFilter}
              isFirst={idx === 0}
              isLast={idx === (groups?.length ?? 0) - 1}
              onDelete={() => setDeleteGroupUid(group.uid)}
              onRename={() => {
                setRenameGroup(group);
                setRenameValue(group.name);
              }}
              onMoveUp={() => handleMoveGroup(group, "up")}
              onMoveDown={() => handleMoveGroup(group, "down")}
              onDeleteCheck={setDeleteCheckUid}
              onChangeGroup={setChangeGroupCheck}
              groups={groups || []}
            />
          ))}

          <UngroupedChecksSection
            org={org}
            search={debouncedSearch}
            internalFilter={internalFilter}
            onDeleteCheck={setDeleteCheckUid}
            onChangeGroup={setChangeGroupCheck}
            groups={groups || []}
          />

          {(!groups || groups.length === 0) && (
            <NoChecksPlaceholder search={debouncedSearch} />
          )}
        </div>
      )}

      {/* Delete Check Dialog */}
      <AlertDialog open={!!deleteCheckUid} onOpenChange={() => setDeleteCheckUid(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("dialog.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("dialog.deleteDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteCheck}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {tc("delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Delete Group Dialog */}
      <AlertDialog open={!!deleteGroupUid} onOpenChange={() => setDeleteGroupUid(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("dialog.deleteGroupTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("dialog.deleteGroupDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteGroup}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              data-testid="confirm-delete-group"
            >
              {t("menu.deleteGroup")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* New Group Dialog */}
      <Dialog open={showNewGroup} onOpenChange={setShowNewGroup}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("dialog.newGroupTitle")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="group-name">{tc("name")}</Label>
              <Input
                id="group-name"
                placeholder={t("dialog.groupNamePlaceholder")}
                value={newGroupName}
                onChange={(e) => setNewGroupName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") handleCreateGroup();
                }}
                autoFocus
                data-testid="new-group-name-input"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowNewGroup(false)}>
              {tc("cancel")}
            </Button>
            <Button
              onClick={handleCreateGroup}
              disabled={!newGroupName.trim() || createGroup.isPending}
              data-testid="new-group-submit"
            >
              {createGroup.isPending ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : null}
              {tc("create")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Rename Group Dialog */}
      <Dialog open={!!renameGroup} onOpenChange={() => setRenameGroup(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("dialog.renameGroupTitle")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="rename-group">{tc("name")}</Label>
              <Input
                id="rename-group"
                value={renameValue}
                onChange={(e) => setRenameValue(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") handleRename();
                }}
                autoFocus
                data-testid="rename-group-input"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRenameGroup(null)}>
              {tc("cancel")}
            </Button>
            <Button
              onClick={handleRename}
              disabled={!renameValue.trim()}
              data-testid="rename-group-submit"
            >
              {t("menu.rename")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Import Preview Dialog */}
      <Dialog open={!!importPreview} onOpenChange={() => setImportPreview(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("dialog.importTitle")}</DialogTitle>
          </DialogHeader>
          {importPreview && (
            <div className="space-y-4 py-2">
              <div className="grid grid-cols-3 gap-4 text-center">
                <div>
                  <div className="text-2xl font-bold text-green-600">{importPreview.result.created}</div>
                  <div className="text-sm text-muted-foreground">{t("dialog.toCreate")}</div>
                </div>
                <div>
                  <div className="text-2xl font-bold text-blue-600">{importPreview.result.updated}</div>
                  <div className="text-sm text-muted-foreground">{t("dialog.toUpdate")}</div>
                </div>
                <div>
                  <div className="text-2xl font-bold text-red-600">{importPreview.result.errors.length}</div>
                  <div className="text-sm text-muted-foreground">{t("dialog.errors")}</div>
                </div>
              </div>
              {importPreview.result.errors.length > 0 && (
                <div className="rounded-md bg-red-50 dark:bg-red-950 p-3 text-sm">
                  {importPreview.result.errors.map((err) => (
                    <div key={err.index} className="text-red-700 dark:text-red-300">
                      <span className="font-mono">{err.slug}</span>: {err.error}
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setImportPreview(null)}>
              {tc("cancel")}
            </Button>
            <Button
              onClick={handleImportConfirm}
              disabled={importChecks.isPending || (importPreview?.result.created === 0 && importPreview?.result.updated === 0)}
            >
              {importChecks.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              {t("import")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Change Group Dialog */}
      <Dialog open={!!changeGroupCheck} onOpenChange={() => setChangeGroupCheck(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("dialog.changeGroupTitle")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <p className="text-sm text-muted-foreground">
              {t("dialog.moveCheckDescription", { name: changeGroupCheck?.name || changeGroupCheck?.slug })}
            </p>
            <div className="space-y-2">
              <Label>{t("dialog.group")}</Label>
              <Select
                value={changeGroupCheck?.checkGroupUid || "none"}
                onValueChange={async (value) => {
                  if (!changeGroupCheck) return;
                  const newGroupUid = value === "none" ? "" : value;
                  try {
                    await apiFetch(`/api/v1/orgs/${org}/checks/${changeGroupCheck.uid}`, {
                      method: "PATCH",
                      body: JSON.stringify({ checkGroupUid: newGroupUid }),
                    });
                    queryClient.invalidateQueries({ queryKey: ["checks", "infinite", org] });
                    queryClient.invalidateQueries({ queryKey: ["checkGroups", org] });
                    toast.success(t("toast.checkMoved"));
                    setChangeGroupCheck(null);
                  } catch (err) {
                    toast.error(err instanceof ApiError ? err.message : t("toast.moveFailed"));
                  }
                }}
              >
                <SelectTrigger data-testid="change-group-select">
                  <SelectValue placeholder={t("dialog.selectGroup")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{t("dialog.noGroup")}</SelectItem>
                  {(groups || []).map((g) => (
                    <SelectItem key={g.uid} value={g.uid}>
                      {g.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function NoChecksPlaceholder({ search }: { search: string }) {
  // This renders when there are no groups. The UngroupedChecksSection handles
  // showing ungrouped checks, so this only appears when there's nothing at all.
  if (search) return null;

  return null; // UngroupedChecksSection will handle the empty state
}
