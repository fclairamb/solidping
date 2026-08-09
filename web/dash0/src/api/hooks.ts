import {
  useQuery,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { apiFetch, getToken } from "./client";
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
  escalationPolicyUid?: string | null;
  type?: "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "email" | "domain" | "smtp" | "udp" | "ssh" | "pop3" | "imap" | "websocket" | "postgresql" | "mysql" | "redis" | "mongodb" | "ftp" | "sftp" | "js" | "mssql" | "oracle" | "clickhouse" | "grpc" | "kafka" | "mqtt" | "a2s" | "minecraft" | "rabbitmq" | "snmp" | "docker" | "browser" | "freebox_line" | "dnsbl" | "sip" | "ntp" | "rdp" | "sleep";
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
    status?: "up" | "down" | "error" | "timeout" | "created";
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
}

export interface CreateCheckRequest {
  name?: string;
  slug?: string;
  description?: string;
  checkGroupUid?: string;
  /** Escalation policy to assign; omit/empty inherits (group → org default → none). */
  escalationPolicyUid?: string;
  type?: "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "email" | "domain" | "smtp" | "udp" | "ssh" | "pop3" | "imap" | "websocket" | "postgresql" | "mysql" | "redis" | "mongodb" | "ftp" | "sftp" | "js" | "mssql" | "oracle" | "clickhouse" | "grpc" | "kafka" | "mqtt" | "a2s" | "minecraft" | "rabbitmq" | "snmp" | "docker" | "browser" | "freebox_line" | "dnsbl" | "sip" | "ntp" | "rdp" | "sleep";
  config: Record<string, unknown>;
  regions?: string[];
  /** Omit to use the automatic default (period / region count). */
  regionSpread?: string;
  labels?: Record<string, string>;
  enabled?: boolean;
  internal?: boolean;
  period?: string;
}

export interface UpdateCheckRequest {
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
  internal?: boolean;
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

export interface IncidentDetail {
  uid?: string;
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
  acknowledgedBy?: string;
  snoozedUntil?: string;
  snoozedBy?: string;
  snoozeReason?: string;
  escalatedAt?: string;
  resolvedAt?: string;
  resolvedBy?: string;
  resolutionType?: "auto" | "manual" | "expired";
  failureCount?: number;
  relapseCount?: number;
  lastReopenedAt?: string;
  causedByIncidentUid?: string;
  pagingSuppressed?: boolean;
}

export interface Event {
  uid?: string;
  eventType?: string;
  actorType?: "system" | "user";
  actorUid?: string;
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
  }
): string {
  const params = new URLSearchParams();
  if (options?.labels) params.set("labels", options.labels);
  if (options?.with) params.set("with", options.with);
  if (options?.q) params.set("q", options.q);
  if (options?.type) params.set("type", options.type);
  if (options?.checkGroupUid) params.set("checkGroupUid", options.checkGroupUid);
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
  }
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
}

/**
 * Fetches the org's aggregate check counters. The endpoint is cached
 * server-side for ~1 minute, so polling it faster than that is wasted work —
 * callers should pass a refetchInterval no shorter than the other dashboard
 * queries use.
 */
export function useCheckStats(
  org: string,
  options?: { refetchInterval?: number }
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
    checkGroupUid?: string;
    internal?: string;
    status?: string;
    limit?: number;
    /** Opt-in ordering; "group" loads in the page's display order. */
    sort?: string;
  }
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
  }
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
        `/api/v1/orgs/${org}/checks/${uid}?with=last_result,last_status_change`
      ),
    enabled: !!org && !!uid,
    refetchInterval: options?.refetchInterval,
    refetchOnMount: options?.refetchOnMount,
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

export interface LabelSuggestion {
  value: string;
  count: number;
}

