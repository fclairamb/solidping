import { useMemo } from "react";
import {
  useQueries,
  useQuery,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { ApiError, apiFetch, getToken } from "./client";

/**
 * Opt-in knobs shared by the plain list hooks. A caller that renders a list
 * page wants the defaults; a caller that only needs the list to *derive* a
 * boolean (the getting-started checklist) wants to switch the request off
 * entirely and to hold the answer for a long time.
 */
export interface ListQueryOptions {
  enabled?: boolean;
  staleTime?: number;
}
import { mergeResultTiers } from "@/lib/result-tiers";
import {
  chartFetchParamsForWindow,
  chartRollupTier,
  chartWindowBounds,
  seamStartFrom,
  type TimeRange,
  type ZoomWindow,
} from "@/lib/chart-window";
import {
  stretchWhileLive,
  useLiveSubscription,
  useScopeLive,
} from "@/contexts/LiveEventsContext";

// Types
export interface CheckGroup {
  uid: string;
  name: string;
  slug: string;
  description?: string;
  sortOrder: number;
  checkCount: number;
  /**
   * Derived, read-time rollup of the group's enabled member checks (see
   * openapi.yaml CheckGroup schema for the exact precedence rule). Never
   * stored — recomputed on every read, so it's always current regardless of
   * which page of members has loaded client-side.
   */
  status: string;
  /** Count of enabled member checks per wire status, zero counts omitted. */
  memberStatusCounts?: Record<string, number>;
  /** Group-level escalation policy member checks inherit; null = none. */
  escalationPolicyUid?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface CreateCheckGroupRequest {
  name: string;
  slug?: string;
  description?: string;
  sortOrder?: number;
  escalationPolicyUid?: string;
}

export interface UpdateCheckGroupRequest {
  name?: string;
  slug?: string;
  description?: string;
  sortOrder?: number;
  /** A UID sets it, "" clears it, omit leaves unchanged. */
  escalationPolicyUid?: string;
}

export interface Check {
  uid: string;
  name?: string;
  slug?: string;
  description?: string;
  checkGroupUid?: string;
  /**
   * Escalation policy assigned directly to this check. When null/absent the
   * check inherits its group's policy, then the org default, then none.
   */
  /** Per-check path-trace policy: `inherit` (the org default), `on` or `off`.
   * Always present on read; a check that never set it reads `inherit`. */
  tracerouteOnFailure?: string;
  escalationPolicyUid?: string | null;
  type?:
    | "http"
    | "tcp"
    | "icmp"
    | "dns"
    | "ssl"
    | "heartbeat"
    | "email"
    | "domain"
    | "smtp"
    | "udp"
    | "ssh"
    | "pop3"
    | "imap"
    | "websocket"
    | "postgresql"
    | "mysql"
    | "redis"
    | "mongodb"
    | "ftp"
    | "sftp"
    | "js"
    | "mssql"
    | "oracle"
    | "clickhouse"
    | "grpc"
    | "kafka"
    | "mqtt"
    | "a2s"
    | "minecraft"
    | "rabbitmq"
    | "snmp"
    | "docker"
    | "browser"
    | "freebox_line"
    | "dnsbl"
    | "sip"
    | "ntp"
    | "rdp"
    | "prometheus"
    | "sleep";
  config?: Record<string, unknown>;
  /**
   * Derived, read-time-only host this check probes: config's `host` when
   * present, else the hostname parsed from `url`, else `target`; absent/null
   * when none apply (e.g. heartbeat/email). Never stored — renaming a host in
   * a check's config changes this on the next read. Drives the checks index's
   * "Group by: Host" view (spec 2026-08-01-04).
   */
  targetHost?: string | null;
  configPrivateKeys?: string[];
  /**
   * Detail responses only. True when this check targets a private location and
   * its sealed credentials no longer match that location's active agent set
   * (an agent enrolled or was revoked since the credentials were saved), so
   * agents cannot decrypt them. Fix: re-save the check's credentials.
   * Undefined when the check targets no private location.
   */
  needsReseal?: boolean;
  regions?: string[];
  /**
   * Optional inter-region scheduling offset override ("spread"), as
   * "HH:MM:SS". Present only when a non-default value is set — absent means
   * the check uses the automatic default (period / region count). See spec
   * 2026-07-20-05 (backend) and 2026-07-21-01 (this UI control).
   */
  regionSpread?: string;
  labels?: Record<string, string>;
  enabled?: boolean;
  internal?: boolean;
  period?: string;
  createdAt?: string;
  updatedAt?: string;
  status?: "up" | "down" | "validating" | "created" | "degraded" | "unknown";
  lastResult?: {
    uid?: string;
    status?: "up" | "down" | "error" | "timeout" | "created" | "abandoned";
    timestamp?: string;
    durationMs?: number;
    metrics?: Record<string, unknown>;
    output?: Record<string, unknown>;
  };
  lastStatusChange?: {
    status?: string;
    time?: string;
  };
  reopenCooldownMultiplier?: number | null;
  flappingWindowSeconds?: number;
  flapBackoffFactor?: number;
  maxRecoveryMultiplier?: number;
  /**
   * Live adaptive-recovery (flapping) state — the effective, lazy-reset-aware
   * counterpart of the raw flapping config above. Absent when the feature is
   * off for this check, or nothing has accumulated (no outage yet, or the
   * rolling window has since lapsed). See spec 2026-08-24-05.
   */
  flapState?: {
    /** Outages counted inside the current rolling window. 1 = 2nd outage
     * within the window, the first that actually counts as a flap. */
    flapCount: number;
    lastOutageAt?: string;
    /** Stability currently required before an open incident auto-resolves. */
    effectiveRecoveryPeriodSeconds: number;
  } | null;
  confirmationPeriodSeconds?: number;
  recoveryPeriodSeconds?: number;
  /**
   * Read-only scheduling telemetry (max across the check's per-region
   * scheduler jobs). Detail responses only — never present on lists — and
   * omitted until the check's first run produces a cost signal.
   */
  scheduling?: {
    /** Smoothed execution cost in milliseconds (EWMA). */
    costEwmaMs: number;
    /** Smoothed start lateness in milliseconds (EWMA, telemetry). */
    delayEwmaMs: number;
    /** round(100 × cost / period): share of a runner slot this check occupies. */
    dutyCyclePct: number;
  };
}

export interface RegionDefinition {
  slug: string;
  emoji: string;
  name: string;
  /** Org-private region served by the customer's own deported agents. */
  private?: boolean;
  /** What the region's LIVE workers report they can do — today only `ipv6`,
   * three-state ("yes" / "no" / "unknown"). Omitted entirely by older servers;
   * an absent map means "unknown", never "no" (spec 2026-08-15-11). */
  capabilities?: Record<string, string>;
}

export interface CreateCheckRequest {
  /** Per-check path-trace policy: `inherit`, `on` or `off` (spec
   * 2026-08-21-10). `inherit` is what puts a check back under the org default;
   * omitting the field leaves it unchanged. */
  tracerouteOnFailure?: string;
  name?: string;
  slug?: string;
  description?: string;
  checkGroupUid?: string;
  /** Escalation policy to assign; omit/empty inherits (group → org default → none). */
  escalationPolicyUid?: string;
  type?:
    | "http"
    | "tcp"
    | "icmp"
    | "dns"
    | "ssl"
    | "heartbeat"
    | "email"
    | "domain"
    | "smtp"
    | "udp"
    | "ssh"
    | "pop3"
    | "imap"
    | "websocket"
    | "postgresql"
    | "mysql"
    | "redis"
    | "mongodb"
    | "ftp"
    | "sftp"
    | "js"
    | "mssql"
    | "oracle"
    | "clickhouse"
    | "grpc"
    | "kafka"
    | "mqtt"
    | "a2s"
    | "minecraft"
    | "rabbitmq"
    | "snmp"
    | "docker"
    | "browser"
    | "freebox_line"
    | "dnsbl"
    | "sip"
    | "ntp"
    | "rdp"
    | "prometheus"
    | "sleep";
  config: Record<string, unknown>;
  regions?: string[];
  /** Omit to use the automatic default (period / region count). */
  regionSpread?: string;
  labels?: Record<string, string>;
  enabled?: boolean;
  /* No `internal`: it is read-only server-side (spec 2026-08-27-01). A create
   * carrying it is refused with a VALIDATION_ERROR naming the field, so a
   * writable type here would only let a future edit compile its way into a
   * 422. It stays on `Check` (the response) and on the list filter. */
  period?: string;
}

export interface UpdateCheckRequest {
  /** Per-check path-trace policy: `inherit`, `on` or `off` (spec
   * 2026-08-21-10). `inherit` is what puts a check back under the org default;
   * omitting the field leaves it unchanged. */
  tracerouteOnFailure?: string;
  name?: string;
  slug?: string;
  description?: string;
  checkGroupUid?: string | null;
  /** A UID assigns it, "" clears it (inherit), omit leaves unchanged. */
  escalationPolicyUid?: string;
  config?: Record<string, unknown>;
  regions?: string[];
  /** A duration string sets it, "" clears it back to automatic, omit leaves unchanged. */
  regionSpread?: string;
  labels?: Record<string, string>;
  enabled?: boolean;
  /* No `internal` — read-only, same as on create (spec 2026-08-27-01). */
  period?: string;
  reopenCooldownMultiplier?: number | null;
  flappingWindowSeconds?: number | null;
  flapBackoffFactor?: number | null;
  maxRecoveryMultiplier?: number | null;
  confirmationPeriodSeconds?: number;
  recoveryPeriodSeconds?: number;
}

export interface OrgResult {
  uid?: string;
  checkUid?: string;
  checkName?: string;
  checkSlug?: string;
  status?: "up" | "down" | "unknown" | "created" | "running";
  durationMs?: number;
  durationMinMs?: number;
  durationMaxMs?: number;
  durationAvgMs?: number;
  durationP95Ms?: number;
  availabilityPct?: number;
  totalChecks?: number;
  successfulChecks?: number;
  periodStart?: string;
  periodEnd?: string;
  periodType?: string;
  region?: string;
  metrics?: Record<string, unknown>;
  output?: Record<string, unknown>;
}

export interface ResultFallbackInfo {
  requestedUid: string;
  requestedAt: string;
  reason: "rolled_up_to_hour" | "rolled_up_to_day" | "rolled_up_to_month";
}

export interface OrgResultDetail extends OrgResult {
  fallback?: ResultFallbackInfo;
  /** Next-older result in the same check + periodType series; absent at the oldest boundary. */
  previousUid?: string;
  /** Next-newer result in the same series; absent at the newest boundary. */
  nextUid?: string;
}

/** Snapshot of one failing result captured on incident.details, either as
 * `first_result` (incident open) or `last_failure` (most recent relapse).
 * Copied at write time — retention may since have deleted the raw result. */
export interface IncidentResultSnapshot {
  resultUid?: string;
  status?: string;
  region?: string;
  duration?: number;
  periodStart?: string;
  output?: Record<string, unknown>;
}

/** What the probe received from a failing HTTP target at this incident's
 * current onset. Present only for checks with `capture_failure_response`
 * enabled that failed AFTER a response existed (a timeout / DNS / TLS failure
 * never produces one). Operator-facing evidence — it is never serialized onto
 * a status page or a subscriber payload. */
export interface IncidentFailureResponse {
  url?: string;
  statusLine?: string;
  statusCode?: number;
  /** Response headers; sensitive values are already `[redacted]` server-side.
   * Request headers are never captured. */
  headers?: Record<string, string>;
  /** Absent when `binary` is true — a non-text body is reduced to metadata. */
  body?: string;
  /** True when `body` holds only the leading bytes; compare with contentLength. */
  truncated?: boolean;
  contentLength?: number;
  contentType?: string;
  bodyBytes?: number;
  bodySha256?: string;
  /** True when the body was not text-like or not valid UTF-8. */
  binary?: boolean;
  capturedAt?: string;
  remoteAddr?: string;
  region?: string;
}

/** One evidence blob attached to an incident (spec 2026-08-21-01).
 *
 * Operator-only: `downloadUrl` is a short-lived SIGNED url and is re-signed on
 * every incident fetch, so it must never be cached or shared onward. */
export interface IncidentAttachment {
  uid: string;
  /** Attachment kind, e.g. `screenshot`. */
  kind?: string;
  name?: string;
  mimeType?: string;
  size?: number;
  /** Relative signed URL: `/pub/files/<uid>?exp=…&sig=…`. */
  downloadUrl: string;
  createdAt?: string;
  /** When the probe took the capture — a moment AFTER failure detection. */
  capturedAt?: string;
  region?: string;
  checkUid?: string;
  trigger?: "incident-open" | "incident-reopen" | "agent-upload";
}

/** One hop in an MTR-style path capture (spec 2026-08-21-10).
 *
 * `address` empty means nothing answered at this TTL — UNLESS the capture's
 * `hopAddressesVisible` is false, in which case the probe mode could not have
 * heard a router even if one had answered. The two look identical here and
 * mean completely different things, so never render a blank address without
 * consulting that flag. */
export interface TracerouteHop {
  ttl: number;
  address?: string;
  /** Every distinct router seen at this TTL, when a load-balanced path
   * answered from more than one. Absent for the ordinary single-router case. */
  addresses?: string[];
  /** Reverse-DNS name of `address`, when one resolved inside the budget. */
  hostname?: string;
  sent: number;
  received: number;
  /** 0-100, two decimals. */
  lossPct: number;
  rttMinMs?: number;
  rttAvgMs?: number;
  rttMaxMs?: number;
  /** The target itself answered here. */
  final?: boolean;
  /** A router answered destination-unreachable rather than TTL-exceeded. */
  unreachable?: boolean;
}

/** The JSON body of a `traceroute` incident attachment.
 *
 * Fetched from the attachment's own signed `downloadUrl` rather than inlined
 * into the incident payload: it is a few kilobytes that only matter on the one
 * page that renders it. */
export interface TracerouteCapture {
  version: number;
  /** `icmp` (privileged raw), `icmp-udp` (unprivileged datagram) or `tcp`. */
  mode: "icmp" | "icmp-udp" | "tcp";
  /** False for `tcp`, which cannot observe intermediate hop addresses at all. */
  hopAddressesVisible: boolean;
  host?: string;
  address: string;
  family: string;
  port?: number;
  region?: string;
  trigger?: string;
  rounds: number;
  maxHops: number;
  /** When the sweep STARTED — always after the failing result was reported. */
  startedAt: string;
  durationMs: number;
  /** The target answered, so the path is whole. */
  complete: boolean;
  /** The budget ran out before the sweep finished. Still evidence. */
  truncated?: boolean;
  hops: TracerouteHop[];
}

/** Fetches a traceroute attachment's JSON from its signed relative URL.
 *
 * The URL is same-origin and self-authenticating (exp+sig), so this is a plain
 * fetch rather than an `apiFetch` — there is no bearer token to attach, and
 * attaching one would be pointless. It is re-signed on every incident fetch, so
 * the query key includes it and a refetched incident naturally refetches this. */
export function useTracerouteCapture(url: string | undefined) {
  return useQuery({
    queryKey: ["traceroute-capture", url],
    queryFn: async (): Promise<TracerouteCapture> => {
      const response = await fetch(url as string);
      if (!response.ok) {
        throw new Error(`traceroute capture: HTTP ${response.status}`);
      }
      return (await response.json()) as TracerouteCapture;
    },
    enabled: !!url,
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
}

export interface IncidentDetails {
  /** Human-readable cause, same key the Slack notifier reads. */
  failure_reason?: string;
  /** The result that opened the incident. */
  first_result?: IncidentResultSnapshot;
  /** The result from the most recent reopen, if the incident has relapsed. */
  last_failure?: IncidentResultSnapshot;
  /** Opt-in capture of the failing response at the current onset. */
  failureResponse?: IncidentFailureResponse;
}

/** Display identity of whoever performed an incident action. */
export interface IncidentActor {
  /** Human label. Never empty — the API falls back to a neutral word. */
  name: string;
  /** Channel the action came from ("web", "slack", "discord", …). */
  via?: string;
  /** SolidPing user, when the actor maps to one. */
  userUid?: string;
}

export interface IncidentDetail {
  /** Evidence blobs. Populated by the DETAIL endpoint only. */
  attachments?: IncidentAttachment[];
  uid?: string;
  /** Short per-org reference, rendered as `#42`. Assigned at creation, never reused. */
  number?: number;
  checkUid?: string;
  checkName?: string;
  checkSlug?: string;
  check?: {
    slug?: string;
    type?: string;
    config?: Record<string, unknown>;
  };
  state?: "active" | "resolved";
  title?: string;
  description?: string;
  startedAt?: string;
  acknowledgedAt?: string;
  /**
   * UID of the SolidPing user credited with the acknowledgment — NULL for
   * every Slack / Discord / phone ack, whose actor has no platform account.
   * Render `acknowledgedByActor` instead.
   */
  acknowledgedBy?: string;
  /**
   * Resolved, display-ready identity of the acker. Returned by the DETAIL
   * endpoint and by `POST .../ack` only; the list endpoint omits it.
   */
  acknowledgedByActor?: IncidentActor;
  snoozedUntil?: string;
  snoozedBy?: string;
  snoozeReason?: string;
  escalatedAt?: string;
  resolvedAt?: string;
  resolvedBy?: string;
  resolutionType?: "auto" | "manual" | "expired";
  failureCount?: number;
  relapseCount?: number;
  /**
   * The check's flap count at the moment this incident opened or last
   * reopened — a point-in-time snapshot, not a live value. 0/absent means it
   * opened at the base level, not escalated by adaptive-recovery flapping.
   * See spec 2026-08-24-05.
   */
  flapLevel?: number;
  lastReopenedAt?: string;
  causedByIncidentUid?: string;
  pagingSuppressed?: boolean;
  details?: IncidentDetails;
}

export interface Event {
  uid?: string;
  eventType?: string;
  actorType?: "system" | "user" | "api_token" | "service";
  actorUid?: string;
  /** Resolved by the API for the returned page; absent for system events. */
  actorName?: string;
  actorEmail?: string;
  /**
   * Request provenance (spec 2026-08-21-09). The API returns these to org
   * admins/owners ONLY, and never at all when `audit.capture_ip` is off — so
   * absent means "not visible to you or not recorded", never "0.0.0.0".
   */
  sourceIp?: string;
  userAgent?: string;
  checkUid?: string;
  incidentUid?: string;
  payload?: Record<string, unknown>;
  createdAt?: string;
}

interface CursorPagination {
  cursor?: string;
  size?: number;
  total?: number;
}

interface ChecksListResponse {
  data?: Check[];
  pagination?: {
    total?: number;
    cursor?: string;
    limit?: number;
  };
}

function buildChecksUrl(
  org: string,
  options?: {
    labels?: string;
    with?: string;
    q?: string;
    type?: string;
    checkGroupUid?: string;
    internal?: string;
    status?: string;
    limit?: number;
    cursor?: string;
    /** Opt-in ordering. "group" = group sortOrder asc, ungrouped last, then
     * created_at DESC within a bucket. Omitted = default created_at DESC. */
    sort?: string;
  },
): string {
  const params = new URLSearchParams();
  if (options?.labels) params.set("labels", options.labels);
  if (options?.with) params.set("with", options.with);
  if (options?.q) params.set("q", options.q);
  if (options?.type) params.set("type", options.type);
  if (options?.checkGroupUid)
    params.set("checkGroupUid", options.checkGroupUid);
  if (options?.internal) params.set("internal", options.internal);
  if (options?.status) params.set("status", options.status);
  if (options?.limit) params.set("limit", options.limit.toString());
  if (options?.cursor) params.set("cursor", options.cursor);
  if (options?.sort) params.set("sort", options.sort);
  const query = params.toString();
  return `/api/v1/orgs/${org}/checks${query ? `?${query}` : ""}`;
}

// Checks hooks
export function useChecks(
  org: string,
  options?: {
    labels?: string;
    with?: string;
    q?: string;
    /** Comma-separated check types, e.g. "ssh" or "http,tcp". */
    type?: string;
    checkGroupUid?: string;
    limit?: number;
  },
) {
  return useQuery({
    queryKey: ["checks", org, options],
    queryFn: async () => {
      const path = buildChecksUrl(org, options);
      const response = await apiFetch<ChecksListResponse>(path);
      return response.data || [];
    },
    enabled: !!org,
  });
}

/**
 * Org-wide aggregate check counters from GET /orgs/:org/checks/stats.
 *
 * Every field is computed server-side with a single SQL aggregation, so —
 * unlike counting a page of `useChecks` — it stays correct past the list
 * endpoint's 100-row page clamp (GitHub issue #172). Scope is non-deleted,
 * non-internal checks, i.e. exactly what the checks list shows by default.
 *
 * `total`, `byStatus`, `down` and `hardDown` span enabled AND disabled checks;
 * `enabled`/`disabled` partition the same set.
 */
export interface CheckStats {
  total: number;
  enabled: number;
  disabled: number;
  /** Every known status key is always present (0 when empty). */
  byStatus: Record<string, number>;
  /** status in (down, error, timeout) — the "currently down" KPI. */
  down: number;
  /** status in (down, error) — down excluding timeouts. */
  hardDown: number;
  /**
   * 100 * successful / total over the trailing 24h window, combining `hour`
   * rollup rows and `raw` result rows server-side. `null` when the window
   * has no countable data (an empty or brand-new org) — render that as "no
   * data", never as a fabricated 100.
   */
  availability24h: number | null;
}

/**
 * Fetches the org's aggregate check counters. The endpoint is cached
 * server-side for ~1 minute, so polling it faster than that is wasted work —
 * callers should pass a refetchInterval no shorter than the other dashboard
 * queries use.
 */
export function useCheckStats(
  org: string,
  options?: { refetchInterval?: number },
) {
  return useQuery({
    queryKey: ["check-stats", org],
    queryFn: async () =>
      apiFetch<CheckStats>(`/api/v1/orgs/${org}/checks/stats`),
    enabled: !!org,
    refetchInterval: options?.refetchInterval,
  });
}

export function useInfiniteChecks(
  org: string,
  options?: {
    labels?: string;
    with?: string;
    q?: string;
    /** Comma-separated check types, e.g. "ssh" or "http,tcp". */
    type?: string;
    checkGroupUid?: string;
    internal?: string;
    status?: string;
    limit?: number;
    /** Opt-in ordering; "group" loads in the page's display order. */
    sort?: string;
  },
  /**
   * Query behavior that must NOT take part in the cache key (a poll interval
   * describes how often to refresh a cache entry, not which entry it is).
   * Kept as a second parameter so it can never accidentally fork the key.
   */
  queryOptions?: { refetchInterval?: number },
) {
  return useInfiniteQuery({
    queryKey: ["checks", "infinite", org, options],
    queryFn: async ({ pageParam }: { pageParam?: string }) => {
      const path = buildChecksUrl(org, {
        ...options,
        cursor: pageParam,
      });
      return apiFetch<ChecksListResponse>(path);
    },
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.pagination?.cursor,
    enabled: !!org,
    refetchInterval: queryOptions?.refetchInterval,
  });
}

export function useCheck(
  org: string,
  uid: string,
  options?: {
    refetchInterval?: number;
    /**
     * Pass "always" when the consumer seeds local state from the response
     * once (e.g. an edit form): combined with `isFetchedAfterMount`, it
     * guarantees the seed comes from fresh data, not a stale cache entry.
     */
    refetchOnMount?: boolean | "always";
  },
) {
  return useQuery({
    // One canonical cache entry per check, keyed by org+uid only — every
    // consumer (breadcrumb, detail page, edit form, badge picker) shares it,
    // so a live invalidation produces exactly one HTTP request. The `with`
    // embed is always requested so the superset payload (name + lastResult +
    // lastStatusChange) satisfies every consumer; extra embeds are ignored by
    // those that only need `name`.
    queryKey: ["check", org, uid],
    queryFn: async () =>
      apiFetch<Check>(
        `/api/v1/orgs/${org}/checks/${uid}?with=last_result,last_status_change`,
      ),
    enabled: !!org && !!uid,
    refetchInterval: options?.refetchInterval,
    refetchOnMount: options?.refetchOnMount,
  });
}

/**
 * Fetches several individual checks in parallel via `useQueries`, one query
 * per uid, using the SAME `["check", org, uid]` key and fetch function as
 * `useCheck` — so a check already resolved elsewhere (list, detail page,
 * `useCheck` itself) is a cache hit rather than a duplicate request, and vice
 * versa. Built for `CheckMultiPicker`, which needs to resolve chip labels for
 * selected uids that fall outside the current search page's result set.
 *
 * Returns one `UseQueryResult<Check>` per input uid, same order as `uids`.
 */
export function useChecksByUids(org: string, uids: string[]) {
  return useQueries({
    queries: uids.map((uid) => ({
      queryKey: ["check", org, uid],
      queryFn: async () =>
        apiFetch<Check>(
          `/api/v1/orgs/${org}/checks/${uid}?with=last_result,last_status_change`,
        ),
      enabled: !!org && !!uid,
    })),
  });
}

export function useCreateCheck(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateCheckRequest) =>
      apiFetch<Check>(`/api/v1/orgs/${org}/checks`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
      queryClient.invalidateQueries({ queryKey: ["checks", "infinite", org] });
      // A new check changes the org's total — the empty-org dashboard hero
      // gate reads this query, so it must not wait out its own poll/TTL
      // window to notice (spec 2026-09-01-01).
      queryClient.invalidateQueries({ queryKey: ["check-stats", org] });
    },
  });
}

export function useUpdateCheck(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateCheckRequest) =>
      apiFetch<Check>(`/api/v1/orgs/${org}/checks/${uid}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
      queryClient.invalidateQueries({ queryKey: ["checks", "infinite", org] });
      queryClient.invalidateQueries({ queryKey: ["check", org, uid] });
    },
  });
}

/** One check's pending schedule change, as the scheduling page batches them. */
export interface CheckScheduleChange {
  uid: string;
  /** Human name, so a partial failure can name what did not save. */
  name: string;
  /** "HH:MM:SS"; omitted when only `enabled` changed. */
  period?: string;
  /** Omitted when only the period changed. */
  enabled?: boolean;
}

export interface CheckScheduleResult {
  applied: number;
  /** One entry per check whose PATCH failed. Empty on full success. */
  failures: { uid: string; name: string; message: string }[];
}

/**
 * Applies a batch of period/enabled changes, one PATCH per check.
 *
 * Sequential and fault-tolerant on purpose. There is no bulk endpoint, and the
 * honest failure mode for "20 checks, 3 rejected" is to report which 3 — not
 * to abort at the first error leaving the org half-rebalanced with no idea
 * where it stopped. Callers refetch afterwards, so the surviving rows re-read
 * their real server state either way.
 */
export function useApplyCheckSchedule(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (
      changes: CheckScheduleChange[],
    ): Promise<CheckScheduleResult> => {
      const failures: CheckScheduleResult["failures"] = [];
      let applied = 0;

      for (const change of changes) {
        const body: UpdateCheckRequest = {};
        if (change.period !== undefined) body.period = change.period;
        if (change.enabled !== undefined) body.enabled = change.enabled;

        try {
          await apiFetch<Check>(`/api/v1/orgs/${org}/checks/${change.uid}`, {
            method: "PATCH",
            body: JSON.stringify(body),
          });
          applied += 1;
        } catch (err) {
          failures.push({
            uid: change.uid,
            name: change.name,
            message: err instanceof Error ? err.message : String(err),
          });
        }
      }

      return { applied, failures };
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
      queryClient.invalidateQueries({ queryKey: ["checks", "infinite", org] });
      queryClient.invalidateQueries({ queryKey: ["entitlements", org] });
    },
  });
}

export interface LabelSuggestion {
  value: string;
  count: number;
}

export function useLabelSuggestions(
  org: string,
  opts: { key?: string; q?: string; limit?: number; enabled?: boolean },
) {
  const params = new URLSearchParams();
  if (opts.key) params.set("key", opts.key);
  if (opts.q) params.set("q", opts.q);
  if (opts.limit) params.set("limit", String(opts.limit));
  const query = params.toString();

  return useQuery({
    queryKey: ["labels", org, opts.key ?? "", opts.q ?? "", opts.limit ?? 50],
    queryFn: async () => {
      const path = `/api/v1/orgs/${org}/labels${query ? `?${query}` : ""}`;
      const response = await apiFetch<{ data: LabelSuggestion[] }>(path);
      return response.data ?? [];
    },
    enabled: (opts.enabled ?? true) && !!org,
    staleTime: 30_000,
  });
}

export function useCloneCheck(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (sourceUid: string) =>
      apiFetch<Check>(`/api/v1/orgs/${org}/checks/${sourceUid}/clone`, {
        method: "POST",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
      queryClient.invalidateQueries({ queryKey: ["checks", "infinite", org] });
    },
  });
}

/** Regenerates a heartbeat check's ping token (heartbeat checks only — the
 *  backend 400s otherwise). Invalidates every previously issued ping URL
 *  immediately, unlike webhook signing-secret rotation which keeps a grace
 *  window: heartbeat pings are frequent and the operator is expected to
 *  update the sender right away. Returns the updated check. */
export function useRotateHeartbeatToken(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () =>
      apiFetch<Check>(`/api/v1/orgs/${org}/checks/${uid}/rotate-token`, {
        method: "POST",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["check", org, uid] });
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
      queryClient.invalidateQueries({ queryKey: ["checks", "infinite", org] });
    },
  });
}

export function useDeleteCheck(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/checks/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
      queryClient.invalidateQueries({ queryKey: ["checks", "infinite", org] });
      // Symmetric with useCreateCheck: deleting the org's last check must
      // flip the dashboard back to the empty-org hero without waiting out
      // the stats query's own poll/TTL window (spec 2026-09-01-01).
      queryClient.invalidateQueries({ queryKey: ["check-stats", org] });
    },
  });
}

// Check Export/Import types
export interface ExportDocument {
  version: number;
  exportedAt: string;
  organization: string;
  checks: ExportCheck[];
}

export interface ExportCheck {
  name: string;
  slug: string;
  description?: string;
  type: string;
  config: Record<string, unknown>;
  regions?: string[];
  labels?: Record<string, string>;
  enabled: boolean;
  period?: string;
  group?: string;
  confirmationPeriodSeconds?: number;
  escalationThreshold?: number;
  recoveryPeriodSeconds?: number;
  reopenCooldownMultiplier?: number | null;
  flappingWindowSeconds?: number | null;
  flapBackoffFactor?: number | null;
  maxRecoveryMultiplier?: number | null;
}

export interface ImportResult {
  created: number;
  updated: number;
  skipped: number;
  errors: { index: number; slug: string; error: string }[];
}

/** Third-party import sources the convert endpoint accepts. */
export const CONVERT_SOURCES = [
  "gatus",
  "betterstack",
  "uptime-kuma",
  "uptimerobot",
] as const;
export type ConvertSource = (typeof CONVERT_SOURCES)[number];

/** One source item (or field) that could not be mapped faithfully. */
export interface ConversionWarning {
  item?: string;
  field?: string;
  message: string;
}

/** Response of POST /checks/import/convert: the apply shape + conversion info. */
export interface ConvertResult {
  source: string;
  converted: number;
  manifest: string;
  dryRun: boolean;
  created: number;
  updated: number;
  unmanaged: number;
  plan: { slug: string; action: string; reason?: string }[];
  errors: { index: number; slug: string; error: string }[];
  warnings: ConversionWarning[];
}

/**
 * Picks the Content-Type for an export document the user pasted or uploaded.
 * The server sniffs the first non-space byte too, but sending an accurate
 * header keeps a JSON body from being handed to the YAML branch (and vice
 * versa) — `application/json` forces the JSON parser server-side.
 */
function documentContentType(body: string): string {
  const trimmed = body.trimStart();
  return trimmed.startsWith("{") || trimmed.startsWith("[")
    ? "application/json"
    : "application/yaml";
}

// Check Export/Import hooks

/**
 * Converts a third-party monitoring configuration and applies it. The body is
 * the raw source payload (Gatus YAML / Uptime Kuma backup JSON) or, for Better
 * Stack, `{"token": "..."}` — the token is sent once and never stored client-
 * or server-side.
 */
export function useConvertChecks(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (params: {
      source: ConvertSource;
      body: string;
      dryRun?: boolean;
    }) =>
      apiFetch<ConvertResult>(
        `/api/v1/orgs/${org}/checks/import/convert?source=${encodeURIComponent(params.source)}${
          params.dryRun ? "&dryRun=true" : ""
        }`,
        {
          method: "POST",
          body: params.body,
        },
      ),
    onSuccess: (_, params) => {
      if (!params.dryRun) {
        queryClient.invalidateQueries({ queryKey: ["checks", org] });
        queryClient.invalidateQueries({ queryKey: ["checkGroups", org] });
      }
    },
  });
}

/**
 * Imports a native SolidPing export document. The body is sent as raw text so
 * the server-side parser decides the format: /checks/import accepts JSON *and*
 * YAML, and parsing client-side would silently narrow that to JSON only.
 */
export function useImportChecks(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (params: { body: string; dryRun?: boolean }) =>
      apiFetch<ImportResult>(
        `/api/v1/orgs/${org}/checks/import${params.dryRun ? "?dryRun=true" : ""}`,
        {
          method: "POST",
          body: params.body,
          headers: { "Content-Type": documentContentType(params.body) },
        },
      ),
    onSuccess: (_, params) => {
      if (!params.dryRun) {
        queryClient.invalidateQueries({ queryKey: ["checks", org] });
        queryClient.invalidateQueries({ queryKey: ["checkGroups", org] });
      }
    },
  });
}

// Check Group hooks
export function useCheckGroups(org: string) {
  return useQuery({
    queryKey: ["checkGroups", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: CheckGroup[] }>(
        `/api/v1/orgs/${org}/check-groups`,
      );
      return response.data || [];
    },
    enabled: !!org,
  });
}

export function useCheckGroup(
  org: string,
  uid: string,
  options?: {
    /**
     * Pass "always" when the consumer seeds local state from the response
     * once (e.g. the edit form): combined with `isFetchedAfterMount`, it
     * guarantees the seed comes from fresh data, not a stale cache entry.
     */
    refetchOnMount?: boolean | "always";
  },
) {
  return useQuery({
    queryKey: ["checkGroups", org, uid],
    queryFn: () =>
      apiFetch<CheckGroup>(`/api/v1/orgs/${org}/check-groups/${uid}`),
    enabled: !!org && !!uid,
    refetchOnMount: options?.refetchOnMount,
  });
}

export function useCreateCheckGroup(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateCheckGroupRequest) =>
      apiFetch<CheckGroup>(`/api/v1/orgs/${org}/check-groups`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["checkGroups", org] });
    },
  });
}

export function useUpdateCheckGroup(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateCheckGroupRequest) =>
      apiFetch<CheckGroup>(`/api/v1/orgs/${org}/check-groups/${uid}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["checkGroups", org] });
    },
  });
}

