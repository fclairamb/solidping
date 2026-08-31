import { useMutation, useQuery } from "@tanstack/react-query";
import { ApiError, NetworkError, apiFetch } from "./client";
import { withKiosk } from "@/lib/kiosk";

export interface ResourceCheckInfo {
  name?: string;
  type: string;
  status: string;
  // True when the check is inside an active maintenance window right now, so
  // the public page shows a "Scheduled Maintenance" badge instead of raw status.
  inMaintenance?: boolean;
  // RFC3339 instant the check entered its CURRENT status — how the TV board
  // says "down for 12m" rather than just "down". CHECK resources only: a group
  // rolls its status up from a count map with no timestamps, so the server
  // omits this rather than guessing, and the board omits the duration in turn.
  statusChangedAt?: string;
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

/** The shared availability vocabulary — mirrors uptimebar's Status* constants. */
export type AvailabilityStatus = "up" | "degraded" | "down" | "noData";

export interface ResponseTimePoint {
  time: string;
  durationP95?: number;
  status?: "up" | "down" | "timeout" | "error" | "created" | "running" | string;
  /**
   * Probe-ratio availability of the slice this point covers: 100 or 0 for a
   * single raw probe, the rollup's successful/total × 100 for an aggregated
   * one. Absent when the row carried no countable probe (a lifecycle marker, an
   * abandoned attempt) — no data is not 100%.
   */
  availabilityPct?: number;
  /** Counts behind availabilityPct, so the strip can say "59 / 60 probes up"
   * without doing availability math of its own. */
  totalChecks?: number;
  successfulChecks?: number;
  /** The SERVER's classification of availabilityPct against the PAGE's
   * thresholds — the same classifier the availability bar above the chart uses.
   * Distinct from `status`, which is the probe's own outcome. */
  availabilityStatus?: AvailabilityStatus;
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
   * PAGE-level uptime over `historyPeriod` (spec 2026-08-29-08): the mean of
   * the per-resource `availability.overallAvailabilityPct` values, resources
   * with no data excluded. Absent when the page hides availability or nothing
   * has reported — in which case the TV board shows no number at all rather
   * than a plausible-looking zero.
   */
  overallAvailabilityPct?: number;
  /**
   * Incident auto-publication settings. Operator-facing, echoed publicly so the
   * dashboard preview and the live page agree.
   */
  autoPublish?: boolean;
  autoPublishDelaySeconds?: number;
  autoResolve?: string;
  /**
   * The RESOLVED effective availability thresholds (never omitted by the
   * server, but optional here so an older cached payload cannot crash the
   * page). They are what the availability bar's colours already encode; the
   * response-time chart's availability strip needs them explicitly because it
   * merges several regions into one slot client-side and must classify the sum
   * itself.
   */
  availabilityThresholds?: AvailabilityThresholds;
}

/** Resolved page availability thresholds (defaults 99.9 / 99.0). */
export interface AvailabilityThresholds {
  thresholdUp: number;
  thresholdDegraded: number;
}

/**
 * Public incident history for a page — the resolved ones the live payload's
 * activeIncidents deliberately omits.
 */
/**
 * Options shared by the public read hooks, for the surfaces that need more
 * than the defaults — today that is TV mode (spec 2026-08-29-08), which polls
 * on its own cadence and may hold a kiosk token.
 *
 * The token is part of the QUERY KEY, not just the URL. Two different tokens
 * (or token vs none) can legitimately produce different answers for the same
 * page — one renders, the other 401s — so sharing a cache entry between them
 * would let a revoked screen keep showing a cached page.
 */
export interface PublicReadOptions {
  kioskToken?: string;
  refetchInterval?: number;
}

export function usePublicIncidentHistory(
  org: string,
  slug: string,
  options?: PublicReadOptions,
) {
  return useQuery<{ data: PublicIncident[] }>({
    queryKey: [
      "public-incident-history",
      org,
      slug,
      options?.kioskToken ?? null,
    ],
    queryFn: () =>
      apiFetch<{ data: PublicIncident[] }>(
        withKiosk(
          `/api/v1/status-pages/${org}/${slug}/incidents`,
          options?.kioskToken,
        ),
      ),
    refetchInterval: options?.refetchInterval,
    enabled: !!org && !!slug,
  });
}

export function usePublicStatusPage(
  org: string,
  slug: string,
  options?: PublicReadOptions,
) {
  return useQuery<StatusPage>({
    queryKey: ["public-status-page", org, slug, options?.kioskToken ?? null],
    queryFn: () =>
      apiFetch<StatusPage>(
        withKiosk(`/api/v1/status-pages/${org}/${slug}`, options?.kioskToken),
      ),
    // Refresh every 30 seconds by default; TV mode tightens this during an
    // incident.
    refetchInterval: options?.refetchInterval ?? 30_000,
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

/**
 * Machine code the API answers a password-protected page with, alongside 401.
 * Deliberately distinct from a plain UNAUTHORIZED: it means "unlock me", not
 * "log in" — status0 has no accounts to log into.
 */
export const STATUS_PAGE_LOCKED = "STATUS_PAGE_LOCKED";

/** True when an error is the API telling us the page needs unlocking. */
export function isLockedError(error: unknown): boolean {
  return error instanceof ApiError && error.code === STATUS_PAGE_LOCKED;
}

/**
 * Submits the unlock password. On success the server sets a host-only,
 * HTTP-only cookie — nothing is returned to store, and nothing about the
 * password stays in JS memory beyond the form's own state.
 *
 * `slug` is optional: the org's default page is addressed without one, and the
 * server resolves it the same way the view endpoint does.
 */
export function useUnlockStatusPage(org: string, slug?: string) {
  return useMutation<void, ApiError | NetworkError, string>({
    mutationFn: async (password: string) => {
      const path = slug
        ? `/api/v1/status-pages/${org}/${slug}/unlock`
        : `/api/v1/status-pages/${org}/unlock`;

      let response: Response;
      try {
        response = await fetch(path, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ password }),
          // The whole point is the Set-Cookie that comes back; a
          // cross-origin embed must send/accept it too.
          credentials: "same-origin",
        });
      } catch {
        throw new NetworkError();
      }

      if (!response.ok) {
        const error = await response.json().catch(() => ({}));
        throw new ApiError(
          error.title || "Unlock failed",
          error.code || "UNKNOWN_ERROR",
          error.detail,
          response.status,
        );
      }
    },
  });
}

export function useDefaultStatusPage(org: string, options?: PublicReadOptions) {
  return useQuery<StatusPage>({
    queryKey: [
      "public-status-page",
      org,
      "__default__",
      options?.kioskToken ?? null,
    ],
    queryFn: () =>
      apiFetch<StatusPage>(
        withKiosk(`/api/v1/status-pages/${org}`, options?.kioskToken),
      ),
    refetchInterval: options?.refetchInterval ?? 30_000,
    enabled: !!org,
  });
}