export function useLabelSuggestions(
  org: string,
  opts: { key?: string; q?: string; limit?: number; enabled?: boolean }
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
export const CONVERT_SOURCES = ["gatus", "betterstack", "uptime-kuma"] as const;
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
    mutationFn: (params: { source: ConvertSource; body: string; dryRun?: boolean }) =>
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
        `/api/v1/orgs/${org}/check-groups`
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
export interface PerCheckDependencies {
  dependsOn: DependencyEdge[];
  dependedOnBy: DependencyEdge[];
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

export function useDependencyGraph(
  org: string,
  opts?: { enabled?: boolean },
) {
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
  }
) {
  const { refetchInterval, ...queryOptions } = options || {};
  return useQuery({
    queryKey: ["results", org, queryOptions],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.checkUid) params.set("checkUid", options.checkUid);
      if (options?.periodType) params.set("periodType", options.periodType);
      if (options?.periodStartAfter) params.set("periodStartAfter", options.periodStartAfter);
      if (options?.periodEndBefore) params.set("periodEndBefore", options.periodEndBefore);
      if (options?.region) params.set("region", options.region);
      if (options?.with) params.set("with", options.with);
      if (options?.cursor) params.set("cursor", options.cursor);
      if (options?.size) params.set("limit", options.size.toString());
      const query = params.toString();
      const path = `/api/v1/orgs/${org}/results${query ? `?${query}` : ""}`;
      const response = await apiFetch<{
        data?: OrgResult[];
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

export function useResult(
  org: string,
  checkUid: string,
  resultUid: string,
  options?: { region?: string }
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

/** Fetches all result pages by following cursors until exhausted. */
export function useAllResults(
  org: string,
  options?: {
    checkUid?: string;
    periodType?: string;
    periodStartAfter?: string;
    periodEndBefore?: string;
    with?: string;
    size?: number;
    refetchInterval?: number;
  }
) {
  const { refetchInterval, ...queryOptions } = options || {};
  return useQuery({
    queryKey: ["allResults", org, queryOptions],
    queryFn: async () => {
      const allData: OrgResult[] = [];
      let cursor: string | undefined;
      const pageSize = options?.size ?? 100;

      do {
        const params = new URLSearchParams();
        if (options?.checkUid) params.set("checkUid", options.checkUid);
        if (options?.periodType) params.set("periodType", options.periodType);
        if (options?.periodStartAfter)
          params.set("periodStartAfter", options.periodStartAfter);
        if (options?.periodEndBefore)
          params.set("periodEndBefore", options.periodEndBefore);
        if (options?.with) params.set("with", options.with);
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

      return { data: allData };
    },
    enabled: !!org,
    refetchInterval,
  });
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
  }
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
      if (options?.causedByIncidentUid) params.set("causedByIncidentUid", options.causedByIncidentUid);
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
  }
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
  }
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
  notifUid: string
) {
  return useQuery<IncidentNotification>({
    queryKey: ["incidentNotification", org, incidentUid, notifUid],
    queryFn: () =>
      apiFetch<IncidentNotification>(
        `/api/v1/orgs/${org}/incidents/${incidentUid}/notifications/${notifUid}`
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
        `/api/v1/orgs/${org}/notifications/${notifUid}`
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
  limit = 10
) {
  return useQuery({
    queryKey: ["integrationNotifications", org, integrationUid, limit],
    queryFn: async () => {
      const params = new URLSearchParams({
        connectionUid: integrationUid,
        limit: String(limit),
      });
      const response = await apiFetch<{ data?: IncidentNotification[] }>(
        `/api/v1/orgs/${org}/notifications?${params.toString()}`
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
        `/api/v1/orgs/${org}/email-suppressions`
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
  }
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
        `/api/v1/orgs/${org}/tokens?type=pat`
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
        `/api/v1/orgs/${org}/tokens?type=refresh`
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

export interface StatusPage {
  uid: string;
  name: string;
  slug: string;
  description?: string;
  visibility: "public" | "private";
  isDefault: boolean;
  enabled: boolean;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyDays: number;
  historyPeriod: StatusPagePeriod;
  // Operator-authored stylesheet applied to the PUBLIC status page (status0).
  // Never applied to dash0's own chrome — only inside the appearance preview
  // iframe, which loads the real status0 renderer.
  customCss?: string;
  // Custom-domain fields are only present on the authenticated org endpoints.
  customDomain?: string;
  customDomainStatus?: CustomDomainStatus;
  customDomainCertStatus?: CustomDomainCertStatus;
  customDomainRecords?: DnsRecord[];
  // Settings mirrors the storage shape (an unset section means "using the
  // default"); AvailabilityThresholds is always the resolved, non-nil pair.
  settings?: StatusPageSettings;
  availabilityThresholds: AvailabilityThresholds;
  sections?: StatusPageSection[];
  createdAt?: string;
}

export interface StatusPageSection {
  uid: string;
  name: string;
  slug: string;
  position: number;
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
  visibility?: "public" | "private";
  isDefault?: boolean;
  showAvailability?: boolean;
  showResponseTime?: boolean;
  historyDays?: number;
  historyPeriod?: StatusPagePeriod;
  customCss?: string;
  customDomain?: string;
}

export interface UpdateStatusPageRequest {
  name?: string;
  slug?: string;
  description?: string;
  visibility?: "public" | "private";
  isDefault?: boolean;
  enabled?: boolean;
  showAvailability?: boolean;
  showResponseTime?: boolean;
  historyDays?: number;
  historyPeriod?: StatusPagePeriod;
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
}

export interface UpdateSectionRequest {
  name?: string;
  slug?: string;
  position?: number;
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
export function useStatusPages(org: string) {
  return useQuery({
    queryKey: ["statusPages", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: StatusPage[] }>(
        `/api/v1/orgs/${org}/status-pages`
      );
      return response.data || [];
    },
    enabled: !!org,
  });
}

export function useStatusPage(org: string, uid: string, options?: { with?: string }) {
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
        { method: "POST" }
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPages", org] });
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, uid] });
    },
  });
}

// Status page subscriber hooks (read-only admin list + remove).
export interface StatusPageSubscriber {
  uid: string;
  email: string;
  scope: string;
  incidentUid?: string;
  confirmed: boolean;
  createdAt: string;
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
  statusPageUid: string
) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/subscribers/${uid}`,
        { method: "DELETE" }
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
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections`
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
        { method: "POST", body: JSON.stringify(request) }
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPageSections", org, statusPageUid] });
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, statusPageUid] });
    },
  });
}

