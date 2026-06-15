import {
  useQuery,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { apiFetch } from "./client";

// Types
export interface CheckGroup {
  uid: string;
  name: string;
  slug: string;
  description?: string;
  sortOrder: number;
  checkCount: number;
  createdAt: string;
  updatedAt: string;
}

export interface CreateCheckGroupRequest {
  name: string;
  slug?: string;
  description?: string;
  sortOrder?: number;
}

export interface UpdateCheckGroupRequest {
  name?: string;
  slug?: string;
  description?: string;
  sortOrder?: number;
}

export interface Check {
  uid: string;
  name?: string;
  slug?: string;
  description?: string;
  checkGroupUid?: string;
  type?: "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "email" | "domain" | "smtp" | "udp" | "ssh" | "pop3" | "imap" | "websocket" | "postgresql" | "mysql" | "redis" | "mongodb" | "ftp" | "sftp" | "js" | "mssql" | "oracle" | "grpc" | "kafka" | "mqtt" | "a2s" | "minecraft" | "rabbitmq" | "snmp" | "docker" | "browser" | "freebox_line" | "dnsbl" | "sip";
  config?: Record<string, unknown>;
  configPrivateKeys?: string[];
  regions?: string[];
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
  maxAdaptiveIncrease?: number | null;
  confirmationPeriodSeconds?: number;
  recoveryPeriodSeconds?: number;
}

export interface RegionDefinition {
  slug: string;
  emoji: string;
  name: string;
}

export interface CreateCheckRequest {
  name?: string;
  slug?: string;
  description?: string;
  checkGroupUid?: string;
  type?: "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "email" | "domain" | "smtp" | "udp" | "ssh" | "pop3" | "imap" | "websocket" | "postgresql" | "mysql" | "redis" | "mongodb" | "ftp" | "sftp" | "js" | "mssql" | "oracle" | "grpc" | "kafka" | "mqtt" | "a2s" | "minecraft" | "rabbitmq" | "snmp" | "docker" | "browser" | "freebox_line" | "dnsbl" | "sip";
  config: Record<string, unknown>;
  regions?: string[];
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
  config?: Record<string, unknown>;
  regions?: string[];
  labels?: Record<string, string>;
  enabled?: boolean;
  internal?: boolean;
  period?: string;
  reopenCooldownMultiplier?: number | null;
  maxAdaptiveIncrease?: number | null;
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
    checkGroupUid?: string;
    internal?: string;
    status?: string;
    limit?: number;
    cursor?: string;
  }
): string {
  const params = new URLSearchParams();
  if (options?.labels) params.set("labels", options.labels);
  if (options?.with) params.set("with", options.with);
  if (options?.q) params.set("q", options.q);
  if (options?.checkGroupUid) params.set("checkGroupUid", options.checkGroupUid);
  if (options?.internal) params.set("internal", options.internal);
  if (options?.status) params.set("status", options.status);
  if (options?.limit) params.set("limit", options.limit.toString());
  if (options?.cursor) params.set("cursor", options.cursor);
  const query = params.toString();
  return `/api/v1/orgs/${org}/checks${query ? `?${query}` : ""}`;
}

// Checks hooks
export function useChecks(
  org: string,
  options?: { labels?: string; with?: string; q?: string; checkGroupUid?: string; limit?: number }
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
  options?: { with?: string; refetchInterval?: number }
) {
  return useQuery({
    queryKey: ["check", org, uid, { with: options?.with }],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.with) params.set("with", options.with);
      const query = params.toString();
      const path = `/api/v1/orgs/${org}/checks/${uid}${query ? `?${query}` : ""}`;
      return apiFetch<Check>(path);
    },
    enabled: !!org && !!uid,
    refetchInterval: options?.refetchInterval,
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
  maxAdaptiveIncrease?: number | null;
}

export interface ImportResult {
  created: number;
  updated: number;
  skipped: number;
  errors: { index: number; slug: string; error: string }[];
}

