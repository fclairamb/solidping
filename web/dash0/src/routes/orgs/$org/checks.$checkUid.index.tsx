import { useState, useEffect, useMemo, useRef } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Trans, useTranslation } from "react-i18next";
import type { IncidentDetail, OrgResult } from "@/api/hooks";
import { flappingSummaryParams } from "@/lib/flap-summary";
import {
  AlertTriangle,
  ArrowLeft,
  BadgeCheck,
  Check as CheckIcon,
  Clock,
  Copy,
  ExternalLink,
  Globe,
  Loader2,
  Pencil,
  Power,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { toast } from "sonner";
import {
  useCheck,
  useCloneCheck,
  useDeleteCheck,
  useUpdateCheck,
  useFeatures,
  useRotateHeartbeatToken,
  useResults,
  useChartWindowResults,
  useIncidents,
  useRegions,
} from "@/api/hooks";
import { useEmailAddressDomain, emailCheckAddress } from "@/api/email-inbox";
import {
  stretchWhileLive,
  useLiveSubscription,
  useScopeError,
  useScopeLive,
} from "@/contexts/LiveEventsContext";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Badge, badgeVariants } from "@/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { regionDisplayLabel, sortRegionSlugs } from "@/lib/region-label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { CollapsibleCode } from "@/components/shared/copyable-code";
import { CollapsibleSection } from "@/components/ui/collapsible-section";
import { Label } from "@/components/ui/label";
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
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { StatusBadge } from "@/components/shared/status-badge";
import { TunnelDependents, TunnelVia } from "@/components/checks/tunnel-detail";
import {
  DeliverySources,
  DeliveryVia,
} from "@/components/checks/smtp-delivery-detail";
import { StatusDot } from "@/components/shared/status-dot";
import {
  CheckTypeBadge,
  CheckTypeIcon,
} from "@/components/shared/check-type-identity";
import { DocsLink } from "@/components/shared/docs-link";
import { docsHrefForType } from "@/components/shared/check-type-docs-anchors";
import { SloCoverageChip } from "@/components/slos/slo-coverage-chip";
import { QueryErrorView } from "@/components/shared/error-views";
import { NeedsResealAlert } from "@/components/checks/needs-reseal-alert";
import { CheckSummaryCards } from "@/components/checks/check-summary-cards";
import { SslChainCard } from "@/components/checks/ssl-chain-card";
import { DockerRestartLoopCard } from "@/components/checks/docker-restart-loop-card";
import { DnsblCard, DNSBL_OUTPUT_KEYS } from "@/components/checks/dnsbl-card";
import { isEvaluationOutput } from "@/components/checks/evaluation-card";
import {
  JsonAssertionResultCard,
  JSON_ASSERTION_RESULT_OUTPUT_KEY,
} from "@/components/checks/json-assertion-result-card";
import {
  ResponseTimeChart,
  formatMs,
} from "@/components/checks/response-time-chart";
import { AvailabilityTable } from "@/components/checks/availability-table";
import { DependenciesCard } from "@/components/checks/dependencies-card";

// The result-output key reporting which address family the probe used, and the
// config key pinning it. Kept next to each other so the pair can't drift.
const IP_VERSION_OUTPUT_KEY = "ip_version";

// The two passive-evaluation output keys that are pure bookkeeping: the
// self-declaration the worker stamps and the uid it points at (spec
// 2026-09-02-04). Both are filtered out of the header's raw Output dump.
const EVALUATION_ROW_MARKER_KEY = "evaluation";
const EVALUATION_LAST_SIGNAL_UID_KEY = "lastSignalResultUid";
const IP_VERSION_CONFIG_KEY = "ipVersion";

/**
 * Check-detail search schema. `graphFrom` / `graphTo` / `graphSelected` are
 * declared **optional** so the ~20 existing literal navigations to this route
 * (which pass only the original four keys) keep compiling — omitting an
 * optional key is legal, whereas a required key would force every call site to
 * add all three.
 */
interface CheckDetailSearch {
  graphPeriod?: "hour" | "day" | "week" | "month";
  graphFull?: true;
  // Single region scope shared by the chart, Recent Results table, and the
  // duration-stats strip — selecting a region anywhere on the page filters
  // all three together (spec 2026-08-13-02). No back-compat for the old
  // split `graphRegion`/`resultsRegion` keys: an old deep link carrying
  // either simply falls back to "All regions" since validateSearch ignores
  // unrecognized keys.
  region?: string;
  graphFrom?: number;
  graphTo?: number;
  graphSelected?: string;
}

export const Route = createFileRoute("/orgs/$org/checks/$checkUid/")({
  validateSearch: (search: Record<string, unknown>): CheckDetailSearch => ({
    graphPeriod: (["hour", "day", "week", "month"].includes(
      search.graphPeriod as string,
    )
      ? search.graphPeriod
      : undefined) as "hour" | "day" | "week" | "month" | undefined,
    // TanStack Router's default search parser already coerces "true"/"false"
    // query-string values to native booleans before validateSearch runs, so a
    // bare `=== "true"` string comparison silently always evaluates to false
    // (see login.tsx's `session_expired` fix for the same bug class).
    graphFull:
      search.graphFull === true || search.graphFull === "true"
        ? true
        : undefined,
    region: typeof search.region === "string" ? search.region : undefined,
    // Drag-to-zoom X (time) window, epoch-ms. Maps onto the results endpoint's
    // periodStartAfter/periodEndBefore so a shared link fetches just this
    // window. Coerced to a finite number; anything else → undefined (full
    // default range). The chart treats the zoom as active only when from < to.
    graphFrom: coerceEpochMs(search.graphFrom),
    graphTo: coerceEpochMs(search.graphTo),
    // The currently-highlighted result (its pinned detail box); persisted so a
    // shared link reproduces the selection.
    graphSelected:
      typeof search.graphSelected === "string" && search.graphSelected !== ""
        ? search.graphSelected
        : undefined,
  }),
  component: CheckDetailPage,
});

/** Coerce a search value to a finite epoch-ms number, else undefined. The
 * TanStack default parser already turns a bare numeric query value into a
 * number, but tolerate a string form too for hand-edited/deep-linked URLs. */
function coerceEpochMs(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value !== "") {
    const n = Number(value);
    if (Number.isFinite(n)) return n;
  }
  return undefined;
}

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

function IncidentDuration({ incident }: { incident: IncidentDetail }) {
  const { t } = useTranslation("checks");
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (incident.state === "active" && !incident.resolvedAt) {
      const interval = setInterval(() => setNow(Date.now()), 1000);
      return () => clearInterval(interval);
    }
  }, [incident.state, incident.resolvedAt]);

  if (incident.startedAt && incident.resolvedAt) {
    return formatDuration(
      new Date(incident.resolvedAt).getTime() -
        new Date(incident.startedAt).getTime(),
    );
  }
  if (incident.startedAt) {
    return (
      formatDuration(now - new Date(incident.startedAt).getTime()) +
      " " +
      t("detail.ongoing")
    );
  }
  return "-";
}

function formatResultTime(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  return sameDay ? d.toLocaleTimeString() : d.toLocaleString();
}

interface DurationStats {
  min: number;
  max: number;
  avg: number;
  p95: number;
  count: number;
  /** True when the window includes any rollup (non-raw) row, so avg/p95 are
   * combined estimates rather than exact values (display with a `~` prefix). */
  isEstimate: boolean;
}