export function useUpdateSection(org: string, statusPageUid: string, sectionUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: UpdateSectionRequest) =>
      apiFetch<StatusPageSection>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}`,
        { method: "PATCH", body: JSON.stringify(request) }
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPageSections", org, statusPageUid] });
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, statusPageUid] });
    },
  });
}

export function useDeleteSection(org: string, statusPageUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (sectionUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}`,
        { method: "DELETE" }
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusPageSections", org, statusPageUid] });
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, statusPageUid] });
    },
  });
}

// Resource hooks
export function useStatusPageResources(
  org: string,
  statusPageUid: string,
  sectionUid: string
) {
  return useQuery({
    queryKey: ["statusPageResources", org, statusPageUid, sectionUid],
    queryFn: async () => {
      const response = await apiFetch<{ data?: StatusPageResource[] }>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources`
      );
      return response.data || [];
    },
    enabled: !!org && !!statusPageUid && !!sectionUid,
  });
}

export function useCreateResource(org: string, statusPageUid: string, sectionUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: CreateResourceRequest) =>
      apiFetch<StatusPageResource>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources`,
        { method: "POST", body: JSON.stringify(request) }
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageResources", org, statusPageUid, sectionUid],
      });
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, statusPageUid] });
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
  const pageWithSectionsKey = ["statusPage", org, statusPageUid, { with: "sections" }];

  return useMutation({
    mutationFn: (uids: string[]) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/reorder`,
        { method: "POST", body: JSON.stringify({ uids }) }
      ),
    onMutate: async (uids) => {
      await queryClient.cancelQueries({ queryKey: ["statusPage", org, statusPageUid] });
      const snapshot = queryClient.getQueryData<StatusPage>(pageWithSectionsKey);
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
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, statusPageUid] });
    },
  });
}

export function useReorderResources(org: string, statusPageUid: string, sectionUid: string) {
  const queryClient = useQueryClient();
  const pageWithSectionsKey = ["statusPage", org, statusPageUid, { with: "sections" }];

  return useMutation({
    mutationFn: (uids: string[]) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources/reorder`,
        { method: "POST", body: JSON.stringify({ uids }) }
      ),
    onMutate: async (uids) => {
      await queryClient.cancelQueries({ queryKey: ["statusPage", org, statusPageUid] });
      const snapshot = queryClient.getQueryData<StatusPage>(pageWithSectionsKey);
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
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, statusPageUid] });
    },
  });
}

