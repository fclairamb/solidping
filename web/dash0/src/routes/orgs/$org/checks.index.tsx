import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useDebounce } from "@/lib/use-debounce";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
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
  ListChecks,
  Pencil,
  ArrowUp,
  ArrowDown,
  Download,
  Upload,
  Waypoints,
  ArrowUpRight,
} from "lucide-react";
import { toast } from "sonner";
import {
  useInfiniteChecks,
  useDeleteCheck,
  useCheckGroups,
  useCreateCheckGroup,
  useImportChecks,
  useEscalationPolicies,
  type Check,
  type CheckGroup,
  type EscalationPolicy,
  type ExportDocument,
  type ImportResult,
} from "@/api/hooks";
import { useEmailAddressDomain, emailCheckAddress } from "@/api/email-inbox";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { StatusBadge } from "@/components/shared/status-badge";
import { StatusDot } from "@/components/shared/status-dot";
import { PageHeader } from "@/components/shared/page-header";
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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { QueryErrorView } from "@/components/shared/error-views";
import { LabelFilter } from "@/components/shared/label-filter";
import { checkLabel, tunnelCheckUidOf } from "@/components/checks/tunnel";
import { ApiError, apiFetch, getApiErrorField } from "@/api/client";
import { useQueryClient } from "@tanstack/react-query";
import { parseLabelsParam, serializeLabelsParam } from "@/lib/labels";
import { slugify } from "@/lib/utils";
import { useAuth } from "@/contexts/AuthContext";
import { useLiveSubscription } from "@/contexts/LiveEventsContext";

// The checks index can bucket its rows by check group (server-side entity,
// the default) or by the derived targetHost (spec 2026-08-01-04) — no
// server-side identity, purely a client-side view over the same rows.
const GROUP_BY_MODES = ["groups", "host"] as const;
type GroupByMode = (typeof GROUP_BY_MODES)[number];

interface ChecksIndexSearch {
  labels?: string;
  status?: string;
  groupBy?: GroupByMode;
}

// Group slugs are 3-40 chars: a lowercase letter followed by 2-39 lowercase
// letters/digits/hyphens. This mirrors slugRegex in
// server/internal/handlers/checkgroups/service.go — deliberately NOT the
// check form's 3-50 rule (check-form.tsx), which is a different resource.
const groupSlugRegex = /^[a-z][a-z0-9-]{2,39}$/;

export const Route = createFileRoute("/orgs/$org/checks/")({
  component: ChecksIndexPage,
  validateSearch: (search: Record<string, unknown>): ChecksIndexSearch => ({
    labels: typeof search.labels === "string" && search.labels ? search.labels : undefined,
    status: typeof search.status === "string" && search.status ? search.status : undefined,
    groupBy: GROUP_BY_MODES.includes(search.groupBy as GroupByMode)
      ? (search.groupBy as GroupByMode)
      : undefined,
  }),
});

// Manual collapse/expand choices for check-group sections, persisted per org
// so a toggle survives a reload. One JSON map per org: { [groupUid]: boolean }.
// Mirrors the try/catch-guarded localStorage pattern used elsewhere in dash0
// (dashboard-page.tsx's FirstResultCelebration, lib/last-auth-method.ts) —
// localStorage can throw in private-browsing mode or when disabled.
function collapsedGroupsStorageKey(org: string): string {
  return `solidping_collapsed_groups_${org}`;
}

function readCollapsedGroups(org: string): Record<string, boolean> {
  if (typeof window === "undefined") return {};
  try {
    const raw = window.localStorage.getItem(collapsedGroupsStorageKey(org));
    return raw ? (JSON.parse(raw) as Record<string, boolean>) : {};
  } catch {
    return {};
  }
}

function writeCollapsedGroup(org: string, groupUid: string, collapsed: boolean): void {
  if (typeof window === "undefined") return;
  try {
    const map = readCollapsedGroups(org);
    map[groupUid] = collapsed;
    window.localStorage.setItem(collapsedGroupsStorageKey(org), JSON.stringify(map));
  } catch {
    // Ignore — private mode / storage disabled / quota exceeded. The toggle
    // still works for this session, it just won't survive a reload.
  }
}

// Severity order for the group header's compact member summary — failures
// first (the thing you came to see), the healthy count last.
const MEMBER_SUMMARY_ORDER = [
  "down",
  "degraded",
  "warning",
  "validating",
  "created",
  "up",
] as const;