/**
 * Tier-aware min/avg/max/p95 + sample count for one region (or all regions
 * when `region` is undefined — used internally by chart color-swatch code,
 * not by the stats strip, which only renders for a specific region) over the
 * given result set. Mirrors the combination method
 * server/internal/jobs/jobtypes/job_aggregation.go actually uses for
 * combining child buckets:
 *   - min/max: exact min-of-mins / max-of-maxes across all contributing rows
 *     (raw rows contribute their own durationMs as both min and max).
 *   - avg: totalChecks-weighted mean of each rollup row's durationAvgMs
 *     (falling back to its plotted durationMs when durationAvgMs is missing —
 *     e.g. a rollup row that predates this field), each raw row contributing
 *     its own durationMs with weight 1.
 *   - p95: an unweighted average of each row's own p95 — rollup rows
 *     contribute their stored durationP95Ms (falling back to durationMs when
 *     absent), raw rows contribute their own durationMs as a degenerate
 *     single-sample p95. This mirrors calculateAggregatedMetrics's plain
 *     p95Sum / p95Count combination (NOT totalChecks-weighted — the
 *     aggregator does not weight p95 by count when combining buckets).
 * Returns null when there is no duration data for the region in this window.
 */
function computeDurationStats(
  allPoints: OrgResult[],
  region: string | undefined,
): DurationStats | null {
  const points = region
    ? allPoints.filter((p) => p.region === region)
    : allPoints;
  if (points.length === 0) return null;

  let min = Infinity;
  let max = -Infinity;
  let count = 0;
  let avgWeightedSum = 0;
  let avgWeight = 0;
  let p95Sum = 0;
  let p95Count = 0;
  let isEstimate = false;

  for (const p of points) {
    const isRaw = p.periodType === "raw" || !p.periodType;

    if (isRaw) {
      if (p.durationMs == null) continue;
      min = Math.min(min, p.durationMs);
      max = Math.max(max, p.durationMs);
      count += 1;
      avgWeightedSum += p.durationMs;
      avgWeight += 1;
      p95Sum += p.durationMs;
      p95Count += 1;
      continue;
    }

    // Rollup row (hour/day/month) — combined stats become estimates.
    isEstimate = true;
    const weight = p.totalChecks ?? 1;
    count += weight;

    if (p.durationMinMs != null) min = Math.min(min, p.durationMinMs);
    if (p.durationMaxMs != null) max = Math.max(max, p.durationMaxMs);

    const avgFallback = p.durationAvgMs ?? p.durationMs;
    if (avgFallback != null) {
      avgWeightedSum += avgFallback * weight;
      avgWeight += weight;
    }

    const p95Fallback = p.durationP95Ms ?? p.durationMs;
    if (p95Fallback != null) {
      p95Sum += p95Fallback;
      p95Count += 1;
    }
  }

  if (
    !Number.isFinite(min) ||
    !Number.isFinite(max) ||
    avgWeight === 0 ||
    p95Count === 0
  ) {
    return null;
  }

  return {
    min,
    max,
    avg: avgWeightedSum / avgWeight,
    p95: p95Sum / p95Count,
    count,
    isEstimate,
  };
}

/** Parse HH:MM:SS period string to milliseconds */
function parsePeriodMs(period?: string): number | undefined {
  if (!period) return undefined;
  const parts = period.split(":").map(Number);
  if (parts.length !== 3 || parts.some(isNaN)) return undefined;
  const [h, m, s] = parts;
  const ms = (h * 3600 + m * 60 + s) * 1000;
  return ms > 0 ? ms : undefined;
}

function HeartbeatEndpoint({
  org,
  check,
}: {
  org: string;
  check: { slug?: string; uid: string; config?: Record<string, unknown> };
}) {
  const { t } = useTranslation("checks");
  const token = check.config?.token as string;
  const identifier = check.slug || check.uid;
  const heartbeatUrl = `${window.location.origin}/api/v1/heartbeat/${org}/${identifier}?token=${token}`;
  const curlCommand = `curl "${heartbeatUrl}"`;
  const rotateToken = useRotateHeartbeatToken(org, check.uid);

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text);
    toast.success(t("detail.toast.copied"));
  };

  const handleRegenerate = async () => {
    try {
      await rotateToken.mutateAsync();
      toast.success(t("endpoints.heartbeat.regenerated"));
    } catch {
      toast.error(t("endpoints.heartbeat.regenerateFailed"));
    }
  };

  return (
    <div>
      <div className="flex items-center justify-between gap-2 mb-2">
        <div className="text-sm font-medium text-muted-foreground">
          {t("endpoints.heartbeat.title")}
        </div>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button
              type="button"
              variant="outline"
              size="sm"
              data-testid="heartbeat-regenerate-token"
            >
              <RefreshCw className="h-3.5 w-3.5 mr-1.5" />
              {t("endpoints.heartbeat.regenerate")}
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t("endpoints.heartbeat.regenerateTitle")}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t("endpoints.heartbeat.regenerateDescription")}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
              <AlertDialogAction
                onClick={handleRegenerate}
                disabled={rotateToken.isPending}
                data-testid="heartbeat-regenerate-confirm"
              >
                {rotateToken.isPending ? (
                  <>
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    {t("endpoints.heartbeat.regenerating")}
                  </>
                ) : (
                  t("endpoints.heartbeat.regenerateConfirm")
                )}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
      <div className="space-y-3">
        <div className="bg-muted rounded-md p-3 text-sm font-mono break-all flex items-start gap-2">
          <span className="flex-1">{heartbeatUrl}</span>
          <button
            type="button"
            onClick={() => copyToClipboard(heartbeatUrl)}
            className="text-muted-foreground hover:text-foreground p-0.5 rounded shrink-0"
          >
            <Copy className="h-4 w-4" />
          </button>
        </div>
        <div>
          <div className="text-xs text-muted-foreground mb-1">
            {t("endpoints.heartbeat.curl")}
          </div>
          <div className="bg-muted rounded-md p-3 text-sm font-mono break-all flex items-start gap-2">
            <span className="flex-1">{curlCommand}</span>
            <button
              type="button"
              onClick={() => copyToClipboard(curlCommand)}
              className="text-muted-foreground hover:text-foreground p-0.5 rounded shrink-0"
            >
              <Copy className="h-4 w-4" />
            </button>
          </div>
        </div>
        <p className="text-xs text-muted-foreground">
          {t("endpoints.heartbeat.callerNote")}
        </p>
        <HeartbeatPushEndpoint org={org} check={check} />
      </div>
    </div>
  );
}

/**
 * Embedded push transports for a heartbeat check (spec 2026-09-01-06).
 *
 * Rendered only when the server reports a listener enabled: the TCP/UDP
 * listeners are off by default and exposing their ports is a deployment
 * decision, so advertising a `nc` one-liner nobody can reach would be worse
 * than showing nothing.
 *
 * Collapsed by default (spec 2026-09-02-01): this is an opt-in,
 * deployment-level feature that most heartbeat checks never touch, so it
 * should be discoverable rather than dominating the endpoint card. The
 * `summary` line keeps the collapsed row honest about what is enabled. The
 * rotate-token nudge is rendered *outside* the collapsible body — it is a
 * security warning and must stay visible even while the section is
 * collapsed.
 */