export function useUpdateResource(org: string, statusPageUid: string, sectionUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ resourceUid, request }: { resourceUid: string; request: UpdateResourceRequest }) =>
      apiFetch<StatusPageResource>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources/${resourceUid}`,
        { method: "PATCH", body: JSON.stringify(request) }
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageResources", org, statusPageUid, sectionUid],
      });
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, statusPageUid] });
    },
  });
}

export function useDeleteResource(org: string, statusPageUid: string, sectionUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (resourceUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/status-pages/${statusPageUid}/sections/${sectionUid}/resources/${resourceUid}`,
        { method: "DELETE" }
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["statusPageResources", org, statusPageUid, sectionUid],
      });
      queryClient.invalidateQueries({ queryKey: ["statusPage", org, statusPageUid] });
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
        { skipAuth: true }
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
      // (2026-07-08 funnel audit).
      apiFetch<{
        accessToken: string;
        refreshToken?: string;
        expiresIn?: number;
        user: { email: string; name?: string; avatarUrl?: string; role: string };
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
        user: { uid: string; email: string; name?: string; avatarUrl?: string; role: string };
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

export function useInvitations(org: string) {
  return useQuery({
    queryKey: ["invitations", org],
    queryFn: () =>
      apiFetch<{ data: Invitation[] }>(`/api/v1/orgs/${org}/invitations`),
  });
}

export function useCreateInvitation(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { email: string; role: string }) =>
      apiFetch<{ token: string; inviteUrl: string }>(
        `/api/v1/orgs/${org}/invitations`,
        {
          method: "POST",
          body: JSON.stringify(data),
        }
      ),
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
      queryClient.invalidateQueries({ queryKey: ["membership-requests", "me"] });
      queryClient.invalidateQueries({ queryKey: ["auth", "me"] });
    },
  });
}

export function useMyMembershipRequests() {
  return useQuery({
    queryKey: ["membership-requests", "me"],
    queryFn: () =>
      apiFetch<{ data: MembershipRequestSummary[] }>(
        "/api/v1/auth/membership-requests"
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
      queryClient.invalidateQueries({ queryKey: ["membership-requests", "me"] });
      queryClient.invalidateQueries({ queryKey: ["auth", "me"] });
    },
  });
}

export function useOrgMembershipRequests(
  org: string,
  opts?: { status?: MembershipRequestStatus; enabled?: boolean }
) {
  const status = opts?.status;
  const qs = status ? `?status=${status}` : "";
  return useQuery({
    queryKey: ["membership-requests", "org", org, status ?? "all"],
    queryFn: () =>
      apiFetch<{ data: MembershipRequestAdminView[] }>(
        `/api/v1/orgs/${org}/membership-requests${qs}`
      ),
    enabled: opts?.enabled !== false,
  });
}