export function useDeleteCheckGroup(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/check-groups/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["checkGroups", org] });
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
    },
  });
}

// Check Dependency types
export type DependencyKind = "hard" | "soft";
export interface CheckRef {
  uid: string;
  slug: string;
  name: string;
}
export interface DependencyEdge {
  uid: string;
  parentCheck: CheckRef;
  childCheck: CheckRef;
  kind: DependencyKind;
  description?: string;
}
// Soft configuration lint over a check's hard `dependsOn` edges
// (spec 2026-08-31-06). Advisory only — the API never rejects a write for it.
export type DependencyWarningCode = "CONFIRMATION_MARGIN_TOO_SHORT";
export interface DependencyWarning {
  code: DependencyWarningCode;
  dependencyUid: string;
  parentCheck: CheckRef;
  childConfirmationSeconds: number;
  recommendedConfirmationSeconds: number;
  /** English fallback from the API; the UI renders off `code` instead. */
  message: string;
}
export interface PerCheckDependencies {
  dependsOn: DependencyEdge[];
  dependedOnBy: DependencyEdge[];
  /** Always present on responses from a current server; optional so an older
   * one (or a cached payload) does not break the page. */
  warnings?: DependencyWarning[];
}
export interface GraphNode {
  uid: string;
  slug: string;
  name: string;
}
export interface GraphEdge {
  uid: string;
  parentCheckUid: string;
  childCheckUid: string;
  kind: DependencyKind;
}
export interface GraphResponse {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

// Check Dependency hooks
export function useCheckDependencies(
  org: string,
  checkUid: string | undefined,
) {
  return useQuery({
    queryKey: ["dependencies", org, checkUid],
    queryFn: async () => {
      const r = await apiFetch<{ data: PerCheckDependencies }>(
        `/api/v1/orgs/${org}/checks/${checkUid}/dependencies`,
      );
      return r.data;
    },
    enabled: !!org && !!checkUid,
  });
}

export function useCreateCheckDependency(org: string, checkUid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: {
      parentCheckUid: string;
      kind: DependencyKind;
      description?: string;
    }) =>
      apiFetch<DependencyEdge>(
        `/api/v1/orgs/${org}/checks/${checkUid}/dependencies`,
        { method: "POST", body: JSON.stringify(body) },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dependencies", org] });
      qc.invalidateQueries({ queryKey: ["dependencyGraph", org] });
    },
  });
}

export function useUpdateCheckDependency(org: string, checkUid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: {
      uid: string;
      kind?: DependencyKind;
      description?: string;
    }) =>
      apiFetch<DependencyEdge>(
        `/api/v1/orgs/${org}/checks/${checkUid}/dependencies/${vars.uid}`,
        {
          method: "PATCH",
          body: JSON.stringify({
            kind: vars.kind,
            description: vars.description,
          }),
        },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dependencies", org] });
      qc.invalidateQueries({ queryKey: ["dependencyGraph", org] });
    },
  });
}

export function useDeleteCheckDependency(org: string, checkUid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/checks/${checkUid}/dependencies/${uid}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dependencies", org] });
      qc.invalidateQueries({ queryKey: ["dependencyGraph", org] });
    },
  });
}

export function useDependencyGraph(org: string, opts?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ["dependencyGraph", org],
    queryFn: async () => {
      const r = await apiFetch<{ data: GraphResponse }>(
        `/api/v1/orgs/${org}/dependencies`,
      );
      return r.data;
    },
    enabled: (opts?.enabled ?? true) && !!org,
    staleTime: 30_000,
  });
}

// Results hooks
export function useResults(
  org: string,
  options?: {
    checkUid?: string;
    periodType?: string;
    periodStartAfter?: string;
    periodEndBefore?: string;
    region?: string;
    with?: string;
    cursor?: string;
    size?: number;
    refetchInterval?: number;
  },
) {
  const { refetchInterval, ...queryOptions } = options || {};
  return useQuery({
    queryKey: ["results", org, queryOptions],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.checkUid) params.set("checkUid", options.checkUid);
      if (options?.periodType) params.set("periodType", options.periodType);
      if (options?.periodStartAfter)
        params.set("periodStartAfter", options.periodStartAfter);
      if (options?.periodEndBefore)
        params.set("periodEndBefore", options.periodEndBefore);
      if (options?.region) params.set("region", options.region);
      if (options?.with) params.set("with", options.with);
      if (options?.cursor) params.set("cursor", options.cursor);
      if (options?.size) params.set("limit", options.size.toString());
      const query = params.toString();
      const path = `/api/v1/orgs/${org}/results${query ? `?${query}` : ""}`;
      // Results pagination is cursor-only — no `total` (the results table is
      // the largest in the system; see wiki/api-specification/results-incidents.md).
      const response = await apiFetch<{
        data?: OrgResult[];
        pagination?: { cursor?: string; size?: number };
      }>(path);
      return {
        data: response.data || [],
        cursor: response.pagination?.cursor,
      };
    },
    enabled: !!org,
    refetchInterval,
  });
}

export function useResult(
  org: string,
  checkUid: string,
  resultUid: string,
  options?: { region?: string },
) {
  const region = options?.region;
  return useQuery<OrgResultDetail>({
    queryKey: ["result", org, checkUid, resultUid, region],
    queryFn: () => {
      const params = new URLSearchParams();
      if (region) params.set("region", region);
      const query = params.toString();
      return apiFetch<OrgResultDetail>(
        `/api/v1/orgs/${org}/checks/${checkUid}/results/${resultUid}${query ? `?${query}` : ""}`,
      );
    },
    enabled: !!org && !!checkUid && !!resultUid,
    staleTime: Infinity,
  });
}

/** Confirmed-outage (wall-clock) stats for one period. */
export interface CheckAvailabilityIncidents {
  count: number;
  longestSeconds?: number;
  averageSeconds?: number;
  totalDowntimeSeconds?: number;
}

/**
 * One period of server-measured availability. `availabilityPct` is `null` (and
 * `hasData` false) when the window has no countable probes — no data is not
 * 100%. `downtimeSeconds` is probe-time downtime; the `incidents` block is
 * confirmed-outage wall-clock time.
 */
export interface CheckAvailabilityPeriod {
  period: string;
  windowStart: string;
  windowEnd: string;
  monitoredSeconds: number;
  partial: boolean;
  hasData: boolean;
  totalChecks: number;
  successfulChecks: number;
  availabilityPct: number | null;
  downtimeSeconds: number;
  incidents: CheckAvailabilityIncidents;
}

export interface CheckAvailabilityResponse {
  data: CheckAvailabilityPeriod[];
}

/**
 * Fetches real per-period availability for one check from the server endpoint.
 * `periods` is a comma-separated token list (e.g. "today,7d,30d,365d"); the
 * server measures each window (probe-ratio availability + downtime + incident
 * block) so the client does no availability math.
 */
export function useCheckAvailability(
  org: string,
  checkUid: string,
  periods: string,
  options?: { tz?: string; refetchInterval?: number },
) {
  return useQuery<CheckAvailabilityResponse>({
    queryKey: ["checkAvailability", org, checkUid, periods, options?.tz],
    queryFn: () => {
      const params = new URLSearchParams({ periods });
      if (options?.tz) params.set("tz", options.tz);
      return apiFetch<CheckAvailabilityResponse>(
        `/api/v1/orgs/${org}/checks/${checkUid}/availability?${params.toString()}`,
      );
    },
    enabled: !!org && !!checkUid && !!periods,
    refetchInterval: options?.refetchInterval,
  });
}

/**
 * One cell of the chart availability strip. `availabilityPct` is `null` (and
 * `hasData` false) when the cell has no countable probes — no data is a distinct
 * third state, never a manufactured 100%. `status` is the SERVER's
 * classification (uptimebar.Classify), shared with the public status page, so
 * those two surfaces cannot paint identical numbers differently. The badge SVG
 * keeps its own four-tier scale on purpose — see `uptimebar.Classify`.
 */
export interface AvailabilityBucket {
  periodStart: string;
  periodEnd: string;
  hasData: boolean;
  availabilityPct: number | null;
  totalChecks: number;
  successfulChecks: number;
  status: "up" | "degraded" | "down" | "noData";
}

export interface CheckAvailabilityBucketsResponse {
  data: AvailabilityBucket[];
  /** The EXACT [from, to) fold — not the sum of the cells, which are aligned
   * outward to bucket boundaries and can span more time than was requested. */
  window: AvailabilityBucket;
  bucketSeconds: number;
  windowStart: string;
  windowEnd: string;
  region?: string;
}

export interface AvailabilityBucketsQuery {
  /** RFC3339 window start. */
  from: string;
  /** RFC3339 window end. */
  to: string;
  /** Cell width in seconds; must be a whole multiple of 3600 (see
   * `@/lib/availability-strip`). Omitted lets the server choose. */
  bucketSeconds?: number;
  /** Single probe region, or undefined to sum across every region. */
  region?: string;
}

/**
 * Fetches bucketed availability for one check over an arbitrary window — the
 * data behind the chart's availability strip.
 *
 * Deliberately server-side: deriving cells from the chart's already-fetched rows
 * would duplicate the counting rules (lifecycle markers and abandoned attempts
 * out of both numerator and denominator, warning counts as up) and drift from
 * the availability table sitting right below the chart.
 */
export function useCheckAvailabilityBuckets(
  org: string,
  checkUid: string,
  query: AvailabilityBucketsQuery,
  options?: { refetchInterval?: number; enabled?: boolean },
) {
  return useQuery<CheckAvailabilityBucketsResponse>({
    queryKey: [
      "checkAvailabilityBuckets",
      org,
      checkUid,
      query.from,
      query.to,
      query.bucketSeconds ?? null,
      query.region ?? null,
    ],
    queryFn: () => {
      const params = new URLSearchParams({ from: query.from, to: query.to });
      if (query.bucketSeconds)
        params.set("bucket", `${Math.round(query.bucketSeconds / 3600)}h`);
      if (query.region) params.set("region", query.region);
      return apiFetch<CheckAvailabilityBucketsResponse>(
        `/api/v1/orgs/${org}/checks/${checkUid}/availability/buckets?${params.toString()}`,
      );
    },
    enabled:
      (options?.enabled ?? true) &&
      !!org &&
      !!checkUid &&
      !!query.from &&
      !!query.to,
    refetchInterval: options?.refetchInterval,
  });
}

/** The per-query slice of `useAllResults`/`useResultTiers` options — everything
 * that lands in the react-query key and on the wire. `refetchInterval` is
 * deliberately NOT part of it: it is a client-side scheduling concern, and
 * folding it into the key would split the cache between two callers that
 * request the same data at different cadences. */
export interface ResultsQueryOptions {
  checkUid?: string;
  periodType?: string;
  periodStartAfter?: string;
  periodEndBefore?: string;
  with?: string;
  size?: number;
}

/** Follows result cursors until exhausted, returning every row in the window.
 * Shared by `useAllResults` and by each tier of `useResultTiers` so both walk
 * pages identically. */
async function fetchAllResultPages(
  org: string,
  options: ResultsQueryOptions,
): Promise<OrgResult[]> {
  const allData: OrgResult[] = [];
  let cursor: string | undefined;
  const pageSize = options.size ?? 100;

  do {
    const params = new URLSearchParams();
    if (options.checkUid) params.set("checkUid", options.checkUid);
    if (options.periodType) params.set("periodType", options.periodType);
    if (options.periodStartAfter)
      params.set("periodStartAfter", options.periodStartAfter);
    if (options.periodEndBefore)
      params.set("periodEndBefore", options.periodEndBefore);
    if (options.with) params.set("with", options.with);
    if (cursor) params.set("cursor", cursor);
    params.set("limit", pageSize.toString());
    const query = params.toString();
    const path = `/api/v1/orgs/${org}/results${query ? `?${query}` : ""}`;
    const response = await apiFetch<{
      data?: OrgResult[];
      pagination?: CursorPagination;
    }>(path);
    if (response.data) allData.push(...response.data);
    cursor = response.pagination?.cursor;
  } while (cursor);

  return allData;
}

/**
 * Fetches all result pages by following cursors until exhausted, as ONE query.
 *
 * @deprecated Retained deliberately, with no in-repo callers. Every caller moved
 * to `useResultTiers` when spec 2026-08-22-04 split the chart fetch at the
 * raw/rollup boundary; a single query naming both sides matches neither partial
 * index on `results` and sequentially scans the largest table in the system. Use
 * this only for a genuinely single-tier window (or none at all) — if you are
 * about to pass `periodType: "raw,hour"`, you want `useResultTiers` instead.
 */
export function useAllResults(
  org: string,
  options?: ResultsQueryOptions & { refetchInterval?: number },
) {
  const { refetchInterval, ...queryOptions } = options || {};
  return useQuery({
    queryKey: ["allResults", org, queryOptions],
    queryFn: async () => ({
      data: await fetchAllResultPages(org, queryOptions),
    }),
    enabled: !!org,
    refetchInterval,
  });
}

/**
 * Runs one `useAllResults`-shaped paginated walk per aggregation tier, **in
 * parallel**, and merges the pages back into the single descending sequence a
 * combined query would have returned.
 *
 * The tiers exist because the two indexes on `results` are partial and split on
 * `period_type = 'raw'`: a query asking for `raw` and a rollup tier at once is
 * satisfied by neither and can only be answered by a sequential scan of the
 * largest table in the system (spec 2026-08-22-04). Callers build the tier list
 * with `chartFetchParams`.
 *
 * Each tier gets the SAME `["allResults", org, {...}]` key shape `useAllResults`
 * uses, so a second caller passing the same tier list (the check-detail route,
 * which derives its region set and duration stats from the chart's window)
 * remains a react-query cache hit rather than a second round of HTTP requests.
 */
export function useResultTiers(
  org: string,
  tiers: ResultsQueryOptions[],
  options?: { refetchInterval?: number; enabled?: boolean },
) {
  const queries = useQueries({
    queries: tiers.map((tier) => ({
      queryKey: ["allResults", org, tier],
      queryFn: async () => ({ data: await fetchAllResultPages(org, tier) }),
      // `enabled` lets a caller hold a pass back until the data its window
      // depends on exists — see useChartWindowResults, where firing the raw
      // pass before the rollup pass resolves would request the full window,
      // which is the whole cost this indirection removes.
      enabled: !!org && (options?.enabled ?? true),
      refetchInterval: options?.refetchInterval,
    })),
  });

  // A single string dependency, not a spread array: the tier count changes when
  // the user switches range (a month view has a rollup tier, an hour view does
  // not) and a variable-length deps array is a hook-rules violation.
  //
  // The signature must name EVERY input that changes which rows belong in the
  // merge — org and checkUid included. Navigating from one check to another
  // while this component stays mounted swaps the query keys but, until either
  // tier resolves, leaves `dataUpdatedAt` at 0 on both; with an identical window
  // a signature built from the window alone would be unchanged and the memo
  // would hand back the PREVIOUS check's rows. The chart hides that behind its
  // own isLoading, but the check-detail route feeds this straight into
  // observedRegions with no such guard, so the region chips would briefly show
  // the wrong check's regions.
  const signature = [
    org,
    ...tiers.map(
      (t) =>
        `${t.checkUid ?? ""}#${t.periodType}@${t.periodStartAfter}~${
          t.periodEndBefore ?? ""
        }+${t.with ?? ""}:${t.size ?? ""}`,
    ),
    ...queries.map((q) => q.dataUpdatedAt),
  ].join("|");

  const data = useMemo(
    () => ({ data: mergeResultTiers(queries.map((q) => q.data?.data)) }),
    // Re-merge whenever a tier resolves or the tier plan itself changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [signature],
  );

  return {
    data,
    isLoading: queries.some((q) => q.isLoading),
    isFetching: queries.some((q) => q.isFetching),
    error: queries.find((q) => q.error)?.error ?? null,
    /** True while this pass has not settled — broader than `isLoading`,
     * which in react-query v5 is `isPending && isFetching` and so reads
     * `false` for a query that is disabled (held back by `enabled`) or has
     * not started fetching yet, even though it plainly has not resolved.
     * `[].some(...)` is also vacuously `false`, so a tier list that has not
     * been populated yet must not be mistaken for "settled" either — the
     * `tiers.length > 0 && queries.length === 0` guard exists for that,
     * even though with `useQueries` mapping 1:1 over `tiers` it can never
     * actually diverge from `queries.some(...)` today. */
    isPending:
      queries.some((q) => q.isPending) ||
      (tiers.length > 0 && queries.length === 0),
  };
}

/** How often pass 1 re-runs. A closed rollup bucket never changes, so the only
 * reason to refetch is that a NEW bucket closed — which happens at the finest
 * rollup width, one hour. Re-downloading a month of settled buckets every 60 s
 * (what the single-pass chart did) is pure waste, and it is where most of the
 * steady-state cost of an open dashboard went. */
const ROLLUP_REFETCH_MS = 60 * 60_000;

