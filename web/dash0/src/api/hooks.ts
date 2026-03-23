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
  type?: "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "domain" | "smtp" | "udp" | "ssh" | "pop3" | "imap" | "websocket" | "postgresql" | "ftp" | "sftp" | "js";
  config?: Record<string, unknown>;
  regions?: string[];
  labels?: Record<string, string>;
  enabled?: boolean;
  internal?: boolean;
  period?: string;
  createdAt?: string;
  updatedAt?: string;
  lastResult?: {
    uid?: string;
    status?: "up" | "down" | "error" | "timeout";
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
  type?: "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "domain" | "smtp" | "udp" | "ssh" | "pop3" | "imap" | "websocket" | "postgresql" | "ftp" | "sftp" | "js";
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
}

export interface OrgResult {
  uid?: string;
  checkUid?: string;
  checkName?: string;
  checkSlug?: string;
  status?: "up" | "down" | "unknown";
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
  escalatedAt?: string;
  resolvedAt?: string;
  failureCount?: number;
  relapseCount?: number;
  lastReopenedAt?: string;
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
  options?: { labels?: string; with?: string; q?: string; checkGroupUid?: string; internal?: string; limit?: number }
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
  incidentThreshold?: number;
  escalationThreshold?: number;
  recoveryThreshold?: number;
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
      if (options?.size) params.set("size", options.size.toString());
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
        params.set("size", pageSize.toString());
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
    state?: "active" | "resolved";
    checkUid?: string;
    since?: string;
    until?: string;
    cursor?: string;
    size?: number;
    with?: string;
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
      if (options?.size) params.set("size", options.size.toString());
      if (options?.with) params.set("with", options.with);
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

export function useAcknowledgeIncident(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<IncidentDetail>(
        `/api/v1/orgs/${org}/incidents/${uid}/acknowledge`,
        {
          method: "POST",
        }
      ),
    onSuccess: (_, uid) => {
      queryClient.invalidateQueries({ queryKey: ["incidents", org] });
      queryClient.invalidateQueries({ queryKey: ["incident", org, uid] });
    },
  });
}

export function useResolveIncident(org: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<IncidentDetail>(`/api/v1/orgs/${org}/incidents/${uid}/resolve`, {
        method: "POST",
      }),
    onSuccess: (_, uid) => {
      queryClient.invalidateQueries({ queryKey: ["incidents", org] });
      queryClient.invalidateQueries({ queryKey: ["incident", org, uid] });
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
  }
) {
  return useQuery({
    queryKey: ["events", org, options],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (options?.checkUid) params.set("checkUid", options.checkUid);
      if (options?.incidentUid) params.set("incidentUid", options.incidentUid);
      if (options?.eventType) params.set("eventType", options.eventType);
      if (options?.cursor) params.set("cursor", options.cursor);
      if (options?.size) params.set("size", options.size.toString());
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

export function useAcceptInvite() {
  return useMutation({
    mutationFn: (data: {
      token: string;
      name?: string;
      email?: string;
      password?: string;
    }) =>
      apiFetch<{
        accessToken: string;
        user: { email: string; name?: string; avatarUrl?: string; role: string };
        organization: { uid: string; slug: string; name?: string };
      }>("/api/v1/auth/accept-invite", {
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
