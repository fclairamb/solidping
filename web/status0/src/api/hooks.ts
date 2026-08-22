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

// ResponseTimeSeries is one region's response-time points for a resource.
// `region` is absent for results with no recorded region (NULL — legacy rows
// from before regions were tracked, or a check with a single implicit
// region).
export interface ResponseTimeSeries {
  region?: string;
  points: ResponseTimePoint[];
}

export interface ResourceAvailabilityData {
  overallAvailabilityPct?: number;
  // dailyAvailability holds the per-bucket points; when bucketUnit is "hour"
  // these are 24 hourly buckets despite the legacy key name.
  dailyAvailability?: AvailabilityPoint[];
  // responseTimeSeries holds one series per region the check has reported
  // results from (spec 2026-08-14-04). Replaces the old flat
  // `responseTimeData: ResponseTimePoint[]` field.
  responseTimeSeries?: ResponseTimeSeries[];
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

/**
 * One customer-facing incident on a status page (spec 2026-08-19-08).
 *
 * This is the PUBLICATION, not the internal incident: every field here is
 * either operator-authored or templated from the page's own public resource
 * names. The server never puts probe output, error strings or internal
 * hostnames on this shape, and this client never asks for them.
 */
export interface PublicIncident {
  uid: string;
  title: string;
  /** "investigating" | "identified" | "monitoring" | "resolved" */
  state: string;
  /** "minor" | "major" | "critical", or absent when the operator set none. */
  severity?: string;
  startedAt: string;
  resolvedAt?: string;
  /** Public display names of the page resources this incident covers. */
  affectedResources?: string[];
  updates?: PublicIncidentUpdate[];
}

/** One append-only narrative entry on a public incident. */
export interface PublicIncidentUpdate {
  uid: string;
  kind: string;
  title: string;
  bodyMarkdown: string;
  publishedAt: string;
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
  /**
   * Stable public path of the page's uploaded logo, e.g.
   * `/pub/status-page-assets/<file uid>`. Absent when the operator uploaded
   * none — the brand bar then falls back to the SolidPing mark.
   */
  logoUrl?: string;
  /** Same, for the favicon written into <link rel="icon">. */
  faviconUrl?: string;
  /**
   * RESOLVED white-label decision: true only when the org holds the
   * `whiteLabel` entitlement AND the page opted in. The server does the AND —
   * status0 never sees entitlements and must not try to reconstruct them.
   */
  hideBranding?: boolean;
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
  /**
   * Currently-open incident publications (spec 2026-08-19-08). Same population
   * rule as overallStatus: public view endpoints only.
   */
  activeIncidents?: PublicIncident[];
  /**
   * Incident auto-publication settings. Operator-facing, echoed publicly so the
   * dashboard preview and the live page agree.
   */
  autoPublish?: boolean;
  autoPublishDelaySeconds?: number;
  autoResolve?: string;
}

/**
 * Public incident history for a page — the resolved ones the live payload's
 * activeIncidents deliberately omits.
 */
export function usePublicIncidentHistory(org: string, slug: string) {
  return useQuery<{ data: PublicIncident[] }>({
    queryKey: ["public-incident-history", org, slug],
    queryFn: () =>
      apiFetch<{ data: PublicIncident[] }>(
        `/api/v1/status-pages/${org}/${slug}/incidents`,
      ),
    enabled: !!org && !!slug,
  });
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
          },
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
          response.status,
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