/**
 * The chart's window, fetched as TWO passes instead of one blocked render
 * (spec 2026-08-22-07).
 *
 * Pass 1 asks for the rollup tier over the whole window and resolves in one
 * round-trip; the chart is interactive from that alone. Pass 2 then asks for
 * raw over only the SEAM — the span between the newest bucket pass 1 returned
 * and now — and merges into the same series. On a 30-day view of a 1-minute
 * check that turns ~4 320 raw points across five sequential round-trips into
 * ~180 in one, without losing the chart's right-hand edge (the open bucket the
 * aggregator has not rolled up yet).
 *
 * The data dependency is what makes this two passes rather than two parallel
 * fetches, and it is also what makes it correct when the aggregator lags: the
 * seam is derived from pass 1's rows, so it widens on its own exactly when
 * rollups fall behind (see seamStartFrom).
 *
 * Both the chart and the check-detail route call this with the same arguments,
 * so they share one set of react-query keys and the route stays a cache hit
 * rather than a second round of HTTP requests.
 *
 * `isLoading` is pass 1 ONLY — the skeleton belongs to the first render, not to
 * the raw merge. A pass-2 failure surfaces as `rawError` and leaves the pass-1
 * data on screen.
 */
export function useChartWindowResults(
  org: string,
  checkUid: string,
  window: { timeRange: TimeRange; periodMs?: number; zoom?: ZoomWindow },
  options?: { rawRefetchInterval?: number },
) {
  const { timeRange, periodMs, zoom } = window;
  const zoomFrom = zoom?.from;
  const zoomTo = zoom?.to;

  // Resolve the window ONCE for both passes. An unzoomed start is
  // startOfMinute(now), and pass 2's plan is rebuilt when pass 1 settles — so
  // re-deriving it per pass would let a minute tick land the two tiers on
  // windows that disagree, and split the raw key between this hook's two call
  // sites (the chart and the check-detail route) into two HTTP requests where
  // there should be one cache hit.
  const bounds = useMemo(
    () => chartWindowBounds(timeRange, zoom),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- zoom is derived from zoomFrom/zoomTo
    [timeRange, zoomFrom, zoomTo],
  );

  // Pass 1 — rollups over the whole window. Empty for a range where raw IS the
  // tier (hour, or a day view of a check slower than 5 min): there is nothing
  // to narrow raw against, so it stays the full window.
  const rollupTier = chartRollupTier(timeRange, periodMs, zoom);
  const hasRollupTier = rollupTier !== "";
  const rollupTiers = useMemo(
    () =>
      hasRollupTier
        ? [{ checkUid, ...chartFetchParamsForWindow(bounds, rollupTier)[0] }]
        : [],
    [hasRollupTier, rollupTier, bounds, checkUid],
  );
  const rollup = useResultTiers(org, rollupTiers, {
    refetchInterval: ROLLUP_REFETCH_MS,
  });

  const rollupRows = rollup.data.data;
  const seamStart = useMemo(() => seamStartFrom(rollupRows), [rollupRows]);

  // Pass 2 — raw over the seam. Held back until pass 1 has settled, because a
  // raw query issued before the seam is known would span the full window; once
  // pass 1 has settled with NO rows (a check younger than one rollup bucket)
  // the seam is undefined and raw correctly covers everything again.
  const rawTiers = useMemo(() => {
    const plan = chartFetchParamsForWindow(bounds, rollupTier, seamStart);

    return [{ checkUid, ...plan[plan.length - 1] }];
  }, [checkUid, bounds, rollupTier, seamStart]);
  const raw = useResultTiers(org, rawTiers, {
    refetchInterval: options?.rawRefetchInterval,
    enabled: !hasRollupTier || !rollup.isLoading,
  });

  const rollupData = rollup.data.data;
  const rawData = raw.data.data;
  const data = useMemo(
    () => ({ data: mergeResultTiers<OrgResult>([rollupData, rawData]) }),
    [rollupData, rawData],
  );

  return {
    data,
    // Pass 1 only. When raw IS the tier there is no pass 1, so raw's own
    // loading state is the first render's.
    isLoading: hasRollupTier ? rollup.isLoading : raw.isLoading,
    isFetching: rollup.isFetching || raw.isFetching,
    error: rollup.error,
    /** Pass 2's failure. Deliberately separate: the chart must stay usable on
     * pass-1 data and surface this without discarding what is drawn. */
    rawError: raw.error,
    /** True while the seam has not arrived yet — the progressive-render phase. */
    rawPending: raw.isLoading,
    /**
     * True when no rows have merged yet AND at least one pass has not
     * settled. Distinct from `isLoading` (pass 1 only) and `rawPending`
     * (pass 2's own `isLoading`, which reads `false` while pass 2 is
     * disabled/held back behind pass 1): a caller that renders a terminal
     * "no data" state off `chartData.length === 0` must gate it on THIS
     * flag instead, or it flashes that message during the window between
     * pass 1 settling empty and pass 2 (now enabled) actually resolving —
     * see spec 2026-08-25-03. Once both passes have genuinely settled with
     * nothing, this is `false` and the empty state is honest.
     */
    isEmptyPending:
      data.data.length === 0 && (rollup.isPending || raw.isPending),
  };
}

// Incidents hooks
export function useIncidents(
  org: string,
  options?: {
    // "acked" / "snoozed" are derived states the backend translates to
    // active + filter; the frontend just passes the literal through.
    state?: "active" | "resolved" | "acked" | "snoozed";
    checkUid?: string;
    since?: string;
    until?: string;
    cursor?: string;
    size?: number;
    with?: string;
    hideSuppressed?: boolean;
    causedByIncidentUid?: string;
    refetchInterval?: number;
    // Lets a caller gate the query on data that isn't ready yet — e.g. the
    // check detail page must wait for useCheck to resolve check.uid (the
    // route param may be a slug) before firing the incidents query, so the
    // wire request always carries a UID and never a slug (issue #127).
    // Defaults to true so existing callers are unaffected.
    enabled?: boolean;
  },
) {
  const { refetchInterval, enabled, ...queryOptions } = options || {};
  return useQuery({
    queryKey: ["incidents", org, queryOptions],
    refetchInterval,
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.state) params.set("state", options.state);
      if (options?.checkUid) params.set("checkUid", options.checkUid);
      if (options?.since) params.set("since", options.since);
      if (options?.until) params.set("until", options.until);
      if (options?.cursor) params.set("cursor", options.cursor);
      if (options?.size) params.set("limit", options.size.toString());
      if (options?.with) params.set("with", options.with);
      if (options?.hideSuppressed) params.set("hideSuppressed", "true");
      if (options?.causedByIncidentUid)
        params.set("causedByIncidentUid", options.causedByIncidentUid);
      const query = params.toString();
      const path = `/api/v1/orgs/${org}/incidents${query ? `?${query}` : ""}`;
      const response = await apiFetch<{
        data?: IncidentDetail[];
        pagination?: CursorPagination;
      }>(path);
      return {
        data: response.data || [],
        cursor: response.pagination?.cursor,
        total: response.pagination?.total,
      };
    },
    enabled: (enabled ?? true) && !!org,
  });
}

export function useIncident(org: string, uid: string) {
  return useQuery({
    queryKey: ["incident", org, uid],
    queryFn: () =>
      apiFetch<IncidentDetail>(`/api/v1/orgs/${org}/incidents/${uid}`),
    enabled: !!org && !!uid,
  });
}

interface IncidentMutationVars {
  uid: string;
  body?: Record<string, unknown>;
}

function useIncidentAction<TVars extends IncidentMutationVars>(
  org: string,
  path: (uid: string) => string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (vars: TVars) =>
      apiFetch<IncidentDetail>(path(vars.uid), {
        method: "POST",
        body: vars.body ? JSON.stringify(vars.body) : undefined,
        headers: vars.body ? { "Content-Type": "application/json" } : undefined,
      }),
    onSuccess: (_, vars) => {
      queryClient.invalidateQueries({ queryKey: ["incidents", org] });
      queryClient.invalidateQueries({ queryKey: ["incident", org, vars.uid] });
    },
  });
}

export function useAcknowledgeIncident(org: string) {
  return useIncidentAction<{ uid: string; body?: { note?: string } }>(
    org,
    (uid) => `/api/v1/orgs/${org}/incidents/${uid}/ack`,
  );
}

export function useUnacknowledgeIncident(org: string) {
  return useIncidentAction<{ uid: string }>(
    org,
    (uid) => `/api/v1/orgs/${org}/incidents/${uid}/unack`,
  );
}

export function useSnoozeIncident(org: string) {
  return useIncidentAction<{
    uid: string;
    body: { duration?: string; until?: string; reason?: string };
  }>(org, (uid) => `/api/v1/orgs/${org}/incidents/${uid}/snooze`);
}

export function useUnsnoozeIncident(org: string) {
  return useIncidentAction<{ uid: string }>(
    org,
    (uid) => `/api/v1/orgs/${org}/incidents/${uid}/unsnooze`,
  );
}

export function useResolveIncident(org: string) {
  return useIncidentAction<{ uid: string; body?: { note?: string } }>(
    org,
    (uid) => `/api/v1/orgs/${org}/incidents/${uid}/resolve`,
  );
}

// useAddComment appends a free-text comment to an incident's timeline. The new
// comment surfaces through the events query, so we invalidate it on success;
// the live events subscription covers Slack- and remote-authored comments too.
export function useAddComment(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (vars: { uid: string; text: string }) =>
      apiFetch<Event>(`/api/v1/orgs/${org}/incidents/${vars.uid}/comments`, {
        method: "POST",
        body: JSON.stringify({ text: vars.text }),
        headers: { "Content-Type": "application/json" },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["events", org] });
    },
  });
}

// Events hooks
export function useEvents(
  org: string,
  options?: {
    checkUid?: string;
    incidentUid?: string;
    eventType?: string;
    cursor?: string;
    size?: number;
    refetchInterval?: number;
  },
) {
  const { refetchInterval, ...queryOptions } = options || {};
  return useQuery({
    queryKey: ["events", org, queryOptions],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.checkUid) params.set("checkUid", options.checkUid);
      if (options?.incidentUid) params.set("incidentUid", options.incidentUid);
      if (options?.eventType) params.set("eventType", options.eventType);
      if (options?.cursor) params.set("cursor", options.cursor);
      if (options?.size) params.set("limit", options.size.toString());
      const query = params.toString();
      const path = `/api/v1/orgs/${org}/events${query ? `?${query}` : ""}`;
      const response = await apiFetch<{
        data?: Event[];
        pagination?: CursorPagination;
      }>(path);
      return {
        data: response.data || [],
        cursor: response.pagination?.cursor,
        total: response.pagination?.total,
      };
    },
    enabled: !!org,
    refetchInterval,
  });
}

/**
 * useAuditEvents is the org Audit page's reader.
 *
 * Distinct from useEvents rather than another pile of optional arguments on
 * it: this one drives a page whose whole job is filtering, so its inputs are
 * required-shaped (family, actor, time window, cursor) and its query key is
 * built from all of them. Sharing useEvents would mean every dashboard feed
 * re-fetching whenever the audit page's filters moved.
 */
export function useAuditEvents(
  org: string,
  options: {
    family?: string;
    actorUserUid?: string;
    targetType?: string;
    /**
     * Free-text target match: an exact object UID, or a case-insensitive
     * substring of the target name captured on the event. Sent as `target`,
     * which is the API's free-text parameter — `targetUid` is the separate,
     * exact-match one.
     */
    target?: string;
    /**
     * Client address. The API honours this for org admins/owners only and
     * silently ignores it otherwise, so a non-admin cannot use it as an
     * oracle for a column they are not allowed to read.
     */
    sourceIp?: string;
    /**
     * Window size in hours, not an absolute timestamp: the caller must not
     * compute `Date.now()` during render (it is impure, and it would also make
     * the query key drift on every re-render). The instant is resolved here,
     * when the request is actually made.
     */
    sinceHours?: number;
    cursor?: string;
    size?: number;
  },
) {
  return useQuery({
    queryKey: ["audit-events", org, options],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options.family) params.set("type", options.family);
      if (options.actorUserUid)
        params.set("actorUserUid", options.actorUserUid);
      if (options.targetType) params.set("targetType", options.targetType);
      if (options.target) params.set("target", options.target);
      if (options.sourceIp) params.set("sourceIp", options.sourceIp);
      if (options.sinceHours) {
        params.set(
          "since",
          new Date(Date.now() - options.sinceHours * 3600_000).toISOString(),
        );
      }
      if (options.cursor) params.set("cursor", options.cursor);
      params.set("limit", String(options.size ?? 50));
      const path = `/api/v1/orgs/${org}/events?${params.toString()}`;
      const response = await apiFetch<{
        data?: Event[];
        pagination?: CursorPagination;
      }>(path);
      return {
        data: response.data || [],
        cursor: response.pagination?.cursor,
      };
    },
    enabled: !!org,
  });
}

// Incident Notification types and hooks

export interface IncidentNotificationUser {
  uid: string;
  name: string;
}

export interface IncidentNotificationConnection {
  uid: string;
  name: string;
  type: string;
}

export interface IncidentNotificationIncident {
  uid: string;
  title: string;
  state: "active" | "resolved";
  startedAt: string;
}

/** Structured per-attempt delivery artifacts captured by the channel sender
 * (webhook today; other channels fill what they can). Every field is optional;
 * the whole object is absent for pre-feature rows and channels that produce no
 * artifacts. Secrets (signing secret, auth headers, URL credentials) are never
 * present — they are stripped server-side before persistence. */
export interface NotificationDeliveryDetails {
  httpStatusCode?: number;
  requestUrl?: string;
  requestBody?: string;
  responseBody?: string;
  durationMs?: number;
  responseHeaders?: Record<string, string>;
}

export interface IncidentNotification {
  uid: string;
  incidentUid: string;
  eventType: string;
  source: string;
  stepUid?: string;
  repeatIndex?: number;
  channelType: string;
  status: "pending" | "sent" | "failed" | "cancelled" | "skipped";
  skipReason?: string;
  error?: string;
  messageId?: string;
  createdAt: string;
  sentAt?: string;
  failedAt?: string;
  cancelledAt?: string;
  jobUid?: string;
  deliveryDetails?: NotificationDeliveryDetails;
  user: IncidentNotificationUser | null;
  connection: IncidentNotificationConnection | null;
  incident?: IncidentNotificationIncident;
}

export function useIncidentNotifications(
  org: string,
  incidentUid: string,
  options?: {
    status?: string;
    limit?: number;
  },
) {
  return useQuery({
    queryKey: ["incidentNotifications", org, incidentUid, options],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.status) params.set("status", options.status);
      if (options?.limit) params.set("limit", options.limit.toString());
      const query = params.toString();
      const path = `/api/v1/orgs/${org}/incidents/${incidentUid}/notifications${query ? `?${query}` : ""}`;
      const response = await apiFetch<{ data?: IncidentNotification[] }>(path);
      return response.data || [];
    },
    enabled: !!org && !!incidentUid,
  });
}

export function useIncidentNotification(
  org: string,
  incidentUid: string,
  notifUid: string,
) {
  return useQuery<IncidentNotification>({
    queryKey: ["incidentNotification", org, incidentUid, notifUid],
    queryFn: () =>
      apiFetch<IncidentNotification>(
        `/api/v1/orgs/${org}/incidents/${incidentUid}/notifications/${notifUid}`,
      ),
    enabled: !!org && !!incidentUid && !!notifUid,
    staleTime: Infinity,
  });
}

/** Fetch a single notification by UID, scoped to the org (no incidentUid required).
 * Query key: ["orgNotification", org, notifUid] — the breadcrumb reads from
 * this key to avoid an extra fetch when the page has already loaded. */
export function useOrgNotification(org: string, notifUid: string) {
  return useQuery<IncidentNotification>({
    queryKey: ["orgNotification", org, notifUid],
    queryFn: () =>
      apiFetch<IncidentNotification>(
        `/api/v1/orgs/${org}/notifications/${notifUid}`,
      ),
    enabled: !!org && !!notifUid,
    staleTime: Infinity,
  });
}

/** List the last `limit` notifications delivered through an integration.
 * Calls GET /api/v1/orgs/:org/notifications?connectionUid=&limit= */
export function useIntegrationNotifications(
  org: string,
  integrationUid: string,
  limit = 10,
) {
  return useQuery({
    queryKey: ["integrationNotifications", org, integrationUid, limit],
    queryFn: async () => {
      const params = new URLSearchParams({
        connectionUid: integrationUid,
        limit: String(limit),
      });
      const response = await apiFetch<{ data?: IncidentNotification[] }>(
        `/api/v1/orgs/${org}/notifications?${params.toString()}`,
      );
      return response.data || [];
    },
    enabled: !!org && !!integrationUid,
  });
}

// Email suppressions (spec 2026-07-05-10, D4) — per-recipient unsubscribe
// list for an org's alert emails. Creation is always recipient-initiated
// (the public /unsubscribe page), so there is no create hook — only
// list + delete (re-subscribe).
export interface EmailSuppression {
  uid: string;
  email: string;
  checkUid?: string; // absent = org-wide (suppresses every check)
  checkName?: string;
  source: "link" | "header" | "dashboard";
  createdAt: string;
}

/** List current email suppressions for an org.
 * Calls GET /api/v1/orgs/:org/email-suppressions */
export function useEmailSuppressions(org: string) {
  return useQuery({
    queryKey: ["emailSuppressions", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: EmailSuppression[] }>(
        `/api/v1/orgs/${org}/email-suppressions`,
      );
      return response.data || [];
    },
    enabled: !!org,
  });
}

/** Delete (re-subscribe) an email suppression.
 * Calls DELETE /api/v1/orgs/:org/email-suppressions/:uid */
export function useDeleteEmailSuppression(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/email-suppressions/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["emailSuppressions", org] });
    },
  });
}

export function useMyNotifications(
  org: string,
  options?: {
    status?: string;
    limit?: number;
    before?: string;
  },
) {
  return useQuery({
    queryKey: ["myNotifications", org, options],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.status) params.set("status", options.status);
      if (options?.limit) params.set("limit", options.limit.toString());
      if (options?.before) params.set("before", options.before);
      const query = params.toString();
      const path = `/api/v1/orgs/${org}/me/notifications${query ? `?${query}` : ""}`;
      const response = await apiFetch<{ data?: IncidentNotification[] }>(path);
      return response.data || [];
    },
    enabled: !!org,
  });
}

// Token types
export interface TokenInfo {
  uid: string;
  name?: string;
  type: string;
  orgSlug?: string;
  createdAt: string;
  lastUsedAt?: string;
  lastActiveAt?: string;
  expiresAt?: string;
  // The following are populated for `refresh`-type rows (sessions) only —
  // always undefined/false for PATs.
  isCurrent?: boolean;
  createdWith?: TokenCreatedWith;
}

export interface TokenCreatedWith {
  method?: string;
  userAgent?: string;
  remoteAddr?: string;
}

// SessionInfo is TokenInfo narrowed to what the sessions page actually
// renders — same wire shape (the backend returns TokenInfo for every token
// type), but this alias documents the fields sessions specifically rely on.
export type SessionInfo = TokenInfo;

export interface CreateTokenRequest {
  name: string;
  expiresAt?: string;
}

export interface CreateTokenResponse {
  uid: string;
  token: string;
  name: string;
  expiresAt?: string;
  createdAt: string;
}

// Token hooks
// PAT-only — sessions (type=refresh) must never be fetched through this
// hook; use useSessions below instead.
export function useTokens(org: string) {
  return useQuery({
    queryKey: ["tokens", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: TokenInfo[] }>(
        `/api/v1/orgs/${org}/tokens?type=pat`,
      );
      return response.data || [];
    },
    enabled: !!org,
  });
}

export function useCreateToken(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateTokenRequest) =>
      apiFetch<CreateTokenResponse>(`/api/v1/orgs/${org}/tokens`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["tokens", org] });
    },
  });
}

// Device authorization (RFC 8628) — the consent side of `sp auth login`.
// The CLI talks to /api/v1/auth/device and /auth/device/token directly; the
// dashboard only ever sees the two consent endpoints below.
export interface DeviceConsentInfo {
  clientName: string;
  userCode: string;
  status: "pending" | "approved" | "denied";
  expiresAt: string;
}

export interface DeviceConsentRequest {
  userCode: string;
  /** Slug of the org the approved token is scoped to. */
  org?: string;
  approve: boolean;
}

/** Looks up a pending device-authorization request by its short user code. */
export function useDeviceConsent(userCode: string) {
  return useQuery({
    queryKey: ["device-consent", userCode],
    queryFn: () =>
      apiFetch<DeviceConsentInfo>(
        `/api/v1/auth/device/consent?userCode=${encodeURIComponent(userCode)}`,
      ),
    enabled: !!userCode,
    retry: false,
  });
}

/** Approves or denies a pending device-authorization request. */
export function useRespondToDeviceConsent() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: DeviceConsentRequest) =>
      apiFetch<{ approved: boolean }>("/api/v1/auth/device/consent", {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      // An approval mints a PAT, so the tokens list is stale.
      queryClient.invalidateQueries({ queryKey: ["tokens"] });
      queryClient.invalidateQueries({ queryKey: ["device-consent"] });
    },
  });
}

export function useRevokeToken() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/auth/tokens/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["tokens"] });
    },
  });
}

// Session hooks — sessions are `user_tokens` rows with type=refresh, listed
// through the same endpoint as PATs (filtered by `?type=`) but surfaced as a
// distinct page/nav entry (see account.sessions.tsx). Mirrors useTokens.
export function useSessions(org: string) {
  return useQuery({
    queryKey: ["sessions", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: SessionInfo[] }>(
        `/api/v1/orgs/${org}/tokens?type=refresh`,
      );
      return response.data || [];
    },
    enabled: !!org,
  });
}

export function useRevokeSession(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/auth/tokens/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["sessions", org] });
    },
  });
}

export interface SignOutOtherSessionsResponse {
  success: boolean;
  tokensDeleted: number;
}

export function useSignOutOtherSessions(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () =>
      apiFetch<SignOutOtherSessionsResponse>(`/api/v1/auth/logout`, {
        method: "POST",
        body: JSON.stringify({ signOutOthers: true }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["sessions", org] });
    },
  });
}

// Status Page types

// StatusPagePeriod is the configured history window. 24h renders 24 hourly
// buckets; 7d/30d/90d render N daily buckets. Mirrors the badge uptime-bar
// vocabulary.
export type StatusPagePeriod = "24h" | "7d" | "30d" | "90d";

// DnsRecord is one DNS record a customer must create to activate a custom
// domain. Since v0.8.0 the API returns exactly one: the routing CNAME (the TXT
// ownership challenge was removed).
export interface DnsRecord {
  type: string;
  name: string;
  value: string;
}

export type CustomDomainStatus = "unverified" | "verified";

// CustomDomainCertStatus is the in-server-ACME certificate state for a verified
// custom domain. Absent when in-server TLS is disabled (TLS handled by an
// external proxy) or the domain is not verified yet.
export type CustomDomainCertStatus = "none" | "issued" | "error";

// CustomDomainState is the domain's lifecycle state (spec 2026-08-23-03).
// `grace` is the one customDomainStatus cannot express: the page is STILL being
// served, but its periodic DNS re-checks are failing and it will go dark if
// nothing is done. `demoted` means it already has.
export type CustomDomainState =
  | "none"
  | "pending"
  | "active"
  | "grace"
  | "demoted";

// AvailabilitySettings customizes a status page's green/amber/red
// availability thresholds. A nil/omitted field means "use the platform
// default" (99.9 / 99.0) — see AvailabilityThresholds for the resolved
// values.
export interface AvailabilitySettings {
  thresholdUp?: number;
  thresholdDegraded?: number;
}

// StatusPageSettings mirrors the API's storage shape: an omitted section
// means "using the defaults".
export interface StatusPageSettings {
  availability?: AvailabilitySettings;
}

// AvailabilityThresholds carries the RESOLVED effective thresholds (never
// undefined) so the UI never needs to know the platform defaults itself.
export interface AvailabilityThresholds {
  thresholdUp: number;
  thresholdDegraded: number;
}

/**
 * Status page visibility. `public` is world-readable, `private` is fully
 * hidden (the public endpoints 404), and `password` is shared-with-a-secret:
 * the public endpoints answer 401 until the visitor unlocks the page.
 */
export type StatusPageVisibility = "public" | "private" | "password";

export interface StatusPage {
  uid: string;
  name: string;
  slug: string;
  description?: string;
  visibility: StatusPageVisibility;
  isDefault: boolean;
  enabled: boolean;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyDays: number;
  historyPeriod: StatusPagePeriod;
  /**
   * Incident auto-publication settings (spec 2026-08-19-08).
   *
   * `autoPublish` is FALSE on every page that existed before this feature
   * shipped — the migration deliberately did not opt anyone in retroactively —
   * and TRUE on pages created since.
   */
  autoPublish: boolean;
  /** 0 publishes immediately; an incident shorter than this never publishes. */
  autoPublishDelaySeconds: number;
  autoResolve: "always" | "if_untouched" | "never";
  // Operator-authored stylesheet applied to the PUBLIC status page (status0).
  // Never applied to dash0's own chrome — only inside the appearance preview
  // iframe, which loads the real status0 renderer.
  customCss?: string;
  /**
   * Public paths of the page's uploaded brand assets (spec 2026-08-21-07), or
   * absent when the slot is empty. Read-only here: they are set by the
   * dedicated upload endpoints, never by PATCH.
   */
  logoUrl?: string;
  faviconUrl?: string;
  /**
   * The page's white-label OPT-IN, as stored — not the resolved decision. On
   * the admin payload this is the raw column so the toggle round-trips
   * honestly even while the org is not entitled; `whiteLabelAllowed` says
   * whether it currently has any effect.
   */
  hideBranding?: boolean;
  /** Whether the org holds the `whiteLabel` entitlement. Admin payload only. */
  whiteLabelAllowed?: boolean;
  /** True when the page is password-protected. The hash is never returned. */
  hasPassword?: boolean;
  /**
   * Whether a kiosk (TV mode) token is minted for this page (spec
   * 2026-08-29-08). Admin payloads only — the public views strip it, and the
   * token itself is only ever returned by the POST that mints it.
   */
  hasKioskToken?: boolean;
  // Custom-domain fields are only present on the authenticated org endpoints.
  customDomain?: string;
  customDomainStatus?: CustomDomainStatus;
  customDomainCertStatus?: CustomDomainCertStatus;
  customDomainRecords?: DnsRecord[];
  customDomainState?: CustomDomainState;
  /** When the domain entered `grace`. Only set while degrading. */
  customDomainDegradedSince?: string;
  /**
   * One-line diagnostic from the last DNS re-check: the mode used, the target
   * expected, what DNS actually returned. Admin-only.
   */
  customDomainLastCheck?: string;
  // Settings mirrors the storage shape (an unset section means "using the
  // default"); AvailabilityThresholds is always the resolved, non-nil pair.
  settings?: StatusPageSettings;
  availabilityThresholds: AvailabilityThresholds;
  sections?: StatusPageSection[];
  createdAt?: string;
}