export function useApproveMembershipRequest(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ uid, role }: { uid: string; role?: string }) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/membership-requests/${uid}/approve`,
        {
          method: "POST",
          body: JSON.stringify(role ? { role } : {}),
        }
      ),
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
      apiFetch<void>(
        `/api/v1/orgs/${org}/membership-requests/${uid}/reject`,
        {
          method: "POST",
          body: JSON.stringify(reason ? { reason } : {}),
        }
      ),
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

export function useMembers(org: string) {
  return useQuery({
    queryKey: ["members", org],
    queryFn: () =>
      apiFetch<{ data: MemberResponse[] }>(`/api/v1/orgs/${org}/members`),
    enabled: !!org,
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

// useDeleteOrg deletes the whole organization. Owner-only, enforced server-side
// by the RequireOrgOwner middleware — an admin gets a 403 even if the UI slipped
// and showed them the button. The body repeats the slug as confirmation.
export function useDeleteOrg(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (slug: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}`, {
        method: "DELETE",
        body: JSON.stringify({ slug }),
      }),
    onSuccess: () => {
      queryClient.clear();
    },
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
  user: { uid: string; email: string; name?: string; avatarUrl?: string; role: string };
  organization: { uid: string; slug: string; name?: string };
  organizations?: Array<{ slug: string; name?: string; role: string }>;
}

export function useAcceptInvite() {
  return useMutation({
    mutationFn: (data: {
      token: string;
      name?: string;
      password?: string;
    }) =>
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
}

export function useOrgSettings(org: string) {
  return useQuery({
    queryKey: ["org-settings", org],
    queryFn: () =>
      apiFetch<OrgSettings>(`/api/v1/orgs/${org}/settings`),
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
    staleTime: Infinity,
  });
}

export interface FeaturesResponse {
  bugReport: boolean;
}

export function useFeatures() {
  return useQuery({
    queryKey: ["features"],
    queryFn: () => apiFetch<FeaturesResponse>("/api/v1/features"),
    staleTime: 5 * 60 * 1000,
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
    "/api/v1/system/parameters"
  );
  return { data: response.data || [], envOverrides: response.envOverrides || [] };
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
        "/api/v1/system/scheduling/lane-load"
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
      apiFetch<SystemParameter>(
        `/api/v1/system/parameters/${key}`,
        {
          method: "PUT",
          body: JSON.stringify({ value, secret }),
        }
      ),
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
      apiFetch<SlackSocketStatus>(
        "/api/v1/integrations/slack/socket/status",
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
        }
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
        `/api/v1/orgs/${org}/check-types`
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
      const response = await apiFetch<{ data: Array<{ checkType: string; samples: SampleConfig[] }> }>(
        `/api/v1/check-types/samples?type=${encodeURIComponent(checkType)}`
      );
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
      apiFetch<OnCallSchedule>(
        `/api/v1/orgs/${org}/on-call-schedules/${uid}`,
      ),
    enabled: !!org && !!uid,
  });
}

export function useOnCallSchedulePreview(
  org: string,
  uid: string,
  days = 14,
) {
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
      apiFetch<OnCallSchedule>(
        `/api/v1/orgs/${org}/on-call-schedules/${uid}`,
        { method: "PATCH", body: JSON.stringify(request) },
      ),
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

export function useEscalationPolicies(org: string) {
  return useQuery({
    queryKey: ["escalationPolicies", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: EscalationPolicy[] }>(
        `/api/v1/orgs/${org}/escalation-policies`,
      );
      return response.data || [];
    },
    enabled: !!org,
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
  | "opsgenie"
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
  opsgenie: NOTIFY,
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

export function useIntegrations(org: string) {
  return useQuery({
    queryKey: ["integrations", org],
    queryFn: async () => {
      const response = await apiFetch<{ data?: Integration[] }>(
        `/api/v1/orgs/${org}/integrations`,
      );
      return response.data || [];
    },
    enabled: !!org,
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
  kind: "investigating" | "identified" | "monitoring" | "resolved" | "maintenance" | "info";
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
  sectionUid?: string;
  checkUid?: string;
  incidentUid?: string;
  title?: string;
  bodyMarkdown?: string;
  linkUrl?: string;
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
  } = {}
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
        `/api/v1/orgs/${org}/status-updates${qs ? `?${qs}` : ""}`
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
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["statusUpdates", org] });
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
      apiFetch<{ data: DiscoveryType[] }>(`/api/v1/orgs/${org}/discovery/types`),
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
      apiFetch<{ data: DiscoveryScan[] }>(`/api/v1/orgs/${org}/discovery/scans`),
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
      queryClient.invalidateQueries({ queryKey: ["discoveryScan", org, jobUid] });
      queryClient.invalidateQueries({ queryKey: ["discoveryScans", org] });
    },
  });
}