// Renders memberStatusCounts as a compact, localized summary: "3/3 up" when
// every counted member is up (exactly the collapse-eligible case), otherwise
// severity-ordered parts like "1 down · 3 up". Returns null when there are no
// counted (enabled) members at all.
function formatMemberSummary(
  counts: Record<string, number> | undefined,
  t: (key: string, options?: Record<string, unknown>) => string,
): string | null {
  if (!counts) return null;
  const total = Object.values(counts).reduce((sum, n) => sum + n, 0);
  if (total === 0) return null;

  const upCount = counts.up ?? 0;
  if (upCount === total) {
    return t("checks:groupSummary.allUp", { count: total });
  }

  const seen = new Set<string>();
  const parts: string[] = [];
  for (const status of MEMBER_SUMMARY_ORDER) {
    const count = counts[status] ?? 0;
    if (count <= 0) continue;
    seen.add(status);
    parts.push(
      t("checks:groupSummary.part", {
        count,
        label: t(`checks:groupSummary.${status}`, { defaultValue: status }),
      }),
    );
  }
  // Any status outside the known order (future wire values) still counts.
  for (const [status, count] of Object.entries(counts)) {
    if (seen.has(status) || count <= 0) continue;
    parts.push(t("checks:groupSummary.part", { count, label: status }));
  }
  return parts.join(" · ");
}

// Host bucket sections have no server-side identity (unlike check groups,
// which carry a precomputed status/memberStatusCounts from the API) — the
// aggregate status here is a pure client-side function of the currently
// loaded rows, following the exact same worst-of precedence as
// models.RollupGroupStatus (server/internal/db/models/check_group_status.go):
// down if every considered (enabled) member is down, degraded if some (not
// all) are down, warning if none are down but at least one is warning,
// validating if none are down/warning but at least one is validating, up if
// at least one is up, otherwise created.
function computeHostSectionStatus(checks: Check[]): {
  status: string;
  counts: Record<string, number>;
} {
  const counts: Record<string, number> = {};
  let total = 0;
  for (const check of checks) {
    if (check.enabled === false) continue;
    const status = check.status ?? "created";
    counts[status] = (counts[status] ?? 0) + 1;
    total++;
  }

  if (total === 0) return { status: "created", counts };

  const down = counts.down ?? 0;
  if (down === total) return { status: "down", counts };
  if (down > 0) return { status: "degraded", counts };
  if ((counts.warning ?? 0) > 0) return { status: "warning", counts };
  if ((counts.validating ?? 0) > 0) return { status: "validating", counts };
  if ((counts.up ?? 0) > 0) return { status: "up", counts };
  return { status: "created", counts };
}

// Presentational: a by-host bucket. No group entity backs it (no edit link,
// no move up/down, no escalation indicator) — just the hostname, member
// count, and the client-computed rollup status/summary, reusing the same
// collapse-by-default-when-up behavior as CheckGroupSection. Collapse choices
// are session-only (component state, not persisted): unlike a group uid, a
// hostname bucket has no stable identity across a check's config edits, so
// persisting it per-org would accumulate stale keys.
function HostSection({
  hostKey,
  checks,
  org,
  onDeleteCheck,
  onChangeGroup,
  groups,
  checksByUid,
}: {
  /** null = the trailing "no host" bucket (checks with no derivable targetHost). */
  hostKey: string | null;
  checks: Check[];
  org: string;
  onDeleteCheck: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
  checksByUid: Map<string, Check>;
}) {
  const { t } = useTranslation("checks");
  const isNoHost = hostKey === null;
  const { status, counts } = useMemo(() => computeHostSectionStatus(checks), [checks]);
  const memberSummary = formatMemberSummary(counts, t);

  const [manualOverride, setManualOverride] = useState<boolean | null>(null);
  const defaultCollapsed = status === "up";
  const collapsed = manualOverride ?? defaultCollapsed;
  const toggleCollapsed = () => setManualOverride(!collapsed);

  return (
    <div className="border rounded-lg" data-testid="host-section">
      <div
        className="flex items-center justify-between gap-2 px-4 py-3 cursor-pointer hover:bg-muted/50 flex-wrap"
        onClick={toggleCollapsed}
        role="button"
        tabIndex={0}
        aria-expanded={!collapsed}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            toggleCollapsed();
          }
        }}
        data-testid="host-section-header"
      >
        <div className="flex items-center gap-2 min-w-0 flex-wrap">
          <span className="inline-flex shrink-0">
            {collapsed ? (
              <ChevronRight className="h-4 w-4 text-muted-foreground" />
            ) : (
              <ChevronDown className="h-4 w-4 text-muted-foreground" />
            )}
          </span>
          <span
            className={`font-semibold truncate ${isNoHost ? "text-muted-foreground italic" : ""}`}
            data-testid="host-section-name"
          >
            {isNoHost ? t("noHostBucket") : hostKey}
          </span>
          <span data-testid="host-section-status-badge">
            <StatusBadge status={status} />
          </span>
          {memberSummary && (
            <span
              className="text-xs text-muted-foreground whitespace-nowrap"
              data-testid="host-section-member-summary"
            >
              {memberSummary}
            </span>
          )}
          <Badge variant="secondary" className="text-xs">
            {checks.length}
          </Badge>
        </div>
      </div>

      {!collapsed && (
        <div className="border-t">
          <ChecksTable
            checks={checks}
            org={org}
            onDelete={onDeleteCheck}
            onChangeGroup={onChangeGroup}
            groups={groups}
            checksByUid={checksByUid}
          />
        </div>
      )}
    </div>
  );
}