/**
 * A section's dynamic-membership rule (spec 2026-08-29-11). Exactly one of the
 * two shapes: `{ all: true }` or `{ labels: { k: v, ... } }` (AND, exact
 * values). Absent means the section is hand-curated, which is the default and
 * what every existing section keeps — a selector is never applied implicitly.
 */
export interface StatusPageSectionSelector {
  all?: boolean;
  labels?: Record<string, string>;
}

export interface StatusPageSection {
  uid: string;
  name: string;
  slug: string;
  position: number;
  /**
   * Returned on AUTHENTICATED responses only — the public page payload omits
   * it, because a selector spells out the org's internal label taxonomy.
   */
  selector?: StatusPageSectionSelector;
  /** How many checks the selector matches in total. Authenticated only. */
  selectorMatchTotal?: number;
  /**
   * True when the match count exceeds the per-section cap and the section is
   * showing a stable alphabetical prefix. Authenticated only.
   */
  selectorTruncated?: boolean;
  /**
   * How many of the selector's matched checks are already displayed by
   * resource rows OUTSIDE this section — an earlier selector section or a
   * manual placement (spec 2026-08-31-01). Authenticated only. Absent/0 means
   * nothing is claimed elsewhere.
   */
  selectorClaimedElsewhere?: number;
  /**
   * Name of the section holding the most of `selectorClaimedElsewhere`'s
   * checks, for the "already shown in '{{section}}'" copy. Authenticated
   * only; absent when `selectorClaimedElsewhere` is 0/absent.
   */
  selectorClaimedSectionName?: string;
  resources?: StatusPageResource[];
  createdAt?: string;
}

export interface StatusPageResource {
  uid: string;
  /**
   * Exactly one of checkUid / checkGroupUid is set. A group resource renders as
   * ONE public component (rolled-up status, weighted-average availability across
   * its members, maintenance from a group- or member-targeted window) and never
   * exposes its members publicly.
   */
  checkUid?: string;
  checkGroupUid?: string;
  publicName?: string;
  explanation?: string;
  /**
   * Per-resource auto-publish override (spec 2026-08-19-08). Three-state:
   * absent means "inherit the page", which is NOT the same as false.
   */
  autoPublish?: boolean;
  /**
   * True when the row was materialized by the section's selector and is owned
   * by it (spec 2026-08-29-11): it cannot be deleted or re-targeted here —
   * change the selector, or add the check manually, which always wins.
   */
  managedBySelector?: boolean;
  position: number;
  check?: {
    name?: string;
    /** Check type for a check resource; empty for a group resource. */
    type: string;
    status: string;
  };
  createdAt?: string;
}

export interface CreateStatusPageRequest {
  name: string;
  slug: string;
  description?: string;
  visibility?: StatusPageVisibility;
  /** Write-only. Required when visibility is "password"; never read back. */
  password?: string;
  hideBranding?: boolean;
  isDefault?: boolean;
  showAvailability?: boolean;
  showResponseTime?: boolean;
  historyDays?: number;
  historyPeriod?: StatusPagePeriod;
  autoPublish?: boolean;
  autoPublishDelaySeconds?: number;
  autoResolve?: "always" | "if_untouched" | "never";
  customCss?: string;
  customDomain?: string;
  /**
   * Optional checks to seed the page with. Every new page always gets a
   * default "Services" section; each UID here becomes one resource in that
   * section, in request order. Every UID must resolve to a check in this
   * org, or the whole request is rejected — nothing is created, including
   * the page itself.
   */
  checkUids?: string[];
}

export interface UpdateStatusPageRequest {
  name?: string;
  slug?: string;
  description?: string;
  visibility?: StatusPageVisibility;
  /**
   * Write-only. A non-empty string sets the unlock password (and invalidates
   * every outstanding unlock cookie); an empty string clears it; omit to leave
   * it untouched. Never read back.
   */
  password?: string;
  hideBranding?: boolean;
  isDefault?: boolean;
  enabled?: boolean;
  showAvailability?: boolean;
  showResponseTime?: boolean;
  historyDays?: number;
  historyPeriod?: StatusPagePeriod;
  autoPublish?: boolean;
  autoPublishDelaySeconds?: number;
  autoResolve?: "always" | "if_untouched" | "never";
  // An empty string clears the custom stylesheet; omit to leave it unchanged.
  customCss?: string;
  // null clears the custom domain; a non-empty string sets it; omit to leave
  // it unchanged.
  customDomain?: string | null;
  // No deep merge: omit to leave settings untouched; when present, each
  // section provided (e.g. availability) replaces that section wholly, and
  // an explicit `{ availability: null }` resets it to defaults.
  settings?: { availability?: AvailabilitySettings | null } | null;
}

export interface CreateSectionRequest {
  name: string;
  slug: string;
  position?: number;
  /** Optional membership rule. Omit for a normal hand-curated section. */
  selector?: StatusPageSectionSelector;
}

export interface UpdateSectionRequest {
  name?: string;
  slug?: string;
  position?: number;
  /**
   * Three-state, matching the API: omit the key to leave the rule alone, send
   * an object to replace it, or send `null` to clear it (which also removes
   * the components the selector owned).
   */
  selector?: StatusPageSectionSelector | null;
}

/**
 * Exactly one of checkUid / checkGroupUid must be set; zero or both is a
 * VALIDATION_ERROR naming both fields.
 */
export interface CreateResourceRequest {
  checkUid?: string;
  checkGroupUid?: string;
  publicName?: string;
  explanation?: string;
  position?: number;
}

/**
 * Supplying checkUid or checkGroupUid switches the resource's target kind;
 * supplying both is a validation error, supplying neither leaves it untouched.
 */
export interface UpdateResourceRequest {
  checkUid?: string;
  checkGroupUid?: string;
  publicName?: string;
  explanation?: string;
  position?: number;
}

// Status Page hooks
export function useStatusPages(org: string, opts?: ListQueryOptions) {
  return useQuery({
    queryKey: ["statusPages", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: StatusPage[] }>(
        `/api/v1/orgs/${org}/status-pages`,
      );
      return response.data || [];
    },
    enabled: (opts?.enabled ?? true) && !!org,
    staleTime: opts?.staleTime,
  });
}

export function useStatusPage(
  org: string,
  uid: string,
  options?: { with?: string },
) {
  return useQuery({
    queryKey: ["statusPage", org, uid, { with: options?.with }],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.with) params.set("with", options.with);
      const query = params.toString();
      const path = `/api/v1/orgs/${org}/status-pages/${uid}${query ? `?${query}` : ""}`;
      return apiFetch<StatusPage>(path);
    },
    enabled: !!org && !!uid,
  });
}

export function useCreateStatusPage(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateStatusPageRequest) =>
      apiFetch<StatusPage>(`/api/v1/orgs/${org}/status-pages`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPages", org] });
    },
  });
}

export function useUpdateStatusPage(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateStatusPageRequest) =>
      apiFetch<StatusPage>(`/api/v1/orgs/${org}/status-pages/${uid}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPages", org] });
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, uid] });
    },
  });
}

/**
 * The one and only time a kiosk token is readable (spec 2026-08-29-08). Only
 * its sha256 is stored, so an operator who loses it regenerates rather than
 * recovers — the same contract as an API token.
 */
export interface KioskTokenResponse {
  token: string;
  hasKioskToken: boolean;
}

/**
 * Mints or REGENERATES the page's kiosk token. Regenerating replaces the
 * stored hash, so the screen still using the old URL stops working the moment
 * this resolves — which is why the UI asks before calling it on a page that
 * already has one.
 */
export function useGenerateKioskToken(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () =>
      apiFetch<KioskTokenResponse>(
        `/api/v1/orgs/${org}/status-pages/${uid}/kiosk-token`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, uid] });
      queryClient.invalidateQueries({ queryKey: ["statusPages", org] });
    },
  });
}

/** Revokes the page's kiosk token. Idempotent server-side. */
export function useRevokeKioskToken(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () =>
      apiFetch<void>(`/api/v1/orgs/${org}/status-pages/${uid}/kiosk-token`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, uid] });
      queryClient.invalidateQueries({ queryKey: ["statusPages", org] });
    },
  });
}

/**
 * Response of the status-page asset endpoints — just enough of the page for
 * the caller to update what it already holds without a refetch.
 */
export interface StatusPageAssetResponse {
  uid: string;
  slug: string;
  name: string;
  logoUrl: string | null;
  faviconUrl: string | null;
}

/** The asset slots a status page has. One logo, one favicon — never a list. */
export type StatusPageAssetKind = "logo" | "favicon";

// useUploadStatusPageAsset posts the image as multipart/form-data. The
// Content-Type header is deliberately left unset so the browser adds the
// multipart boundary (same reason as useUploadOrgLogo).
export function useUploadStatusPageAsset(
  org: string,
  uid: string,
  kind: StatusPageAssetKind,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (file: File) => {
      const body = new FormData();
      body.append(kind, file);
      return apiFetch<StatusPageAssetResponse>(
        `/api/v1/orgs/${org}/status-pages/${uid}/${kind}`,
        { method: "POST", body },
      );
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, uid] });
      queryClient.invalidateQueries({ queryKey: ["statusPages", org] });
    },
  });
}

export function useClearStatusPageAsset(
  org: string,
  uid: string,
  kind: StatusPageAssetKind,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<StatusPageAssetResponse>(
        `/api/v1/orgs/${org}/status-pages/${uid}/${kind}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, uid] });
      queryClient.invalidateQueries({ queryKey: ["statusPages", org] });
    },
  });
}

export function useDeleteStatusPage(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/status-pages/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPages", org] });
    },
  });
}

// useVerifyStatusPageDomain runs the synchronous DNS verification for a status
// page's custom domain and returns the updated page.
export function useVerifyStatusPageDomain(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () =>
      apiFetch<StatusPage>(
        `/api/v1/orgs/${org}/status-pages/${uid}/custom-domain/verify`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPages", org] });
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, uid] });
    },
  });
}

// Status page subscriber hooks (read-only admin list + remove).
/** Where a status-page subscription delivers. */
export type StatusPageSubscriberChannel = "email" | "webhook" | "slack";

export interface StatusPageSubscriber {
  uid: string;
  /** Empty for a webhook/Slack subscription. */
  email: string;
  channel: StatusPageSubscriberChannel;
  /**
   * MASKED delivery URL. The API never returns the real one — an
   * incoming-webhook URL is a credential — so this is only ever enough to
   * recognise which endpoint a row is.
   */
  endpoint?: string;
  scope: string;
  incidentUid?: string;
  confirmed: boolean;
  /** Consecutive delivery failures; reset by a success. */
  failureCount?: number;
  /** True once the circuit breaker disabled the subscription. */
  disabled?: boolean;
  createdAt: string;
}

export interface CreateEndpointSubscriberRequest {
  channel: Exclude<StatusPageSubscriberChannel, "email">;
  url: string;
  /**
   * Optional. When omitted the server generates one and returns it in the
   * create response — see CreateEndpointSubscriberResponse. Either way it is
   * never readable again afterwards.
   */
  signingSecret?: string;
}

/**
 * Create-only response. `signingSecret` is returned EXACTLY ONCE, here: the
 * receiver needs it to verify the HMAC on every delivery, and it is never
 * stored in a readable column nor echoed by the list endpoint.
 */
export interface CreateEndpointSubscriberResponse extends StatusPageSubscriber {
  signingSecret: string;
}

/**
 * Registers a webhook or Slack delivery for a status page.
 *
 * Operator-side by design: the public subscribe endpoint is email-only,
 * because a visitor pasting an incoming-webhook URL has no verification story.
 */
export function useCreateEndpointSubscriber(
  org: string,
  statusPageUid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateEndpointSubscriberRequest) =>
      apiFetch<{ data: CreateEndpointSubscriberResponse }>(
        // `/subscribers/endpoints`, not `/subscribers`: the latter is the
        // PUBLIC email-only subscribe route, and the two cannot share a
        // pattern on the router (see app/server.go).
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/subscribers/endpoints`,
        { method: "POST", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageSubscribers", org, statusPageUid],
      });
    },
  });
}

export function useStatusPageSubscribers(org: string, statusPageUid: string) {
  return useQuery({
    queryKey: ["statusPageSubscribers", org, statusPageUid],
    queryFn: async () => {
      const response = await apiFetch<{
        data?: StatusPageSubscriber[];
        meta?: { count: number };
      }>(`/api/v1/orgs/${org}/status-pages/${statusPageUid}/subscribers`);
      return {
        subscribers: response.data ?? [],
        count: response.meta?.count ?? response.data?.length ?? 0,
      };
    },
    enabled: !!org && !!statusPageUid,
  });
}

export function useDeleteStatusPageSubscriber(
  org: string,
  statusPageUid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/subscribers/${uid}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageSubscribers", org, statusPageUid],
      });
    },
  });
}

// Section hooks
export function useStatusPageSections(org: string, statusPageUid: string) {
  return useQuery({
    queryKey: ["statusPageSections", org, statusPageUid],
    queryFn: async () => {
      const response = await apiFetch<{ data?: StatusPageSection[] }>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections`,
      );
      return response.data || [];
    },
    enabled: !!org && !!statusPageUid,
  });
}

export function useCreateSection(org: string, statusPageUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateSectionRequest) =>
      apiFetch<StatusPageSection>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections`,
        { method: "POST", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageSections", org, statusPageUid],
      });
      queryClient.invalidateQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
    },
  });
}

export function useUpdateSection(
  org: string,
  statusPageUid: string,
  sectionUid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateSectionRequest) =>
      apiFetch<StatusPageSection>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}`,
        { method: "PATCH", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageSections", org, statusPageUid],
      });
      queryClient.invalidateQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
    },
  });
}

export function useDeleteSection(org: string, statusPageUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (sectionUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageSections", org, statusPageUid],
      });
      queryClient.invalidateQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
    },
  });
}

// Resource hooks
export function useStatusPageResources(
  org: string,
  statusPageUid: string,
  sectionUid: string,
) {
  return useQuery({
    queryKey: ["statusPageResources", org, statusPageUid, sectionUid],
    queryFn: async () => {
      const response = await apiFetch<{ data?: StatusPageResource[] }>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources`,
      );
      return response.data || [];
    },
    enabled: !!org && !!statusPageUid && !!sectionUid,
  });
}

export function useCreateResource(
  org: string,
  statusPageUid: string,
  sectionUid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateResourceRequest) =>
      apiFetch<StatusPageResource>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources`,
        { method: "POST", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageResources", org, statusPageUid, sectionUid],
      });
      queryClient.invalidateQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
    },
  });
}

// Reorder all resources in a section in one round-trip. The dashboard's
// drag-and-drop UI sends the new ordered list of UIDs; the backend renumbers
// `position` to match.
//
// Optimistically updates the cached `useStatusPage` payload so the React
// tree already reflects the new order by the time dnd-kit runs the drop
// animation — without it the dragged item visually snaps back to its
// original slot before the server roundtrip lands.
export function useReorderSections(org: string, statusPageUid: string) {
  const queryClient = useQueryClient();
  const pageWithSectionsKey = [
    "statusPage",
    org,
    statusPageUid,
    { with: "sections" },
  ];

  return useMutation({
    mutationFn: (uids: string[]) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/reorder`,
        { method: "POST", body: JSON.stringify({ uids }) },
      ),
    onMutate: async (uids) => {
      await queryClient.cancelQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
      const snapshot =
        queryClient.getQueryData<StatusPage>(pageWithSectionsKey);
      if (snapshot?.sections) {
        const byUid = new Map(snapshot.sections.map((s) => [s.uid, s]));
        const reordered = uids
          .map((uid) => byUid.get(uid))
          .filter((s): s is StatusPageSection => Boolean(s));
        queryClient.setQueryData<StatusPage>(pageWithSectionsKey, {
          ...snapshot,
          sections: reordered,
        });
      }
      return { snapshot };
    },
    onError: (_err, _uids, context) => {
      if (context?.snapshot) {
        queryClient.setQueryData(pageWithSectionsKey, context.snapshot);
      }
    },
    onSettled: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
    },
  });
}

export function useReorderResources(
  org: string,
  statusPageUid: string,
  sectionUid: string,
) {
  const queryClient = useQueryClient();
  const pageWithSectionsKey = [
    "statusPage",
    org,
    statusPageUid,
    { with: "sections" },
  ];

  return useMutation({
    mutationFn: (uids: string[]) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources/reorder`,
        { method: "POST", body: JSON.stringify({ uids }) },
      ),
    onMutate: async (uids) => {
      await queryClient.cancelQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
      const snapshot =
        queryClient.getQueryData<StatusPage>(pageWithSectionsKey);
      if (snapshot) {
        queryClient.setQueryData<StatusPage>(pageWithSectionsKey, {
          ...snapshot,
          sections: snapshot.sections?.map((s) => {
            if (s.uid !== sectionUid || !s.resources) return s;
            const byUid = new Map(s.resources.map((r) => [r.uid, r]));
            const reordered = uids
              .map((uid) => byUid.get(uid))
              .filter((r): r is StatusPageResource => Boolean(r));
            return { ...s, resources: reordered };
          }),
        });
      }
      return { snapshot };
    },
    onError: (_err, _uids, context) => {
      if (context?.snapshot) {
        queryClient.setQueryData(pageWithSectionsKey, context.snapshot);
      }
    },
    onSettled: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageResources", org, statusPageUid, sectionUid],
      });
      queryClient.invalidateQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
    },
  });
}

export function useUpdateResource(
  org: string,
  statusPageUid: string,
  sectionUid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      resourceUid,
      request,
    }: {
      resourceUid: string;
      request: UpdateResourceRequest;
    }) =>
      apiFetch<StatusPageResource>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources/${resourceUid}`,
        { method: "PATCH", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageResources", org, statusPageUid, sectionUid],
      });
      queryClient.invalidateQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
    },
  });
}

export function useDeleteResource(
  org: string,
  statusPageUid: string,
  sectionUid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (resourceUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources/${resourceUid}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageResources", org, statusPageUid, sectionUid],
      });
      queryClient.invalidateQueries({
        queryKey: ["statusPage", org, statusPageUid],
      });
    },
  });
}

// Auth providers hook
export interface AuthProvider {
  name: string;
  type: string;
}

interface ProvidersResponse {
  data?: AuthProvider[];
  registrationEnabled?: boolean;
}

export function useProviders() {
  return useQuery({
    queryKey: ["providers"],
    queryFn: async () => {
      const response = await apiFetch<ProvidersResponse>(
        "/api/v1/auth/providers",
        { skipAuth: true },
      );
      return {
        providers: response.data || [],
        registrationEnabled: response.registrationEnabled || false,
      };
    },
    staleTime: Infinity,
  });
}

// Registration hooks
export function useRegister() {
  return useMutation({
    mutationFn: (data: { name?: string; email: string; password: string }) =>
      apiFetch<{ message: string }>("/api/v1/auth/register", {
        method: "POST",
        body: JSON.stringify(data),
        skipAuth: true,
      }),
  });
}

export function useConfirmRegistration() {
  return useMutation({
    mutationFn: (data: { token: string }) =>
      // Login-shaped response — the backend mints a full session (refresh
      // token + expiry included) just like password/OAuth login, so both
      // fields must be captured here and passed to setSession, not dropped
      // (2026-07-08 funnel audit). accessToken is optional in the type
      // (not just in practice): applyConfirmRegistrationHandoff treats its
      // absence as a failure path rather than assuming the string always
      // shows up (spec 2026-08-29-06).
      apiFetch<{
        accessToken?: string;
        refreshToken?: string;
        expiresIn?: number;
        user: {
          email: string;
          name?: string;
          avatarUrl?: string;
          role: string;
        };
        organization?: { uid: string; slug: string; name?: string };
      }>("/api/v1/auth/confirm-registration", {
        method: "POST",
        body: JSON.stringify(data),
        skipAuth: true,
      }),
  });
}

export function useRequestPasswordReset() {
  return useMutation({
    mutationFn: (data: { email: string }) =>
      apiFetch<{ message: string }>("/api/v1/auth/request-password-reset", {
        method: "POST",
        body: JSON.stringify(data),
        skipAuth: true,
      }),
  });
}

export function useResetPassword() {
  return useMutation({
    mutationFn: (data: { token: string; password: string }) =>
      apiFetch<{ message: string }>("/api/v1/auth/reset-password", {
        method: "POST",
        body: JSON.stringify(data),
        skipAuth: true,
      }),
  });
}

// Profile update hook
export function useUpdateProfile() {
  return useMutation({
    mutationFn: (data: { name: string }) =>
      apiFetch<{
        user: {
          uid: string;
          email: string;
          name?: string;
          avatarUrl?: string;
          role: string;
        };
        organization: { uid: string; slug: string; name?: string };
        organizations: { slug: string; name?: string; role: string }[];
      }>("/api/v1/auth/me", {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
  });
}

// Organization creation hook. The response carries a fresh session
// (accessToken/refreshToken/expiresIn/tokenType) scoped to the new org —
// the caller must adopt it (see api/client.ts setSession) before navigating
// into the org, or every org-scoped call 403s.
export function useCreateOrg() {
  return useMutation({
    mutationFn: (data: { name: string; slug: string }) =>
      apiFetch<{
        uid: string;
        slug: string;
        name: string;
        accessToken: string;
        refreshToken: string;
        expiresIn: number;
        tokenType: string;
      }>("/api/v1/orgs", {
        method: "POST",
        body: JSON.stringify(data),
      }),
  });
}

// Invitation hooks
export interface Invitation {
  uid: string;
  email: string;
  role: string;
  createdAt: string;
  expiresAt: string;
}

export function useInvitations(org: string, opts?: ListQueryOptions) {
  return useQuery({
    queryKey: ["invitations", org],
    queryFn: () =>
      apiFetch<{ data: Invitation[] }>(`/api/v1/orgs/${org}/invitations`),
    enabled: opts?.enabled ?? true,
    staleTime: opts?.staleTime,
  });
}

export interface CreateInvitationResponse {
  token: string;
  inviteUrl: string;
  // Whether the invitation email was queued for delivery — not proof it
  // reached an inbox, since delivery is async. False when email sending is
  // disabled on this instance, or when enqueueing the job failed; the
  // dashboard must then treat inviteUrl as the only channel.
  emailSent: boolean;
}

export function useCreateInvitation(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { email: string; role: string }) =>
      apiFetch<CreateInvitationResponse>(`/api/v1/orgs/${org}/invitations`, {
        method: "POST",
        body: JSON.stringify(data),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["invitations", org] });
    },
  });
}

export function useRevokeInvitation(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/invitations/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["invitations", org] });
    },
  });
}

// Membership-request hooks
export type MembershipRequestStatus =
  | "pending"
  | "approved"
  | "rejected"
  | "canceled";

export interface MembershipRequestSummary {
  uid: string;
  organization: { uid: string; slug: string; name: string };
  status: MembershipRequestStatus;
  message?: string;
  decisionReason?: string;
  createdAt: string;
  decidedAt?: string;
}

export interface MembershipRequestAdminView {
  uid: string;
  user: { uid: string; email: string; name?: string; avatarUrl?: string };
  status: MembershipRequestStatus;
  message?: string;
  decisionReason?: string;
  createdAt: string;
  decidedAt?: string;
}

export function useCreateMembershipRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { orgSlug: string; message?: string }) =>
      apiFetch<MembershipRequestSummary>("/api/v1/auth/membership-requests", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["membership-requests", "me"],
      });
      queryClient.invalidateQueries({ queryKey: ["auth", "me"] });
    },
  });
}

export function useMyMembershipRequests() {
  return useQuery({
    queryKey: ["membership-requests", "me"],
    queryFn: () =>
      apiFetch<{ data: MembershipRequestSummary[] }>(
        "/api/v1/auth/membership-requests",
      ),
  });
}

export function useCancelMembershipRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/auth/membership-requests/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["membership-requests", "me"],
      });
      queryClient.invalidateQueries({ queryKey: ["auth", "me"] });
    },
  });
}

export function useOrgMembershipRequests(
  org: string,
  opts?: { status?: MembershipRequestStatus; enabled?: boolean },
) {
  const status = opts?.status;
  const qs = status ? `?status=${status}` : "";
  return useQuery({
    queryKey: ["membership-requests", "org", org, status ?? "all"],
    queryFn: () =>
      apiFetch<{ data: MembershipRequestAdminView[] }>(
        `/api/v1/orgs/${org}/membership-requests${qs}`,
      ),
    enabled: opts?.enabled !== false,
  });
}

export function useApproveMembershipRequest(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ uid, role }: { uid: string; role?: string }) =>
      apiFetch<void>(`/api/v1/orgs/${org}/membership-requests/${uid}/approve`, {
        method: "POST",
        body: JSON.stringify(role ? { role } : {}),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["membership-requests", "org", org],
      });
      queryClient.invalidateQueries({ queryKey: ["members", org] });
    },
  });
}

export function useRejectMembershipRequest(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ uid, reason }: { uid: string; reason?: string }) =>
      apiFetch<void>(`/api/v1/orgs/${org}/membership-requests/${uid}/reject`, {
        method: "POST",
        body: JSON.stringify(reason ? { reason } : {}),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["membership-requests", "org", org],
      });
    },
  });
}

// Member hooks
// Ordered most- to least-privileged: owner > admin > user > viewer.
export type MemberRole = "owner" | "admin" | "user" | "viewer";

export interface MemberResponse {
  uid: string;
  userUid: string;
  email: string;
  name?: string;
  avatarUrl?: string;
  role: MemberRole;
  joinedAt?: string;
  createdAt: string;
}

export function useMembers(org: string, opts?: ListQueryOptions) {
  return useQuery({
    queryKey: ["members", org],
    queryFn: () =>
      apiFetch<{ data: MemberResponse[] }>(`/api/v1/orgs/${org}/members`),
    enabled: (opts?.enabled ?? true) && !!org,
    staleTime: opts?.staleTime,
  });
}

export function useUpdateMember(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ uid, role }: { uid: string; role: MemberRole }) =>
      apiFetch<MemberResponse>(`/api/v1/orgs/${org}/members/${uid}`, {
        method: "PATCH",
        body: JSON.stringify({ role }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["members", org] });
    },
  });
}

export function useRemoveMember(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/members/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["members", org] });
    },
  });
}

// DeleteOrgResponse is the login-shaped session DELETE /orgs/:org answers with:
// the org the caller's token named is gone, so the server mints a replacement
// scoped to a surviving org — or an org-less one (no `organization`, no
// `refreshToken`) when the caller belongs to nothing any more.
export interface DeleteOrgResponse {
  accessToken?: string;
  refreshToken?: string;
  expiresIn?: number;
  user?: {
    uid: string;
    email: string;
    name?: string;
    avatarUrl?: string;
    role: string;
  };
  organization?: { uid: string; slug: string; name?: string };
  organizations?: {
    slug: string;
    name?: string;
    logoUrl?: string;
    role: string;
  }[];
  loginAction?: string;
}

// useDeleteOrg deletes the whole organization. Owner-only, enforced server-side
// by the RequireOrgOwner middleware — an admin gets a 403 even if the UI slipped
// and showed them the button. The body repeats the slug as confirmation.
//
// Deliberately NO cache invalidation here. `queryClient.clear()` used to run in
// onSuccess, i.e. while the app was still mounted on the deleted org's routes:
// every cleared query immediately refetched against a slug that now 404s, and
// those failures raced the navigation into "session expired". The caller adopts
// the returned session, navigates away, and only then evicts the dead org's
// queries (see organization.settings.tsx).
export function useDeleteOrg(org: string) {
  return useMutation({
    mutationFn: (slug: string) =>
      apiFetch<DeleteOrgResponse>(`/api/v1/orgs/${org}`, {
        method: "DELETE",
        body: JSON.stringify({ slug }),
      }),
  });
}

// --- Organization profile (name / slug / logo) -----------------------------
// Owner-only, enforced server-side by RequireOrgOwner (spec 2026-08-08-12).

export interface UpdateOrgProfileRequest {
  name?: string;
  slug?: string;
  // An absolute http(s) URL sets the logo; null clears it; omit to leave it
  // untouched (the server distinguishes "absent" from "null").
  logoUrl?: string | null;
}

export interface OrgProfileResponse {
  uid: string;
  slug: string;
  name: string;
  logoUrl: string | null;
  // Set only when the slug changed. The old slug keeps redirecting to the new
  // one until another organization claims it.
  previousSlug?: string;
  // A rename re-mints the session for the new slug — the caller must adopt it
  // (AuthContext.adoptRenamedOrgSession) before navigating.
  accessToken?: string;
  refreshToken?: string;
  expiresIn?: number;
  tokenType?: string;
}

export function useUpdateOrgProfile(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateOrgProfileRequest) =>
      apiFetch<OrgProfileResponse>(`/api/v1/orgs/${org}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["org-settings", org] });
    },
  });
}

