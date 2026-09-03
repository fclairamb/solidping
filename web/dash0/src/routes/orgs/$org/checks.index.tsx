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
  CalendarClock,
} from "lucide-react";
import { toast } from "sonner";
import {
  useInfiniteChecks,
  useDeleteCheck,
  useCheckGroups,
  useCreateCheckGroup,
  useImportChecks,
  useConvertChecks,
  useEscalationPolicies,
  useCheckTypes,
  useEntitlements,
  useStalePublications,
  type Check,
  type CheckGroup,
  type ConversionWarning,
  type ConvertSource,
  type EscalationPolicy,
} from "@/api/hooks";
import { useEmailAddressDomain, emailCheckAddress } from "@/api/email-inbox";
import { Button } from "@/components/ui/button";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { StatusBadge } from "@/components/shared/status-badge";
import { StatusDot } from "@/components/shared/status-dot";
import { PageHeader } from "@/components/shared/page-header";
import { CheckRateLimitBanner } from "@/components/shared/check-rate-limit-banner";
import { StalePublicationsBanner } from "@/components/shared/stale-publications-banner";
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
import { Textarea } from "@/components/ui/textarea";
import { PasswordInput } from "@/components/ui/password-input";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
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
import { FacetedFilter } from "@/components/shared/faceted-filter";
import { checkLabel, tunnelCheckUidOf } from "@/components/checks/tunnel";
import { CheckTypeBadge, getCheckTypeIdentity } from "@/components/shared/check-type-identity";
import { ApiError, apiFetch, getApiErrorField } from "@/api/client";
import { useQueryClient } from "@tanstack/react-query";
import { parseLabelsParam, serializeLabelsParam } from "@/lib/labels";
import {
  facetedFilterTriggerLabel,
  parseFacetedFilterParam,
  serializeFacetedFilterParam,
} from "@/lib/faceted-filter";
import { slugify } from "@/lib/utils";
import { useAuth } from "@/contexts/AuthContext";
import { CHECKS_LIST_POLL_MS, useLiveSubscription } from "@/contexts/LiveEventsContext";

// The checks index can bucket its rows by check group (server-side entity,
// the default) or by the derived targetHost (spec 2026-08-01-04) — no
// server-side identity, purely a client-side view over the same rows.
const GROUP_BY_MODES = ["groups", "host"] as const;
type GroupByMode = (typeof GROUP_BY_MODES)[number];

interface ChecksIndexSearch {
  labels?: string;
  status?: string;
  type?: string;
  groupBy?: GroupByMode;
  q?: string;
}

// Status tokens the faceted filter offers, in display order. `degraded` is
// deliberately excluded: models.CheckStatusDegraded is an aggregated/summary
// value the live pipeline never assigns to a check
// (server/internal/db/models/check.go), so it would sit in the list as a dead
// option nothing could ever match. `?status=degraded` still parses and 200s
// if a caller hand-types it — this only decides what the popover renders.
const STATUS_FILTER_VALUES = ["up", "down", "validating", "warning", "created"] as const;

// Group slugs are 3-100 chars: a lowercase letter followed by 2-99 lowercase
// letters/digits/hyphens. This mirrors slugRegex in
// server/internal/handlers/checkgroups/service.go — same 3-100 rule as the
// check form's slugRegex (check-form.tsx), which validates a different
// resource.
const groupSlugRegex = /^[a-z][a-z0-9-]{2,99}$/;

// Import sources offered by the checks-list import flow: the native SolidPing
// export document, plus the third-party converters served by
// POST /checks/import/convert.
const IMPORT_SOURCES = ["solidping", "gatus", "betterstack", "uptime-kuma", "uptimerobot"] as const;
type ImportSourceId = (typeof IMPORT_SOURCES)[number];

// Sources whose payload is a file the user pastes or uploads. Better Stack is
// the odd one out: it takes an API token and the server does the fetching.
const FILE_IMPORT_SOURCES: ImportSourceId[] = ["solidping", "gatus", "uptime-kuma", "uptimerobot"];