function HeartbeatPushEndpoint({
  org,
  check,
}: {
  org: string;
  check: { slug?: string; uid: string; config?: Record<string, unknown> };
}) {
  const { t } = useTranslation("checks");
  const { data: features } = useFeatures();
  const updateCheck = useUpdateCheck(org, check.uid);
  const push = features?.heartbeatPush;
  const requireHmac = check.config?.require_hmac === true;

  if (!push || (!push.tcpEnabled && !push.udpEnabled)) return null;

  const token = (check.config?.token as string) ?? "";
  const identifier = check.slug || check.uid;
  const host = push.host || window.location.hostname;
  const target = `${org}/${identifier}`;
  // The trailing "\\n" is TWO characters on purpose — the backslash escape
  // printf turns into the newline that frames a TCP beat. Writing a real LF
  // here would render as a trailing space (HTML collapses it), so anyone
  // retyping what they see would send an unterminated line that never frames.
  const tcpCommand = `printf 'SP1 ${target} ${token}\\n' | nc ${host} ${push.tcpPort}`;
  // UDP needs no terminator: the datagram boundary is the frame.
  const udpCommand = `printf 'SP1 ${target} ${token}' | nc -u -w1 ${host} ${push.udpPort}`;

  const summaryParts: string[] = [];
  if (push.tcpEnabled) {
    summaryParts.push(
      t("endpoints.heartbeat.push.summaryTcp", { port: push.tcpPort }),
    );
  }
  if (push.udpEnabled) {
    summaryParts.push(
      t("endpoints.heartbeat.push.summaryUdp", { port: push.udpPort }),
    );
  }
  if (requireHmac) {
    summaryParts.push(t("endpoints.heartbeat.push.summarySignedOnly"));
  }
  const summary = summaryParts.join(" · ");

  const handleToggle = async (next: boolean) => {
    try {
      // The public side of a config PATCH is REPLACE, not merge, so the whole
      // config goes back — sending only the flag would drop the ping token
      // this very card renders a URL from.
      await updateCheck.mutateAsync({
        config: { ...(check.config ?? {}), require_hmac: next },
      });
      toast.success(
        next
          ? t("endpoints.heartbeat.push.hmacEnabled")
          : t("endpoints.heartbeat.push.hmacDisabled"),
      );
    } catch {
      toast.error(t("endpoints.heartbeat.push.hmacFailed"));
    }
  };

  return (
    <div className="space-y-3 border-t pt-3" data-testid="heartbeat-push">
      <CollapsibleSection
        title={t("endpoints.heartbeat.push.title")}
        summary={summary}
        defaultOpen={false}
        data-testid="heartbeat-push-toggle"
      >
        <p className="text-xs text-muted-foreground">
          {t("endpoints.heartbeat.push.description")}
        </p>

        {push.tcpEnabled && (
          <CollapsibleCode
            label={t("endpoints.heartbeat.push.tcpLabel", {
              port: push.tcpPort,
            })}
            value={tcpCommand}
            data-testid="heartbeat-push-tcp"
          />
        )}

        {push.udpEnabled && (
          <CollapsibleCode
            label={t("endpoints.heartbeat.push.udpLabel", {
              port: push.udpPort,
            })}
            value={udpCommand}
            data-testid="heartbeat-push-udp"
          />
        )}

        <p className="text-xs text-muted-foreground">
          {t("endpoints.heartbeat.push.annotationHint")}
        </p>

        <div className="flex items-start gap-2">
          <p className="flex-1 text-xs text-muted-foreground">
            {t("endpoints.heartbeat.push.sketchDocsHint")}
          </p>
          <DocsLink href="/docs/features/embedded-push#a-minimal-arduino--esp-sketch" />
        </div>

        <div className="flex items-start gap-2">
          <Switch
            id="heartbeat-require-hmac"
            checked={requireHmac}
            disabled={updateCheck.isPending}
            onCheckedChange={handleToggle}
            data-testid="heartbeat-require-hmac"
          />
          <div className="space-y-1">
            <Label htmlFor="heartbeat-require-hmac">
              {t("endpoints.heartbeat.push.requireHmac")}
            </Label>
            <p className="text-xs text-muted-foreground">
              {t("endpoints.heartbeat.push.requireHmacHint")}
            </p>
          </div>
        </div>
      </CollapsibleSection>

      {requireHmac && (
        <Alert data-testid="heartbeat-rotate-nudge">
          <AlertTriangle className="h-4 w-4" />
          <AlertDescription>
            {t("endpoints.heartbeat.push.rotateNudge")}
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}

function EmailEndpoint({
  check,
}: {
  check: { config?: Record<string, unknown> };
}) {
  const { t } = useTranslation("checks");
  const token = check.config?.token as string;
  const { data: domain } = useEmailAddressDomain();
  const [showHelp, setShowHelp] = useState(false);

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text);
    toast.success(t("detail.toast.copied"));
  };

  if (!domain) {
    return (
      <div>
        <div className="text-sm font-medium text-muted-foreground mb-2">
          {t("endpoints.email.title")}
        </div>
        <div className="bg-muted rounded-md p-3 text-sm text-muted-foreground">
          {t("endpoints.email.notConfigured")}
        </div>
      </div>
    );
  }

  const address = emailCheckAddress(token, domain);
  const mailto = `mailto:${address}?subject=Test`;

  return (
    <div>
      <div className="text-sm font-medium text-muted-foreground mb-2">
        {t("endpoints.email.title")}
      </div>
      <div className="space-y-3">
        <div className="bg-muted rounded-md p-3 text-sm font-mono break-all flex items-start gap-2">
          <span className="flex-1" data-testid="email-check-address">
            {address}
          </span>
          <button
            type="button"
            data-testid="email-check-copy-btn"
            onClick={() => copyToClipboard(address)}
            className="text-muted-foreground hover:text-foreground p-0.5 rounded shrink-0"
          >
            <Copy className="h-4 w-4" />
          </button>
        </div>
        <div>
          <a
            href={mailto}
            className="text-sm text-primary hover:underline inline-flex items-center gap-1"
          >
            {t("endpoints.email.sendTest")}
            <ExternalLink className="h-3 w-3" />
          </a>
        </div>
        <button
          type="button"
          onClick={() => setShowHelp((v) => !v)}
          className="text-sm text-muted-foreground hover:text-foreground"
        >
          {showHelp
            ? t("endpoints.email.hideOptions")
            : t("endpoints.email.showOptions")}
        </button>
        {showHelp && (
          <div className="bg-muted rounded-md p-3 text-sm space-y-2">
            <p>{t("endpoints.email.help.intro")}</p>
            <ul className="list-disc pl-5 space-y-1 text-muted-foreground">
              <li>
                <Trans
                  i18nKey="checks:endpoints.email.help.plus"
                  values={{ token, domain }}
                  components={{ code: <code className="font-mono" /> }}
                />
              </li>
              <li>
                <Trans
                  i18nKey="checks:endpoints.email.help.header"
                  components={{ code: <code className="font-mono" /> }}
                />
              </li>
              <li>
                <Trans
                  i18nKey="checks:endpoints.email.help.subject"
                  components={{ code: <code className="font-mono" /> }}
                />
              </li>
            </ul>
            <p className="text-xs text-muted-foreground">
              <Trans
                i18nKey="checks:endpoints.email.help.priority"
                components={{ code: <code className="font-mono" /> }}
              />
            </p>
          </div>
        )}
      </div>
    </div>
  );
}

function CheckDetailPage() {
  const { t } = useTranslation(["checks", "common"]);
  const { org, checkUid } = Route.useParams();
  const { graphPeriod, graphFull, region, graphFrom, graphTo, graphSelected } =
    Route.useSearch();
  const navigate = useNavigate();
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [editingSlug, setEditingSlug] = useState(false);
  const [slugValue, setSlugValue] = useState("");
  const slugInputRef = useRef<HTMLInputElement>(null);

  const {
    data: check,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useCheck(org, checkUid);

  const periodMs = useMemo(() => parsePeriodMs(check?.period), [check?.period]);

  // The address family the last probe actually used, and the one the check asks
  // for. Showing both is the point: "pinned to IPv6, resolved IPv6" is a
  // verified IPv6 path, while a check with no pin only ever tells you which
  // family it happened to land on — an IPv4 result is not evidence that IPv6
  // works. See the `ipVersion` option (spec 2026-08-09-02).
  const resolvedIpVersion =
    typeof check?.lastResult?.output?.[IP_VERSION_OUTPUT_KEY] === "string"
      ? (check.lastResult.output[IP_VERSION_OUTPUT_KEY] as string)
      : null;
  const pinnedIpVersionRaw = check?.config?.[IP_VERSION_CONFIG_KEY];
  const pinnedIpVersion =
    typeof pinnedIpVersionRaw === "string" &&
    pinnedIpVersionRaw !== "" &&
    pinnedIpVersionRaw !== "auto"
      ? pinnedIpVersionRaw
      : null;

  // Subscribe with the canonical uid from the loaded check, never the raw
  // route param — `$checkUid` also accepts a slug (the REST fetch above
  // resolves either via GetCheckByUidOrSlug), but the realtime WS scope is
  // validated by a uid-only lookup server-side, so a slug is rejected with
  // NOT_FOUND. Gate the subscription until the check has resolved: passing
  // `undefined` is a no-op (see useLiveSubscription/useScopeLive), so this
  // never subscribes with a stale/wrong identifier while loading.
  const canonicalUid = check?.uid;
  const checkScope = canonicalUid
    ? { entity: "check" as const, uid: canonicalUid }
    : undefined;

  // Watch this check (status/results) and the org's incidents collection —
  // an open/resolved incident for this check must reflect live too.
  useLiveSubscription(checkScope);
  useLiveSubscription({ entity: "incidents" });
  const checkLive = useScopeLive(checkScope);
  const checkLiveError = useScopeError(checkScope);

  // While the check has never produced a real result, poll fast enough that
  // a freshly-created check shows its first real status without making the
  // user wait for a full check period. Cap the fast phase so a stuck worker
  // can't trigger runaway polling at 1.5 s.
  // When this check's live subscription is acked, the first result arrives
  // as a hint-driven invalidation, so the hot poll is skipped entirely and
  // the regular interval stretches to the lazy safety net.
  //
  // `lastResult` is absent (not a "created"-status object) for a check that
  // has never run: the API excludes the non-terminal "created" placeholder
  // row from lastResult entirely (spec 2026-08-18-03) rather than surfacing
  // it as if it were a real result, so pending-first-run is now "no
  // lastResult at all", not "lastResult.status === created".
  const isLive = checkLive;
  const fastPollMs = 1500;
  const fastPollWindowMs = 30_000;
  const isPendingFirstRun = !check?.lastResult;
  const [withinFastWindow, setWithinFastWindow] = useState(true);
  useEffect(() => {
    const id = setTimeout(() => setWithinFastWindow(false), fastPollWindowMs);
    return () => clearTimeout(id);
  }, []);

  const refetchInterval = isLive
    ? stretchWhileLive(periodMs ?? 0, isLive)
    : isPendingFirstRun && withinFastWindow
      ? fastPollMs
      : periodMs;

  // Re-fetch check (with lastResult) at the same interval
  useCheck(org, checkUid, {
    refetchInterval,
  });

  // The SAME two-pass window the chart draws, through the same hook — so the
  // react-query keys are identical and these are cache hits, not a second round
  // of HTTP requests. Used to derive the observed-region set for the results
  // filter and the tier-aware duration stats strip, both scoped to the chart's
  // current graphPeriod window — one page, one time window, per the spec.
  const graphTimeRange = graphPeriod ?? "day";
  // A zoom is active only for a well-formed forward window; when set, the same
  // window is fed to the hook here as in the chart, keeping these cache hits.
  const graphZoom = useMemo(
    () =>
      graphFrom != null && graphTo != null && graphTo > graphFrom
        ? { from: graphFrom, to: graphTo }
        : undefined,
    [graphFrom, graphTo],
  );
  const { data: chartWindowResults } = useChartWindowResults(
    org,
    checkUid,
    { timeRange: graphTimeRange, periodMs, zoom: graphZoom },
    { rawRefetchInterval: refetchInterval },
  );

  const { data: regionsData } = useRegions(org);

  const observedRegions = useMemo(() => {
    const set = new Set<string>();
    for (const r of chartWindowResults?.data ?? []) {
      if (r.region) set.add(r.region);
    }
    return sortRegionSlugs(regionsData?.regions, Array.from(set));
  }, [chartWindowResults, regionsData]);

  // Stale-selection guard, mirroring the chart's own effectiveRegion: only
  // honor ?region= when that slug is actually present in the current
  // window's observed regions. A stale deep link (regions changed, data aged
  // out) falls back to "All" instead of silently emptying the table.
  const effectiveRegion =
    region && observedRegions.includes(region) ? region : undefined;

  // Single navigate shared by the chart's region chips/controlled prop, the
  // Recent Results filter chips, and the region badge in each result row —
  // selecting a region anywhere scopes chart + Recent Results + stats
  // together (spec 2026-08-13-02). `replace: true` since this is an
  // incidental refinement, not primary navigation. Clearing graphSelected is
  // a deliberate judgment call: a pinned point may belong to a region the new
  // filter excludes, so drop the pin rather than leave a stale/unanchored box
  // (see response-time-chart.tsx's own effectiveRegion guard, which behaves
  // the same way for the chart's dot-selection state).
  const setRegion = (nextRegion: string | undefined) => {
    navigate({
      to: ".",
      search: (prev) => ({
        ...prev,
        region: nextRegion,
        graphSelected: undefined,
      }),
      replace: true,
    });
  };

  const durationStats = useMemo(
    () => computeDurationStats(chartWindowResults?.data ?? [], effectiveRegion),
    [chartWindowResults, effectiveRegion],
  );

  // Passive checks (heartbeat, email) interleave two kinds of raw row that
  // look identical in this table — the beat, and the scheduler's own
  // evaluation of the schedule (spec 2026-09-02-04) — so for those types only
  // we also pull `output` and badge the evaluations. Deliberately NOT widened
  // for other types: nothing else in this table needs the payload, and the
  // chart-window query (which fetches far more rows) is untouched.
  const isPassiveCheckType = check?.type === "heartbeat" || check?.type === "email";

  const { data: results } = useResults(org, {
    checkUid,
    size: 10,
    region: effectiveRegion,
    with: isPassiveCheckType ? "durationMs,region,output" : "durationMs,region",
    refetchInterval,
  });

  // Always send the resolved UUID, never the raw route param — `checkUid`
  // may be a slug, and unlike /results and the other check-scoped queries on
  // this page, /incidents does not resolve a slug filter (issue #127: a
  // slug-addressed page 500ed loading incidents). Gate on check.uid so the
  // query fires only once the check has resolved.
  const { data: incidents } = useIncidents(org, {
    checkUid: check?.uid,
    size: 100,
    enabled: !!check?.uid,
  });

  const deleteCheck = useDeleteCheck(org);
  const cloneCheck = useCloneCheck(org);
  const updateCheck = useUpdateCheck(org, checkUid);

  const startEditingSlug = () => {
    setSlugValue(check?.slug || "");
    setEditingSlug(true);
    setTimeout(() => slugInputRef.current?.focus(), 0);
  };

  const saveSlug = async () => {
    const trimmed = slugValue.trim();
    if (trimmed === (check?.slug || "")) {
      setEditingSlug(false);
      return;
    }
    try {
      await updateCheck.mutateAsync({ slug: trimmed });
      toast.success(t("checks:detail.toast.slugUpdated"));
      setEditingSlug(false);
      navigate({
        to: "/orgs/$org/checks/$checkUid",
        params: { org, checkUid: trimmed },
        search: {
          graphPeriod: undefined,
          graphFull: undefined,
          region: undefined,
          graphFrom: undefined,
          graphTo: undefined,
          graphSelected: undefined,
        },
        replace: true,
      });
    } catch {
      toast.error(t("checks:detail.toast.slugUpdateFailed"));
    }
  };

  const cancelEditingSlug = () => {
    setEditingSlug(false);
    setSlugValue(check?.slug || "");
  };

  const handleDelete = async () => {
    try {
      await deleteCheck.mutateAsync(checkUid);
      toast.success(t("checks:toast.deleted"));
      navigate({ to: "/orgs/$org/checks", params: { org } });
    } catch {
      toast.error(t("checks:detail.toast.deleteFailed"));
    }
  };

  const handleClone = async () => {
    try {
      const newCheck = await cloneCheck.mutateAsync(checkUid);
      toast.success(t("checks:detail.cloned") ?? "Check cloned");
      navigate({
        to: "/orgs/$org/checks/$checkUid/edit",
        params: { org, checkUid: newCheck.uid! },
      });
    } catch {
      toast.error(t("checks:detail.cloneFailed") ?? "Failed to clone check");
    }
  };

  const handleToggleEnabled = async () => {
    if (!check) return;
    const next = !check.enabled;
    try {
      await updateCheck.mutateAsync({ enabled: next });
      toast.success(
        next
          ? (t("checks:detail.enableToast") ?? "Check enabled")
          : (t("checks:detail.disableToast") ?? "Check disabled"),
      );
    } catch {
      toast.error(t("checks:detail.toggleFailed") ?? "Failed to update check");
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
        <Skeleton className="h-64 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("checks:title")}
        backTo="/orgs/$org/checks"
        backLabel={t("checks:detail.backToChecks")}
        onRetry={() => refetch()}
      />
    );
  }

  if (!check) {
    return (
      <div className="text-center py-12">
        <p className="text-muted-foreground mb-4">
          {t("checks:detail.notFound")}
        </p>
        <Link to="/orgs/$org/checks" params={{ org }}>
          <Button variant="outline">{t("checks:detail.backToChecks")}</Button>
        </Link>
      </div>
    );
  }

  const headerStatus = check.status ?? check.lastResult?.status;
  const flapSummary = flappingSummaryParams(check);

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-3" data-testid="check-detail-header">
        <div className="flex min-w-0 flex-1 items-center gap-3">
          <StatusDot
            status={headerStatus}
            enabled={check.enabled}
            className="h-3 w-3"
            title={
              check.enabled === false ? t("checks:detail.disabled") : undefined
            }
          />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-3">
              <h1 className="truncate text-2xl font-bold tracking-tight sm:text-3xl">
                {check.name || check.slug || check.uid?.slice(0, 8)}
              </h1>
              <span className="hidden shrink-0 items-center gap-1.5 sm:inline-flex">
                <CheckTypeIcon type={check.type} />
                <CheckTypeBadge type={check.type} />
              </span>
              {check.uid && <SloCoverageChip org={org} checkUid={check.uid} />}
              {isPendingFirstRun && (
                <Badge
                  variant="outline"
                  className="hidden shrink-0 gap-1 text-muted-foreground sm:inline-flex"
                  aria-live="polite"
                >
                  <Loader2 className="h-3 w-3 animate-spin" />
                  {t("checks:detail.pendingFirstRun")}
                </Badge>
              )}
              {checkLiveError && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Badge
                      variant="warning"
                      className="shrink-0 gap-1"
                      data-testid="check-live-error-badge"
                      aria-live="polite"
                    >
                      <AlertTriangle className="h-3 w-3" />
                      {t("checks:detail.liveUnavailable")}
                    </Badge>
                  </TooltipTrigger>
                  <TooltipContent>
                    {t("checks:detail.liveUnavailableTooltip", {
                      title: checkLiveError.title,
                    })}
                  </TooltipContent>
                </Tooltip>
              )}
            </div>
            {check.slug && !editingSlug && (
              <div className="hidden sm:flex items-center gap-1 mt-1">
                <Link
                  to="/orgs/$org/checks/$checkUid"
                  params={{ org, checkUid: check.slug }}
                  search={{
                    graphPeriod: undefined,
                    graphFull: undefined,
                    region: undefined,
                    graphFrom: undefined,
                    graphTo: undefined,
                    graphSelected: undefined,
                  }}
                  className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-xs font-mono text-muted-foreground hover:text-foreground transition-colors"
                >
                  <span>🔗</span>
                  {check.slug}
                </Link>
                <button
                  type="button"
                  onClick={startEditingSlug}
                  className="text-muted-foreground hover:text-foreground p-0.5 rounded"
                >
                  <Pencil className="h-3 w-3" />
                </button>
              </div>
            )}
            {editingSlug && (
              <div className="hidden sm:flex items-center gap-1 mt-1">
                <span className="text-xs">🔗</span>
                <input
                  ref={slugInputRef}
                  value={slugValue}
                  onChange={(e) => setSlugValue(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") saveSlug();
                    if (e.key === "Escape") cancelEditingSlug();
                  }}
                  className="h-6 rounded border bg-background px-1.5 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-ring"
                  disabled={updateCheck.isPending}
                />
                <button
                  type="button"
                  onClick={saveSlug}
                  disabled={updateCheck.isPending}
                  className="text-muted-foreground hover:text-green-500 p-0.5 rounded"
                >
                  {updateCheck.isPending ? (
                    <Loader2 className="h-3 w-3 animate-spin" />
                  ) : (
                    <CheckIcon className="h-3 w-3" />
                  )}
                </button>
                <button
                  type="button"
                  onClick={cancelEditingSlug}
                  disabled={updateCheck.isPending}
                  className="text-muted-foreground hover:text-red-500 p-0.5 rounded"
                >
                  <X className="h-3 w-3" />
                </button>
              </div>
            )}
            {check.uid && checkUid !== check.uid && (
              <div className="hidden sm:flex items-center gap-1 mt-1">
                <Link
                  to="/orgs/$org/checks/$checkUid"
                  params={{ org, checkUid: check.uid }}
                  search={{
                    graphPeriod: undefined,
                    graphFull: undefined,
                    region: undefined,
                    graphFrom: undefined,
                    graphTo: undefined,
                    graphSelected: undefined,
                  }}
                  className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-xs font-mono text-muted-foreground hover:text-foreground transition-colors"
                >
                  uid: {check.uid.slice(0, 8)}...
                </Link>
              </div>
            )}
          </div>
        </div>
        <div className="flex flex-wrap items-center justify-end gap-2">
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("checks:detail.backToChecks") ?? "Back to checks"}
            onClick={() =>
              navigate({ to: "/orgs/$org/checks", params: { org } })
            }
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>

          {/* Inline toolbar — always visible; icon-only below lg, icon + label at lg+ */}
          <div className="flex items-center gap-2">
            <Button
              asChild
              variant="outline"
              size="icon"
              className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"
              aria-label={t("checks:edit")}
            >
              <Link
                to="/orgs/$org/checks/$checkUid/edit"
                params={{ org, checkUid }}
              >
                <Pencil className="h-4 w-4 lg:mr-2" />
                <span className="hidden lg:inline">{t("checks:edit")}</span>
              </Link>
            </Button>
            <Button
              variant="outline"
              size="icon"
              className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"
              aria-label={
                check.enabled
                  ? (t("checks:detail.disable") ?? "Disable")
                  : (t("checks:detail.enable") ?? "Enable")
              }
              disabled={updateCheck.isPending}
              onClick={handleToggleEnabled}
            >
              <Power className="h-4 w-4 lg:mr-2" />
              <span className="hidden lg:inline">
                {check.enabled
                  ? t("checks:detail.disable")
                  : t("checks:detail.enable")}
              </span>
            </Button>
            <Button
              variant="outline"
              size="icon"
              className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"
              aria-label={t("checks:detail.clone") ?? "Clone"}
              disabled={cloneCheck.isPending}
              onClick={handleClone}
            >
              <Copy className="h-4 w-4 lg:mr-2" />
              <span className="hidden lg:inline">
                {t("checks:detail.clone")}
              </span>
            </Button>
            <Button
              asChild
              variant="outline"
              size="icon"
              className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"
              aria-label={t("checks:detail.badges") ?? "Badges"}
            >
              <Link
                to="/orgs/$org/badges"
                params={{ org }}
                search={{ check: check.slug ?? checkUid }}
              >
                <BadgeCheck className="h-4 w-4 lg:mr-2" />
                <span className="hidden lg:inline">
                  {t("checks:detail.badges")}
                </span>
              </Link>
            </Button>
            <Button
              asChild
              variant="outline"
              size="icon"
              className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"
              aria-label={
                t("checks:detail.publishOnStatusPage") ??
                "Publish on a status page"
              }
            >
              <Link
                to="/orgs/$org/status-pages/new"
                params={{ org }}
                search={{ checkUid }}
                data-testid="publish-status-page-link"
              >
                <Globe className="h-4 w-4 lg:mr-2" />
                <span className="hidden lg:inline">
                  {t("checks:detail.publishOnStatusPage")}
                </span>
              </Link>
            </Button>
            <Button
              variant="outline"
              size="icon"
              className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"
              aria-label={t("checks:detail.refresh") ?? "Refresh"}
              onClick={() => refetch()}
              disabled={isRefetching}
            >
              <RefreshCw
                className={`h-4 w-4 lg:mr-2 ${isRefetching ? "animate-spin" : ""}`}
              />
              <span className="hidden lg:inline">
                {t("checks:detail.refresh")}
              </span>
            </Button>
            <Button
              variant="destructive"
              size="icon"
              className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"
              aria-label={t("checks:detail.delete") ?? "Delete"}
              onClick={() => setDeleteOpen(true)}
            >
              <Trash2 className="h-4 w-4 lg:mr-2" />
              <span className="hidden lg:inline">
                {t("checks:detail.delete")}
              </span>
            </Button>
          </div>

          {/* Deep link into the docs section for *this* check's type, so the
              page's own protocol reference is one click away. */}
          <DocsLink href={docsHrefForType(check.type)} />

          {/* Triggerless, controlled delete dialog */}
          <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  {t("checks:detail.deleteTitle")}
                </AlertDialogTitle>
                <AlertDialogDescription>
                  {t("checks:detail.deleteDescription")}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
                <AlertDialogAction
                  onClick={handleDelete}
                  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                >
                  {deleteCheck.isPending ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      {t("checks:detail.deleting")}
                    </>
                  ) : (
                    t("checks:detail.delete")
                  )}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>

      {/* Needs-re-seal warning (spec 2026-07-16-02): this check runs in a
          private location and its sealed credentials no longer match that
          location's active agents. Re-saving the credentials (Edit, above) is
          the only fix — the server cannot re-seal what it cannot read. */}
      <NeedsResealAlert needsReseal={check.needsReseal} />

      {/* Duty-cycle warning (spec 2026-07-01-04 D3): the check's execution
          cost eats >= 50% of a runner slot — nudge toward a longer period. */}
      {check.scheduling && check.scheduling.dutyCyclePct >= 50 && (
        <Alert variant="warning" data-testid="duty-cycle-warning">
          <AlertTriangle />
          <AlertTitle>
            {t("checks:detail.dutyCycle.title", {
              duty: check.scheduling.dutyCyclePct,
            })}
          </AlertTitle>
          <AlertDescription>
            {t("checks:detail.dutyCycle.description", {
              cost: formatDuration(check.scheduling.costEwmaMs),
              period: periodMs
                ? formatDuration(periodMs)
                : (check.period ?? ""),
              duty: check.scheduling.dutyCyclePct,
            })}
          </AlertDescription>
        </Alert>
      )}

      {/* Summary cards */}
      <CheckSummaryCards
        check={check}
        totalIncidents={incidents?.total ?? incidents?.data?.length ?? 0}
      />

      {/* Response time chart */}
      <ResponseTimeChart
        org={org}
        checkUid={checkUid}
        periodMs={periodMs}
        initialPeriod={graphPeriod}
        initialFullRange={graphFull}
        region={region}
        onRegionChange={setRegion}
        zoomFrom={graphFrom}
        zoomTo={graphTo}
        selectedUid={graphSelected}
        onSettingsChange={(period, full) =>
          navigate({
            to: ".",
            // Functional form so the region param (shared with Recent
            // Results/stats) survives chart period/range changes instead of
            // being wiped by this literal-object search.
            search: (prev) => {
              const nextPeriod = period !== "day" ? period : undefined;
              // Changing the time range invalidates the zoom window (and its
              // selected point), which belongs to the previous range's context.
              const periodChanged = nextPeriod !== prev.graphPeriod;
              return {
                ...prev,
                graphPeriod: nextPeriod,
                graphFull: full ? true : undefined,
                ...(periodChanged
                  ? {
                      graphFrom: undefined,
                      graphTo: undefined,
                      graphSelected: undefined,
                    }
                  : {}),
              };
            },
            replace: true,
          })
        }
        onZoomChange={(from, to) =>
          navigate({
            to: ".",
            search: (prev) => ({
              ...prev,
              graphFrom: from,
              graphTo: to,
              // Reset (from/to cleared) also clears the selected point (spec §4).
              ...(from == null || to == null
                ? { graphSelected: undefined }
                : {}),
            }),
            replace: true,
          })
        }
        onSelectChange={(uid) =>
          navigate({
            to: ".",
            search: (prev) => ({ ...prev, graphSelected: uid }),
            replace: true,
          })
        }
      />

      {/* Availability table */}
      <AvailabilityTable
        org={org}
        checkUid={checkUid}
        refetchInterval={refetchInterval}
      />

      <div className="grid gap-6 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>{t("checks:detail.configuration")}</CardTitle>
            <CardDescription>
              {t("checks:detail.configurationDescription")}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {check.description && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  {t("checks:detail.descriptionLabel")}
                </div>
                <div>{check.description}</div>
              </div>
            )}
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                {t("checks:detail.typeLabel")}
              </div>
              <div className="flex items-center gap-1.5">
                <CheckTypeIcon type={check.type} />
                <CheckTypeBadge type={check.type} />
              </div>
            </div>
            {check.period && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  {t("checks:detail.checkInterval")}
                </div>
                <div>{check.period}</div>
              </div>
            )}
            {check.regions && check.regions.length > 0 && (
              <div>
                <div className="text-sm font-medium text-muted-foreground mb-1">
                  {t("checks:detail.regionsLabel")}
                </div>
                <div className="flex gap-1 flex-wrap">
                  {check.regions.map((slug) => (
                    <Badge key={slug} variant="outline">
                      {regionDisplayLabel(regionsData?.regions, slug)}
                    </Badge>
                  ))}
                </div>
              </div>
            )}
            <TunnelVia org={org} check={check} />
            <TunnelDependents org={org} check={check} />
            <DeliveryVia org={org} check={check} />
            <DeliverySources org={org} check={check} />
            <div>
              <div className="text-sm font-medium text-muted-foreground">
                {t("checks:detail.statusLabel")}
              </div>
              <div className="flex items-center gap-2">
                <StatusBadge
                  status={
                    headerStatus || t("checks:detail.unknown").toLowerCase()
                  }
                />
                {check.enabled === false && (
                  <Badge variant="outline">{t("checks:detail.disabled")}</Badge>
                )}
              </div>
            </div>
            {flapSummary && (
              <div>
                <div className="text-sm font-medium text-muted-foreground">
                  {t("checks:detail.flapping")}
                </div>
                <div data-testid="check-flapping-summary" className="text-sm">
                  {flapSummary.multiplier !== undefined
                    ? t("checks:detail.flappingSummary", {
                        count: flapSummary.count,
                        window: flapSummary.window,
                        multiplier: flapSummary.multiplier,
                        effective: flapSummary.effective,
                      })
                    : t("checks:detail.flappingSummaryImmediate", {
                        count: flapSummary.count,
                        window: flapSummary.window,
                      })}
                </div>
              </div>
            )}
            {check.config && Object.keys(check.config).length > 0 && (
              <div>
                <div className="text-sm font-medium text-muted-foreground mb-2">
                  {t("checks:detail.configuration")}
                </div>
                <div className="bg-muted rounded-md p-3 text-sm font-mono">
                  {Object.entries(check.config).map(([key, value]) => (
                    <div key={key} className="flex gap-2">
                      <span className="text-muted-foreground">{key}:</span>
                      <span>
                        {typeof value === "string" ? (
                          value.startsWith("http") ? (
                            <a
                              href={value}
                              target="_blank"
                              rel="noopener noreferrer"
                              className="text-primary hover:underline inline-flex items-center gap-1"
                            >
                              {value}
                              <ExternalLink className="h-3 w-3" />
                            </a>
                          ) : (
                            value
                          )
                        ) : (
                          JSON.stringify(value)
                        )}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}
            {check.labels && Object.keys(check.labels).length > 0 && (
              <div>
                <div className="text-sm font-medium text-muted-foreground mb-2">
                  {t("checks:detail.labelsLabel")}
                </div>
                <div className="flex gap-1 flex-wrap">
                  {Object.entries(check.labels).map(([key, value]) => (
                    <Badge key={key} variant="outline">
                      {key}: {value}
                    </Badge>
                  ))}
                </div>
              </div>
            )}
            {check.type === "heartbeat" && (check.config?.token as string) && (
              <HeartbeatEndpoint org={org} check={check} />
            )}
            {check.type === "email" && (check.config?.token as string) && (
              <EmailEndpoint check={check} />
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t("checks:detail.lastResult")}</CardTitle>
            <CardDescription>
              {t("checks:detail.lastResultDescription")}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {check.lastResult ? (
              <>
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Clock className="h-4 w-4" />
                  {check.lastResult.timestamp
                    ? new Date(check.lastResult.timestamp).toLocaleString()
                    : t("checks:detail.unknown")}
                </div>
                {check.lastResult.metrics && (
                  <div>
                    <div className="text-sm font-medium text-muted-foreground mb-2">
                      {t("checks:detail.metricsLabel")}
                    </div>
                    <div className="bg-muted rounded-md p-3 text-sm font-mono">
                      {Object.entries(check.lastResult.metrics).map(
                        ([key, value]) => (
                          <div key={key} className="flex gap-2">
                            <span className="text-muted-foreground">
                              {key}:
                            </span>
                            <span>
                              {typeof value === "number"
                                ? Math.round(value * 100) / 100
                                : JSON.stringify(value)}
                            </span>
                          </div>
                        ),
                      )}
                    </div>
                  </div>
                )}
                {resolvedIpVersion && (
                  <div
                    className="flex flex-wrap items-center gap-2 text-sm"
                    data-testid="last-result-ip-version"
                  >
                    <span className="text-muted-foreground">
                      {t("checks:detail.ipVersionLabel")}:
                    </span>
                    <Badge variant="secondary">{resolvedIpVersion}</Badge>
                    <span className="text-xs text-muted-foreground">
                      {pinnedIpVersion
                        ? t("checks:detail.ipVersionPinned", {
                            version: pinnedIpVersion,
                          })
                        : t("checks:detail.ipVersionAuto")}
                    </span>
                  </div>
                )}
                {check.lastResult.output &&
                  Object.keys(check.lastResult.output).length > 0 && (
                    <div>
                      <div className="text-sm font-medium text-muted-foreground mb-2">
                        {t("checks:detail.outputLabel")}
                      </div>
                      <div className="bg-muted rounded-md p-3 text-sm font-mono max-h-32 overflow-auto">
                        {Object.entries(check.lastResult.output)
                          // The SSL chain + soonest-expiring details, the DNSBL
                          // zone/code fields and the resolved address family
                          // each get their own presentation; don't repeat them
                          // as raw JSON.
                          .filter(
                            ([key]) =>
                              key !== "chain" &&
                              key !== "soonestExpiring" &&
                              key !== IP_VERSION_OUTPUT_KEY &&
                              key !== JSON_ASSERTION_RESULT_OUTPUT_KEY &&
                              // Bookkeeping a passive evaluation row stamps on
                              // itself (spec 2026-09-02-04). "evaluation: true"
                              // and a bare uid are noise here; the badge on the
                              // Recent Results rows below is what conveys the
                              // row kind. message + lastSignalAt still show.
                              key !== EVALUATION_ROW_MARKER_KEY &&
                              key !== EVALUATION_LAST_SIGNAL_UID_KEY &&
                              !(
                                DNSBL_OUTPUT_KEYS as readonly string[]
                              ).includes(key),
                          )
                          .map(([key, value]) => (
                            <div key={key} className="flex gap-2">
                              <span className="text-muted-foreground">
                                {key}:
                              </span>
                              <span>
                                {typeof value === "string"
                                  ? value
                                  : JSON.stringify(value)}
                              </span>
                            </div>
                          ))}
                      </div>
                    </div>
                  )}
              </>
            ) : (
              <p className="text-muted-foreground">
                {t("checks:detail.noResults")}
              </p>
            )}
          </CardContent>
        </Card>
      </div>

      {check.type === "ssl" && (
        <SslChainCard
          output={
            check.lastResult?.output as Record<string, unknown> | undefined
          }
        />
      )}

      {check.type === "docker" && (
        <DockerRestartLoopCard
          output={
            check.lastResult?.output as Record<string, unknown> | undefined
          }
        />
      )}

      {check.type === "dnsbl" && (
        <DnsblCard
          output={
            check.lastResult?.output as Record<string, unknown> | undefined
          }
        />
      )}

      {check.type === "http" && (
        <JsonAssertionResultCard
          output={
            check.lastResult?.output as Record<string, unknown> | undefined
          }
        />
      )}

      <DependenciesCard org={org} checkUid={checkUid} />

      <Card>
        <CardHeader className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between space-y-0">
          <div>
            <CardTitle>{t("checks:detail.recentResults")}</CardTitle>
            <CardDescription>
              {t("checks:detail.recentResultsDescription")}
            </CardDescription>
          </div>
          {observedRegions.length > 1 && (
            <div
              className="flex flex-wrap items-center gap-1"
              data-testid="results-region-filter"
            >
              <Button
                variant={effectiveRegion === undefined ? "default" : "outline"}
                size="sm"
                onClick={() => setRegion(undefined)}
                className="px-2 text-xs"
              >
                {t("checks:detail.results.filterAll")}
              </Button>
              {observedRegions.map((slug) => (
                <Button
                  key={slug}
                  variant={effectiveRegion === slug ? "default" : "outline"}
                  size="sm"
                  onClick={() => setRegion(slug)}
                  className="px-2 text-xs"
                  data-testid={`results-region-chip-${slug}`}
                >
                  {regionDisplayLabel(regionsData?.regions, slug)}
                </Button>
              ))}
            </div>
          )}
        </CardHeader>
        <CardContent className="space-y-4">
          {effectiveRegion && durationStats && (
            <div
              className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-md border bg-muted/30 px-3 py-2 text-sm"
              data-testid="results-duration-stats"
            >
              <span className="text-xs text-muted-foreground">
                {t("checks:detail.results.stats.window", {
                  period: t(
                    `checks:detail.results.stats.windowPeriod.${graphTimeRange}`,
                  ),
                })}
              </span>
              <span>
                <span className="text-muted-foreground">
                  {t("checks:detail.results.stats.min")}:{" "}
                </span>
                <span className="font-medium">
                  {formatMs(durationStats.min)}
                </span>
              </span>
              <span>
                <span className="text-muted-foreground">
                  {t("checks:detail.results.stats.avg")}:{" "}
                </span>
                <span className="font-medium">
                  {durationStats.isEstimate ? "~" : ""}
                  {formatMs(durationStats.avg)}
                </span>
              </span>
              <span>
                <span className="text-muted-foreground">
                  {t("checks:detail.results.stats.max")}:{" "}
                </span>
                <span className="font-medium">
                  {formatMs(durationStats.max)}
                </span>
              </span>
              <span>
                <span className="text-muted-foreground">
                  {t("checks:detail.results.stats.p95")}:{" "}
                </span>
                <span className="font-medium">
                  {durationStats.isEstimate ? "~" : ""}
                  {formatMs(durationStats.p95)}
                </span>
              </span>
              <span>
                <span className="text-muted-foreground">
                  {t("checks:detail.results.stats.samples")}:{" "}
                </span>
                <span className="font-medium">{durationStats.count}</span>
              </span>
            </div>
          )}
          {results?.data && results.data.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("checks:detail.results.time")}</TableHead>
                  <TableHead>{t("checks:detail.results.status")}</TableHead>
                  <TableHead>{t("checks:detail.results.duration")}</TableHead>
                  <TableHead>{t("checks:detail.results.region")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {results.data.map((result) => {
                  // `output` is only requested for passive checks, so this is
                  // always false elsewhere — no other type can be badged by
                  // accident (spec 2026-09-02-04).
                  const isEvaluationRow =
                    isPassiveCheckType && isEvaluationOutput(result.output);

                  return (
                    <TableRow
                      key={result.uid}
                      className={
                        result.uid ? "cursor-pointer hover:bg-muted/50" : ""
                      }
                      data-testid={`result-row-${result.uid}`}
                      onClick={() => {
                        if (!result.uid) return;
                        navigate({
                          to: "/orgs/$org/checks/$checkUid/results/$resultUid",
                          params: { org, checkUid, resultUid: result.uid },
                          search: { region: effectiveRegion },
                        });
                      }}
                    >
                      <TableCell
                        className={cn(
                          "text-sm",
                          isEvaluationRow && "text-muted-foreground",
                        )}
                      >
                        {result.periodStart
                          ? formatResultTime(result.periodStart)
                          : "-"}
                      </TableCell>
                      <TableCell>
                        {/* Wraps rather than truncates on a narrow screen: the
                            badge sits under the status badge instead of forcing
                            a horizontal scroll. */}
                        <div className="flex flex-wrap items-center gap-1">
                          <StatusBadge status={result.status} />
                          {isEvaluationRow && (
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Badge
                                  variant="outline"
                                  className="text-muted-foreground"
                                  data-testid={`result-evaluation-badge-${result.uid}`}
                                >
                                  {t("checks:detail.results.evaluationBadge")}
                                </Badge>
                              </TooltipTrigger>
                              <TooltipContent>
                                {t("checks:detail.results.evaluationTooltip")}
                              </TooltipContent>
                            </Tooltip>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="text-sm">
                        {result.durationMs !== undefined
                          ? `${Math.round(result.durationMs)}ms`
                          : "-"}
                      </TableCell>
                      <TableCell
                        className="text-sm"
                        data-testid="result-region-cell"
                      >
                        {result.region
                          ? (() => {
                              const slug = result.region;
                              return (
                                // A real <button> styled with badgeVariants (not
                                // <Badge>, which renders a plain <div> with no
                                // asChild/Slot support) — keeps the Badge look
                                // while being a genuine interactive element, with
                                // a hover/focus affordance signaling it's clickable.
                                <button
                                  type="button"
                                  className={cn(
                                    badgeVariants({ variant: "outline" }),
                                    "cursor-pointer transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                                  )}
                                  data-testid={`result-region-badge-${result.uid}`}
                                  onClick={(e) => {
                                    e.stopPropagation();
                                    setRegion(slug);
                                  }}
                                >
                                  {regionDisplayLabel(regionsData?.regions, slug)}
                                </button>
                              );
                            })()
                          : "-"}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          ) : (
            <p className="text-center py-6 text-muted-foreground">
              {t("checks:detail.noResultsAvailable")}
            </p>
          )}
        </CardContent>
      </Card>

      {incidents?.data && incidents.data.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>{t("checks:detail.recentIncidents")}</CardTitle>
            <CardDescription>
              {t("checks:detail.recentIncidentsDescription")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("checks:detail.incidents.started")}</TableHead>
                  <TableHead>{t("checks:detail.incidents.state")}</TableHead>
                  <TableHead>{t("checks:detail.incidents.duration")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {incidents.data.map((incident) => (
                  <TableRow
                    key={incident.uid}
                    className={
                      incident.uid ? "cursor-pointer hover:bg-muted/50" : ""
                    }
                    data-testid={`incident-row-${incident.uid}`}
                    onClick={() => {
                      if (!incident.uid) return;
                      navigate({
                        to: "/orgs/$org/incidents/$incidentUid",
                        params: { org, incidentUid: incident.uid },
                      });
                    }}
                  >
                    <TableCell className="text-sm">
                      {incident.startedAt
                        ? formatResultTime(incident.startedAt)
                        : "-"}
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-1">
                        <Badge
                          variant={
                            incident.state === "active"
                              ? "destructive"
                              : "secondary"
                          }
                        >
                          {incident.state}
                        </Badge>
                        {incident.pagingSuppressed && (
                          <Badge variant="outline" className="text-xs">
                            {t("incidents:rollup.rolledUpBadge")}
                          </Badge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="text-sm">
                      <IncidentDuration incident={incident} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
