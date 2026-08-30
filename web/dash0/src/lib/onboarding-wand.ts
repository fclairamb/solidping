import type { TFunction } from "i18next";

import { slugify } from "@/lib/utils";
import type {
  CreateIntegrationRequest,
  CreateReportScheduleRequest,
  CreateStatusPageRequest,
} from "@/api/hooks";

/**
 * Magic-wand default payloads (spec 2026-08-29-03).
 *
 * Pure builders, kept separate from the pages that call them so they can be
 * unit tested (including locale-parity, via the `t` argument) without
 * rendering a route. Each shape mirrors what `seedOrgDefaults`
 * (server/internal/handlers/auth/service.go:3073) already writes for a new
 * org, so a wand-created resource is indistinguishable from a seeded one —
 * except the report's timezone, which uses the browser's own zone instead of
 * the seeder's hardcoded UTC.
 */

/** Payload for the "Set up email alerts for me" wand
 *  (POST /orgs/:org/integrations). `t` must already be bound to the
 *  "integrations" namespace. */
export function buildEmailAlertsWandPayload(
  t: TFunction,
  email: string,
): CreateIntegrationRequest {
  return {
    type: "email",
    name: t("wand.defaultName", "Email alerts"),
    enabled: true,
    isDefault: true,
    settings: { to: [email] },
  };
}

/** Payload for the "Create a weekly uptime report for me" wand
 *  (POST /orgs/:org/report-schedules, via useCreateReportSchedule). `t` must
 *  already be bound to the "slos" namespace. Empty check/group scopes mean
 *  org-wide, matching the seeder. */
export function buildWeeklyReportWandPayload(
  t: TFunction,
  email: string,
  timezone: string,
): CreateReportScheduleRequest {
  return {
    name: t("reports.wand.defaultName", "Weekly uptime report"),
    frequency: "weekly",
    timezone,
    recipients: [email],
    checkUids: [],
    checkGroupUids: [],
    includeSlos: true,
    enabled: true,
  };
}

/**
 * What the status-page "Prefill for me" wand seeds the create form with: the
 * org's display name and every check currently in the org. Prefill only —
 * the page is public-facing, so the operator still reviews and clicks
 * Create.
 */
export function buildStatusPageWandPrefill(
  orgName: string | undefined,
  checks: readonly { uid: string }[],
): { name: string; checkUids: string[] } {
  return {
    name: orgName ?? "",
    checkUids: checks.map((check) => check.uid),
  };
}

/**
 * Payload for the status-pages LIST page's "Create a status page for me"
 * wand (POST /orgs/:org/status-pages) — spec 2026-08-30-10. Unlike
 * `buildStatusPageWandPrefill` (which only seeds the create form for the
 * operator to review), this creates the page outright, so every field must
 * match what the create form would submit if the operator opened it, clicked
 * "Prefill for me", and hit Create without touching anything else — see
 * status-page-form.tsx's initial state for each default (visibility
 * "public", autoPublish on, 90-day history, etc.). The slug uses the same
 * `slugify` the form's name -> slug effect uses.
 */
export function buildStatusPageWandAutoCreatePayload(
  orgName: string | undefined,
  checks: readonly { uid: string }[],
): CreateStatusPageRequest {
  const name = orgName ?? "";
  return {
    name,
    slug: slugify(name),
    visibility: "public",
    isDefault: false,
    showAvailability: true,
    showResponseTime: true,
    historyPeriod: "90d",
    hideBranding: false,
    autoPublish: true,
    autoPublishDelaySeconds: 60,
    autoResolve: "if_untouched",
    checkUids: checks.map((check) => check.uid),
  };
}