// Accepted upload extensions per source.
const IMPORT_ACCEPT: Record<ImportSourceId, string> = {
  solidping: ".json,.yaml,.yml",
  gatus: ".yaml,.yml",
  betterstack: "",
  "uptime-kuma": ".json",
  uptimerobot: ".json",
};

// Normalized preview shape shared by the native import and every converter.
interface ImportPreview {
  source: ImportSourceId;
  created: number;
  updated: number;
  errors: { index: number; slug: string; error: string }[];
  warnings: ConversionWarning[];
}

export const Route = createFileRoute("/orgs/$org/checks/")({
  component: ChecksIndexPage,
  validateSearch: (search: Record<string, unknown>): ChecksIndexSearch => ({
    labels: typeof search.labels === "string" && search.labels ? search.labels : undefined,
    status: typeof search.status === "string" && search.status ? search.status : undefined,
    type: typeof search.type === "string" && search.type ? search.type : undefined,
    groupBy: GROUP_BY_MODES.includes(search.groupBy as GroupByMode)
      ? (search.groupBy as GroupByMode)
      : undefined,
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  }),
});

// Manual collapse/expand choices for check-group sections, persisted per org
// so a toggle survives a reload. One JSON map per org: { [groupUid]: boolean }.
// Mirrors the try/catch-guarded localStorage pattern used elsewhere in dash0
// (lib/last-auth-method.ts, api/client.ts's setSession) —
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