// useUploadOrgLogo posts the image as multipart/form-data. The Content-Type
// header is deliberately left unset so the browser adds the multipart boundary.
export function useUploadOrgLogo(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (file: File) => {
      const body = new FormData();
      body.append("logo", file);
      return apiFetch<OrgProfileResponse>(`/api/v1/orgs/${org}/logo`, {
        method: "POST",
        body,
      });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["org-settings", org] });
    },
  });
}

export function useClearOrgLogo(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<OrgProfileResponse>(`/api/v1/orgs/${org}/logo`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["org-settings", org] });
    },
  });
}

export interface InviteInfo {
  orgName: string;
  orgSlug: string;
  email: string;
  role: string;
}

export function useInviteInfo(token: string) {
  return useQuery({
    queryKey: ["invite-info", token],
    queryFn: () =>
      apiFetch<InviteInfo>(`/api/v1/auth/invite/${token}`, {
        skipAuth: true,
      }),
    enabled: !!token,
  });
}

export interface AcceptInviteResponse {
  accessToken: string;
  user: {
    uid: string;
    email: string;
    name?: string;
    avatarUrl?: string;
    role: string;
  };
  organization: { uid: string; slug: string; name?: string };
  organizations?: Array<{ slug: string; name?: string; role: string }>;
}

export function useAcceptInvite() {
  return useMutation({
    mutationFn: (data: { token: string; name?: string; password?: string }) =>
      apiFetch<AcceptInviteResponse>("/api/v1/auth/accept-invite", {
        method: "POST",
        body: JSON.stringify(data),
        skipAuth: true,
      }),
  });
}

// Org settings hooks
export interface OrgSettings {
  registrationEmailPattern: string;
  // Org-level auth.session_max_duration override, in seconds. Absent/null
  // when the org has no override and inherits the system-wide default (see
  // server OrgSettingsResponse — spec 2026-07-08-01 B.4).
  sessionMaxDurationSeconds?: number | null;
  // Org-wide default escalation policy for checks that resolve to no policy of
  // their own (check → group → org default → none). null/absent = unset.
  defaultEscalationPolicyUid?: string | null;
  // How many live checks currently resolve to no policy of their own — the
  // blast radius of setting/changing the org default. Always present.
  inheritingCheckCount?: number;
  // Org-level default for path-trace-on-failure (spec 2026-08-21-10). Applies
  // to every check whose own `tracerouteOnFailure` is `inherit`. Always
  // present, and true for an org that never set it.
  tracerouteOnFailure?: boolean;
}

export function useOrgSettings(org: string) {
  return useQuery({
    queryKey: ["org-settings", org],
    queryFn: () => apiFetch<OrgSettings>(`/api/v1/orgs/${org}/settings`),
  });
}

export interface UpdateOrgSettingsRequest {
  registrationEmailPattern?: string;
  // A value <= 0 clears the override (org reverts to inheriting the
  // system-wide value); omit the field entirely to leave it untouched.
  sessionMaxDurationSeconds?: number;
  // A UID sets the org default escalation policy; "" clears it; omit to leave
  // it untouched.
  defaultEscalationPolicyUid?: string;
  // Org-level default for path-trace-on-failure; omit to leave it untouched.
  tracerouteOnFailure?: boolean;
}

export function useUpdateOrgSettings(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateOrgSettingsRequest) =>
      apiFetch<OrgSettings>(`/api/v1/orgs/${org}/settings`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["org-settings", org] });
    },
  });
}

// Server hooks
export function useHealth() {
  return useQuery({
    queryKey: ["health"],
    queryFn: () => apiFetch<{ status?: string }>("/api/mgmt/health"),
    refetchInterval: 30000,
  });
}

export function useVersion() {
  return useQuery({
    queryKey: ["version"],
    queryFn: () =>
      apiFetch<{
        version?: string;
        commit?: string;
        gitTime?: string;
        runMode?: string;
      }>("/api/mgmt/version"),
    // Poll for server redeploys (spec 2026-08-28-01: dash0 has no way to
    // tell the user their loaded app is older than the running server).
    // "always" ignores staleness so a laptop waking from sleep refetches on
    // focus/visibilitychange immediately instead of waiting out the rest of
    // the interval.
    refetchInterval: 15 * 60 * 1000,
    refetchOnWindowFocus: "always",
  });
}

/** One row of the dev-only email template catalog (GET /api/mgmt/email-preview). */
export interface EmailTemplateSummary {
  template: string;
  subject: string;
  hasText: boolean;
  previewUrl: string;
  error?: string;
}

/**
 * Lists the previewable email templates. The endpoint only exists when the
 * server runs with SP_RUNMODE=test — outside it the query 404s and the page
 * shows its "not available" state rather than an error toast.
 */
export function useEmailPreviewIndex() {
  return useQuery({
    queryKey: ["email-preview-index"],
    queryFn: () =>
      apiFetch<{ data: EmailTemplateSummary[] }>("/api/mgmt/email-preview"),
    staleTime: 30000,
    retry: false,
  });
}

/**
 * Embedded TCP/UDP heartbeat push transports (spec 2026-09-01-06).
 *
 * Reported by the server so the check detail page only advertises a transport
 * a device can actually reach — the listeners are off by default and opening
 * their ports is a deployment decision.
 */
export interface HeartbeatPushFeature {
  tcpEnabled: boolean;
  udpEnabled: boolean;
  /** Hostname devices should send beats to; empty when it can't be derived. */
  host: string;
  /** 0 when the matching transport is disabled. */
  tcpPort: number;
  udpPort: number;
}

export interface FeaturesResponse {
  bugReport: boolean;
  heartbeatPush?: HeartbeatPushFeature;
}

export function useFeatures(opts?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ["features"],
    queryFn: () => apiFetch<FeaturesResponse>("/api/v1/features"),
    staleTime: 5 * 60 * 1000,
    enabled: opts?.enabled ?? true,
  });
}

// Bulk test checks hooks
export interface BulkCreateChecksParams {
  org: string;
  type: string;
  slug: string;
  url?: string;
  name?: string;
  period?: string;
  count: number;
}

export interface BulkCreateChecksResponse {
  created: number;
  failed: number;
  errors?: string[];
  firstSlug?: string;
  lastSlug?: string;
}

export interface BulkDeleteChecksResponse {
  deleted: number;
}

export function useBulkCreateChecks() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      org,
      type,
      slug,
      url,
      name,
      period,
      count,
    }: BulkCreateChecksParams) => {
      const params = new URLSearchParams({
        type,
        slug,
        count: String(count),
        org,
      });
      if (url) params.set("url", url);
      if (name) params.set("name", name);
      if (period) params.set("period", period);
      return apiFetch<BulkCreateChecksResponse>(
        `/api/v1/test/checks/bulk?${params}`,
        { method: "POST" },
      );
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ["checks", variables.org] });
    },
  });
}

export function useBulkDeleteChecks() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      org,
      slug,
      count,
    }: {
      org: string;
      slug: string;
      count: number;
    }) => {
      const params = new URLSearchParams({
        slug,
        count: String(count),
        org,
      });
      return apiFetch<BulkDeleteChecksResponse>(
        `/api/v1/test/checks/bulk?${params}`,
        { method: "DELETE" },
      );
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ["checks", variables.org] });
    },
  });
}

// Generate data hook
export interface GenerateDataParams {
  org: string;
  name: string;
  checkPeriodSec: number;
  startDate: string;
  failureRate: number;
  failureBurstSec: number;
  avgDurationMs: number;
}

export interface GenerateDataResponse {
  checkUid: string;
  checkSlug: string;
  resultsCount: number;
}

export function useGenerateData() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (params: GenerateDataParams) =>
      apiFetch<GenerateDataResponse>("/api/v1/test/generate-data", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(params),
      }),
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ["checks", variables.org] });
    },
  });
}

// Reset hooks
export interface ResetChecksResponse {
  deleted: number;
  failed: number;
}

export function useDeleteAllChecks() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (org: string) =>
      apiFetch<ResetChecksResponse>(`/api/v1/test/checks/all?org=${org}`, {
        method: "DELETE",
      }),
    onSuccess: (_, org) => {
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
    },
  });
}

// System parameters hooks
export interface SystemParameter {
  key: string;
  value: unknown;
  secret: boolean;
  updatedAt: string;
}

interface SystemParametersResponse {
  data: SystemParameter[];
  // Keys whose effective value is forced by an SP_* environment variable, so
  // a DB edit made here would appear not to take effect. Names only, no values.
  envOverrides?: string[];
}

async function fetchSystemParameters(): Promise<SystemParametersResponse> {
  const response = await apiFetch<SystemParametersResponse>(
    "/api/v1/system/parameters",
  );
  return {
    data: response.data || [],
    envOverrides: response.envOverrides || [],
  };
}

export function useSystemParameters() {
  return useQuery({
    queryKey: ["system-parameters"],
    queryFn: fetchSystemParameters,
    select: (response: SystemParametersResponse) => response.data,
  });
}

/**
 * Parameter keys currently overridden by an environment variable. Shares the
 * `system-parameters` query cache, so this costs no extra request.
 */
export function useSystemParameterEnvOverrides() {
  return useQuery({
    queryKey: ["system-parameters"],
    queryFn: fetchSystemParameters,
    select: (response: SystemParametersResponse) => response.envOverrides ?? [],
  });
}

// Per-worker check-lane load report (super-admin): job counts, summed
// cost/delay EWMAs, and summed duty cycle per lane, computed server-side.
export interface LaneLoadStats {
  jobs: number;
  costEwmaSumMs: number;
  delayEwmaSumMs: number;
  dutySumPct: number;
}

export interface WorkerLaneLoad {
  workerUid: string;
  name: string;
  region?: string;
  lastActiveAt?: string;
  fast: LaneLoadStats;
  slow: LaneLoadStats;
}

export function useSchedulingLaneLoad() {
  return useQuery({
    queryKey: ["system-scheduling-lane-load"],
    queryFn: async () => {
      const response = await apiFetch<{ data: WorkerLaneLoad[] }>(
        "/api/v1/system/scheduling/lane-load",
      );
      return response.data || [];
    },
    refetchInterval: 30000,
  });
}

export function useSetSystemParameter() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      key,
      value,
      secret,
    }: {
      key: string;
      value: unknown;
      secret?: boolean;
    }) =>
      apiFetch<SystemParameter>(`/api/v1/system/parameters/${key}`, {
        method: "PUT",
        body: JSON.stringify({ value, secret }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["system-parameters"] });
    },
  });
}

// Slack Socket Mode status hook
export interface SlackSocketStatus {
  enabled: boolean;
  connected: boolean;
  lastConnectedAt?: string;
  lastError?: string;
  teamCount?: number;
}

export function useSlackSocketStatus() {
  return useQuery({
    queryKey: ["slack-socket-status"],
    queryFn: async () =>
      apiFetch<SlackSocketStatus>("/api/v1/integrations/slack/socket/status"),
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  });
}

// Discord Gateway status hook. The Gateway is what carries everything a human
// types at the bot (thread replies, mention commands); the HTTP interactions
// endpoint only carries buttons and slash commands.
export interface DiscordGatewayStatus {
  enabled: boolean;
  connected: boolean;
  lastConnectedAt?: string;
  lastError?: string;
  guildCount?: number;
  botUserId?: string;
}

export function useDiscordGatewayStatus() {
  return useQuery({
    queryKey: ["discord-gateway-status"],
    queryFn: async () =>
      apiFetch<DiscordGatewayStatus>(
        "/api/v1/integrations/discord/gateway/status",
      ),
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  });
}

export function useTestEmail() {
  return useMutation({
    mutationFn: (recipient: string) =>
      apiFetch<{ sent: boolean; message: string }>(
        "/api/v1/system/test-email",
        {
          method: "POST",
          body: JSON.stringify({ recipient }),
        },
      ),
  });
}

// Region hooks
export function useRegions(org: string) {
  return useQuery({
    queryKey: ["regions", org],
    queryFn: async () => {
      const response = await apiFetch<{
        data?: RegionDefinition[];
        defaultRegions?: string[];
      }>(`/api/v1/orgs/${org}/regions`);
      return {
        regions: response.data || [],
        defaultRegions: response.defaultRegions || [],
      };
    },
    enabled: !!org,
  });
}

export interface SampleConfig {
  name: string;
  slug: string;
  periodSeconds: number;
  config: Record<string, unknown>;
}

export interface CheckTypeInfo {
  type: string;
  description: string;
  labels: string[];
  enabled: boolean;
  disabledReason?: string;
  minPeriodSeconds?: number;
  maxPeriodSeconds?: number;
  defaultPeriodSeconds?: number;
  /**
   * True when the type can be run through an SSH check's tunnel — i.e. its
   * config may carry `tunnelCheckUid`. Server-declared capability metadata, so
   * the form gates its tunnel selector on this rather than on a hard-coded
   * type list that would drift as more checkers gain tunnel support.
   */
  supportsTunnel?: boolean;
  /**
   * True when the type honors the shared `ipVersion` config key
   * (auto/ipv4/ipv6). Server-declared capability metadata, for the same reason
   * as supportsTunnel: the form gates its selector on this rather than on a
   * hard-coded type list.
   */
  supportsIpVersion?: boolean;
}

export function useCheckTypes(org: string) {
  return useQuery({
    queryKey: ["check-types", org],
    queryFn: async () => {
      const response = await apiFetch<{ data: CheckTypeInfo[] }>(
        `/api/v1/orgs/${org}/check-types`,
      );
      return response.data || [];
    },
    staleTime: 5 * 60 * 1000, // 5 min cache — types rarely change
    enabled: !!org,
  });
}

export function useSampleConfigs(checkType: string) {
  return useQuery({
    queryKey: ["check-types", "samples", checkType],
    queryFn: async () => {
      const response = await apiFetch<{
        data: Array<{ checkType: string; samples: SampleConfig[] }>;
      }>(`/api/v1/check-types/samples?type=${encodeURIComponent(checkType)}`);
      return response.data?.[0]?.samples || [];
    },
    staleTime: 10 * 60 * 1000,
    enabled: false, // manually triggered via refetch
  });
}

// ============================================================================
// On-call schedules
// ============================================================================

export type OnCallRotationType = "daily" | "weekly";

export interface OnCallUserRef {
  uid: string;
  name: string;
  email: string;
}

export interface OnCallSchedule {
  uid: string;
  name: string;
  description?: string;
  timezone: string;
  rotationType: OnCallRotationType;
  handoffTime: string;
  handoffWeekday?: number;
  startAt: string;
  icalEnabled: boolean;
  createdAt: string;
  updatedAt: string;
  userUids?: string[];
  currentlyOnCall?: OnCallUserRef;
}

export interface OnCallPreviewSlot {
  userUid: string;
  from: string;
  to: string;
}

export interface OnCallOverride {
  uid: string;
  scheduleUid: string;
  userUid: string;
  startAt: string;
  endAt: string;
  reason?: string;
  createdByUid?: string;
  createdAt: string;
}

export interface CreateOnCallScheduleRequest {
  name: string;
  description?: string;
  timezone: string;
  rotationType: OnCallRotationType;
  handoffTime: string;
  handoffWeekday?: number;
  startAt: string;
  userUids: string[];
}

export interface UpdateOnCallScheduleRequest {
  name?: string;
  description?: string;
  timezone?: string;
  rotationType?: OnCallRotationType;
  handoffTime?: string;
  handoffWeekday?: number;
  startAt?: string;
  userUids?: string[];
}

export interface CreateOnCallOverrideRequest {
  userUid: string;
  startAt: string;
  endAt: string;
  reason?: string;
}

export function useOnCallSchedules(org: string) {
  return useQuery({
    queryKey: ["onCallSchedules", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: OnCallSchedule[] }>(
        `/api/v1/orgs/${org}/on-call-schedules`,
      );
      return response.data || [];
    },
    enabled: !!org,
  });
}

export function useOnCallSchedule(org: string, uid: string) {
  return useQuery({
    queryKey: ["onCallSchedules", org, uid],
    queryFn: () =>
      apiFetch<OnCallSchedule>(`/api/v1/orgs/${org}/on-call-schedules/${uid}`),
    enabled: !!org && !!uid,
  });
}

export function useOnCallSchedulePreview(org: string, uid: string, days = 14) {
  return useQuery({
    queryKey: ["onCallSchedules", org, uid, "preview", days],
    queryFn: async () => {
      const response = await apiFetch<{ data?: OnCallPreviewSlot[] }>(
        `/api/v1/orgs/${org}/on-call-schedules/${uid}/preview?days=${days}`,
      );
      return response.data || [];
    },
    enabled: !!org && !!uid,
  });
}

export function useOnCallScheduleOverrides(org: string, uid: string) {
  return useQuery({
    queryKey: ["onCallSchedules", org, uid, "overrides"],
    queryFn: async () => {
      const response = await apiFetch<{ data?: OnCallOverride[] }>(
        `/api/v1/orgs/${org}/on-call-schedules/${uid}/overrides`,
      );
      return response.data || [];
    },
    enabled: !!org && !!uid,
  });
}

export function useCreateOnCallSchedule(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateOnCallScheduleRequest) =>
      apiFetch<OnCallSchedule>(`/api/v1/orgs/${org}/on-call-schedules`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["onCallSchedules", org] });
    },
  });
}

export function useUpdateOnCallSchedule(org: string, uid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: UpdateOnCallScheduleRequest) =>
      apiFetch<OnCallSchedule>(`/api/v1/orgs/${org}/on-call-schedules/${uid}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["onCallSchedules", org] });
    },
  });
}

export function useDeleteOnCallSchedule(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/on-call-schedules/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["onCallSchedules", org] });
    },
  });
}

export function useCreateOnCallOverride(org: string, uid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateOnCallOverrideRequest) =>
      apiFetch<OnCallOverride>(
        `/api/v1/orgs/${org}/on-call-schedules/${uid}/overrides`,
        { method: "POST", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, uid, "overrides"],
      });
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, uid, "preview"],
      });
    },
  });
}

export function useDeleteOnCallOverride(org: string, uid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (overrideUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/on-call-schedules/${uid}/overrides/${overrideUid}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, uid, "overrides"],
      });
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, uid, "preview"],
      });
    },
  });
}

export interface OnCallICalFeedResponse {
  secret: string;
  url: string;
}

export function useEnableOnCallICalFeed(org: string, uid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<OnCallICalFeedResponse>(
        `/api/v1/orgs/${org}/on-call-schedules/${uid}/ical-feed/enable`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, uid],
      });
    },
  });
}

export function useDisableOnCallICalFeed(org: string, uid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/on-call-schedules/${uid}/ical-feed/disable`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, uid],
      });
    },
  });
}

export function useRotateOnCallICalFeed(org: string, uid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<OnCallICalFeedResponse>(
        `/api/v1/orgs/${org}/on-call-schedules/${uid}/ical-feed/rotate`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, uid],
      });
    },
  });
}

// Escalation policies — orchestration that fires steps in order with
// delays, with cancellation on ack/snooze/resolve. Distinct from
// check_connections (per-check broadcast).
export type EscalationTargetType =
  | "user"
  | "schedule"
  | "connection"
  | "all_admins";

export interface EscalationPolicyTarget {
  uid?: string;
  type: EscalationTargetType;
  targetUid?: string;
  position?: number;
}

export interface EscalationPolicyStep {
  uid?: string;
  position: number;
  delaySeconds: number;
  // severityUid points at the per-org Severity row that gates which
  // channel-types fire when this step pages. null/undefined means
  // "no severity filter — fall through to the target's own channel".
  severityUid?: string | null;
  targets: EscalationPolicyTarget[];
}

// Severity is the per-org channel-set primitive (spec 2026-05-08-03).
// `channels` is a list of channel-type strings (e.g. "email", "slack",
// "sms", "voice", "critical_push").
export interface Severity {
  uid: string;
  slug: string;
  name: string;
  description?: string;
  channels: string[];
  isDefault: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface CreateSeverityRequest {
  slug: string;
  name: string;
  description?: string;
  channels: string[];
  isDefault?: boolean;
}

export interface UpdateSeverityRequest {
  slug?: string;
  name?: string;
  description?: string;
  channels?: string[];
  isDefault?: boolean;
}

export function useSeverities(org: string) {
  return useQuery({
    queryKey: ["severities", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: Severity[] }>(
        `/api/v1/orgs/${org}/severities`,
      );
      return response.data || [];
    },
    enabled: !!org,
  });
}

export function useSeverity(org: string, identifier: string) {
  return useQuery({
    queryKey: ["severity", org, identifier],
    queryFn: () =>
      apiFetch<Severity>(`/api/v1/orgs/${org}/severities/${identifier}`),
    enabled: !!org && !!identifier,
  });
}

export function useCreateSeverity(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateSeverityRequest) =>
      apiFetch<Severity>(`/api/v1/orgs/${org}/severities`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["severities", org] });
    },
  });
}