function CheckRow({
  check,
  org,
  onDelete,
  onChangeGroup,
  groups,
  checksByUid,
}: {
  check: Check;
  org: string;
  onDelete: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
  /** Every check loaded on this page, keyed by uid — used to resolve a
   * tunneled check's bastion name without an extra request. */
  checksByUid: Map<string, Check>;
}) {
  const { t } = useTranslation("checks");
  const { data: emailDomain } = useEmailAddressDomain();
  const tunnelUid = tunnelCheckUidOf(check);
  const bastion = tunnelUid ? checksByUid.get(tunnelUid) : undefined;

  function renderTarget(): React.ReactNode {
    if (check.type === "heartbeat") {
      return check.slug || "heartbeat";
    }

    if (check.type === "email") {
      const token = check.config?.token as string | undefined;
      if (!token || !emailDomain) {
        return check.slug || "email";
      }
      const full = emailCheckAddress(token, emailDomain);
      const truncated = `${token.slice(0, 8)}…@${emailDomain}`;
      return (
        <span title={full} className="font-mono text-xs">
          {truncated}
        </span>
      );
    }

    return (
      (check.config?.url as string) ||
      (check.config?.host as string) ||
      check.slug
    );
  }

  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-2">
          <Link
            to="/orgs/$org/checks/$checkUid"
            params={{ org, checkUid: check.uid }}
            search={{ graphPeriod: undefined, graphFull: undefined, graphRegion: undefined, resultsRegion: undefined }}
            className="flex items-center gap-2 hover:underline font-medium"
          >
            <StatusDot
              status={check.status ?? check.lastResult?.status}
              enabled={check.enabled}
              title={check.enabled === false ? t("checks:detail.disabled") : undefined}
            />
            {check.name || check.slug || check.uid?.slice(0, 8)}
          </Link>
          {tunnelUid && (
            <Tooltip>
              <TooltipTrigger asChild>
                <span
                  className="inline-flex text-muted-foreground"
                  data-testid="check-tunnel-indicator"
                >
                  <Waypoints className="h-3.5 w-3.5" />
                </span>
              </TooltipTrigger>
              <TooltipContent>
                via {bastion ? checkLabel(bastion) : "SSH tunnel"}
              </TooltipContent>
            </Tooltip>
          )}
        </div>
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
        {renderTarget()}
      </TableCell>
      <TableCell>
        <StatusBadge status={check.status ?? check.lastResult?.status} />
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
                search={{ graphPeriod: undefined, graphFull: undefined, graphRegion: undefined, resultsRegion: undefined }}
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
  checksByUid,
}: {
  checks: Check[];
  org: string;
  onDelete: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
  checksByUid: Map<string, Check>;
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
              checksByUid={checksByUid}
            />
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

// Presentational: the checks it shows come from the page-level batched query
// (bucketed by checkGroupUid), not from its own request. Collapsing no longer
// affects request cost — there is a single query for the whole page.
function CheckGroupSection({
  group,
  org,
  checks,
  isLoading,
  error,
  search,
  isFirst,
  isLast,
  onMoveUp,
  onMoveDown,
  onDeleteCheck,
  onChangeGroup,
  groups,
  checksByUid,
  escalationPolicyByUid,
}: {
  group: CheckGroup;
  org: string;
  checks: Check[];
  isLoading: boolean;
  error: unknown;
  search: string;
  isFirst: boolean;
  isLast: boolean;
  onMoveUp: () => void;
  onMoveDown: () => void;
  onDeleteCheck: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
  checksByUid: Map<string, Check>;
  escalationPolicyByUid: Map<string, EscalationPolicy>;
}) {
  const { t } = useTranslation("checks");

  // Manual toggles win over the status-based default and persist per org,
  // keyed by group uid (see readCollapsedGroups/writeCollapsedGroup above).
  // `null` means "no manual choice yet" — fall back to the default.
  const [manualOverride, setManualOverride] = useState<boolean | null>(() => {
    const stored = readCollapsedGroups(org)[group.uid];
    return stored === undefined ? null : stored;
  });
  // Groups whose rollup status is "up" start collapsed — anything else
  // (down/degraded/warning/validating/created) starts expanded so the
  // failure that brought the user here is immediately visible.
  const defaultCollapsed = group.status === "up";
  // Searching always force-expands (without touching the persisted choice)
  // so matches are never hidden behind a collapsed section.
  const collapsed = search ? false : (manualOverride ?? defaultCollapsed);

  const toggleCollapsed = () => {
    const next = !collapsed;
    setManualOverride(next);
    writeCollapsedGroup(org, group.uid, next);
  };

  const escalationPolicy = group.escalationPolicyUid
    ? escalationPolicyByUid.get(group.escalationPolicyUid)
    : undefined;
  const memberSummary = formatMemberSummary(group.memberStatusCounts, t);

  return (
    <div className="border rounded-lg" data-testid="group-section">
      <div
        className="flex items-center justify-between gap-2 px-4 py-3 cursor-pointer hover:bg-muted/50 flex-wrap"
        onClick={toggleCollapsed}
        role="button"
        tabIndex={0}
        aria-expanded={!collapsed}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            toggleCollapsed();
          }
        }}
        data-testid="group-header"
      >
        <div className="flex items-center gap-2 min-w-0 flex-wrap">
          <Tooltip>
            <TooltipTrigger asChild>
              <span
                className="inline-flex shrink-0"
                data-testid="group-collapse-toggle"
              >
                {collapsed ? (
                  <ChevronRight className="h-4 w-4 text-muted-foreground" />
                ) : (
                  <ChevronDown className="h-4 w-4 text-muted-foreground" />
                )}
              </span>
            </TooltipTrigger>
            <TooltipContent>
              {collapsed ? t("menu.expandGroup") : t("menu.collapseGroup")}
            </TooltipContent>
          </Tooltip>
          <span className="font-semibold truncate" data-testid="group-name">{group.name}</span>
          <span data-testid="group-status-badge">
            <StatusBadge status={group.status} />
          </span>
          {memberSummary && (
            <span
              className="text-xs text-muted-foreground whitespace-nowrap"
              data-testid="group-member-summary"
            >
              {memberSummary}
            </span>
          )}
          <Badge variant="secondary" className="text-xs">
            {group.checkCount}
          </Badge>
          {group.escalationPolicyUid && (
            <Tooltip>
              <TooltipTrigger asChild>
                <span
                  className="inline-flex text-muted-foreground"
                  data-testid="group-escalation-indicator"
                >
                  <ArrowUpRight className="h-3.5 w-3.5" />
                </span>
              </TooltipTrigger>
              <TooltipContent>
                {t("groupForm.indicatorTooltip", {
                  name: escalationPolicy
                    ? `${escalationPolicy.name}${(escalationPolicy.stepCount ?? escalationPolicy.steps?.length ?? 0) === 0 ? " — silent" : ""}`
                    : "…",
                })}
              </TooltipContent>
            </Tooltip>
          )}
        </div>
        <div
          className="flex items-center gap-1"
          onClick={(e) => e.stopPropagation()}
        >
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label={t("menu.moveUp")}
            disabled={isFirst}
            onClick={onMoveUp}
            data-testid="group-move-up-button"
          >
            <ArrowUp className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label={t("menu.moveDown")}
            disabled={isLast}
            onClick={onMoveDown}
            data-testid="group-move-down-button"
          >
            <ArrowDown className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label={t("menu.editGroup")}
            data-testid="group-edit-button"
            asChild
          >
            <Link
              to="/orgs/$org/check-groups/$uid/edit"
              params={{ org, uid: group.uid }}
            >
              <Pencil className="h-4 w-4" />
            </Link>
          </Button>
        </div>
      </div>

      {!collapsed && (
        <div className="border-t">
          {/* Rows win over the loading branch: an already-populated bucket
           * keeps rendering its rows while the page-level stream continues.
           * A still-empty bucket reads as loading (skeletons) as long as the
           * stream can still deliver more (`isLoading` folds in
           * hasNextPage/isFetchingNextPage) — the truthful "no checks" empty
           * state only shows once the stream is fully drained. */}
          {error ? (
            <div className="p-4 text-sm text-destructive">
              {t("failedToLoadChecks")}
            </div>
          ) : checks.length > 0 ? (
            <ChecksTable
              checks={checks}
              org={org}
              onDelete={onDeleteCheck}
              onChangeGroup={onChangeGroup}
              groups={groups}
              checksByUid={checksByUid}
            />
          ) : isLoading ? (
            <div className="p-4 space-y-2">
              {[...Array(3)].map((_, i) => (
                <Skeleton key={i} className="h-10 rounded-lg" />
              ))}
            </div>
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

// Presentational: renders the ungrouped bucket from the page-level batched
// query. Hidden entirely when there is nothing ungrouped to show and no active
// search (an empty "Ungrouped Checks" heading would just be noise).
function UngroupedChecksSection({
  org,
  checks,
  isLoading,
  error,
  search,
  onDeleteCheck,
  onChangeGroup,
  groups,
  checksByUid,
}: {
  org: string;
  checks: Check[];
  isLoading: boolean;
  error: unknown;
  search: string;
  onDeleteCheck: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
  checksByUid: Map<string, Check>;
}) {
  const { t } = useTranslation("checks");

  if (!isLoading && !error && checks.length === 0 && !search) {
    return null;
  }

  return (
    <div>
      <h3 className="text-sm font-medium text-muted-foreground mb-2">
        {t("ungroupedChecks")}
      </h3>
      {/* Rows win over the loading branch, same as CheckGroupSection: keep any
       * loaded ungrouped rows visible while the stream continues, and treat an
       * empty bucket as loading (skeletons) until the stream drains. */}
      {error ? (
        <div className="p-4 text-sm text-destructive">
          {t("failedToLoadChecks")}
        </div>
      ) : checks.length > 0 ? (
        <ChecksTable checks={checks} org={org} onDelete={onDeleteCheck} onChangeGroup={onChangeGroup} groups={groups} checksByUid={checksByUid} />
      ) : isLoading ? (
        <div className="space-y-2">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-10 rounded-lg" />
          ))}
        </div>
      ) : search ? (
        <div className="text-center py-6 text-muted-foreground text-sm">
          {t("noUngroupedChecks")}
        </div>
      ) : null}
    </div>
  );
}

function ChecksIndexPage() {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const { org } = Route.useParams();
  const { labels: labelsParam, status: statusParam, groupBy: groupByParam } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const labelFilters = parseLabelsParam(labelsParam);
  const queryClient = useQueryClient();
  const { user } = useAuth();

  // The view mode ("Group by: Groups / Host") is the page's primary
  // navigation, so it lives in the URL (search param `groupBy`) like the jobs
  // page's `tab`. It's ALSO mirrored into local state and re-synced whenever
  // groupByParam changes: this route sits under the /orgs/$org layout route,
  // where a cold deep-link's search params are known not to reliably seed a
  // value read only via Route.useSearch() on first render (see
  // memory/reference_dash0_url_search_state.md) — plain validateSearch is not
  // enough, so this is the write-through that catches the value once the
  // router settles. Resyncing during render (comparing against a *state*
  // snapshot of the last-seen groupByParam, per React's "adjusting state
  // during rendering" pattern) rather than in a useEffect avoids the extra
  // commit+re-render a post-commit effect would add. This can't use a ref for
  // the snapshot — this repo's lint config (react-hooks/refs) forbids reading
  // or writing ref.current during render — so it's a second useState instead.
  const [groupBy, setGroupBy] = useState<GroupByMode>(() => groupByParam ?? "groups");
  const [lastGroupByParam, setLastGroupByParam] = useState(groupByParam);
  if (lastGroupByParam !== groupByParam) {
    setLastGroupByParam(groupByParam);
    setGroupBy(groupByParam ?? "groups");
  }
  const setGroupByMode = (mode: GroupByMode) => {
    setGroupBy(mode);
    void navigate({
      search: (prev) => ({ ...prev, groupBy: mode === "groups" ? undefined : mode }),
    });
  };

  const [search, setSearch] = useState("");
  const [internalFilter, setInternalFilter] = useState<string>("false");
  const [deleteCheckUid, setDeleteCheckUid] = useState<string | null>(null);
  const [showNewGroup, setShowNewGroup] = useState(false);
  const [newGroupName, setNewGroupName] = useState("");
  const [newGroupSlug, setNewGroupSlug] = useState("");
  const [newGroupSlugManuallyEdited, setNewGroupSlugManuallyEdited] =
    useState(false);
  const [newGroupSlugError, setNewGroupSlugError] = useState<
    string | undefined
  >(undefined);
  const [changeGroupCheck, setChangeGroupCheck] = useState<Check | null>(null);
  const debouncedSearch = useDebounce(search, 300);

  // Live updates: `checks`/`results` hints invalidate both the flat
  // ["checks", org] root and the infinite ["checks", "infinite", org] root
  // (see infiniteOrgRoot in LiveEventsContext), so every group section's
  // paginated query refreshes status and last-result cells without a reload.
  useLiveSubscription({ entity: "checks" });

  const {
    data: groups,
    isLoading: groupsLoading,
    error: groupsError,
    refetch: refetchGroups,
    isRefetching,
  } = useCheckGroups(org);

  // Only fetched for the group-header escalation indicator (name/silent
  // lookup) — a small, already-cached list shared with the check form.
  const { data: escalationPolicies } = useEscalationPolicies(org);
  const escalationPolicyByUid = useMemo(() => {
    const map = new Map<string, EscalationPolicy>();
    for (const p of escalationPolicies ?? []) map.set(p.uid, p);
    return map;
  }, [escalationPolicies]);

  // Single page-level infinite query — replaces the former per-group N+1
  // fan-out (one useInfiniteChecks per CheckGroupSection plus one for the
  // ungrouped section). Every row comes from this one query and is bucketed by
  // checkGroupUid client-side, so a live-hint invalidation refreshes exactly
  // one query per tick instead of (groups + 1). Its key
  // ["checks", "infinite", org, options] still matches handleRefresh, the
  // change-group flow, and the infiniteOrgRoot("checks") live predicate.
  // See spec 2026-07-17-02.
  const {
    data: checksData,
    isLoading: checksLoading,
    error: checksError,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteChecks(org, {
    with: "last_result",
    q: debouncedSearch || undefined,
    internal: internalFilter,
    labels: labelsParam,
    status: statusParam,
    limit: 100,
    // Load the stream in the exact order the page renders it, so the top of
    // the page fills first instead of arriving in unrelated created_at order:
    // "group" sortOrder asc / ungrouped last in Groups mode, "targetHost"
    // ascending / no-host last in Host mode. Distinct sort values give the two
    // modes distinct query-cache entries (the queryKey includes this options
    // object), so switching modes is a normal cache miss/hit, not a manual reset.
    sort: groupBy === "host" ? "targetHost" : "group",
  });

  // A bucket that is still empty must read as *loading*, never as an empty
  // state, as long as the page-level stream can still deliver more rows
  // (initial load, or a not-yet-fetched later page). Folded into each section's
  // `isLoading` prop so a not-yet-reached group shows skeletons — never the
  // false "No checks" — until the stream is fully drained.
  const checksStreaming = checksLoading || hasNextPage || isFetchingNextPage;

  // Bucket loaded checks by group. Ungrouped = falsy checkGroupUid (matches the
  // server's checkGroupUid=none semantics). Order within a bucket follows the
  // server's page order; group *section* order stays driven by the groups list
  // (sortOrder), so bucketing is order-independent. Group count badges keep
  // coming from group.checkCount (independent of the loaded page), so a
  // partially-loaded group still shows its true total.
  const { checksByGroup, ungroupedChecks, checksByUid } = useMemo(() => {
    const byGroup = new Map<string, Check[]>();
    const ungrouped: Check[] = [];
    // Every check loaded on this page (any type, any group), keyed by uid —
    // lets a tunneled check's row resolve its bastion's name without a new
    // request, since the bastion (an SSH check) is loaded on this same page.
    const byUid = new Map<string, Check>();
    for (const page of checksData?.pages ?? []) {
      for (const check of page.data ?? []) {
        byUid.set(check.uid, check);
        if (check.checkGroupUid) {
          const bucket = byGroup.get(check.checkGroupUid);
          if (bucket) bucket.push(check);
          else byGroup.set(check.checkGroupUid, [check]);
        } else {
          ungrouped.push(check);
        }
      }
    }
    return { checksByGroup: byGroup, ungroupedChecks: ungrouped, checksByUid: byUid };
  }, [checksData]);

  // Host-mode bucketing (spec 2026-08-01-04): every loaded check bucketed by
  // its derived targetHost, section order following first-appearance in the
  // sort=targetHost stream (already host-ascending, so sections fill top to
  // bottom as pages load) with the null (no derivable host) bucket forced to
  // the end regardless — belt-and-suspenders alongside the server's own
  // nulls-last ordering, since this is purely a client-side view with no
  // server-side identity of its own.
  const hostBuckets = useMemo(() => {
    const buckets = new Map<string | null, Check[]>();
    for (const page of checksData?.pages ?? []) {
      for (const check of page.data ?? []) {
        const key = check.targetHost ?? null;
        const bucket = buckets.get(key);
        if (bucket) bucket.push(check);
        else buckets.set(key, [check]);
      }
    }

    const entries = Array.from(buckets.entries());
    const withHost = entries.filter(([key]) => key !== null);
    const noHost = entries.find(([key]) => key === null);
    return noHost ? [...withHost, noHost] : withHost;
  }, [checksData]);

  // One page-level infinite-scroll sentinel (below the last section) instead of
  // one IntersectionObserver per group section.
  const sentinelRef = useRef<HTMLDivElement>(null);
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
    // rootMargin ~two viewports so the next page starts loading well before
    // the sentinel scrolls into view — draining self-chains (the effect
    // re-runs when handleObserver's identity flips on isFetchingNextPage).
    const observer = new IntersectionObserver(handleObserver, {
      rootMargin: "1200px 0px",
      threshold: 0,
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [handleObserver]);

  const [importPreview, setImportPreview] = useState<{
    doc: ExportDocument;
    result: ImportResult;
  } | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const deleteCheck = useDeleteCheck(org);
  const createGroup = useCreateCheckGroup(org);
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

  const closeNewGroupDialog = () => {
    setShowNewGroup(false);
    setNewGroupName("");
    setNewGroupSlug("");
    setNewGroupSlugManuallyEdited(false);
    setNewGroupSlugError(undefined);
  };

  const handleCreateGroup = async () => {
    if (!newGroupName.trim()) return;
    const trimmedSlug = newGroupSlug.trim();
    if (trimmedSlug && !groupSlugRegex.test(trimmedSlug)) {
      setNewGroupSlugError(t("dialog.groupSlugInvalid"));
      return;
    }
    try {
      await createGroup.mutateAsync({
        name: newGroupName.trim(),
        // Untouched and empty keeps sending nothing so the server derives
        // the slug from the name, exactly like before this field existed.
        ...(trimmedSlug ? { slug: trimmedSlug } : {}),
      });
      toast.success(t("toast.groupCreated"));
      closeNewGroupDialog();
    } catch (err) {
      const fieldMessage = getApiErrorField(err, "slug");
      if (fieldMessage && err instanceof ApiError) {
        setNewGroupSlugError(
          err.message.toLowerCase().includes("already exists")
            ? t("dialog.groupSlugTaken")
            : t("dialog.groupSlugInvalid"),
        );
      } else {
        toast.error(err instanceof ApiError ? err.message : t("toast.groupCreateFailed"));
      }
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
      <PageHeader
        icon={ListChecks}
        title={t("title")}
        description={t("subtitle")}
        actions={
          <>
            <Button
              variant="outline"
              onClick={handleExport}
              data-testid="export-button"
              className="hidden sm:inline-flex"
            >
              <Download className="mr-2 h-4 w-4" />
              {t("export")}
            </Button>
            <Button
              variant="outline"
              onClick={() => fileInputRef.current?.click()}
              data-testid="import-button"
              className="hidden sm:inline-flex"
            >
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
              <FolderPlus className="sm:mr-2 h-4 w-4" />
              <span className="hidden sm:inline">{t("newGroup")}</span>
            </Button>
            <Link to="/orgs/$org/checks/new" params={{ org }} search={{ checkType: undefined, checkPeriod: undefined, checkName: undefined, checkSlug: undefined, httpUrl: undefined, httpMethod: undefined, host: undefined, port: undefined, url: undefined, domain: undefined, username: undefined, database: undefined, expectedStatus: undefined, timeout: undefined, label: undefined, region: undefined, group: undefined, confirmationPeriod: undefined, recoveryPeriod: undefined, section: undefined }}>
              <Button data-testid="new-check-button">
                <Plus className="sm:mr-2 h-4 w-4" />
                <span className="hidden sm:inline">{t("newCheck")}</span>
              </Button>
            </Link>
          </>
        }
        className="flex-wrap"
      />

      <div className="flex flex-wrap items-center gap-4">
        <div className="relative flex-1 min-w-[200px] max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("searchChecks")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9"
          />
        </div>
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-muted-foreground">
            {t("groupBy.label")}
          </span>
          <div className="inline-flex rounded-lg border p-0.5" role="group">
            <Button
              size="sm"
              variant={groupBy === "groups" ? "secondary" : "ghost"}
              onClick={() => setGroupByMode("groups")}
              aria-pressed={groupBy === "groups"}
              data-testid="group-by-groups"
            >
              {t("groupBy.groups")}
            </Button>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  size="sm"
                  variant={groupBy === "host" ? "secondary" : "ghost"}
                  onClick={() => setGroupByMode("host")}
                  aria-pressed={groupBy === "host"}
                  data-testid="group-by-host"
                >
                  {t("groupBy.host")}
                </Button>
              </TooltipTrigger>
              <TooltipContent>{t("hostBucketDescription")}</TooltipContent>
            </Tooltip>
          </div>
        </div>
        {user?.isSuperAdmin && (
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
        )}
        <Select
          value={statusParam ?? "all"}
          onValueChange={(value) =>
            void navigate({
              search: (prev) => ({
                ...prev,
                status: value === "all" ? undefined : value,
              }),
              replace: true,
            })
          }
        >
          <SelectTrigger className="w-[140px]" data-testid="status-filter">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            <SelectItem value="up">Up</SelectItem>
            <SelectItem value="down">Down</SelectItem>
            <SelectItem value="created">Pending</SelectItem>
          </SelectContent>
        </Select>
        <Button
          variant="outline"
          onClick={handleRefresh}
          disabled={isRefetching}
          aria-label={t("common:refresh")}
        >
          <RefreshCw
            className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`}
          />
          <span className="hidden sm:inline">{t("common:refresh")}</span>
        </Button>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium text-muted-foreground">Labels:</span>
          <LabelFilter
            org={org}
            value={labelFilters}
            onChange={(next) => {
              const serialized = serializeLabelsParam(next);
              void navigate({
                search: (prev) => ({ ...prev, labels: serialized || undefined }),
                replace: true,
              });
            }}
          />
          {Object.keys(labelFilters).length > 0 && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() =>
                void navigate({
                  search: (prev) => ({ ...prev, labels: undefined }),
                  replace: true,
                })
              }
              data-testid="clear-label-filters"
            >
              Clear filters
            </Button>
          )}
        </div>
      </div>

      {groupBy === "groups" ? (
        groupsError ? (
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
                checks={checksByGroup.get(group.uid) ?? []}
                isLoading={checksStreaming}
                error={checksError}
                search={debouncedSearch}
                isFirst={idx === 0}
                isLast={idx === (groups?.length ?? 0) - 1}
                onMoveUp={() => handleMoveGroup(group, "up")}
                onMoveDown={() => handleMoveGroup(group, "down")}
                onDeleteCheck={setDeleteCheckUid}
                onChangeGroup={setChangeGroupCheck}
                groups={groups || []}
                checksByUid={checksByUid}
                escalationPolicyByUid={escalationPolicyByUid}
              />
            ))}

            <UngroupedChecksSection
              org={org}
              checks={ungroupedChecks}
              isLoading={checksStreaming}
              error={checksError}
              search={debouncedSearch}
              onDeleteCheck={setDeleteCheckUid}
              onChangeGroup={setChangeGroupCheck}
              groups={groups || []}
              checksByUid={checksByUid}
            />

            {/* One page-level infinite-scroll sentinel for the whole list. */}
            <div ref={sentinelRef} className="flex justify-center py-4">
              {isFetchingNextPage && (
                <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
              )}
            </div>

            {(!groups || groups.length === 0) && (
              <NoChecksPlaceholder search={debouncedSearch} />
            )}
          </div>
        )
      ) : checksError && hostBuckets.length === 0 ? (
        <QueryErrorView
          error={checksError}
          org={org}
          onRetry={() => queryClient.invalidateQueries({ queryKey: ["checks", "infinite", org] })}
        />
      ) : checksLoading && hostBuckets.length === 0 ? (
        <div className="space-y-2">
          {[...Array(6)].map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-lg" />
          ))}
        </div>
      ) : (
        <div className="space-y-4" data-testid="host-view">
          {hostBuckets.map(([hostKey, checks]) => (
            <HostSection
              key={hostKey ?? "no-host"}
              hostKey={hostKey}
              checks={checks}
              org={org}
              onDeleteCheck={setDeleteCheckUid}
              onChangeGroup={setChangeGroupCheck}
              groups={groups || []}
              checksByUid={checksByUid}
            />
          ))}

          {/* One page-level infinite-scroll sentinel for the whole list. */}
          <div ref={sentinelRef} className="flex justify-center py-4">
            {isFetchingNextPage && (
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            )}
          </div>

          {hostBuckets.length === 0 && !checksStreaming && !debouncedSearch && (
            <div className="text-center py-10 text-muted-foreground text-sm">
              {t("noChecksAtAll")}
            </div>
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

      {/* New Group Dialog */}
      <Dialog
        open={showNewGroup}
        onOpenChange={(open) => {
          if (open) {
            setShowNewGroup(true);
          } else {
            closeNewGroupDialog();
          }
        }}
      >
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
                onChange={(e) => {
                  setNewGroupName(e.target.value);
                  if (!newGroupSlugManuallyEdited) {
                    setNewGroupSlug(slugify(e.target.value));
                  }
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter") handleCreateGroup();
                }}
                autoFocus
                data-testid="new-group-name-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="group-slug">{t("dialog.groupSlug")}</Label>
              <Input
                id="group-slug"
                placeholder="prod-eu-west"
                value={newGroupSlug}
                onChange={(e) => {
                  setNewGroupSlug(e.target.value);
                  setNewGroupSlugManuallyEdited(true);
                  setNewGroupSlugError(undefined);
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter") handleCreateGroup();
                }}
                className={newGroupSlugError ? "border-destructive" : ""}
                aria-invalid={!!newGroupSlugError}
                data-testid="new-group-slug-input"
              />
              {newGroupSlugError ? (
                <p
                  className="text-xs text-destructive"
                  data-testid="new-group-slug-error"
                >
                  {newGroupSlugError}
                </p>
              ) : (
                <p className="text-xs text-muted-foreground">
                  {t("dialog.groupSlugHelp")}
                </p>
              )}
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeNewGroupDialog}>
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