// Builds the search object for `/orgs/$org/checks/new` that preselects a
// group (every other new-check search key explicit-`undefined`, matching the
// route's validateSearch). Shared by the group empty-state link and the
// group-header "add a check" button so the ~20-key literal isn't duplicated.
function newCheckSearchForGroup(slug: string) {
  return {
    checkType: undefined,
    checkPeriod: undefined,
    checkName: undefined,
    checkSlug: undefined,
    httpUrl: undefined,
    httpMethod: undefined,
    host: undefined,
    port: undefined,
    url: undefined,
    domain: undefined,
    username: undefined,
    database: undefined,
    expectedStatus: undefined,
    timeout: undefined,
    label: undefined,
    region: undefined,
    group: slug,
    confirmationPeriod: undefined,
    recoveryPeriod: undefined,
    section: undefined,
  };
}

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

  const durationMs = check.lastResult?.durationMs;

  return (
    <TableRow className="hover:bg-muted/40 transition-colors">
      <TableCell className="max-w-0">
        <div className="flex items-center gap-2 min-w-0">
          <Link
            to="/orgs/$org/checks/$checkUid"
            params={{ org, checkUid: check.uid }}
            search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
            className="flex items-center gap-2 hover:underline font-medium min-w-0"
          >
            <StatusDot
              status={check.status ?? check.lastResult?.status}
              enabled={check.enabled}
              title={check.enabled === false ? t("checks:detail.disabled") : undefined}
            />
            <span className="min-w-0 truncate">
              {check.name || check.slug || check.uid?.slice(0, 8)}
            </span>
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
      <TableCell className="hidden sm:table-cell">
        <div className="flex items-center gap-1.5">
          <CheckTypeBadge type={check.type} />
          {check.internal && (
            <Badge variant="secondary" className="text-xs">
              {t("internal")}
            </Badge>
          )}
        </div>
      </TableCell>
      <TableCell className="hidden md:table-cell text-muted-foreground font-mono text-xs max-w-[280px] truncate">
        {renderTarget()}
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <StatusBadge status={check.status ?? check.lastResult?.status} />
      </TableCell>
      <TableCell>
        {durationMs != null ? (
          <span
            className={`inline-flex items-center text-xs font-mono tabular-nums px-2 py-0.5 rounded-md font-medium ${
              durationMs < 50
                ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 border border-emerald-500/20"
                : durationMs < 250
                ? "bg-sky-500/10 text-sky-700 dark:text-sky-300 border border-sky-500/20"
                : "bg-amber-500/10 text-amber-700 dark:text-amber-300 border border-amber-500/20"
            }`}
          >
            {Math.round(durationMs)}ms
          </span>
        ) : (
          <span className="text-muted-foreground text-xs font-mono">—</span>
        )}
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
                search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
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
  elevated = false,
}: {
  checks: Check[];
  org: string;
  onDelete: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
  checksByUid: Map<string, Check>;
  /** True for a table standing directly on the page background (the
   * ungrouped section) — gives it the shadow-card "list surface" treatment
   * from maintenance-windows/design-reference. False (default) for a table
   * nested inside a CheckGroupSection/HostSection, which already supplies
   * its own bordered rounded-lg card — stacking another elevated card inside
   * it would double the framing. */
  elevated?: boolean;
}) {
  const { t } = useTranslation("checks");
  const table = (
    <Table>
      <TableHeader className="bg-muted/30">
        <TableRow>
          <TableHead>{t("table.name")}</TableHead>
          <TableHead className="hidden sm:table-cell">{t("table.type")}</TableHead>
          <TableHead className="hidden md:table-cell">{t("table.target")}</TableHead>
          <TableHead className="hidden md:table-cell">{t("table.status")}</TableHead>
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
  );

  if (elevated) {
    return (
      <div className="overflow-hidden rounded-xl border bg-card shadow-card">
        <div className="overflow-x-auto">{table}</div>
      </div>
    );
  }

  return <div className="rounded-md border">{table}</div>;
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
  isFiltering,
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
  /** True when any of the four filter dimensions (search/status/labels/
   * internal) is active — see spec 2026-08-02-07. While true, the header
   * shows the count of *matching* checks (bucket length) instead of the
   * unfiltered group.checkCount/memberStatusCounts, so it never contradicts
   * the filtered rows below it. */
  isFiltering: boolean;
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
  // While filtering, the unfiltered member-status breakdown would contradict
  // the (filtered) rows shown below it, so it's dropped in favor of the plain
  // matching count on the badge below.
  const memberSummary = isFiltering
    ? undefined
    : formatMemberSummary(group.memberStatusCounts, t);

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
          <Badge
            variant="secondary"
            className="text-xs"
            data-testid="group-check-count"
          >
            {isFiltering ? checks.length : group.checkCount}
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
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                aria-label={t("addCheckToGroup")}
                data-testid="group-add-check-button"
                asChild
              >
                <Link
                  to="/orgs/$org/checks/new"
                  params={{ org }}
                  search={newCheckSearchForGroup(group.slug)}
                >
                  <Plus className="h-4 w-4" />
                </Link>
              </Button>
            </TooltipTrigger>
            <TooltipContent>{t("addCheckToGroup")}</TooltipContent>
          </Tooltip>
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
           * state only shows once the stream is fully drained.
           *
           * Exception: when NOT filtering, `group.checkCount` is the
           * authoritative server-side total (independent of the loaded
           * pages — see the comment above `checksByGroup`). If it's already
           * 0, no page of the stream can ever deliver a row for this group,
           * so treating an empty bucket as "still loading" would show
           * skeletons forever on orgs with more checks than one page. Skip
           * straight to the empty state instead. While filtering, keep
           * deferring to the stream — `checkCount` is the unfiltered total,
           * so a filtered-to-zero bucket may still be genuinely loading. */}
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
          ) : isLoading && (isFiltering || group.checkCount !== 0) ? (
            <div className="p-4 space-y-2">
              {[...Array(3)].map((_, i) => (
                <Skeleton key={i} className="h-10 rounded-lg" />
              ))}
            </div>
          ) : (
            <div className="p-4 text-center text-sm text-muted-foreground">
              <p>{t("noChecks")}</p>
              {!isFiltering && (
                <Link
                  to="/orgs/$org/checks/new"
                  params={{ org }}
                  search={newCheckSearchForGroup(group.slug)}
                  className="mt-1 inline-flex items-center gap-1 text-primary hover:underline"
                  data-testid="group-empty-new-check-link"
                >
                  {t("addCheckToGroup")}
                </Link>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// Presentational: renders the ungrouped bucket from the page-level batched
// query. Hidden entirely whenever there is nothing ungrouped to show (once the
// stream has resolved) — regardless of whether that's because the org simply
// has no ungrouped checks, or because the active filters (search/status/
// labels/internal) narrowed the bucket to zero. An empty "Ungrouped Checks"
// heading, or a "no matches" placeholder under it, would just be noise; the
// group-hiding behavior above already gives the same "filtered to nothing"
// feedback by omission. See spec 2026-08-02-07.
function UngroupedChecksSection({
  org,
  checks,
  isLoading,
  error,
  onDeleteCheck,
  onChangeGroup,
  groups,
  checksByUid,
}: {
  org: string;
  checks: Check[];
  isLoading: boolean;
  error: unknown;
  onDeleteCheck: (uid: string) => void;
  onChangeGroup: (check: Check) => void;
  groups: CheckGroup[];
  checksByUid: Map<string, Check>;
}) {
  const { t } = useTranslation("checks");

  if (!isLoading && !error && checks.length === 0) {
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
        <ChecksTable checks={checks} org={org} onDelete={onDeleteCheck} onChangeGroup={onChangeGroup} groups={groups} checksByUid={checksByUid} elevated />
      ) : isLoading ? (
        <div className="space-y-2">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-10 rounded-lg" />
          ))}
        </div>
      ) : null}
    </div>
  );
}