// useListDiscoveredChecks returns the suggested checks for a scan (or org). The
// frontend groups them by groupKey. While a fan-out scan is active, pass
// pollWhileActive to stream rows in as chunks land.
export function useListDiscoveredChecks(
  org: string,
  opts?: { jobUid?: string; group?: string; promoted?: boolean; source?: string },
  pollWhileActive = false,
) {
  const params = new URLSearchParams();
  if (opts?.jobUid) params.set("jobUid", opts.jobUid);
  if (opts?.group) params.set("group", opts.group);
  if (opts?.promoted !== undefined) params.set("promoted", String(opts.promoted));
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
      apiFetch<NotificationRoutesResponse>(`/api/v1/orgs/${org}/users/me/notification-routes`),
  });
}

export function useCreateNotificationContact(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { type: string; value: string; label?: string }) =>
      apiFetch<NotificationRoute>(`/api/v1/orgs/${org}/users/me/notification-contacts`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["notificationRoutes", org] });
    },
  });
}

export function useDeleteNotificationContact(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (contactUid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/users/me/notification-contacts/${contactUid}`, {
        method: "DELETE",
      }),
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
    mutationFn: ({ routeUid, patch }: { routeUid: string; patch: { enabled?: boolean } }) =>
      apiFetch<NotificationRoute>(`/api/v1/orgs/${org}/users/me/notification-routes/${routeUid}`, {
        method: "PATCH",
        body: JSON.stringify(patch),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["notificationRoutes", org] });
    },
  });
}

export function useTestNotificationRoute(org: string) {
  return useMutation({
    mutationFn: (routeUid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/users/me/notification-routes/${routeUid}/test`, {
        method: "POST",
      }),
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
  /** Cap on outbound WhatsApp template messages per UTC month. null = unlimited. */
  maxWhatsappPerMonth?: number | null;
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
}

export interface EntitlementsResponse {
  limits: EntitlementsLimits;
  usage?: EntitlementsUsage;
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
export function useBackgroundJobChain(org: string, uid: string, opts?: JobsScope) {
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

export type UpdateMaintenanceWindowRequest = Partial<CreateMaintenanceWindowRequest>;

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
      apiFetch<MaintenanceWindow>(`/api/v1/orgs/${org}/maintenance-windows/${uid}`),
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
      queryClient.invalidateQueries({ queryKey: ["maintenanceWindow", org, uid] });
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
  /** Fully-qualified stored region string, e.g. `@acme/dc1`. */
  region: string;
  agentCount: number;
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
      apiFetch<{ status: string }>(`/api/v1/orgs/${org}/private-regions/${slug}`, {
        method: "DELETE",
      }),
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
      const response = await apiFetch<{ data?: AgentInfo[] }>(`/api/v1/orgs/${org}/agents`);
      return response.data || [];
    },
    enabled: (options?.enabled ?? true) && !!org,
    refetchInterval: options?.refetchInterval ?? 30_000,
  });
}

/** Fleet-wide agent list (superadmin only): every org agent plus every
 * platform-operated system agent, across all organizations. */
export function useAllAgents(options?: { refetchInterval?: number; enabled?: boolean }) {
  return useQuery({
    queryKey: ["system-agents"],
    queryFn: async () => {
      const response = await apiFetch<{ data?: AgentInfo[] }>(`/api/v1/system/agents`);
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
      apiFetch<MintedEnrollmentToken>(`/api/v1/orgs/${org}/agent-enrollment-tokens`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["agent-enrollment-tokens", org] });
    },
  });
}

export function useDeleteEnrollmentToken(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<{ status: string }>(`/api/v1/orgs/${org}/agent-enrollment-tokens/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["agent-enrollment-tokens", org] });
    },
  });
}