// Check Export/Import hooks
export function useImportChecks(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (params: { doc: ExportDocument; dryRun?: boolean }) =>
      apiFetch<ImportResult>(
        `/api/v1/orgs/${org}/checks/import${params.dryRun ? "?dryRun=true" : ""}`,
        {
          method: "POST",
          body: JSON.stringify(params.doc),
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

export function useResult(org: string, checkUid: string, resultUid: string) {
  return useQuery<OrgResultDetail>({
    queryKey: ["result", org, checkUid, resultUid],
    queryFn: () =>
      apiFetch<OrgResultDetail>(
        `/api/v1/orgs/${org}/checks/${checkUid}/results/${resultUid}`,
      ),
    enabled: !!org && !!checkUid && !!resultUid,
    staleTime: Infinity,
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
  }
) {
  const { refetchInterval, ...queryOptions } = options || {};
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
    enabled: !!org,
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
  expiresAt?: string;
}

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

// Status Page types
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
  checkUid: string;
  publicName?: string;
  explanation?: string;
  position: number;
  check?: {
    name?: string;
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

export interface CreateResourceRequest {
  checkUid: string;
  publicName?: string;
  explanation?: string;
  position?: number;
}

export interface UpdateResourceRequest {
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
      apiFetch<{
        accessToken: string;
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

// Organization creation hook
export function useCreateOrg() {
  return useMutation({
    mutationFn: (data: { name: string; slug: string }) =>
      apiFetch<{
        uid: string;
        slug: string;
        name: string;
        accessToken: string;
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
export type MemberRole = "admin" | "user" | "viewer";

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
  user: { email: string; name?: string; avatarUrl?: string; role: string };
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
}

export function useOrgSettings(org: string) {
  return useQuery({
    queryKey: ["org-settings", org],
    queryFn: () =>
      apiFetch<OrgSettings>(`/api/v1/orgs/${org}/settings`),
  });
}

export function useUpdateOrgSettings(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { registrationEmailPattern: string }) =>
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

export function useSystemParameters() {
  return useQuery({
    queryKey: ["system-parameters"],
    queryFn: async () => {
      const response = await apiFetch<{ data: SystemParameter[] }>(
        "/api/v1/system/parameters"
      );
      return response.data || [];
    },
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
  slug: string;
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
  slug: string;
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
  slug?: string;
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

export function useOnCallSchedule(org: string, slug: string) {
  return useQuery({
    queryKey: ["onCallSchedules", org, slug],
    queryFn: () =>
      apiFetch<OnCallSchedule>(
        `/api/v1/orgs/${org}/on-call-schedules/${slug}`,
      ),
    enabled: !!org && !!slug,
  });
}

export function useOnCallSchedulePreview(
  org: string,
  slug: string,
  days = 14,
) {
  return useQuery({
    queryKey: ["onCallSchedules", org, slug, "preview", days],
    queryFn: async () => {
      const response = await apiFetch<{ data?: OnCallPreviewSlot[] }>(
        `/api/v1/orgs/${org}/on-call-schedules/${slug}/preview?days=${days}`,
      );
      return response.data || [];
    },
    enabled: !!org && !!slug,
  });
}

export function useOnCallScheduleOverrides(org: string, slug: string) {
  return useQuery({
    queryKey: ["onCallSchedules", org, slug, "overrides"],
    queryFn: async () => {
      const response = await apiFetch<{ data?: OnCallOverride[] }>(
        `/api/v1/orgs/${org}/on-call-schedules/${slug}/overrides`,
      );
      return response.data || [];
    },
    enabled: !!org && !!slug,
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

export function useUpdateOnCallSchedule(org: string, slug: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: UpdateOnCallScheduleRequest) =>
      apiFetch<OnCallSchedule>(
        `/api/v1/orgs/${org}/on-call-schedules/${slug}`,
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
    mutationFn: (slug: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/on-call-schedules/${slug}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["onCallSchedules", org] });
    },
  });
}

export function useCreateOnCallOverride(org: string, slug: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateOnCallOverrideRequest) =>
      apiFetch<OnCallOverride>(
        `/api/v1/orgs/${org}/on-call-schedules/${slug}/overrides`,
        { method: "POST", body: JSON.stringify(request) },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, slug, "overrides"],
      });
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, slug, "preview"],
      });
    },
  });
}

export function useDeleteOnCallOverride(org: string, slug: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (overrideUid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/on-call-schedules/${slug}/overrides/${overrideUid}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, slug, "overrides"],
      });
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, slug, "preview"],
      });
    },
  });
}

export interface OnCallICalFeedResponse {
  secret: string;
  url: string;
}

export function useEnableOnCallICalFeed(org: string, slug: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<OnCallICalFeedResponse>(
        `/api/v1/orgs/${org}/on-call-schedules/${slug}/ical-feed/enable`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, slug],
      });
    },
  });
}