function ChecksIndexPage() {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const { org } = Route.useParams();
  const {
    labels: labelsParam,
    status: statusParam,
    type: typeParam,
    groupBy: groupByParam,
    q: qParam,
  } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const labelFilters = parseLabelsParam(labelsParam);
  const queryClient = useQueryClient();
  const { user } = useAuth();

  // Entitlements without ?with=usage: the over-limit banner needs only
  // `checksPerMinute`, which the server computes unconditionally, so the checks
  // list never pays for the full usage roll-up.
  const { data: entitlements } = useEntitlements(org);

  // Publications still open on a public page while the incident behind them has
  // recovered. Almost always empty; when it is not, this list is the wrong
  // thing to be reading and the banner says so (spec 2026-09-02-05).
  const stalePublications = useStalePublications(org);

  // Status and type are both faceted (multi-value) filters. The URL is the
  // source of truth — read directly off Route.useSearch() with no local-state
  // mirror (like `groupBy` above), so a cold deep link with several values
  // selected renders correctly on first paint. Parsing is lenient (unknown
  // status tokens are dropped rather than crashing the UI); the backend still
  // 400s a genuinely bad ?status= and the list error state covers that.
  const statusValues = useMemo(
    () => parseFacetedFilterParam(statusParam, new Set<string>(STATUS_FILTER_VALUES)),
    [statusParam],
  );
  const typeValues = useMemo(() => parseFacetedFilterParam(typeParam), [typeParam]);

  const statusOptions = useMemo(
    () => STATUS_FILTER_VALUES.map((value) => ({ value, label: t(`status.${value}`) })),
    [t],
  );
  const statusTriggerLabel = facetedFilterTriggerLabel(statusValues, statusOptions, {
    all: t("statusFilter.all"),
    count: (count) => t("statusFilter.count", { count }),
    plusOne: (label, extra) => t("statusFilter.plusOne", { label, count: extra }),
  });
  const setStatusValues = (next: string[]) => {
    void navigate({
      search: (prev) => ({ ...prev, status: serializeFacetedFilterParam(next) || undefined }),
      replace: true,
    });
  };

  const { data: checkTypes } = useCheckTypes(org);
  const typeOptions = useMemo(
    () =>
      (checkTypes ?? [])
        .map((ct) => ({ value: ct.type, label: getCheckTypeIdentity(ct.type).label }))
        .sort((a, b) => a.label.localeCompare(b.label)),
    [checkTypes],
  );
  const typeTriggerLabel = facetedFilterTriggerLabel(typeValues, typeOptions, {
    all: t("typeFilter.all"),
    count: (count) => t("typeFilter.count", { count }),
    plusOne: (label, extra) => t("typeFilter.plusOne", { label, count: extra }),
  });
  const setTypeValues = (next: string[]) => {
    void navigate({
      search: (prev) => ({ ...prev, type: serializeFacetedFilterParam(next) || undefined }),
      replace: true,
    });
  };

  // The view mode ("Group by: Groups / Host") is the page's primary
  // navigation, so — per this repo's convention (see web/dash0/CLAUDE.md,
  // "A page's core navigation belongs in the URL", and jobs.index.tsx's
  // `tab`) — it lives in the URL search param `groupBy` and is read directly
  // via Route.useSearch(), with no local-state mirror: validateSearch already
  // normalizes it to a safe default ("groups") on every render, including the
  // first one on a deep link.
  const groupBy = groupByParam ?? "groups";
  const setGroupByMode = (mode: GroupByMode) => {
    void navigate({
      search: (prev) => ({ ...prev, groupBy: mode === "groups" ? undefined : mode }),
    });
  };

  // The free-text search box mirrors `?q=` in the URL so a filtered view is
  // bookmarkable/shareable and survives a reload. We seed local state from the
  // URL once on mount via a lazy initializer (the layout route otherwise races
  // a second search-validation pass that can drop the param on a cold load —
  // same pitfall/fix as discovery.new.tsx's `type` state) and write the
  // debounced value back to the URL on every change so the URL stays the
  // source of truth.
  const [search, setSearch] = useState(() => qParam ?? "");
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

  // True when any of the five filter dimensions narrows the check list away
  // from its default view: free-text search, the status filter, the type
  // filter, the label filter, or a non-default internal-checks toggle
  // ("false" is the default, user-facing view — "true"/"all" are the
  // filtered ones). Drives hiding fully-filtered-out groups (and the
  // ungrouped section) and swapping group headers to show matching counts
  // instead of full-fleet counts. See spec 2026-08-02-07 (GitHub issue #171).
  const isFiltering =
    Boolean(debouncedSearch) ||
    statusValues.length > 0 ||
    typeValues.length > 0 ||
    Boolean(labelsParam) ||
    internalFilter !== "false";

  // Live updates: a `checks` hint (status transition, membership/config
  // change) invalidates both the flat ["checks", org] root and the infinite
  // ["checks", "infinite", org] root (see infiniteOrgRoot in
  // LiveEventsContext), so the list reflects transitions without a reload.
  // `results` hints deliberately no longer touch those roots — the
  // refetchInterval below covers the steady-state per-run cells instead.
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
    // Pass the raw URL params through untouched, not the leniently-parsed
    // statusValues/typeValues used for the popover's checked state: an
    // unknown ?status= token must still reach the backend and 400 (covered
    // by the list error state) rather than silently vanish from the request.
    status: statusParam,
    type: typeParam,
    limit: 100,
    // Load the stream in the exact order the page renders it, so the top of
    // the page fills first instead of arriving in unrelated created_at order:
    // "group" sortOrder asc / ungrouped last in Groups mode, "targetHost"
    // ascending / no-host last in Host mode. Distinct sort values give the two
    // modes distinct query-cache entries (the queryKey includes this options
    // object), so switching modes is a normal cache miss/hit, not a manual reset.
    sort: groupBy === "host" ? "targetHost" : "group",
  }, {
    // A `results` live hint no longer invalidates this list (spec
    // 2026-08-09-07 — it fired continuously and made one open tab worth ~0.5
    // whole-list refetches per second). Status transitions still arrive as
    // "checks" hints; this poll is what keeps the per-run cells (status dot,
    // latency) fresh in between, bounded by one interval.
    refetchInterval: CHECKS_LIST_POLL_MS,
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

  // Import flow: a source picker (SolidPing export, or one of the third-party
  // converters) feeds the same dry-run preview dialog before anything is
  // applied. The Better Stack token lives in component state for the duration
  // of the import only — it is never persisted here or server-side.
  const [importOpen, setImportOpen] = useState(false);
  const [importSource, setImportSource] = useState<ImportSourceId>("solidping");
  const [importText, setImportText] = useState("");
  const [importToken, setImportToken] = useState("");
  const [importFileName, setImportFileName] = useState("");
  const [importPreview, setImportPreview] = useState<ImportPreview | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const deleteCheck = useDeleteCheck(org);
  const createGroup = useCreateCheckGroup(org);
  const importChecks = useImportChecks(org);
  const convertChecks = useConvertChecks(org);

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

  const openImportDialog = () => {
    setImportSource("solidping");
    setImportText("");
    setImportToken("");
    setImportFileName("");
    setImportOpen(true);
  };

  const closeImportDialog = () => {
    setImportOpen(false);
    setImportText("");
    // Never keep the Better Stack credential around after the dialog closes.
    setImportToken("");
    setImportFileName("");
  };

  const handleImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    try {
      setImportText(await file.text());
      setImportFileName(file.name);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("toast.parseFailed"));
    }
    // Reset file input so the same file can be re-selected
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  // Runs the import (dry run first, then for real on confirm) for whichever
  // source is selected. Returns the normalized preview shape.
  const runImport = async (dryRun: boolean): Promise<ImportPreview> => {
    if (importSource === "solidping") {
      // Sent as raw text: /checks/import parses JSON *and* YAML server-side, so
      // parsing here would narrow the accepted formats to JSON only.
      const result = await importChecks.mutateAsync({ body: importText, dryRun });
      return {
        source: importSource,
        created: result.created,
        updated: result.updated,
        errors: result.errors,
        warnings: [],
      };
    }

    const body =
      importSource === "betterstack"
        ? JSON.stringify({ token: importToken.trim() })
        : importText;
    const result = await convertChecks.mutateAsync({
      source: importSource as ConvertSource,
      body,
      dryRun,
    });
    return {
      source: importSource,
      created: result.created,
      updated: result.updated,
      errors: result.errors,
      warnings: result.warnings ?? [],
    };
  };

  const handleImportPreview = async () => {
    try {
      setImportPreview(await runImport(true));
      setImportOpen(false);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("toast.parseFailed"));
    }
  };

  const handleImportConfirm = async () => {
    if (!importPreview) return;
    try {
      const result = await runImport(false);
      toast.success(t("toast.importSuccess", { count: result.created + result.updated, created: result.created, updated: result.updated }));
      setImportPreview(null);
      closeImportDialog();
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
              onClick={openImportDialog}
              data-testid="import-button"
              className="hidden sm:inline-flex"
            >
              <Upload className="mr-2 h-4 w-4" />
              {t("import")}
            </Button>
            <Button variant="outline" onClick={() => setShowNewGroup(true)} data-testid="new-group-button">
              <FolderPlus className="sm:mr-2 h-4 w-4" />
              <span className="hidden sm:inline">{t("newGroup")}</span>
            </Button>
            {/*
              The scheduling page (spec 2026-08-26-04) edits one field across
              many checks, so it belongs next to the list rather than inside a
              row. Reachable here whether or not the org is over its cap — an
              org can want to plan its execution budget before it breaches it.
            */}
            <Button asChild variant="outline">
              <Link
                to="/orgs/$org/checks/scheduling"
                params={{ org }}
                data-testid="scheduling-link"
                aria-label={t("scheduling.title")}
              >
                <CalendarClock className="sm:mr-2 h-4 w-4" />
                <span className="hidden sm:inline">{t("scheduling.title")}</span>
              </Link>
            </Button>
            <Link to="/orgs/$org/checks/new" params={{ org }} search={{ checkType: undefined, checkPeriod: undefined, checkName: undefined, checkSlug: undefined, httpUrl: undefined, httpMethod: undefined, host: undefined, port: undefined, url: undefined, domain: undefined, username: undefined, database: undefined, expectedStatus: undefined, timeout: undefined, label: undefined, region: undefined, group: undefined, confirmationPeriod: undefined, recoveryPeriod: undefined, section: undefined }}>
              <Button data-testid="new-check-button">
                <Plus className="sm:mr-2 h-4 w-4" />
                <span className="hidden sm:inline">{t("newCheck")}</span>
              </Button>
            </Link>
          </>
        }
        docsHref="/docs/features/check-types"
        className="flex-wrap"
      />

      {/*
        Directly under the header, above the filters: the gaps this explains are
        in the rows below, and an org has to see the cause before it starts
        hunting broken checks.
      */}
      <CheckRateLimitBanner
        org={org}
        checksPerMinute={entitlements?.checksPerMinute}
        upgradeUrl={entitlements?.upgradeUrl}
        showUsageLink
      />

      {/*
        This list is where an operator lands when the wallboard goes amber. If
        the amber comes from a status-page entry rather than a check, an
        all-green list here is the most misleading thing we could show them
        (spec 2026-09-02-05).
      */}
      <StalePublicationsBanner org={org} stale={stalePublications} />

      <div className="flex flex-wrap items-center gap-4">
        <div className="relative flex-1 min-w-[200px] max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("searchChecks")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9"
            data-testid="checks-search-input"
          />
        </div>
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-muted-foreground">
            {t("groupBy.label")}
          </span>
          <SegmentedControl
            value={groupBy}
            onValueChange={setGroupByMode}
            aria-label={t("groupBy.label")}
            options={[
              {
                value: "groups",
                label: t("groupBy.groups"),
                testId: "group-by-groups",
              },
              {
                value: "host",
                label: t("groupBy.host"),
                tooltip: t("hostBucketDescription"),
                testId: "group-by-host",
              },
            ]}
          />
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
        <FacetedFilter
          options={statusOptions}
          selected={statusValues}
          onChange={setStatusValues}
          triggerLabel={statusTriggerLabel}
          testId="status-filter"
        />
        <FacetedFilter
          options={typeOptions}
          selected={typeValues}
          onChange={setTypeValues}
          triggerLabel={typeTriggerLabel}
          testId="type-filter"
        />
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
            {(groups || [])
              .map((group, idx) => ({ group, idx }))
              .filter(({ group }) => {
                // While filtering, skip groups whose bucket has zero matches
                // — but only once the stream has fully resolved
                // (checksStreaming false), so a group never flickers out
                // before its later pages have had a chance to load. Not
                // filtering: keep today's behavior (empty groups stay
                // visible with their placeholder, so users can find/manage
                // them).
                if (!isFiltering || checksStreaming) return true;
                return (checksByGroup.get(group.uid)?.length ?? 0) > 0;
              })
              .map(({ group, idx }) => (
                <CheckGroupSection
                  key={group.uid}
                  group={group}
                  org={org}
                  checks={checksByGroup.get(group.uid) ?? []}
                  isLoading={checksStreaming}
                  error={checksError}
                  search={debouncedSearch}
                  isFiltering={isFiltering}
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

          {hostBuckets.length === 0 && !checksStreaming && !isFiltering && (
            <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
              <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                <ListChecks className="h-6 w-6 text-muted-foreground" />
              </div>
              <p className="text-sm font-medium text-foreground">
                {t("noChecksAtAll")}
              </p>
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

      {/* Import source dialog: pick where the checks come from, then preview. */}
      <Dialog open={importOpen} onOpenChange={(open) => (open ? setImportOpen(true) : closeImportDialog())}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("dialog.importTitle")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="import-source">{t("dialog.importSource")}</Label>
              <Select
                value={importSource}
                onValueChange={(value) => {
                  setImportSource(value as ImportSourceId);
                  setImportText("");
                  setImportToken("");
                  setImportFileName("");
                }}
              >
                <SelectTrigger id="import-source" data-testid="import-source">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {IMPORT_SOURCES.map((source) => (
                    <SelectItem key={source} value={source} data-testid={`import-source-${source}`}>
                      {t(`dialog.importSources.${source}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                {t(`dialog.importSourceHelp.${importSource}`)}
              </p>
            </div>

            {FILE_IMPORT_SOURCES.includes(importSource) ? (
              <div className="space-y-2">
                <Label htmlFor="import-payload">{t("dialog.importPayload")}</Label>
                <Textarea
                  id="import-payload"
                  data-testid="import-payload"
                  rows={8}
                  className="font-mono text-xs"
                  value={importText}
                  onChange={(e) => {
                    setImportText(e.target.value);
                    setImportFileName("");
                  }}
                  placeholder={t(`dialog.importPlaceholder.${importSource}`)}
                />
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => fileInputRef.current?.click()}
                    data-testid="import-upload-button"
                  >
                    <Upload className="mr-2 h-4 w-4" />
                    {t("dialog.importUpload")}
                  </Button>
                  {importFileName && (
                    <span className="text-xs text-muted-foreground truncate">{importFileName}</span>
                  )}
                </div>
                <input
                  ref={fileInputRef}
                  type="file"
                  accept={IMPORT_ACCEPT[importSource]}
                  className="hidden"
                  onChange={handleImportFile}
                  data-testid="import-file-input"
                />
              </div>
            ) : (
              <div className="space-y-2">
                <Label htmlFor="import-token">{t("dialog.importToken")}</Label>
                <PasswordInput
                  id="import-token"
                  data-testid="import-token"
                  value={importToken}
                  onChange={(e) => setImportToken(e.target.value)}
                  placeholder="••••••••"
                />
                <Alert>
                  <AlertTitle>{t("dialog.importTokenNoticeTitle")}</AlertTitle>
                  <AlertDescription>{t("dialog.importTokenNotice")}</AlertDescription>
                </Alert>
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeImportDialog}>
              {tc("cancel")}
            </Button>
            <Button
              onClick={handleImportPreview}
              data-testid="import-preview-button"
              disabled={
                importChecks.isPending ||
                convertChecks.isPending ||
                (FILE_IMPORT_SOURCES.includes(importSource)
                  ? importText.trim() === ""
                  : importToken.trim() === "")
              }
            >
              {(importChecks.isPending || convertChecks.isPending) && (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              )}
              {t("dialog.importPreview")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Import Preview Dialog */}
      <Dialog open={!!importPreview} onOpenChange={() => setImportPreview(null)}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("dialog.importPreview")}</DialogTitle>
          </DialogHeader>
          {importPreview && (
            <div className="space-y-4 py-2 max-h-[60vh] overflow-y-auto" data-testid="import-preview">
              <div className="grid grid-cols-3 gap-4 text-center">
                <div>
                  <div className="text-2xl font-bold text-green-600" data-testid="import-preview-created">
                    {importPreview.created}
                  </div>
                  <div className="text-sm text-muted-foreground">{t("dialog.toCreate")}</div>
                </div>
                <div>
                  <div className="text-2xl font-bold text-blue-600" data-testid="import-preview-updated">
                    {importPreview.updated}
                  </div>
                  <div className="text-sm text-muted-foreground">{t("dialog.toUpdate")}</div>
                </div>
                <div>
                  <div className="text-2xl font-bold text-red-600">{importPreview.errors.length}</div>
                  <div className="text-sm text-muted-foreground">{t("dialog.errors")}</div>
                </div>
              </div>
              {importPreview.errors.length > 0 && (
                <div className="rounded-md bg-red-50 dark:bg-red-950 p-3 text-sm">
                  {importPreview.errors.map((err) => (
                    <div key={err.index} className="text-red-700 dark:text-red-300">
                      <span className="font-mono">{err.slug}</span>: {err.error}
                    </div>
                  ))}
                </div>
              )}
              {importPreview.warnings.length > 0 && (
                <Alert variant="warning" data-testid="import-preview-warnings">
                  <AlertTitle>
                    {t("dialog.importWarnings", { count: importPreview.warnings.length })}
                  </AlertTitle>
                  <AlertDescription>
                    <ul className="list-disc space-y-1 pl-4">
                      {importPreview.warnings.map((warning, index) => (
                        <li key={`${warning.item ?? ""}-${warning.field ?? ""}-${index}`}>
                          {warning.item && <span className="font-medium">{warning.item}: </span>}
                          {warning.message}
                        </li>
                      ))}
                    </ul>
                  </AlertDescription>
                </Alert>
              )}
            </div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setImportPreview(null)}>
              {tc("cancel")}
            </Button>
            <Button
              onClick={handleImportConfirm}
              data-testid="import-confirm-button"
              disabled={
                importChecks.isPending ||
                convertChecks.isPending ||
                (importPreview?.created === 0 && importPreview?.updated === 0)
              }
            >
              {(importChecks.isPending || convertChecks.isPending) && (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              )}
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
