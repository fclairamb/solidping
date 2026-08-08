import { useMutation, useQuery } from "@tanstack/react-query";
import { ApiError, NetworkError, apiFetch } from "./client";

export interface ResourceCheckInfo {
  name?: string;
  type: string;
  status: string;
  // True when the check is inside an active maintenance window right now, so
  // the public page shows a "Scheduled Maintenance" badge instead of raw status.
  inMaintenance?: boolean;
}

// AvailabilityPoint is a single bucket: a day in daily mode, an hour in 24h
// mode. `time` (RFC3339 bucket start) is the authoritative anchor for hourly
// buckets; `date` is kept for back-compat.
export interface AvailabilityPoint {
  date: string;
  time?: string;
  availabilityPct: number;
  status: string;
}

export interface ResponseTimePoint {
  time: string;
  durationP95?: number;
  status?: "up" | "down" | "timeout" | "error" | "created" | "running" | string;
}

export interface ResourceAvailabilityData {
  overallAvailabilityPct?: number;
  // dailyAvailability holds the per-bucket points; when bucketUnit is "hour"
  // these are 24 hourly buckets despite the legacy key name.
  dailyAvailability?: AvailabilityPoint[];
  responseTimeData?: ResponseTimePoint[];
  // period is the active history period ("24h"|"7d"|"30d"|"90d").
  period?: string;
  // bucketUnit is the granularity of each point: "day" or "hour".
  bucketUnit?: "day" | "hour" | string;
}

export interface StatusPageResource {
  uid: string;
  // Exactly one of checkUid / checkGroupUid is set. A group resource renders as
  // ONE component here — same shape as a check resource (name, status,
  // availability series, maintenance flag) — and never exposes its members.
  checkUid?: string;
  checkGroupUid?: string;
  publicName?: string;
  explanation?: string;
  position: number;
  check?: ResourceCheckInfo;
  availability?: ResourceAvailabilityData;
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

export interface StatusUpdatePublicResponse {
  uid: string;
  sectionUid?: string;
  checkUid?: string;
  incidentUid?: string;
  title: string;
  bodyMarkdown: string;
  linkUrl?: string;
  kind: string;
  publishedAt: string;
}

// StatusCounts tallies resources per overallStatus category (spec
// 2026-08-08-05). Only present on the public view payloads.
export interface StatusCounts {
  operational: number;
  degraded: number;
  down: number;
  maintenance: number;
  unknown: number;
}

export interface StatusPage {
  uid: string;
  name: string;
  slug: string;
  description?: string;
  visibility: string;
  isDefault: boolean;
  enabled: boolean;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyDays: number;
  historyPeriod: "24h" | "7d" | "30d" | "90d";
  language?: string;
  /**
   * Operator-authored stylesheet, rendered by StatusPageView as a <style> text
   * child. Unlike the custom-domain fields it IS served on the public
   * endpoint — this page is its only consumer.
   */
  customCss?: string;
  sections?: StatusPageSection[];
  recentUpdates?: StatusUpdatePublicResponse[];
  createdAt?: string;
  /**
   * Server-computed page-level rollup (spec 2026-08-08-05):
   * "operational" | "degraded" | "down" | "maintenance" | "unknown". Only
   * populated on the public view endpoints (usePublicStatusPage /
   * useDefaultStatusPage) — absent on admin listings.
   */
  overallStatus?: string;
  statusCounts?: StatusCounts;
}

export function usePublicStatusPage(org: string, slug: string) {
  return useQuery<StatusPage>({
    queryKey: ["public-status-page", org, slug],
    queryFn: () => apiFetch<StatusPage>(`/api/v1/status-pages/${org}/${slug}`),
    refetchInterval: 30_000, // Refresh every 30 seconds
    enabled: !!org && !!slug,
  });
}

export interface VersionInfo {
  version: string;
  commit: string;
  gitTime: string;
}

export function useVersion() {
  return useQuery<VersionInfo>({
    queryKey: ["version"],
    queryFn: () => apiFetch<VersionInfo>("/api/mgmt/version"),
    staleTime: Infinity,
  });
}

export interface SubscribeInput {
  org: string;
  statusPageUid: string;
  email: string;
}

// useSubscribe starts a double opt-in email subscription for a status page.
// The backend returns 202 and sends a confirmation email; the UI then shows a
// "check your inbox" state.
export function useSubscribe() {
  return useMutation<void, ApiError | NetworkError, SubscribeInput>({
    mutationFn: async ({ org, statusPageUid, email }) => {
      let response: Response;
      try {
        response = await fetch(
          `/api/v1/orgs/${org}/status-pages/${statusPageUid}/subscribers`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ email }),
          }
        );
      } catch {
        throw new NetworkError();
      }

      if (!response.ok) {
        const error = await response.json().catch(() => ({}));
        throw new ApiError(
          error.title || "Subscription failed",
          error.code || "UNKNOWN_ERROR",
          error.detail,
          response.status
        );
      }
    },
  });
}

export function useDefaultStatusPage(org: string) {
  return useQuery<StatusPage>({
    queryKey: ["public-status-page", org, "__default__"],
    queryFn: () => apiFetch<StatusPage>(`/api/v1/status-pages/${org}`),
    refetchInterval: 30_000,
    enabled: !!org,
  });
}