export function useDisableOnCallICalFeed(org: string, slug: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/on-call-schedules/${slug}/ical-feed/disable`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, slug],
      });
    },
  });
}

export function useRotateOnCallICalFeed(org: string, slug: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<OnCallICalFeedResponse>(
        `/api/v1/orgs/${org}/on-call-schedules/${slug}/ical-feed/rotate`,
        { method: "POST" },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["onCallSchedules", org, slug],
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
  slug: string;
  name: string;
  description?: string;
  repeatMax: number;
  repeatAfterSeconds?: number | null;
  steps?: EscalationPolicyStep[];
  createdAt?: string;
  updatedAt?: string;
}

export interface CreateEscalationPolicyRequest {
  slug: string;
  name: string;
  description?: string;
  repeatMax: number;
  repeatAfterSeconds?: number | null;
  steps: EscalationPolicyStep[];
}

export interface UpdateEscalationPolicyRequest {
  slug?: string;
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

export function useEscalationPolicy(org: string, slug: string) {
  return useQuery({
    queryKey: ["escalationPolicies", org, slug],
    queryFn: () =>
      apiFetch<EscalationPolicy>(
        `/api/v1/orgs/${org}/escalation-policies/${slug}`,
      ),
    enabled: !!org && !!slug,
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
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["escalationPolicies", org] });
    },
  });
}

export function useUpdateEscalationPolicy(org: string, slug: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: UpdateEscalationPolicyRequest) =>
      apiFetch<EscalationPolicy>(
        `/api/v1/orgs/${org}/escalation-policies/${slug}`,
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
    mutationFn: (slug: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/escalation-policies/${slug}`, {
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
  | "ntfy"
  | "opsgenie"
  | "pushover"
  | "freebox"
  | "webpush";

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
  ntfy: NOTIFY,
  opsgenie: NOTIFY,
  pushover: NOTIFY,
  freebox: SOURCE,
  webpush: NOTIFY,
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

// DiscoveryScanProgress is the derived fan-out progress block returned alongside
// a plan scan: chunk counts, an overall derived status, and the host roll-up count.
export interface DiscoveryScanProgress {
  totalChunks: number;
  completedChunks: number;
  failedChunks: number;
  runningChunks: number;
  pendingChunks: number;
  derivedStatus: "pending" | "running" | "success" | "failed";
  hostCount: number;
}

export interface SuggestedCheck {
  type: string;
  config: Record<string, unknown>;
}

export type DiscoverySource = "lan" | "freebox";

export interface DiscoveredHost {
  uid: string;
  organizationUid: string;
  jobUid: string;
  ip: string;
  hostname?: string;
  openPorts: number[];
  icmpReachable: boolean;
  suggestedChecks: SuggestedCheck[];
  source: DiscoverySource;
  promotedToCheckUid?: string;
  discoveredAt: string;
}

export interface StartDiscoveryScanRequest {
  cidrs: string[];
  ports?: number[];
  timeout?: string;
  concurrency?: number;
}

export interface PromoteCheckSpec {
  checkType: string;
  name?: string;
  slug?: string;
  period?: string;
  extraConfig?: Record<string, unknown>;
}

export interface PromoteCandidateRequest {
  checks: PromoteCheckSpec[];
}

export function useStartDiscoveryScan(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (req: StartDiscoveryScanRequest) =>
      apiFetch<{ data: DiscoveryScan }>(`/api/v1/orgs/${org}/discovery/scans`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["discoveryScans", org] });
    },
  });
}

export function useStartFreeboxScan(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (channelUid: string) =>
      apiFetch<{ data: DiscoveryScan }>(
        `/api/v1/orgs/${org}/discovery/freebox-scans`,
        {
          method: "POST",
          body: JSON.stringify({ channelUid }),
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

export function useListCandidateHosts(
  org: string,
  opts?: { jobUid?: string; promoted?: boolean; source?: string },
  // pollWhileActive streams hosts into the table while a fan-out scan is running
  // (chunks land progressively), by polling the list every 3s.
  pollWhileActive = false,
) {
  const params = new URLSearchParams();
  if (opts?.jobUid) params.set("jobUid", opts.jobUid);
  if (opts?.promoted !== undefined) params.set("promoted", String(opts.promoted));
  if (opts?.source) params.set("source", opts.source);
  const qs = params.toString();

  return useQuery({
    queryKey: ["discoveryHosts", org, opts],
    queryFn: () =>
      apiFetch<{ data: DiscoveredHost[] }>(
        `/api/v1/orgs/${org}/discovery/hosts${qs ? `?${qs}` : ""}`,
      ),
    select: (res) => res?.data ?? [],
    refetchInterval: pollWhileActive ? 3000 : false,
  });
}

export function usePromoteCandidate(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ uid, req }: { uid: string; req: PromoteCandidateRequest }) =>
      apiFetch<{ data: Check[] }>(
        `/api/v1/orgs/${org}/discovery/hosts/${uid}/promote`,
        {
          method: "POST",
          body: JSON.stringify(req),
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["discoveryHosts", org] });
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
    },
  });
}

export function useDismissCandidate(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(`/api/v1/orgs/${org}/discovery/hosts/${uid}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["discoveryHosts", org] });
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
  maxSsoUsers?: number | null;
}

export interface EntitlementsUsage {
  checks: number;
  checksPerMinute: number;
  ssoUsers: number;
}

export interface EntitlementsResponse {
  limits: EntitlementsLimits;
  usage?: EntitlementsUsage;
  source: string;
  stale: boolean;
  upgradeUrl?: string;
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

function jobsAdaptiveInterval(active: boolean): number {
  return active ? JOBS_ACTIVE_INTERVAL_MS : JOBS_IDLE_INTERVAL_MS;
}

interface JobsScope {
  allOrgs?: boolean;
}

// useJobsStats fetches the activity overview and drives adaptive refresh.
export function useJobsStats(org: string, opts?: JobsScope) {
  const allOrgs = opts?.allOrgs ?? false;
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
      jobsAdaptiveInterval(jobsStatsAreActive(query.state.data as JobsStats | undefined)),
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
    refetchInterval: () => jobsAdaptiveInterval(opts?.active ?? false),
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
    refetchInterval: () => jobsAdaptiveInterval(opts?.active ?? false),
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