export function useUpdateSeverity(org: string, identifier: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: UpdateSeverityRequest) =>
      apiFetch<Severity>(`/api/v1/orgs/${org}/severities/${identifier}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["severities", org] });
      queryClient.invalidateQueries({
        queryKey: ["severity", org, identifier],
      });
    },
  });
}

export function useDeleteSeverity(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (identifier: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/severities/${identifier}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["severities", org] });
    },
  });
}

export interface EscalationPolicy {
  uid: string;
  name: string;
  description?: string;
  repeatMax: number;
  repeatAfterSeconds?: number | null;
  steps?: EscalationPolicyStep[];
  /** Number of steps; 0 = "silent" (pages nobody). Present on the list response. */
  stepCount?: number;
  /** Checks referencing this policy — list response only (delete-guard). */
  usageCheckCount?: number;
  /** Groups referencing this policy — list response only (delete-guard). */
  usageGroupCount?: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface CreateEscalationPolicyRequest {
  name: string;
  description?: string;
  repeatMax: number;
  repeatAfterSeconds?: number | null;
  steps: EscalationPolicyStep[];
}

export interface UpdateEscalationPolicyRequest {
  name?: string;
  description?: string;
  repeatMax?: number;
  repeatAfterSeconds?: number | null;
  steps?: EscalationPolicyStep[];
}

export function useEscalationPolicies(org: string, opts?: ListQueryOptions) {
  return useQuery({
    queryKey: ["escalationPolicies", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: EscalationPolicy[] }>(
        `/api/v1/orgs/${org}/escalation-policies`,
      );
      return response.data || [];
    },
    enabled: (opts?.enabled ?? true) && !!org,
    staleTime: opts?.staleTime,
  });
}

export function useEscalationPolicy(org: string, uid: string) {
  return useQuery({
    queryKey: ["escalationPolicies", org, uid],
    queryFn: () =>
      apiFetch<EscalationPolicy>(
        `/api/v1/orgs/${org}/escalation-policies/${uid}`,
      ),
    enabled: !!org && !!uid,
  });
}

export function useCreateEscalationPolicy(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateEscalationPolicyRequest) =>
      apiFetch<EscalationPolicy>(`/api/v1/orgs/${org}/escalation-policies`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: (created) => {
      // Seed the list cache synchronously so a caller that immediately
      // selects the newly created policy (e.g. the check form's "No
      // escalation (silent)" shortcut) can resolve it from the very next
      // render, instead of waiting on the invalidate-triggered refetch below.
      queryClient.setQueryData<EscalationPolicy[]>(
        ["escalationPolicies", org],
        (old) => (old ? [...old, created] : old),
      );
      queryClient.invalidateQueries({ queryKey: ["escalationPolicies", org] });
    },
  });
}

export function useUpdateEscalationPolicy(org: string, uid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: UpdateEscalationPolicyRequest) =>
      apiFetch<EscalationPolicy>(
        `/api/v1/orgs/${org}/escalation-policies/${uid}`,
        { method: "PATCH", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["escalationPolicies", org],
      });
    },
  });
}

export function useDeleteEscalationPolicy(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/escalation-policies/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["escalationPolicies", org] });
    },
  });
}

// Notification channels (integration_connections). The settings shape
// is per-type and intentionally narrow — we hand-write the per-type
// types rather than a single anything-goes blob.
export type ConnectionType =
  | "slack"
  | "discord"
  | "webhook"
  | "email"
  | "googlechat"
  | "mattermost"
  | "msteams"
  | "msteams-bot"
  | "ntfy"
  | "gotify"
  | "matrix"
  | "zulip"
  | "pagerduty"
  | "pushover"
  | "freebox"
  | "webpush"
  | "kubernetes"
  | "twilio";

// Capabilities mirror the backend capability registry
// (server/internal/db/models/integration.go `CapabilitiesFor`). The two flags
// are independent: a type may be both a notification sink and a data source.
//   - canNotify: can receive outbound notifications (acts as a "channel")
//   - canSource: provides data that checks read from
export interface IntegrationCapabilities {
  canNotify: boolean;
  canSource: boolean;
}

const NOTIFY: IntegrationCapabilities = { canNotify: true, canSource: false };
const SOURCE: IntegrationCapabilities = { canNotify: false, canSource: true };

export const CAPABILITIES: Record<ConnectionType, IntegrationCapabilities> = {
  slack: NOTIFY,
  discord: NOTIFY,
  webhook: NOTIFY,
  email: NOTIFY,
  googlechat: NOTIFY,
  mattermost: NOTIFY,
  msteams: NOTIFY,
  "msteams-bot": NOTIFY,
  ntfy: NOTIFY,
  gotify: NOTIFY,
  matrix: NOTIFY,
  zulip: NOTIFY,
  pagerduty: NOTIFY,
  pushover: NOTIFY,
  freebox: SOURCE,
  webpush: NOTIFY,
  kubernetes: SOURCE,
  twilio: NOTIFY,
};

/** Whether an integration type can receive notifications (act as a channel). */
export function canNotify(type: ConnectionType): boolean {
  return CAPABILITIES[type]?.canNotify ?? true;
}

/** Whether an integration type provides data that checks read from. */
export function canSource(type: ConnectionType): boolean {
  return CAPABILITIES[type]?.canSource ?? false;
}

export interface Integration {
  uid: string;
  type: ConnectionType;
  name: string;
  enabled: boolean;
  isDefault: boolean;
  settings?: Record<string, unknown>;
  settingsPrivateKeys?: string[];
  createdAt: string;
  updatedAt: string;
}

export interface CreateIntegrationRequest {
  type: ConnectionType;
  name: string;
  enabled?: boolean;
  isDefault?: boolean;
  settings?: Record<string, unknown>;
}

export interface UpdateIntegrationRequest {
  name?: string;
  enabled?: boolean;
  isDefault?: boolean;
  settings?: Record<string, unknown>;
}

export function useIntegrations(org: string, opts?: ListQueryOptions) {
  return useQuery({
    queryKey: ["integrations", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: Integration[] }>(
        `/api/v1/orgs/${org}/integrations`,
      );
      return response.data || [];
    },
    enabled: (opts?.enabled ?? true) && !!org,
    staleTime: opts?.staleTime,
  });
}

export function useIntegration(org: string, uid: string) {
  return useQuery({
    queryKey: ["integration", org, uid],
    queryFn: () =>
      apiFetch<Integration>(`/api/v1/orgs/${org}/integrations/${uid}`),
    enabled: !!org && !!uid,
  });
}

export function useCreateIntegration(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateIntegrationRequest) =>
      apiFetch<Integration>(`/api/v1/orgs/${org}/integrations`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations", org] });
    },
  });
}

export function useUpdateIntegration(org: string, uid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: UpdateIntegrationRequest) =>
      apiFetch<Integration>(`/api/v1/orgs/${org}/integrations/${uid}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations", org] });
      queryClient.invalidateQueries({ queryKey: ["integration", org, uid] });
    },
  });
}

export function useDeleteIntegration(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/integrations/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations", org] });
    },
  });
}

// Per-integration signing-secret rotation (webhook-only) and the
// test-notification delivery (available for every notifiable integration).

/** Result of a POST /integrations/:uid/test sample delivery. statusCode is the
 *  HTTP status for HTTP-based integrations (webhooks); 0 otherwise. */
export interface IntegrationTestResult {
  success: boolean;
  statusCode: number;
  durationMs: number;
  error?: string;
  /** Optional human-readable success note (e.g. the Kubernetes server version). */
  detail?: string;
}

export function useRotateWebhookSecret(org: string, integrationUid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<Integration>(
        `/api/v1/orgs/${org}/integrations/${integrationUid}/rotate-secret`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["integration", org, integrationUid],
      });
      queryClient.invalidateQueries({ queryKey: ["integrations", org] });
    },
  });
}

/** Sends a sample notification through any notifiable integration to verify it
 *  delivers. Returns the outcome; the backend always responds 200, so inspect
 *  `success`. */
export function useTestIntegration(org: string) {
  return useMutation({
    mutationFn: (integrationUid: string) =>
      apiFetch<IntegrationTestResult>(
        `/api/v1/orgs/${org}/integrations/${integrationUid}/test`,
        { method: "POST" },
      ),
  });
}

// Freebox pairing helpers. The Freebox API requires a one-time
// LCD-approval step, so the pairing flow is split across two endpoints
// rather than mapped onto the generic CRUD: POST asks the Freebox for
// an app_token (which we encrypt and store immediately), GET polls the
// LCD-approval status every ~2 s until it terminates.

export interface StartFreeboxPairingRequest {
  name?: string;
  baseUrl?: string;
}

export interface FreeboxPairingResponse {
  connectionUid: string;
  trackId: number;
  status: string;
}

export interface FreeboxPairingStatusResponse {
  status: string;
}

export function useStartFreeboxPairing(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: StartFreeboxPairingRequest) =>
      apiFetch<FreeboxPairingResponse>(
        `/api/v1/orgs/${org}/integrations/freebox/pair`,
        {
          method: "POST",
          body: JSON.stringify(request),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations", org] });
    },
  });
}

// useFreeboxPairingStatus polls the status endpoint while `enabled`
// is true. The dashboard switches `enabled` off once the status
// becomes terminal (granted/denied/timeout).
export function useFreeboxPairingStatus(
  org: string,
  connectionUid: string | undefined,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ["freeboxPairingStatus", org, connectionUid],
    queryFn: () =>
      apiFetch<FreeboxPairingStatusResponse>(
        `/api/v1/orgs/${org}/integrations/freebox/pair/${connectionUid}/status`,
      ),
    enabled: enabled && !!org && !!connectionUid,
    refetchInterval: 2000,
  });
}

// Freebox LAN-discovery types and hooks. The endpoint surfaces hosts
// currently visible to a paired Freebox so the operator can pre-fill
// an ICMP check without typing an IP — see spec
// `2026-05-24-08-freebox-lan-discovery.md`.

export interface FreeboxLanHost {
  id: string;
  name: string;
  ip: string;
  hostType: string;
  reachable: boolean;
  lastSeen?: string;
}

export interface ListFreeboxLanHostsResponse {
  data: FreeboxLanHost[];
}

// useFreeboxLanHosts polls the LAN-browser endpoint on demand. We do
// not auto-refresh — the dashboard opens the picker, gets a snapshot,
// and the user moves on. A manual refetch is one click away.
export function useFreeboxLanHosts(
  org: string,
  connectionUid: string | undefined,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ["freeboxLanHosts", org, connectionUid],
    queryFn: async () => {
      const response = await apiFetch<ListFreeboxLanHostsResponse>(
        `/api/v1/orgs/${org}/integrations/freebox/${connectionUid}/lan-hosts`,
      );
      return response.data || [];
    },
    enabled: enabled && !!org && !!connectionUid,
    staleTime: 30_000,
  });
}

// CheckConnection is what GET /checks/$check/channels returns — a
// flattened view of the bound channel (uid is the underlying channel
// UID), not the join row. The TS-side name keeps "Connection" until
// the join-table rename in the follow-up DB-rename spec.
export interface CheckConnection {
  uid: string;
  type: ConnectionType;
  name: string;
  enabled: boolean;
  isDefault: boolean;
}

export function useCheckConnections(org: string, checkUid: string | undefined) {
  return useQuery({
    queryKey: ["checkConnections", org, checkUid],
    queryFn: async () => {
      const response = await apiFetch<{ data?: CheckConnection[] }>(
        `/api/v1/orgs/${org}/checks/${checkUid}/channels`,
      );
      return response.data || [];
    },
    enabled: !!org && !!checkUid,
  });
}

export function useSetCheckConnections(org: string, checkUid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (connectionUids: string[]) =>
      apiFetch<{ data?: CheckConnection[] }>(
        `/api/v1/orgs/${org}/checks/${checkUid}/channels`,
        {
          method: "PUT",
          body: JSON.stringify({ connectionUids }),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["checkConnections", org, checkUid],
      });
    },
  });
}

// Slack destination picker types and hook

export interface SlackChannel {
  id: string;
  name: string;
  isPrivate: boolean;
  isMember: boolean;
}

export interface SlackUser {
  id: string;
  name: string;
  realName: string;
}

export interface SlackDestinationsResponse {
  channels: SlackChannel[];
  users: SlackUser[];
}

export function useSlackDestinations(
  org: string,
  channelUid: string,
  enabled = true,
) {
  return useQuery({
    queryKey: ["slack-destinations", org, channelUid],
    queryFn: () =>
      apiFetch<SlackDestinationsResponse>(
        `/api/v1/orgs/${org}/channels/${channelUid}/slack/destinations`,
      ),
    enabled: enabled && Boolean(org && channelUid),
    staleTime: 60_000,
  });
}

// Discord destination picker types and hook

export interface DiscordChannel {
  id: string;
  name: string;
  type: number;
}

export interface DiscordDestinationsResponse {
  channels: DiscordChannel[];
  guildId: string;
  guildName: string;
  connected: boolean;
}

export function useDiscordDestinations(
  org: string,
  channelUid: string,
  enabled = true,
) {
  return useQuery({
    queryKey: ["discord-destinations", org, channelUid],
    queryFn: () =>
      apiFetch<DiscordDestinationsResponse>(
        `/api/v1/orgs/${org}/channels/${channelUid}/discord/destinations`,
      ),
    enabled: enabled && Boolean(org && channelUid),
    staleTime: 60_000,
  });
}

// Per-member paging coverage (spec 2026-08-12-03, phase 2).
//
// Admin-only, and type-level only: the API deliberately returns channel types
// and verified/enabled flags, never a phone number or chat id.

export interface MemberChannelCoverage {
  type: string;
  verified: boolean;
  enabled: boolean;
}

export interface MemberCoverage {
  userUid: string;
  email: string;
  name?: string;
  role: string;
  channels: MemberChannelCoverage[];
  /** Nothing but email can reach this member — escalation's silent fallback. */
  emailFallbackOnly: boolean;
}

export interface MemberCoverageListResponse {
  data: MemberCoverage[];
}

export function useMemberCoverage(org: string, enabled = true) {
  return useQuery({
    queryKey: ["member-coverage", org],
    queryFn: () =>
      apiFetch<MemberCoverageListResponse>(
        `/api/v1/orgs/${org}/members/coverage`,
      ),
    enabled: enabled && Boolean(org),
    staleTime: 30_000,
    // A non-admin gets a 403 here; the members page simply renders no coverage
    // column rather than retrying a call it is not allowed to make.
    retry: false,
  });
}

/**
 * Adds an UNVERIFIED phone/WhatsApp contact for another member. The server
 * refuses to create it verified — the member still has to complete the code
 * round-trip before anything pages that number.
 */
export function useAddMemberContact(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (vars: {
      memberUid: string;
      type: "phone" | "whatsapp";
      value: string;
      label?: string;
    }) =>
      apiFetch<{ uid: string; verified: boolean }>(
        `/api/v1/orgs/${org}/members/${vars.memberUid}/contacts`,
        {
          method: "POST",
          body: JSON.stringify({
            type: vars.type,
            value: vars.value,
            label: vars.label,
          }),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["member-coverage", org] });
    },
  });
}

/** Emails a member asking them to finish setting up their paging contacts. */
export function useSendPagingNudge(org: string) {
  return useMutation({
    mutationFn: (memberUid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/members/${memberUid}/paging-nudge`, {
        method: "POST",
      }),
  });
}

// Per-integration member identity mapping (spec 2026-08-12-03).
//
// "Who is this org member on this Slack workspace" — used to mention the
// on-call person in channel alerts. Never used for paging, which is why the
// admin surface only ever shows workspace ids and names, no contact values.

export type IntegrationIdentityStatus = "matched" | "notFound" | "ambiguous";

export interface IntegrationIdentity {
  userUid: string;
  email: string;
  name?: string;
  status: IntegrationIdentityStatus;
  externalId?: string;
  displayName?: string;
  /** `auto` = email auto-match, `manual` = an admin picked it. */
  source?: "auto" | "manual";
}

export interface IntegrationIdentityListResponse {
  data: IntegrationIdentity[];
}

export interface IntegrationIdentitySyncResponse extends IntegrationIdentityListResponse {
  matchedCount: number;
  notFoundCount: number;
  ambiguousCount: number;
}

export function useIntegrationIdentities(
  org: string,
  integrationUid: string,
  enabled = true,
) {
  return useQuery({
    queryKey: ["integration-identities", org, integrationUid],
    queryFn: () =>
      apiFetch<IntegrationIdentityListResponse>(
        `/api/v1/orgs/${org}/integrations/${integrationUid}/identities`,
      ),
    enabled: enabled && Boolean(org && integrationUid),
    staleTime: 30_000,
  });
}

export function useSyncIntegrationIdentities(
  org: string,
  integrationUid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () =>
      apiFetch<IntegrationIdentitySyncResponse>(
        `/api/v1/orgs/${org}/integrations/${integrationUid}/identities/sync`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["integration-identities", org, integrationUid],
      });
    },
  });
}

export function useSetIntegrationIdentity(org: string, integrationUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (vars: {
      userUid: string;
      externalId: string;
      displayName?: string;
    }) =>
      apiFetch<IntegrationIdentity>(
        `/api/v1/orgs/${org}/integrations/${integrationUid}/identities/${vars.userUid}`,
        {
          method: "PUT",
          body: JSON.stringify({
            externalId: vars.externalId,
            displayName: vars.displayName,
          }),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["integration-identities", org, integrationUid],
      });
    },
  });
}

export function useDeleteIntegrationIdentity(
  org: string,
  integrationUid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (userUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/integrations/${integrationUid}/identities/${userUid}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["integration-identities", org, integrationUid],
      });
    },
  });
}

// Microsoft Teams bot (msteams-bot) setup types and hooks.
//
// Unlike Slack there is no live channel list to fetch: a Teams bot cannot
// enumerate the channels of a team it was never added to, so the destinations
// are the conversation references the backend captured at install time.

export interface MSTeamsDestination {
  id: string;
  name: string;
  /** Owning Teams team id — channels are per-team in Teams. */
  team_id?: string;
  team_name?: string;
  service_url?: string;
  type?: string;
}

export interface MSTeamsDestinationsResponse {
  destinations: MSTeamsDestination[];
  tenantId: string;
  connected: boolean;
  uninstalled: boolean;
}

export function useMSTeamsBotDestinations(
  org: string,
  channelUid: string,
  enabled = true,
) {
  return useQuery({
    queryKey: ["msteams-destinations", org, channelUid],
    queryFn: () =>
      apiFetch<MSTeamsDestinationsResponse>(
        `/api/v1/orgs/${org}/channels/${channelUid}/msteams/destinations`,
      ),
    enabled: enabled && Boolean(org && channelUid),
    staleTime: 30_000,
  });
}

export interface MSTeamsBotStatus {
  enabled: boolean;
  configured: boolean;
  appId?: string;
  /** The URL Microsoft must be able to reach over public HTTPS. */
  messagingEndpoint: string;
  installedTenants: number;
  singleTenant?: string;
}

export function useMSTeamsBotStatus(org: string, enabled = true) {
  return useQuery({
    queryKey: ["msteams-status", org],
    queryFn: () =>
      apiFetch<MSTeamsBotStatus>(
        `/api/v1/orgs/${org}/integrations/msteams/status`,
      ),
    enabled: enabled && Boolean(org),
    staleTime: 60_000,
  });
}

export interface MSTeamsPendingLink {
  connectionUid: string;
  code: string;
  expiresInSeconds: number;
}

/**
 * Mints a one-time code that links a Microsoft 365 tenant to this org.
 *
 * The tenant id is never sent by the client: it is written server-side, only
 * from a signature-verified Bot Framework activity that quotes this code
 * back. That round-trip is what proves the org asking for the link and the
 * tenant being linked are the same actor — Bot Framework has no OAuth
 * redirect to carry that context the way Slack's install flow does.
 */
export function useStartMSTeamsLink(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (connectionUid?: string) =>
      apiFetch<MSTeamsPendingLink>(
        `/api/v1/orgs/${org}/integrations/msteams/link-code`,
        {
          method: "POST",
          body: JSON.stringify(connectionUid ? { connectionUid } : {}),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations", org] });
    },
  });
}

/**
 * Downloads the generated, instance-filled Teams app package.
 *
 * The endpoint is authenticated (it names the instance's Entra app id), so
 * this cannot be a plain anchor href — fetch it with the session token and
 * hand the browser a blob URL instead.
 */
export async function downloadMSTeamsManifest(org: string): Promise<void> {
  const token = getToken();
  const response = await fetch(
    `/api/v1/orgs/${org}/integrations/msteams/manifest.zip`,
    { headers: token ? { Authorization: `Bearer ${token}` } : {} },
  );

  if (!response.ok) {
    throw new Error("Failed to download the Teams app package");
  }

  const blob = await response.blob();
  const url = URL.createObjectURL(blob);

  try {
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = "solidping-teams-app.zip";
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

interface SlackInstallURLResponse {
  url: string;
}

/**
 * Mints an org-scoped Slack OAuth install URL via the authenticated
 * install-url endpoint and navigates the browser there. Used by both Slack
 * "Install app" CTAs (new-integration tile and the unconnected-channel edit
 * page) instead of the legacy unauthenticated
 * `/api/v1/integrations/slack/install?org=...&channelUid=...` link — the org
 * now comes from the authenticated session, not a forgeable query param.
 */
export async function startSlackInstall(
  org: string,
  channelUid?: string,
): Promise<void> {
  const { url } = await apiFetch<SlackInstallURLResponse>(
    `/api/v1/orgs/${org}/integrations/slack/install-url`,
    {
      method: "POST",
      body: JSON.stringify(channelUid ? { channelUid } : {}),
    },
  );
  window.location.href = url;
}

interface DiscordInstallURLResponse {
  url: string;
}

/**
 * Mints an org-scoped Discord bot install URL and navigates the browser there.
 *
 * Unlike Slack there is deliberately no unauthenticated install entry point:
 * an anonymous Discord install would have to trust a caller-supplied org, which
 * is exactly the hole the Slack org-scoped endpoint was introduced to close.
 */
export async function startDiscordInstall(
  org: string,
  channelUid?: string,
): Promise<void> {
  const { url } = await apiFetch<DiscordInstallURLResponse>(
    `/api/v1/orgs/${org}/integrations/discord/install-url`,
    {
      method: "POST",
      body: JSON.stringify(channelUid ? { channelUid } : {}),
    },
  );
  window.location.href = url;
}

// --- Status Update types and hooks ---

export interface StatusUpdate {
  uid: string;
  statusPageUid: string;
  sectionUid?: string;
  checkUid?: string;
  incidentUid?: string;
  title: string;
  bodyMarkdown: string;
  linkUrl?: string;
  kind:
    | "investigating"
    | "identified"
    | "monitoring"
    | "resolved"
    | "maintenance"
    | "info";
  publishedAt: string;
  authorUid: string;
  createdAt: string;
  updatedAt: string;
}

export interface CreateStatusUpdateRequest {
  statusPageUid: string;
  sectionUid?: string;
  checkUid?: string;
  incidentUid?: string;
  title: string;
  bodyMarkdown: string;
  linkUrl?: string;
  kind: string;
  publishedAt?: string;
}

export interface UpdateStatusUpdateRequest {
  // Presence-aware nullable fields: omit the key to leave the column
  // untouched, send `null` to clear it, or a non-empty value to set it. A
  // plain optional string can't express "clear" — see
  // server/internal/handlers/statusupdates/service.go.
  sectionUid?: string | null;
  checkUid?: string | null;
  incidentUid?: string | null;
  linkUrl?: string | null;
  title?: string;
  bodyMarkdown?: string;
  kind?: string;
  publishedAt?: string;
}

export function useStatusUpdates(
  org: string,
  params: {
    statusPage?: string;
    section?: string;
    check?: string;
    incident?: string;
    limit?: number;
    offset?: number;
  } = {},
) {
  return useQuery({
    queryKey: ["statusUpdates", org, params],
    queryFn: async () => {
      const query = new URLSearchParams();
      if (params.statusPage) query.set("statusPage", params.statusPage);
      if (params.section) query.set("section", params.section);
      if (params.check) query.set("check", params.check);
      if (params.incident) query.set("incident", params.incident);
      if (params.limit) query.set("limit", params.limit.toString());
      if (params.offset) query.set("offset", params.offset.toString());
      const qs = query.toString();
      const response = await apiFetch<{ data?: StatusUpdate[] }>(
        `/api/v1/orgs/${org}/status-updates${qs ? `?${qs}` : ""}`,
      );
      return response.data || [];
    },
    enabled: !!org,
  });
}

export function useStatusUpdate(org: string, uid: string) {
  return useQuery({
    queryKey: ["statusUpdate", org, uid],
    queryFn: async () =>
      apiFetch<StatusUpdate>(`/api/v1/orgs/${org}/status-updates/${uid}`),
    enabled: !!org && !!uid,
  });
}

export function useCreateStatusUpdate(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateStatusUpdateRequest) =>
      apiFetch<StatusUpdate>(`/api/v1/orgs/${org}/status-updates`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusUpdates", org] });
    },
  });
}

export function useUpdateStatusUpdate(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateStatusUpdateRequest) =>
      apiFetch<StatusUpdate>(`/api/v1/orgs/${org}/status-updates/${uid}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusUpdates", org] });
      // The edit page reads through the singular key, which the app-wide
      // 1-minute staleTime would otherwise leave stale after a save.
      queryClient.invalidateQueries({ queryKey: ["statusUpdate", org, uid] });
    },
  });
}

export function useDeleteStatusUpdate(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/status-updates/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: (_data, uid) => {
      queryClient.invalidateQueries({ queryKey: ["statusUpdates", org] });
      queryClient.removeQueries({ queryKey: ["statusUpdate", org, uid] });
    },
  });
}

// ── Discovery ─────────────────────────────────────────────────────────────────

export interface DiscoveryScan {
  uid: string;
  type: string;
  status: string;
  config: Record<string, unknown>;
  scheduledAt: string;
  createdAt: string;
  updatedAt: string;
}

// DiscoveryScanProgress is the uniform progress block returned alongside every
// scan: chunk counts, an overall derived status, and the group/check roll-up
// counts. Non-chunked scans report totalChunks=1.
export interface DiscoveryScanProgress {
  totalChunks: number;
  completedChunks: number;
  failedChunks: number;
  runningChunks: number;
  pendingChunks: number;
  derivedStatus: "pending" | "running" | "success" | "failed";
  groupCount: number;
  checkCount: number;
}

export type DiscoverySource = "lan" | "freebox" | "container" | "kubernetes";

// DiscoveryType is a registered discovery type returned by GET /discovery/types,
// driving the registry-aware type picker.
export interface DiscoveryType {
  type: string;
  source: DiscoverySource;
}

// DiscoveredCheck is one suggested check produced by a scan. Rows are grouped for
// display by groupKey; the stored unit is the check.
export interface DiscoveredCheck {
  uid: string;
  organizationUid: string;
  jobUid: string;
  source: DiscoverySource;
  groupKey: string;
  groupLabel: string;
  name: string;
  slug: string;
  type: string;
  config: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  promotedToCheckUid?: string;
  discoveredAt: string;
}

// StartDiscoveryScanRequest is the generic scan-start body: a registered type
// plus its type-specific parameters.
export interface StartDiscoveryScanRequest {
  type: string;
  parameters: Record<string, unknown>;
}

// PromoteChecksRequest promotes one or more discovered checks into real checks.
// A group's UIDs promote the whole group; overrides adjust name/period.
export interface PromoteChecksRequest {
  uids: string[];
  overrides?: {
    name?: string;
    period?: string;
  };
}

export function useDiscoveryTypes(org: string) {
  return useQuery({
    queryKey: ["discoveryTypes", org],
    queryFn: () =>
      apiFetch<{ data: DiscoveryType[] }>(
        `/api/v1/orgs/${org}/discovery/types`,
      ),
    select: (res) => res?.data ?? [],
  });
}

export function useStartDiscoveryScan(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (req: StartDiscoveryScanRequest) =>
      apiFetch<{ data: DiscoveryScan; progress?: DiscoveryScanProgress }>(
        `/api/v1/orgs/${org}/discovery/scans`,
        {
          method: "POST",
          body: JSON.stringify(req),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["discoveryScans", org] });
    },
  });
}

