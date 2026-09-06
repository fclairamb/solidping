// Builds the outbound link from the dashboard to the marketing site
// (www.solidping.io), tagged with UTM parameters so the two deployment
// flavours — SaaS vs self-hosted — are distinguishable on the other end.
// Spec 2026-09-06-01 introduces this convention (there was none before it:
// no `utm_` string existed anywhere in the repo) starting from the login
// page footer's "SolidPing" link; every future outbound link from the
// dashboard should reuse this helper with a different `utm_content`.
//
// The marketing origin is intentionally hard-coded, matching the other
// `https://www.solidping.io/...` links already in dash0
// (auth.slack.complete.tsx, account.notifications.tsx) — this is not meant
// to be configurable.

export type DeploymentMode = "saas" | "self-hosted";

const MARKETING_ORIGIN = "https://www.solidping.io/";

/**
 * Returns the marketing-site URL for the given deployment mode, with UTM
 * parameters identifying the dashboard as the source. `deploymentMode` falls
 * back to "self-hosted" when unknown (e.g. an older server that predates the
 * field, or the version request hasn't resolved yet). `utmContent` defaults
 * to "login-footer" (the only placement today) — pass a different value for
 * a future outbound link elsewhere in the dashboard.
 */
export function marketingSiteUrl(
  deploymentMode: DeploymentMode | undefined,
  utmContent = "login-footer",
): string {
  const url = new URL(MARKETING_ORIGIN);
  url.search = new URLSearchParams({
    utm_source: "solidping-dashboard",
    utm_medium: "app",
    utm_campaign: deploymentMode ?? "self-hosted",
    utm_content: utmContent,
  }).toString();

  return url.toString();
}

/** The changelog link used alongside the marketing link — no UTMs, no anchor. */
export const CHANGELOG_URL = "https://solidping.io/docs/changelog";