export function useListDiscoveryScans(org: string) {
  return useQuery({
    queryKey: ["discoveryScans", org],
    queryFn: () =>
      apiFetch<{ data: DiscoveryScan[] }>(
        `/api/v1/orgs/${org}/discovery/scans`,
      ),
    select: (res) => res?.data ?? [],
  });
}

// scanIsActive returns true while a scan (by its derived status, falling back to
// the plan job's own status for legacy/standalone jobs) is still in flight.
function scanIsActive(
  scan?: DiscoveryScan,
  progress?: DiscoveryScanProgress,
): boolean {
  const status = progress?.derivedStatus ?? scan?.status;
  return status === "pending" || status === "running";
}

// useDiscoveryScan returns the plan job plus its derived fan-out progress block
// (when present). It polls every 3s while the scan is active so the progress
// indicator and host table update as chunks finish.
export function useDiscoveryScan(org: string, jobUid: string) {
  return useQuery({
    queryKey: ["discoveryScan", org, jobUid],
    queryFn: () =>
      apiFetch<{ data: DiscoveryScan; progress?: DiscoveryScanProgress }>(
        `/api/v1/orgs/${org}/discovery/scans/${jobUid}`,
      ),
    select: (res) => ({ scan: res?.data, progress: res?.progress }),
    enabled: !!jobUid,
    refetchInterval: (query) => {
      const data = query.state.data;
      return scanIsActive(data?.data, data?.progress) ? 3000 : false;
    },
  });
}

// useCancelScan stops a running fan-out scan: cancels the plan job (if pending)
// and drops every pending child chunk. Running children finish naturally.
export function useCancelScan(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (jobUid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/discovery/scans/${jobUid}/cancel`, {
        method: "POST",
      }),
    onSuccess: (_data, jobUid) => {
      queryClient.invalidateQueries({
        queryKey: ["discoveryScan", org, jobUid],
      });
      queryClient.invalidateQueries({ queryKey: ["discoveryScans", org] });
    },
  });
}

// useListDiscoveredChecks returns the suggested checks for a scan (or org). The
// frontend groups them by groupKey. While a fan-out scan is active, pass
// pollWhileActive to stream rows in as chunks land.
export function useListDiscoveredChecks(
  org: string,
  opts?: {
    jobUid?: string;
    group?: string;
    promoted?: boolean;
    source?: string;
  },
  pollWhileActive = false,
) {
  const params = new URLSearchParams();
  if (opts?.jobUid) params.set("jobUid", opts.jobUid);
  if (opts?.group) params.set("group", opts.group);
  if (opts?.promoted !== undefined)
    params.set("promoted", String(opts.promoted));
  if (opts?.source) params.set("source", opts.source);
  const qs = params.toString();

  return useQuery({
    queryKey: ["discoveryChecks", org, opts],
    queryFn: () =>
      apiFetch<{ data: DiscoveredCheck[] }>(
        `/api/v1/orgs/${org}/discovery/checks${qs ? `?${qs}` : ""}`,
      ),
    select: (res) => res?.data ?? [],
    refetchInterval: pollWhileActive ? 3000 : false,
  });
}

// usePromoteChecks promotes the selected discovered-check UIDs into real checks.
export function usePromoteChecks(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (req: PromoteChecksRequest) =>
      apiFetch<{ data: Check[] }>(
        `/api/v1/orgs/${org}/discovery/checks/promote`,
        {
          method: "POST",
          body: JSON.stringify(req),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["discoveryChecks", org] });
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
    },
  });
}

// useDismissCheck dismisses a single discovered check (soft delete).
export function useDismissCheck(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/discovery/checks/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["discoveryChecks", org] });
    },
  });
}

// useDismissGroup dismisses every discovered check in a group for a scan.
export function useDismissGroup(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ jobUid, group }: { jobUid?: string; group: string }) => {
      const params = new URLSearchParams();
      if (jobUid) params.set("jobUid", jobUid);
      params.set("group", group);
      return apiFetch<void>(
        `/api/v1/orgs/${org}/discovery/checks?${params.toString()}`,
        { method: "DELETE" },
      );
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["discoveryChecks", org] });
    },
  });
}

// Notification routes & contacts

export interface NotificationContact {
  uid: string;
  type: string;
  value: string;
  label: string;
  verifiedAt?: string;
}

export interface NotificationRoute {
  uid: string;
  enabled: boolean;
  position: number;
  contact: NotificationContact;
  createdAt: string;
}

export interface SlackSuggestion {
  slackUserId: string;
  workspaceName: string;
  channelUid: string;
}

export interface NotificationRoutesResponse {
  data: NotificationRoute[];
  slackSuggestion?: SlackSuggestion;
}

export function useNotificationRoutes(org: string) {
  return useQuery({
    queryKey: ["notificationRoutes", org],
    queryFn: () =>
      apiFetch<NotificationRoutesResponse>(
        `/api/v1/orgs/${org}/users/me/notification-routes`,
      ),
  });
}

export function useCreateNotificationContact(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { type: string; value: string; label?: string }) =>
      apiFetch<NotificationRoute>(
        `/api/v1/orgs/${org}/users/me/notification-contacts`,
        {
          method: "POST",
          body: JSON.stringify(body),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["notificationRoutes", org] });
    },
  });
}

export function useDeleteNotificationContact(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (contactUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/users/me/notification-contacts/${contactUid}`,
        {
          method: "DELETE",
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["notificationRoutes", org] });
    },
  });
}

/** Issues (and sends) a verification code for a phone notification contact. */
export function useVerifyContact(org: string) {
  return useMutation({
    mutationFn: (contactUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/users/me/notification-contacts/${contactUid}/verify`,
        { method: "POST" },
      ),
  });
}

/** The connect link returned by POST /users/me/telegram/link. */
export interface TelegramLinkResponse {
  url: string;
  expiresAt: string;
}

/**
 * Mints a single-use Telegram connect link (TTL 15 minutes).
 *
 * Nothing is created by this call: the contact only appears once the user
 * presses Start in Telegram and the resulting `/start <token>` reaches the
 * instance webhook. Callers therefore poll the routes list afterwards rather
 * than reading a contact out of this response.
 */
export function useCreateTelegramLink(org: string) {
  return useMutation({
    mutationFn: () =>
      apiFetch<TelegramLinkResponse>(
        `/api/v1/orgs/${org}/users/me/telegram/link`,
        {
          method: "POST",
        },
      ),
  });
}

/** Confirms a phone notification contact with the emailed/texted code. */
export function useConfirmVerifyContact(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ contactUid, code }: { contactUid: string; code: string }) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/users/me/notification-contacts/${contactUid}/verify/confirm`,
        { method: "POST", body: JSON.stringify({ code }) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["notificationRoutes", org] });
    },
  });
}

export function usePatchNotificationRoute(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      routeUid,
      patch,
    }: {
      routeUid: string;
      patch: { enabled?: boolean };
    }) =>
      apiFetch<NotificationRoute>(
        `/api/v1/orgs/${org}/users/me/notification-routes/${routeUid}`,
        {
          method: "PATCH",
          body: JSON.stringify(patch),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["notificationRoutes", org] });
    },
  });
}

export function useTestNotificationRoute(org: string) {
  return useMutation({
    mutationFn: (routeUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/users/me/notification-routes/${routeUid}/test`,
        {
          method: "POST",
        },
      ),
  });
}

// Entitlements hooks
export interface EntitlementsLimits {
  maxChecks?: number | null;
  maxChecksPerMinute?: number | null;
  maxUsers?: number | null;
  /** Cap on active deported (private-location) agents. null = unlimited. */
  maxDeportedAgents?: number | null;
  /** Cap on status pages served on a customer-owned domain. null = unlimited. */
  maxCustomDomains?: number | null;
  /** Cap on outbound SMS per UTC month. null = unlimited. */
  maxSmsPerMonth?: number | null;
  /** Cap on outbound voice calls per UTC month. null = unlimited. */
  maxCallsPerMonth?: number | null;
  /** Cap on outbound WhatsApp template messages per UTC month. null = unlimited. */
  maxWhatsappPerMonth?: number | null;
  /** Cap on service-level objectives. null = unlimited. */
  maxSlos?: number | null;
  /**
   * The one non-numeric entitlement: whether the org may drop the "powered by
   * SolidPing" badge from its status pages. Unlike the caps above, null here
   * does NOT mean unlimited — it means the server sent no value, which the UI
   * renders as "not included".
   */
  whiteLabel?: boolean | null;
}

/**
 * The instance-spend SMS guards and how many of this organization's sends they
 * have refused since the server started.
 *
 * These two guard the INSTANCE's bill, so they apply only to sends made on the
 * server's own SMS credentials — an organization sending through its own
 * Twilio integration is billed by Twilio directly and is never gated by them.
 *
 * Absent entirely when the deployment configures no instance-spend guard.
 */
export interface EntitlementsSMSGuard {
  /** Instance-wide hourly cap; 0 when disabled. */
  globalRunawayPerHour: number;
  /** Allowed E.164 country calling codes; absent/empty means all countries. */
  allowedCountries?: string[];
  globalRunawayBreaches: number;
  countryBlockedBreaches: number;
  lastBreachAt?: string;
}

export interface EntitlementsUsage {
  checks: number;
  checksPerMinute: number;
  ssoUsers: number;
  /** Count of active deported (private-location) agents. */
  agents: number;
  /** Count of live status pages with a custom domain set. */
  customDomains: number;
  /**
   * Outbound WhatsApp template messages sent in the current UTC month. A
   * persistent counter, not a live count — sent messages cannot be un-sent.
   */
  whatsappThisMonth: number;
  /** Count of live service-level objectives. */
  slos: number;
  /**
   * Instance-spend guard state. A breach must never fail silently — what it
   * drops is an alert — so it surfaces here as well as in the server logs.
   */
  smsGuard?: EntitlementsSMSGuard;
}

/**
 * The org's scheduled check-execution rate against its `maxChecksPerMinute`
 * cap, plus what the rate gate actually threw away today.
 *
 * Unlike `usage`, this is NOT behind `?with=usage`: the over-limit banner it
 * drives also renders on the checks list, which has no business paying for the
 * full usage roll-up just to learn it is being throttled.
 */
export interface EntitlementsChecksPerMinute {
  /**
   * Scheduled executions per minute: sum over enabled, non-deleted,
   * non-internal, **non-passive** checks of `max(1, regions) × 60s / period`.
   * Heartbeat and email checks are excluded — they return before the rate gate
   * and can never be the reason an org is throttled, which is why this can be
   * lower than `usage.checksPerMinute`.
   */
  demand: number;
  /** Resolved `maxChecksPerMinute`. null/undefined = unlimited. */
  limit?: number | null;
  /**
   * Executions the per-org rate gate deferred today (UTC), across both the
   * in-process worker path and the agent dispatch path. A persistent counter:
   * an org that has just dropped back under its cap still lost executions
   * earlier today, and must be told so.
   */
  skippedToday: number;
}

export interface EntitlementsResponse {
  limits: EntitlementsLimits;
  usage?: EntitlementsUsage;
  /** Always present unless the server could not compute it. */
  checksPerMinute?: EntitlementsChecksPerMinute;
  source: string;
  stale: boolean;
  upgradeUrl?: string;
  /** Plan identity supplied by the billing service (display-only). */
  displayName?: string | null;
  displayEmoji?: string | null;
}

export function useEntitlements(org: string, opts?: { withUsage?: boolean }) {
  return useQuery({
    queryKey: ["entitlements", org, opts?.withUsage ?? false],
    queryFn: () =>
      apiFetch<EntitlementsResponse>(
        `/api/v1/orgs/${org}/entitlements${opts?.withUsage ? "?with=usage" : ""}`,
      ),
    enabled: !!org,
    staleTime: 60 * 1000,
  });
}

// ----- Superadmin entitlements editor (spec 2026-08-26-06) -----

/** One entitlement change, as the audit log records it. */
export interface EntitlementAudit {
  uid: string;
  organizationUid: string;
  /**
   * `admin`, `billing-service`, `default` … plus two markers the editor cares
   * about: `billing-service:suppressed` (a billing push that an admin override
   * discarded) and `admin:released` (the override was handed back to billing).
   */
  source: string;
  actor: string;
  beforeSnapshot?: Record<string, unknown> | null;
  afterSnapshot?: Record<string, unknown>;
  reason?: string | null;
  createdAt: string;
}

/** The stored org_entitlements row; absent when the org has never had one. */
export interface AdminEntitlementsStored {
  source: string;
  limits: EntitlementsLimits;
  displayName?: string | null;
  displayEmoji?: string | null;
  externalRef?: string | null;
  expiresAt?: string | null;
  lastSyncedAt?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface AdminEntitlementsRow {
  organizationUid: string;
  slug: string;
  name: string;
  limits: EntitlementsLimits;
  source: string;
  stale: boolean;
  displayName?: string | null;
  displayEmoji?: string | null;
  lastSyncedAt?: string | null;
  checksPerMinute?: EntitlementsChecksPerMinute;
  /** Scheduled demand exceeds the resolved cap. Unlimited is never over. */
  overCheckRate: boolean;
  /** When the current admin override was written; absent unless one holds. */
  adminOverrideSince?: string | null;
}

export interface AdminEntitlementsListResponse {
  data: AdminEntitlementsRow[];
  total: number;
}

export interface AdminEntitlementsDetail extends AdminEntitlementsRow {
  stored?: AdminEntitlementsStored | null;
  /** What this deployment resolves to with no row — where a release lands. */
  defaults: EntitlementsLimits;
  /**
   * The org's change history, newest first. Optional and nullable on purpose:
   * an org that has never been edited has no trail, and a server that sends
   * `null` for that must not be able to blank the page.
   */
  audits?: EntitlementAudit[] | null;
}

export function useAdminEntitlementsList(params: {
  q?: string;
  limit?: number;
  enabled?: boolean;
}) {
  const search = new URLSearchParams();
  if (params.q) search.set("q", params.q);
  if (params.limit) search.set("limit", String(params.limit));

  const suffix = search.toString() ? `?${search.toString()}` : "";

  return useQuery({
    queryKey: ["adminEntitlements", params.q ?? "", params.limit ?? 0],
    queryFn: () =>
      apiFetch<AdminEntitlementsListResponse>(
        `/api/v1/system/entitlements${suffix}`,
      ),
    enabled: params.enabled !== false,
  });
}

export function useAdminEntitlementsDetail(orgSlug: string, enabled = true) {
  return useQuery({
    queryKey: ["adminEntitlements", "detail", orgSlug],
    queryFn: () =>
      apiFetch<AdminEntitlementsDetail>(
        `/api/v1/system/entitlements/${orgSlug}`,
      ),
    enabled: enabled && !!orgSlug,
  });
}

/** What the superadmin PUT / DELETE answer with. */
export interface AdminEntitlementsWriteResponse {
  limits: EntitlementsLimits;
  source: string;
  stale: boolean;
  displayName?: string | null;
  displayEmoji?: string | null;
  /** False only if the precedence rule discarded the write. */
  applied: boolean;
  /**
   * A stored row was actually removed. False when releasing an organization
   * that had no override — a no-op, not an error.
   */
  released?: boolean;
}

export interface SetAdminEntitlementsRequest {
  limits: EntitlementsLimits;
  displayName?: string | null;
  displayEmoji?: string | null;
  /** Stored on the audit row; sent as a header, not a body field. */
  reason?: string;
}

export function useSetAdminEntitlements(orgSlug: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ reason, ...body }: SetAdminEntitlementsRequest) =>
      apiFetch<AdminEntitlementsWriteResponse>(
        `/api/v1/system/entitlements/${orgSlug}`,
        {
          method: "PUT",
          body: JSON.stringify(body),
          headers: reason ? { "X-Entitlements-Reason": reason } : undefined,
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["adminEntitlements"] });
      queryClient.invalidateQueries({ queryKey: ["entitlements", orgSlug] });
    },
  });
}

export function useReleaseAdminEntitlements(orgSlug: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (reason?: string) =>
      apiFetch<AdminEntitlementsWriteResponse>(
        `/api/v1/system/entitlements/${orgSlug}`,
        {
          method: "DELETE",
          headers: reason ? { "X-Entitlements-Reason": reason } : undefined,
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["adminEntitlements"] });
      queryClient.invalidateQueries({ queryKey: ["entitlements", orgSlug] });
    },
  });
}

// ----- Web Push -----

interface VapidPublicKeyData {
  publicKey: string;
}

interface VapidPublicKeyResponse {
  data: VapidPublicKeyData;
}

export function useVapidPublicKey(org: string) {
  return useQuery({
    queryKey: ["vapidPublicKey", org],
    queryFn: () =>
      apiFetch<VapidPublicKeyResponse>(
        `/api/v1/orgs/${org}/webpush/vapid-public-key`,
      ).then((r) => r.data),
    enabled: !!org,
    staleTime: Infinity, // key is stable; refetch only on mount
  });
}

// ---------------------------------------------------------------------------
// Admin Jobs observability (spec 2026-06-15-05)
// Read-only views over the background-jobs queue and the check schedule.
// All hooks accept `allOrgs` to switch between the org-scoped endpoints and
// the super-admin /system endpoints, and adapt their poll rate to activity.
// ---------------------------------------------------------------------------

// Poll fast while the instance is busy so the page visibly tracks activity;
// poll slowly (not off) when idle so new activity still surfaces.
const JOBS_ACTIVE_INTERVAL_MS = 2500;
const JOBS_IDLE_INTERVAL_MS = 15000;

export interface BackgroundJob {
  uid: string;
  organizationUid: string | null;
  type: string;
  config: Record<string, unknown> | null;
  output: Record<string, unknown> | null;
  status: "pending" | "running" | "success" | "retried" | "failed";
  retryCount: number;
  scheduledAt: string;
  previousJobUid: string | null;
  createdAt: string;
  updatedAt: string;
}

export type CheckJobState = "idle" | "inFlight" | "stalled" | "crashLooping";

export interface CheckScheduleJob {
  uid: string;
  organizationUid: string;
  checkUid: string;
  checkName: string | null;
  region: string | null;
  type: string;
  config: Record<string, unknown> | null;
  encryptedKeys: string[];
  encrypted: boolean;
  periodSeconds: number;
  scheduledAt: string | null;
  leaseWorkerUid: string | null;
  leaseExpiresAt: string | null;
  leaseStarts: number;
  updatedAt: string;
  state: CheckJobState;
}

export interface JobsStats {
  jobs: {
    pending: number;
    running: number;
    failed24h: number;
  };
  checks: {
    total: number;
    dueNow: number;
    inFlight: number;
    stalled: number;
    crashLooping: number;
  };
}

// jobsStatsAreActive reports whether the instance is busy enough to warrant a
// fast poll. Feeds the adaptive refetchInterval across all jobs hooks.
function jobsStatsAreActive(stats?: JobsStats): boolean {
  if (!stats) return false;
  return (
    stats.jobs.pending +
      stats.jobs.running +
      stats.checks.inFlight +
      stats.checks.dueNow >
    0
  );
}

// jobsAdaptiveInterval picks the poll cadence. While the live hint stream is
// connected, `jobs` hints drive freshness and the interval stretches to the
// lazy safety net — the 2.5s active poll only runs when not live.
function jobsAdaptiveInterval(active: boolean, isLive = false): number {
  return stretchWhileLive(
    active ? JOBS_ACTIVE_INTERVAL_MS : JOBS_IDLE_INTERVAL_MS,
    isLive,
  );
}

interface JobsScope {
  allOrgs?: boolean;
}

// useJobsStats fetches the activity overview and drives adaptive refresh.
export function useJobsStats(org: string, opts?: JobsScope) {
  const allOrgs = opts?.allOrgs ?? false;
  useLiveSubscription({ entity: "jobs" });
  const isLive = useScopeLive({ entity: "jobs" });
  return useQuery({
    queryKey: ["jobsStats", org, { allOrgs }],
    queryFn: () =>
      apiFetch<{ data: JobsStats }>(
        allOrgs
          ? `/api/v1/system/jobs/stats`
          : `/api/v1/orgs/${org}/jobs/stats`,
      ).then((r) => r.data),
    enabled: !!org,
    refetchInterval: (query) =>
      jobsAdaptiveInterval(
        jobsStatsAreActive(query.state.data as JobsStats | undefined),
        isLive,
      ),
    refetchIntervalInBackground: false,
  });
}

interface BackgroundJobsOptions extends JobsScope {
  status?: string;
  type?: string;
  limit?: number;
  offset?: number;
  active?: boolean;
}

// useBackgroundJobs lists background jobs (admin/super-admin).
export function useBackgroundJobs(org: string, opts?: BackgroundJobsOptions) {
  const allOrgs = opts?.allOrgs ?? false;
  useLiveSubscription({ entity: "jobs" });
  const isLive = useScopeLive({ entity: "jobs" });
  return useQuery({
    queryKey: ["backgroundJobs", org, opts],
    queryFn: () => {
      const params = new URLSearchParams();
      if (opts?.status) params.set("status", opts.status);
      if (opts?.type) params.set("type", opts.type);
      if (opts?.limit) params.set("limit", String(opts.limit));
      if (opts?.offset) params.set("offset", String(opts.offset));
      const qs = params.toString();
      const base = allOrgs
        ? `/api/v1/system/jobs`
        : `/api/v1/orgs/${org}/admin/jobs`;
      return apiFetch<{ data: BackgroundJob[] }>(
        `${base}${qs ? `?${qs}` : ""}`,
      ).then((r) => r.data ?? []);
    },
    enabled: !!org,
    refetchInterval: () => jobsAdaptiveInterval(opts?.active ?? false, isLive),
    refetchIntervalInBackground: false,
  });
}

interface CheckScheduleOptions extends JobsScope {
  limit?: number;
  offset?: number;
  active?: boolean;
}

// useCheckSchedule lists check-schedule rows (admin/super-admin).
export function useCheckSchedule(org: string, opts?: CheckScheduleOptions) {
  const allOrgs = opts?.allOrgs ?? false;
  useLiveSubscription({ entity: "jobs" });
  const isLive = useScopeLive({ entity: "jobs" });
  return useQuery({
    queryKey: ["checkSchedule", org, opts],
    queryFn: () => {
      const params = new URLSearchParams();
      if (opts?.limit) params.set("limit", String(opts.limit));
      if (opts?.offset) params.set("offset", String(opts.offset));
      const qs = params.toString();
      const base = allOrgs
        ? `/api/v1/system/check-jobs`
        : `/api/v1/orgs/${org}/check-jobs`;
      return apiFetch<{ data: CheckScheduleJob[] }>(
        `${base}${qs ? `?${qs}` : ""}`,
      ).then((r) => r.data ?? []);
    },
    enabled: !!org,
    refetchInterval: () => jobsAdaptiveInterval(opts?.active ?? false, isLive),
    refetchIntervalInBackground: false,
  });
}

// useBackgroundJob fetches a single background job's detail.
export function useBackgroundJob(org: string, uid: string, opts?: JobsScope) {
  const allOrgs = opts?.allOrgs ?? false;
  return useQuery({
    queryKey: ["backgroundJob", org, uid, { allOrgs }],
    queryFn: () =>
      apiFetch<{ data: BackgroundJob }>(
        allOrgs
          ? `/api/v1/system/jobs/${uid}`
          : `/api/v1/orgs/${org}/admin/jobs/${uid}`,
      ).then((r) => r.data),
    enabled: !!org && !!uid,
  });
}

// useBackgroundJobChain fetches the ordered retry chain a job belongs to.
export function useBackgroundJobChain(
  org: string,
  uid: string,
  opts?: JobsScope,
) {
  const allOrgs = opts?.allOrgs ?? false;
  return useQuery({
    queryKey: ["backgroundJobChain", org, uid, { allOrgs }],
    queryFn: () =>
      apiFetch<{ data: BackgroundJob[] }>(
        allOrgs
          ? `/api/v1/system/jobs/${uid}/chain`
          : `/api/v1/orgs/${org}/admin/jobs/${uid}/chain`,
      ).then((r) => r.data ?? []),
    enabled: !!org && !!uid,
  });
}

// useCheckJob fetches a single check-schedule row's detail.
export function useCheckJob(org: string, uid: string, opts?: JobsScope) {
  const allOrgs = opts?.allOrgs ?? false;
  return useQuery({
    queryKey: ["checkJob", org, uid, { allOrgs }],
    queryFn: () =>
      apiFetch<{ data: CheckScheduleJob }>(
        allOrgs
          ? `/api/v1/system/check-jobs/${uid}`
          : `/api/v1/orgs/${org}/check-jobs/${uid}`,
      ).then((r) => r.data),
    enabled: !!org && !!uid,
  });
}

// Maintenance windows ---------------------------------------------------------
// Backend: server/internal/handlers/maintenancewindows. The window response
// carries no server-computed status or check counts — the UI derives status
// client-side (see lib/maintenance-window-status.ts) and counts from the
// /checks association endpoint.

// One concrete activation of a (possibly recurring) maintenance window.
// Mirrors models.Occurrence on the backend.
export interface MaintenanceOccurrence {
  startAt: string;
  endAt: string;
}

export interface MaintenanceWindow {
  uid: string;
  title: string;
  description?: string;
  startAt: string;
  endAt: string;
  recurrence: "none" | "daily" | "weekly" | "monthly";
  recurrenceEnd?: string;
  createdBy?: string;
  createdAt: string;
  updatedAt: string;
  // Server-computed lifecycle at response time. Optional so the client keeps
  // working against older servers; views fall back to computeMaintenanceStatus.
  status?: "active" | "upcoming" | "past";
  // Server-computed upcoming activations (active one first). Optional; views
  // fall back to the client nextOccurrences port.
  nextOccurrences?: MaintenanceOccurrence[];
}

export interface MaintenanceWindowCheck {
  uid: string;
  checkUid?: string;
  checkGroupUid?: string;
}

export interface CreateMaintenanceWindowRequest {
  title: string;
  description?: string;
  startAt: string;
  endAt: string;
  recurrence: string;
  recurrenceEnd?: string | null;
}

export type UpdateMaintenanceWindowRequest =
  Partial<CreateMaintenanceWindowRequest>;

export interface SetMaintenanceWindowChecksRequest {
  checkUids: string[];
  checkGroupUids: string[];
}

export function useMaintenanceWindows(
  org: string,
  params?: { status?: string; limit?: number },
) {
  return useQuery({
    queryKey: ["maintenanceWindows", org, params ?? {}],
    queryFn: async () => {
      const search = new URLSearchParams();
      if (params?.status) search.set("status", params.status);
      if (params?.limit) search.set("limit", String(params.limit));
      const query = search.toString();
      const response = await apiFetch<{ data?: MaintenanceWindow[] }>(
        `/api/v1/orgs/${org}/maintenance-windows${query ? `?${query}` : ""}`,
      );
      return response.data ?? [];
    },
    enabled: !!org,
  });
}

export function useMaintenanceWindow(org: string, uid: string) {
  return useQuery({
    queryKey: ["maintenanceWindow", org, uid],
    queryFn: () =>
      apiFetch<MaintenanceWindow>(
        `/api/v1/orgs/${org}/maintenance-windows/${uid}`,
      ),
    enabled: !!org && !!uid,
  });
}

export function useMaintenanceWindowChecks(org: string, uid: string) {
  return useQuery({
    queryKey: ["maintenanceWindowChecks", org, uid],
    queryFn: async () => {
      const response = await apiFetch<{ data?: MaintenanceWindowCheck[] }>(
        `/api/v1/orgs/${org}/maintenance-windows/${uid}/checks`,
      );
      return response.data ?? [];
    },
    enabled: !!org && !!uid,
  });
}

export function useCreateMaintenanceWindow(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateMaintenanceWindowRequest) =>
      apiFetch<MaintenanceWindow>(`/api/v1/orgs/${org}/maintenance-windows`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["maintenanceWindows", org] });
    },
  });
}

export function useUpdateMaintenanceWindow(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateMaintenanceWindowRequest) =>
      apiFetch<MaintenanceWindow>(
        `/api/v1/orgs/${org}/maintenance-windows/${uid}`,
        {
          method: "PATCH",
          body: JSON.stringify(request),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["maintenanceWindows", org] });
      queryClient.invalidateQueries({
        queryKey: ["maintenanceWindow", org, uid],
      });
    },
  });
}

export function useDeleteMaintenanceWindow(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/maintenance-windows/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["maintenanceWindows", org] });
    },
  });
}

export function useSetMaintenanceWindowChecks(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: SetMaintenanceWindowChecksRequest) =>
      apiFetch<void>(`/api/v1/orgs/${org}/maintenance-windows/${uid}/checks`, {
        method: "PUT",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["maintenanceWindowChecks", org, uid],
      });
    },
  });
}

// ---------------------------------------------------------------------------
// Private locations (org-private regions) & deported agents (spec 2026-07-16-02)
// ---------------------------------------------------------------------------

export interface PrivateRegion {
  slug: string;
  name: string;
  emoji: string;
  /** Stored region string, org-relative, e.g. `@dc1`. */
  region: string;
  agentCount: number;
  /** Egress families this location's LIVE agents report — today only `ipv6`,
   * three-state ("yes" / "no" / "unknown"). See spec 2026-08-15-11. */
  capabilities?: Record<string, string>;
}

export interface AgentInfo {
  uid: string;
  name: string;
  /** Only populated on the fleet-wide view (useAllAgents) — "org" or "system". */
  kind?: "org" | "system";
  /** Owning org slug, only populated on the fleet-wide view. Absent/null for
   * system agents, which have no owning organization. */
  org?: string | null;
  region: string;
  fingerprint: string;
  status: "active" | "revoked";
  lastSeenAt?: string;
  enrolledAt: string;
  revokedAt?: string;
  /** Self-reported build version (spec 2026-08-19-07), resolved from the
   * agent's worker row. `null`/absent means "never reported" — an agent
   * predating this feature, or one that has not sent a claim frame yet —
   * and must be rendered as unknown, never as drifted. */
  version?: string | null;
}

export interface EnrollmentToken {
  uid: string;
  region: string;
  /** "pending" while waiting for an agent; "used" once consumed — used tokens
   * stay listed for a viewing window so the register wizard can report which
   * agent enrolled with them (they can never enroll another agent). */
  status: "pending" | "used";
  expiresAt: string;
  createdAt: string;
  usedAt?: string;
  usedByAgentUid?: string;
}

export interface MintedEnrollmentToken {
  uid: string;
  /** The one-shot spe_ secret — shown exactly once, never retrievable again. */
  token: string;
  region: string;
  expiresAt: string;
}

export function usePrivateRegions(org: string) {
  return useQuery({
    queryKey: ["private-regions", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: PrivateRegion[] }>(
        `/api/v1/orgs/${org}/private-regions`,
      );
      return response.data || [];
    },
    enabled: !!org,
  });
}

export function useCreatePrivateRegion(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: { slug: string; name?: string; emoji?: string }) =>
      apiFetch<PrivateRegion>(`/api/v1/orgs/${org}/private-regions`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["private-regions", org] });
      queryClient.invalidateQueries({ queryKey: ["regions", org] });
    },
  });
}

export function useDeletePrivateRegion(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (slug: string) =>
      apiFetch<{ status: string }>(
        `/api/v1/orgs/${org}/private-regions/${slug}`,
        {
          method: "DELETE",
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["private-regions", org] });
      queryClient.invalidateQueries({ queryKey: ["regions", org] });
    },
  });
}

export function useAgents(
  org: string,
  options?: { refetchInterval?: number; enabled?: boolean },
) {
  return useQuery({
    queryKey: ["agents", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: AgentInfo[] }>(
        `/api/v1/orgs/${org}/agents`,
      );
      return response.data || [];
    },
    enabled: (options?.enabled ?? true) && !!org,
    refetchInterval: options?.refetchInterval ?? 30_000,
  });
}

/** Fleet-wide agent list (superadmin only): every org agent plus every
 * platform-operated system agent, across all organizations. */
export function useAllAgents(options?: {
  refetchInterval?: number;
  enabled?: boolean;
}) {
  return useQuery({
    queryKey: ["system-agents"],
    queryFn: async () => {
      const response = await apiFetch<{ data?: AgentInfo[] }>(
        `/api/v1/system/agents`,
      );
      return response.data || [];
    },
    enabled: options?.enabled ?? true,
    refetchInterval: options?.refetchInterval ?? 30_000,
  });
}

export function useRevokeAgent(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<{ status: string }>(`/api/v1/orgs/${org}/agents/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["agents", org] });
      queryClient.invalidateQueries({ queryKey: ["private-regions", org] });
    },
  });
}

export function useEnrollmentTokens(
  org: string,
  options?: { refetchInterval?: number; enabled?: boolean },
) {
  return useQuery({
    queryKey: ["agent-enrollment-tokens", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: EnrollmentToken[] }>(
        `/api/v1/orgs/${org}/agent-enrollment-tokens`,
      );
      return response.data || [];
    },
    enabled: (options?.enabled ?? true) && !!org,
    refetchInterval: options?.refetchInterval,
  });
}

export function useMintEnrollmentToken(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: { regionSlug: string; expiresIn?: string }) =>
      apiFetch<MintedEnrollmentToken>(
        `/api/v1/orgs/${org}/agent-enrollment-tokens`,
        {
          method: "POST",
          body: JSON.stringify(request),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["agent-enrollment-tokens", org],
      });
    },
  });
}

export function useDeleteEnrollmentToken(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<{ status: string }>(
        `/api/v1/orgs/${org}/agent-enrollment-tokens/${uid}`,
        {
          method: "DELETE",
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["agent-enrollment-tokens", org],
      });
    },
  });
}

// ---------------------------------------------------------------------------
// Incident publications — the status-page publication overlay (spec
// 2026-08-19-08).
//
// A publication is the CUSTOMER-FACING incident: it has a public title, a
// public state and an append-only narrative. It is deliberately not the
// internal `Incident`, which carries ack/snooze metadata, an auto-generated
// title built from the check slug, and probe diagnostics that must never reach
// a customer.
// ---------------------------------------------------------------------------

export type PublicationState =
  | "investigating"
  | "identified"
  | "monitoring"
  | "resolved";

export type PublicationSeverity = "minor" | "major" | "critical";

export interface PublicationUpdate {
  uid: string;
  kind: string;
  title: string;
  bodyMarkdown: string;
  publishedAt: string;
  /** Absent for updates generated by the auto-publish pipeline. */
  authorUid?: string;
}

export interface IncidentPublication {
  uid: string;
  statusPageUid: string;
  incidentUid?: string;
  title: string;
  state: PublicationState;
  severity?: PublicationSeverity;
  autoCreated: boolean;
  /** True once a person edited the publication or posted an update on it. */
  humanTouched: boolean;
  publishedAt: string;
  resolvedAt?: string;
  createdAt: string;
  updatedAt: string;
  updates?: PublicationUpdate[];
  affectedResources?: string[];
  /**
   * True when the entry is still open on the public page while the internal
   * incident it tracks has already resolved — the state that leaves a status
   * page (and a TV board) claiming trouble with every check up.
   */
  stale?: boolean;
}

export function useIncidentPublications(
  org: string,
  statusPageUid: string,
  options?: { activeOnly?: boolean; staleOnly?: boolean; refetchInterval?: number },
) {
  return useQuery({
    queryKey: [
      "incidentPublications",
      org,
      statusPageUid,
      options?.activeOnly,
      options?.staleOnly,
    ],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.activeOnly) params.set("active", "true");
      if (options?.staleOnly) params.set("stale", "true");
      const query = params.toString();
      const response = await apiFetch<{ data?: IncidentPublication[] }>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/incidents${query ? `?${query}` : ""}`,
      );
      return response.data || [];
    },
    enabled: !!org && !!statusPageUid,
    refetchInterval: options?.refetchInterval,
  });
}

/** One open publication whose incident has recovered, with the page it sits on. */
export interface StalePublication {
  page: StatusPage;
  publication: IncidentPublication;
}

/**
 * Every publication in the org that is open on a public page while the
 * incident behind it has resolved.
 *
 * One request per status page rather than a new org-wide endpoint: an org has
 * a handful of pages, the response is the `active=true&stale=true` slice (near
 * always empty), and the alternative would be a second listing surface to keep
 * in step with the first. Disabled entirely while the org has no status page,
 * so the ordinary checks list costs nothing extra.
 */
export function useStalePublications(org: string): StalePublication[] {
  const { data: pages } = useStatusPages(org, { staleTime: 60_000 });

  const results = useQueries({
    queries: (pages ?? []).map((page) => ({
      queryKey: ["incidentPublications", org, page.uid, true, true],
      queryFn: async () => {
        const response = await apiFetch<{ data?: IncidentPublication[] }>(
          `/api/v1/orgs/${org}/status-pages/${page.uid}/incidents?active=true&stale=true`,
        );
        return response.data || [];
      },
      enabled: !!org,
      staleTime: 30_000,
    })),
  });

  const out: StalePublication[] = [];

  (pages ?? []).forEach((page, index) => {
    for (const publication of results[index]?.data ?? []) {
      out.push({ page, publication });
    }
  });

  return out;
}

export function useIncidentPublication(
  org: string,
  statusPageUid: string,
  uid: string,
) {
  return useQuery({
    queryKey: ["incidentPublication", org, statusPageUid, uid],
    queryFn: () =>
      apiFetch<IncidentPublication>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/incidents/${uid}`,
      ),
    enabled: !!org && !!statusPageUid && !!uid,
  });
}

export interface CreateIncidentPublicationRequest {
  title: string;
  state?: PublicationState;
  severity?: PublicationSeverity;
  incidentUid?: string;
  bodyMarkdown?: string;
}

export function useCreateIncidentPublication(
  org: string,
  statusPageUid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateIncidentPublicationRequest) =>
      apiFetch<IncidentPublication>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/incidents`,
        { method: "POST", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["incidentPublications", org, statusPageUid],
      });
    },
  });
}

export interface UpdateIncidentPublicationRequest {
  title?: string;
  state?: PublicationState;
  /** An empty string clears the severity badge. */
  severity?: PublicationSeverity | "";
}

export function useUpdateIncidentPublication(
  org: string,
  statusPageUid: string,
  uid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateIncidentPublicationRequest) =>
      apiFetch<IncidentPublication>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/incidents/${uid}`,
        { method: "PATCH", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["incidentPublications", org, statusPageUid],
      });
      queryClient.invalidateQueries({
        queryKey: ["incidentPublication", org, statusPageUid, uid],
      });
    },
  });
}

export interface AppendPublicationUpdateRequest {
  kind: string;
  title?: string;
  bodyMarkdown: string;
}

/**
 * Appends one narrative entry. Updates are append-only by design — there is no
 * edit or delete endpoint, because a posted update is a promise, not a draft.
 */
export function useAppendPublicationUpdate(
  org: string,
  statusPageUid: string,
  uid: string,
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: AppendPublicationUpdateRequest) =>
      apiFetch<PublicationUpdate>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/incidents/${uid}/updates`,
        { method: "POST", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["incidentPublication", org, statusPageUid, uid],
      });
      queryClient.invalidateQueries({
        queryKey: ["incidentPublications", org, statusPageUid],
      });
    },
  });
}

/** Publications of one internal incident, across every status page. */
export function useIncidentPublicationsForIncident(
  org: string,
  incidentUid: string,
) {
  return useQuery({
    queryKey: ["incidentPublicationsForIncident", org, incidentUid],
    queryFn: async () => {
      const response = await apiFetch<{ data?: IncidentPublication[] }>(
        `/api/v1/orgs/${org}/incidents/${incidentUid}/publications`,
      );
      return response.data || [];
    },
    enabled: !!org && !!incidentUid,
  });
}

export function usePublishIncident(org: string, incidentUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: {
      statusPageUid: string;
      title?: string;
      severity?: PublicationSeverity;
    }) =>
      apiFetch<IncidentPublication>(
        `/api/v1/orgs/${org}/incidents/${incidentUid}/publications`,
        { method: "POST", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["incidentPublicationsForIncident", org, incidentUid],
      });
    },
  });
}

export function useUnpublishIncident(org: string, incidentUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (publicationUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/incidents/${incidentUid}/publications/${publicationUid}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["incidentPublicationsForIncident", org, incidentUid],
      });
    },
  });
}

// ---------------------------------------------------------------------------
// SLOs (spec 2026-08-20-01)
// ---------------------------------------------------------------------------

export type SloState = "healthy" | "at_risk" | "breached" | "unknown";

export interface Slo {
  uid: string;
  name: string;
  slug: string;
  /** Exactly one of checkUid / checkGroupUid is set. */
  checkUid?: string;
  checkGroupUid?: string;
  checkName?: string;
  checkGroupName?: string;
  targetPct: number;
  timezone: string;
  excludeMaintenance: boolean;
  enabled: boolean;
  /**
   * True while at least one burn-rate alert policy on this objective has an
   * open incident. Derived server-side from the incident rows, so it cannot go
   * stale when somebody resolves the incident by hand.
   */
  burning?: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface SloWindow {
  start: string;
  end: string;
  label: string;
}

export interface SloStatusRow {
  window: SloWindow;
  /**
   * null when the window carries no countable probe. NEVER render null as
   * 100% — no data is not "everything was fine", it is "we were not watching".
   */
  attainmentPct: number | null;
  hasData: boolean;
  targetPct: number;
  totalChecks: number;
  successfulChecks: number;
  monitoredSeconds: number;
  elapsedSeconds: number;
  budgetTotalSeconds: number;
  budgetConsumedSeconds: number;
  budgetRemainingSeconds: number;
  excludedMaintenanceSeconds: number;
  burnRate: number | null;
  projectedExhaustionAt: string | null;
  state: SloState;
  partial: boolean;
}

export interface SloIncidents {
  count: number;
  longestSeconds?: number;
  averageSeconds?: number;
  totalDowntimeSeconds?: number;
}

export interface SloStatus {
  slo: Slo;
  current: SloStatusRow;
  incidents: SloIncidents;
}

export interface CreateSloRequest {
  name: string;
  slug?: string;
  checkUid?: string | null;
  checkGroupUid?: string | null;
  targetPct?: number;
  timezone?: string;
  excludeMaintenance?: boolean;
  enabled?: boolean;
}

export type UpdateSloRequest = Partial<CreateSloRequest>;

export function useSlos(
  org: string,
  params?: { checkUid?: string; enabled?: boolean; staleTime?: number },
) {
  return useQuery({
    queryKey: ["slos", org, { checkUid: params?.checkUid }],
    queryFn: async () => {
      const search = new URLSearchParams();
      if (params?.checkUid) search.set("checkUid", params.checkUid);
      const query = search.toString();
      const response = await apiFetch<{ data?: Slo[] }>(
        `/api/v1/orgs/${org}/slos${query ? `?${query}` : ""}`,
      );
      return response.data ?? [];
    },
    enabled: (params?.enabled ?? true) && !!org,
    staleTime: params?.staleTime,
  });
}

export function useSlo(org: string, uid: string) {
  return useQuery({
    queryKey: ["slo", org, uid],
    queryFn: () => apiFetch<Slo>(`/api/v1/orgs/${org}/slos/${uid}`),
    enabled: !!org && !!uid,
  });
}

export function useSloStatus(org: string, uid: string) {
  return useQuery({
    queryKey: ["sloStatus", org, uid],
    queryFn: () =>
      apiFetch<SloStatus>(`/api/v1/orgs/${org}/slos/${uid}/status`),
    enabled: !!org && !!uid,
  });
}

export function useSloHistory(org: string, uid: string, months = 12) {
  return useQuery({
    queryKey: ["sloHistory", org, uid, months],
    queryFn: async () => {
      const response = await apiFetch<{ data?: SloStatusRow[] }>(
        `/api/v1/orgs/${org}/slos/${uid}/history?months=${months}`,
      );
      return response.data ?? [];
    },
    enabled: !!org && !!uid,
  });
}

export interface SloBurndownPoint {
  at: string;
  /** Negative once the budget is overspent — never clamped. */
  budgetRemainingSeconds: number;
  /** The pace that spends the budget exactly over the window, no faster. */
  idealRemainingSeconds: number;
  attainmentPct: number | null;
  hasData: boolean;
}

export interface SloBurndown {
  window: SloWindow;
  targetPct: number;
  budgetTotalSeconds: number;
  data: SloBurndownPoint[];
}

export function useSloBurndown(org: string, uid: string) {
  return useQuery({
    queryKey: ["sloBurndown", org, uid],
    queryFn: () =>
      apiFetch<SloBurndown>(`/api/v1/orgs/${org}/slos/${uid}/burndown`),
    enabled: !!org && !!uid,
  });
}

export function useCreateSlo(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateSloRequest) =>
      apiFetch<Slo>(`/api/v1/orgs/${org}/slos`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["slos", org] });
    },
  });
}

export function useUpdateSlo(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateSloRequest) =>
      apiFetch<Slo>(`/api/v1/orgs/${org}/slos/${uid}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["slos", org] });
      queryClient.invalidateQueries({ queryKey: ["slo", org, uid] });
      queryClient.invalidateQueries({ queryKey: ["sloStatus", org, uid] });
      queryClient.invalidateQueries({ queryKey: ["sloBurndown", org, uid] });
    },
  });
}

export function useDeleteSlo(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/slos/${uid}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["slos", org] });
    },
  });
}

// ---------------------------------------------------------------------------
// SLO burn-rate alert policies (spec 2026-08-21-08)
// ---------------------------------------------------------------------------

export type SloAlertPolicyKind = "fast" | "slow";
export type SloAlertSeverity = "critical" | "warning";

export interface SloAlertPolicy {
  uid: string;
  sloUid: string;
  /** Built-in identity. Not writable. */
  kind: SloAlertPolicyKind;
  enabled: boolean;
  longWindowSeconds: number;
  shortWindowSeconds: number;
  /** Burn-rate multiple both windows must exceed for the policy to fire. */
  threshold: number;
  severity: SloAlertSeverity;
  /** Per-window probe floor. Below it a window is inconclusive. */
  minSamples: number;
  lastEvaluatedAt: string | null;
  /**
   * Recomputed per request. `null` means the window carried no countable probe
   * — it is NEVER 0, which would read as "healthy".
   */
  longBurnRate: number | null;
  shortBurnRate: number | null;
  longSamples: number;
  shortSamples: number;
  longConclusive: boolean;
  shortConclusive: boolean;
  overThresholdNow: boolean;
  firing: boolean;
  incidentUid: string | null;
  incidentNumber: number | null;
  firingSince: string | null;
  /** Hysteresis anchor: both windows below threshold since this instant. */
  resolvingSince: string | null;
}

export type UpdateSloAlertPolicyRequest = Partial<
  Pick<
    SloAlertPolicy,
    | "enabled"
    | "longWindowSeconds"
    | "shortWindowSeconds"
    | "threshold"
    | "severity"
    | "minSamples"
  >
>;

export function useSloAlertPolicies(org: string, uid: string) {
  return useQuery({
    queryKey: ["sloAlertPolicies", org, uid],
    queryFn: async () => {
      const response = await apiFetch<{ data?: SloAlertPolicy[] }>(
        `/api/v1/orgs/${org}/slos/${uid}/alert-policies`,
      );
      return response.data ?? [];
    },
    enabled: !!org && !!uid,
  });
}

export function useUpdateSloAlertPolicy(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      policyUid,
      request,
    }: {
      policyUid: string;
      request: UpdateSloAlertPolicyRequest;
    }) =>
      apiFetch<SloAlertPolicy>(
        `/api/v1/orgs/${org}/slos/${uid}/alert-policies/${policyUid}`,
        { method: "PATCH", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["sloAlertPolicies", org, uid],
      });
      // The burning badge is derived from the same incidents, so the list and
      // the detail header have to re-read too.
      queryClient.invalidateQueries({ queryKey: ["slos", org] });
      queryClient.invalidateQueries({ queryKey: ["slo", org, uid] });
    },
  });
}

// ---------------------------------------------------------------------------
// Report schedules (spec 2026-08-20-01)
// ---------------------------------------------------------------------------

export type ReportFrequency = "weekly" | "monthly";

export interface ReportSchedule {
  uid: string;
  name: string;
  frequency: ReportFrequency;
  timezone: string;
  /** PII: only ever shown to the organization's own admins. */
  recipients: string[];
  checkUids: string[];
  checkGroupUids: string[];
  includeSlos: boolean;
  enabled: boolean;
  lastPeriodStart?: string;
  lastRunAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface CreateReportScheduleRequest {
  name: string;
  frequency: ReportFrequency;
  timezone?: string;
  recipients: string[];
  checkUids?: string[];
  checkGroupUids?: string[];
  includeSlos?: boolean;
  enabled?: boolean;
}

export type UpdateReportScheduleRequest = Partial<CreateReportScheduleRequest>;

export function useReportSchedules(org: string, opts?: ListQueryOptions) {
  return useQuery({
    queryKey: ["reportSchedules", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: ReportSchedule[] }>(
        `/api/v1/orgs/${org}/report-schedules`,
      );
      return response.data ?? [];
    },
    enabled: (opts?.enabled ?? true) && !!org,
    staleTime: opts?.staleTime,
  });
}

export function useReportSchedule(org: string, uid: string) {
  return useQuery({
    queryKey: ["reportSchedule", org, uid],
    queryFn: () =>
      apiFetch<ReportSchedule>(`/api/v1/orgs/${org}/report-schedules/${uid}`),
    enabled: !!org && !!uid,
  });
}

export function useCreateReportSchedule(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateReportScheduleRequest) =>
      apiFetch<ReportSchedule>(`/api/v1/orgs/${org}/report-schedules`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["reportSchedules", org] });
    },
  });
}

export function useUpdateReportSchedule(org: string, uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateReportScheduleRequest) =>
      apiFetch<ReportSchedule>(`/api/v1/orgs/${org}/report-schedules/${uid}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["reportSchedules", org] });
      queryClient.invalidateQueries({ queryKey: ["reportSchedule", org, uid] });
    },
  });
}

export function useDeleteReportSchedule(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/report-schedules/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["reportSchedules", org] });
    },
  });
}

export function useTestReportSchedule(org: string) {
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/report-schedules/${uid}/test`, {
        method: "POST",
        body: JSON.stringify({}),
      }),
  });
}

// ---------------------------------------------------------------------------
// Per-user UI state (spec 2026-08-28-17)
//
// Small preferences that belong to the *user*, not the org, stored server-side
// so they follow the user across devices instead of living in one browser's
// localStorage. The API allowlists the key shape (v1: `onboarding.<org>`) and
// caps the value size, so this is deliberately not a general key-value store.
// ---------------------------------------------------------------------------

/** Value stored under `onboarding.<org>` once the checklist is dismissed. */
export interface OnboardingUiState {
  dismissedAt?: string;
}

/** Query key for one ui-state entry, shared by the read and the mutations. */
function uiStateQueryKey(key: string) {
  return ["uiState", key] as const;
}

/**
 * Reads a ui-state entry. A missing entry is `null`, not an error: the server
 * answers 404 for "nothing stored", which is the ordinary case.
 */
export function useUiState<T = Record<string, unknown>>(
  key: string,
  opts?: ListQueryOptions,
) {
  return useQuery({
    queryKey: uiStateQueryKey(key),
    queryFn: async (): Promise<T | null> => {
      try {
        const response = await apiFetch<{ value: T }>(
          `/api/v1/me/ui-state/${encodeURIComponent(key)}`,
        );
        return response.value ?? null;
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }
    },
    enabled: (opts?.enabled ?? true) && !!key,
    staleTime: opts?.staleTime,
  });
}

export function useSetUiState<T = Record<string, unknown>>(key: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (value: T) =>
      apiFetch<{ value: T }>(`/api/v1/me/ui-state/${encodeURIComponent(key)}`, {
        method: "PUT",
        body: JSON.stringify(value),
      }),
    onSuccess: (response) => {
      // Write through rather than invalidate: the card that just dismissed
      // itself must not flicker back while a refetch is in flight.
      queryClient.setQueryData(uiStateQueryKey(key), response.value);
    },
  });
}

export function useDeleteUiState(key: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<void>(`/api/v1/me/ui-state/${encodeURIComponent(key)}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.setQueryData(uiStateQueryKey(key), null);
    },
  });
}

/** The ui-state key holding a user's getting-started checklist dismissal. */
export function onboardingUiStateKey(org: string) {
  return `onboarding.${org}`;
}
