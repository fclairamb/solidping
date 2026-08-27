import { useEffect } from "react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Logo } from "@/components/ui/logo";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  useVersion,
  type AvailabilityThresholds,
  type StatusPage,
  type StatusPageSection,
  type StatusPageResource,
} from "@/api/hooks";
import { useTranslation } from "react-i18next";
import { AvailabilityBar } from "./availability-bar";
import { ResponseTimeChart } from "./response-time-chart";
import { LanguageSwitcher } from "./language-switcher";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { StatusUpdatesTimeline } from "./status-updates-timeline";
import { ActiveIncidents } from "./active-incidents";
import { IncidentHistory } from "./incident-history";
import { SubscribeWidget } from "./subscribe-widget";
import {
  highestSeverity,
  severityStyle,
  statusStyle,
} from "@/lib/status-style";
import { usePreviewCss } from "@/lib/preview-css";

function getStatusColor(status: string) {
  return statusStyle(status).color;
}

function getStatusBadgeVariant(status: string) {
  return statusStyle(status).badgeVariant;
}

function getStatusLabelKey(status: string) {
  return statusStyle(status).labelKey;
}

/**
 * Marks a subtree as "never machine-translate this".
 *
 * Translation tools (Chrome auto-translate, the Google Translate widget,
 * translating browser extensions) rewrite a text node by MOVING it into nested
 * <font> wrappers. React's fiber still records the original parent, so the next
 * commit that removes or reorders that text node calls
 * `originalParent.removeChild(textNode)` and the DOM throws
 *
 *   NotFoundError: Failed to execute 'removeChild' on 'Node'
 *
 * which visitors see as "Something went wrong!". `index.html` already opts the
 * whole document out, but that is a *hint*: a visitor who explicitly picks
 * "Translate to…" — or any extension that ignores the document-level hint —
 * still gets the rewrite. Element-level opt-outs are honoured even then
 * (RESIDUAL ASSUMPTION: this is the behaviour the HTML spec defines for
 * `translate="no"` and what Google Translate documents for `.notranslate`; it
 * was NOT independently verified against a real Chrome translation in this
 * work, and the E2E simulation honours it by policy, so the test proves the
 * markers are on the DOM, not that Chrome obeys them).
 *
 * The rule for applying it: every element below whose TEXT CHANGES BETWEEN
 * RENDERS while its element is reused — status labels, availability
 * percentages, incident kinds, relative timestamps, the subscribe button.
 * Operator-authored content (page name, description, section and resource
 * names) deliberately does NOT carry it: that text is static for the lifetime
 * of the page, so it is safe to translate and valuable to keep translatable.
 * The version line is the one exception to "changes between renders" — it is
 * fetched once (`useVersion` uses `staleTime: Infinity` and no
 * `refetchInterval`, so it never refetches) and is opted out for consistency
 * with the rest of the machine-generated chrome, not because it churns.
 *
 * e2e/translate-resilience.spec.ts asserts these markers are on the rendered
 * DOM; see also the block comment in main.tsx.
 */
const NO_TRANSLATE = { translate: "no" } as const;

// Hero banner label per page-level overallStatus (spec 2026-08-08-05). The
// rollup itself — including the maintenance-masks-down rule and honest
// "unknown" handling — is computed server-side (models.RollupPageStatus);
// this is purely a display mapping.
function getOverallStatusLabelKey(overallStatus: string | undefined): string {
  switch (overallStatus) {
    case "operational":
      return "allSystemsOperational";
    case "degraded":
      return "someSystemsDegraded";
    case "down":
      return "systemOutage";
    case "maintenance":
      return "underMaintenance";
    case "unknown":
    default:
      return "statusUnknown";
  }
}

interface ResourceCardProps {
  /** The page's resolved availability thresholds, threaded down so the
   * response-time chart's availability strip classifies a client-merged
   * multi-region slot against the SAME numbers the availability bar above it
   * was coloured with. */
  availabilityThresholds?: AvailabilityThresholds;
  resource: StatusPageResource;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyDays: number;
  /**
   * Public display names covered by an OPEN incident publication, keyed by the
   * severity to colour the badge with (empty string = the operator graded the
   * incident with no severity).
   *
   * Keying on the display name is deliberate: `affectedResources` is exactly
   * the set of public names the SERVER resolved by walking this page's own
   * resources, so matching on the same string is matching on the same thing.
   * No check UID crosses into the public payload to join on, and inventing one
   * would mean exposing internal identity to do it.
   */
  affectedSeverities: Map<string, string>;
}

function ResourceCard({
  availabilityThresholds,
  resource,
  showAvailability,
  showResponseTime,
  historyDays,
  affectedSeverities,
}: ResourceCardProps) {
  const { t } = useTranslation();
  const name = resource.publicName || resource.check?.name || t("unknown");
  const incidentSeverity = affectedSeverities.get(name);
  const isAffected = incidentSeverity !== undefined;
  const incidentStyle = severityStyle(incidentSeverity);
  const status = resource.check?.status ?? "unknown";
  const inMaintenance = resource.check?.inMaintenance ?? false;
  const avail = resource.availability;

  return (
    <div
      className="py-3 px-4 sm:px-6 transition-colors hover:bg-muted/40"
      data-testid="resource-row"
      data-resource-affected={isAffected ? "true" : "false"}
    >
      {/* Header row */}
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-3 min-w-0">
          <Tooltip>
            <TooltipTrigger>
              <span
                data-testid="resource-status-dot"
                className={`inline-block h-2.5 w-2.5 rounded-full ${getStatusColor(status)}`}
              />
            </TooltipTrigger>
            <TooltipContent {...NO_TRANSLATE}>
              {t(getStatusLabelKey(status))}
            </TooltipContent>
          </Tooltip>
          <span className="text-sm font-medium truncate">{name}</span>
          {resource.check?.type && (
            // Monospace + uppercase, matching how dash0's check list renders a
            // protocol tag — it reads as machine metadata, not a status.
            <Badge
              variant="outline"
              className="hidden sm:inline-flex font-mono text-[10px] uppercase tracking-wider text-muted-foreground shrink-0"
            >
              {resource.check.type}
            </Badge>
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {/* "Affected" sits BEFORE the status badge: it explains the red dot
              to the left of it, and a reader scanning the row should meet the
              explanation on the way to the verdict. Only rendered for a
              resource an open publication actually names — that is the whole
              point of the badge, and rendering it everywhere would make it
              meaningless. */}
          {isAffected && (
            <Badge
              variant={incidentStyle?.badgeVariant ?? "secondary"}
              data-testid="resource-incident-badge"
              data-incident-severity={incidentSeverity || "none"}
              {...NO_TRANSLATE}
            >
              {t("resourceAffected")}
            </Badge>
          )}
          {showAvailability && avail?.overallAvailabilityPct != null && (
            <span
              className="text-sm font-medium tabular-nums text-status-ok-foreground"
              data-testid="resource-availability-pct"
              {...NO_TRANSLATE}
            >
              {avail.overallAvailabilityPct.toFixed(3)}%
            </span>
          )}
          {inMaintenance ? (
            <Badge
              variant="warning"
              data-testid="resource-maintenance-badge"
              {...NO_TRANSLATE}
            >
              {t("scheduledMaintenance")}
            </Badge>
          ) : (
            <Badge
              variant={getStatusBadgeVariant(status)}
              data-testid="resource-status-badge"
              {...NO_TRANSLATE}
            >
              {t(getStatusLabelKey(status))}
            </Badge>
          )}
        </div>
      </div>

      {/* Availability bars */}
      {showAvailability && avail?.dailyAvailability && (
        <AvailabilityBar
          dailyAvailability={avail.dailyAvailability}
          overallAvailabilityPct={avail.overallAvailabilityPct}
          historyDays={historyDays}
          bucketUnit={avail.bucketUnit}
        />
      )}

      {/* Response time chart */}
      {showResponseTime && avail?.responseTimeSeries && (
        <ResponseTimeChart
          series={avail.responseTimeSeries}
          thresholds={availabilityThresholds}
        />
      )}
    </div>
  );
}

interface SectionCardProps {
  availabilityThresholds?: AvailabilityThresholds;
  section: StatusPageSection;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyDays: number;
  affectedSeverities: Map<string, string>;
}

function SectionCard({
  availabilityThresholds,
  section,
  showAvailability,
  showResponseTime,
  historyDays,
  affectedSeverities,
}: SectionCardProps) {
  const { t } = useTranslation();
  const resources = section.resources ?? [];

  return (
    <Card>
      {/* Same treatment dash0 gives a card that groups a list ("Checks at a
          glance"): text-base semibold, not the text-lg it used to be, so the
          section stops competing with the page h1. The uppercase micro-label
          style is deliberately NOT used here — in dash0 that belongs to KPI
          tiles, and a section is a list container. */}
      <CardHeader className="pb-3">
        <CardTitle className="text-base font-semibold tracking-tight">
          {section.name}
        </CardTitle>
      </CardHeader>
      <CardContent className="px-0 pb-0">
        {resources.length === 0 ? (
          <p className="px-6 pb-4 text-sm text-muted-foreground">
            {t("noResourcesConfigured")}
          </p>
        ) : (
          <div className="divide-y">
            {resources
              .sort((a, b) => a.position - b.position)
              .map((resource) => (
                <ResourceCard
                  key={resource.uid}
                  resource={resource}
                  showAvailability={showAvailability}
                  showResponseTime={showResponseTime}
                  historyDays={historyDays}
                  availabilityThresholds={availabilityThresholds}
                  affectedSeverities={affectedSeverities}
                />
              ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function StatusPageView({
  page,
  org,
}: {
  page: StatusPage;
  org: string;
}) {
  const { t } = useTranslation();
  const sections = page.sections ?? [];
  // Server-computed (spec 2026-08-08-05); falls back to "unknown" if a
  // cached/older response ever lacks the field.
  const overallStatus = page.overallStatus ?? "unknown";
  const overallStyle = statusStyle(overallStatus);
  // Supporting count for the banner. Uses the SERVER-computed per-status
  // tallies (spec 2026-08-08-05, same rollup that produced overallStatus) so
  // the headline and the count can never disagree — recounting the resources
  // client-side would silently drift from the server's rules (notably
  // maintenance masking a down check). Absent on a cached/older response, in
  // which case the line is simply omitted.
  const counts = page.statusCounts;
  // Open incident publications drive three things at once: the banner colour,
  // the active-incidents section, and the per-resource "Affected" badge. They
  // are all derived here so a resource can never be badged as affected by an
  // incident the page is not also showing.
  const activeIncidents = page.activeIncidents ?? [];
  const affectedSeverities = new Map<string, string>();

  for (const incident of activeIncidents) {
    for (const resourceName of incident.affectedResources ?? []) {
      const existing = affectedSeverities.get(resourceName);
      // A resource caught by two incidents wears the worse severity.
      const worst = highestSeverity([existing, incident.severity]);
      affectedSeverities.set(resourceName, worst ?? existing ?? "");
    }
  }

  // The banner takes the WORST severity across the open incidents. When no
  // open incident carries one — the operator graded none — it stays on the
  // rollup colour rather than inventing a severity for them.
  const bannerSeverity = severityStyle(
    highestSeverity(activeIncidents.map((incident) => incident.severity)),
  );
  const resourceTotal = counts
    ? counts.operational +
      counts.degraded +
      counts.down +
      counts.maintenance +
      counts.unknown
    : 0;
  const operationalCount = counts?.operational ?? 0;
  const allResources = sections.flatMap((s) => s.resources ?? []);
  // Mean availability across the resources that actually report one. Null (and
  // the pill hidden) when none do, or when the page hides availability
  // entirely — better no number than a fabricated 100%.
  const uptimeSamples = page.showAvailability
    ? allResources
        .map((r) => r.availability?.overallAvailabilityPct)
        .filter((pct): pct is number => pct != null)
    : [];
  const aggregateUptimePct = uptimeSamples.length
    ? uptimeSamples.reduce((sum, pct) => sum + pct, 0) / uptimeSamples.length
    : null;
  const { data: versionInfo } = useVersion();
  const feedUrl = `/api/v1/status-pages/${org}/${page.slug}/feed.xml`;
  // Outside preview mode this is just page.customCss; with ?preview=1 the
  // dash0 appearance editor can drive it live over postMessage.
  const customCss = usePreviewCss(page.customCss);

  // Favicon: written from here rather than from index.html so it also applies
  // on a custom domain, where the served document is the same SPA shell for
  // every page. The previous href is restored on unmount so navigating between
  // two pages of the same org never leaves the wrong icon behind.
  useEffect(() => {
    if (!page.faviconUrl) return;

    const link =
      document.querySelector<HTMLLinkElement>("link[rel~='icon']") ??
      (() => {
        const created = document.createElement("link");
        created.rel = "icon";
        document.head.appendChild(created);
        return created;
      })();

    const previous = link.getAttribute("href");
    link.setAttribute("href", page.faviconUrl);

    return () => {
      if (previous === null) {
        link.removeAttribute("href");
      } else {
        link.setAttribute("href", previous);
      }
    };
  }, [page.faviconUrl]);

  return (
    <div className="min-h-screen">
      {/* Operator-authored theme override. Rendered as a React TEXT CHILD, so
          React escapes it and a "</style>" in the payload cannot break out of
          the element — never dangerouslySetInnerHTML here. It sits first in
          the tree and arrives in the same payload as the content, so the page
          paints already themed (no flash of default styling). */}
      {customCss ? <style>{customCss}</style> : null}
      {/* Brand bar — white surface with a brand-pink logo. The brand
          color is confined to the logo so the page reads as one
          continuous document; the status banner below uses status
          colors only. */}
      <header className="sticky top-0 z-30 border-b border-border bg-background/80 backdrop-blur-md supports-[backdrop-filter]:bg-background/60">
        {/* h-12 (48px), not py-3 around a 32px logo (56px): this bar carries a
            logo, a name and two icon buttons, and the page repeats the name as
            an <h1> right below it — it does not need the height of a nav bar. */}
        <div className="mx-auto flex h-12 max-w-3xl items-center justify-between px-4">
          <div className="flex items-center gap-2.5">
            {/* `sp-logo` is added by <Logo> itself; `sp-page-name` here.
                Both are documented custom-CSS hooks (public API) — see
                web/docs/docs/features/status-pages.md. Do not rename. */}
            {page.logoUrl ? (
              /* An uploaded logo REPLACES the SolidPing mark rather than
                 sitting next to it — a status page wearing two logos reads as
                 a co-brand nobody asked for. `sp-logo` is kept on the <img> so
                 the documented custom-CSS hook keeps working either way, and
                 the alt text is the page name because that is what the mark
                 stands for here. */
              <img
                src={page.logoUrl}
                alt={page.name}
                className="sp-logo h-[26px] w-auto max-w-[160px] object-contain"
                data-testid="status-page-logo"
              />
            ) : (
              <Logo size={26} />
            )}
            <span className="sp-page-name text-sm font-semibold tracking-tight">
              {page.name}
            </span>
          </div>
          <div className="flex items-center gap-2">
            <LanguageSwitcher />
            <ThemeToggle />
          </div>
        </div>
      </header>

      <div className="mx-auto max-w-3xl px-4 py-12">
        {/* Status hero — color is determined exclusively by status.
            Brand color is never used here so the banner reads as
            green / yellow / red, not as a sibling of the brand bar.

            Structure is ported from dash0's OverallStatusBanner (tinted
            gradient surface, pulsing state dot, headline + supporting count,
            trailing pill) so an operator who knows the console recognises the
            public page instantly. The <Badge> carrying
            data-testid="overall-status-badge" is kept as the trailing pill —
            e2e/overall-status-badge.spec.ts asserts on it. */}
        <div className="mb-8">
          <h1 className="sp-page-title text-2xl sm:text-3xl font-bold tracking-tight">
            {page.name}
          </h1>
          {page.description && (
            <p className="sp-page-description mt-1.5 text-sm text-muted-foreground">
              {page.description}
            </p>
          )}

          <div
            className={`sp-status-banner relative overflow-hidden mt-5 rounded-xl border p-3.5 sm:p-4 shadow-sm ${
              bannerSeverity?.bannerSurface ?? overallStyle.bannerSurface
            }`}
            data-testid="overall-status-banner"
            data-incident-severity={
              bannerSeverity ? bannerSeverity.labelKey.split(".")[1] : "none"
            }
          >
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex items-center gap-3">
                <span className="relative flex h-3 w-3 shrink-0">
                  {/* The ping ring is decorative; it is skipped for the
                      "unknown" rollup, where there is no live state worth
                      drawing attention to. */}
                  {overallStatus !== "unknown" && (
                    <span
                      className={`animate-ping absolute inline-flex h-full w-full rounded-full opacity-75 ${overallStyle.color}`}
                    />
                  )}
                  <span
                    className={`relative inline-flex rounded-full h-3 w-3 ${overallStyle.color}`}
                  />
                </span>
                <div className="flex flex-wrap items-baseline gap-2">
                  {/* THE hero label. It carries data-testid="overall-status-badge"
                      because it is the element that announces the page status —
                      the name is historical (it used to be a small <Badge>) and
                      e2e/overall-status-badge.spec.ts asserts this exact text
                      for all five rollup values. Renaming the testid would break
                      that suite for no gain. */}
                  <h2
                    className={`text-sm sm:text-base font-semibold ${
                      bannerSeverity?.bannerTitle ?? overallStyle.bannerTitle
                    }`}
                    data-testid="overall-status-badge"
                    {...NO_TRANSLATE}
                  >
                    {t(getOverallStatusLabelKey(overallStatus))}
                  </h2>
                  {resourceTotal > 0 && (
                    <span
                      className="text-xs text-muted-foreground"
                      data-testid="overall-status-summary"
                      {...NO_TRANSLATE}
                    >
                      {t("servicesOperational", {
                        ok: operationalCount,
                        total: resourceTotal,
                      })}
                    </span>
                  )}
                </div>
              </div>
              {/* Trailing pill carries a DIFFERENT fact from the headline (the
                  aggregate uptime), mirroring dash0's "24h SLA Operational"
                  pill. Repeating the status label here read as a rendering
                  bug — the two sat side by side with identical text. */}
              {aggregateUptimePct != null && (
                <div
                  className={`flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium tabular-nums ${
                    bannerSeverity?.bannerPill ?? overallStyle.bannerPill
                  }`}
                  data-testid="overall-uptime-pill"
                  {...NO_TRANSLATE}
                >
                  {aggregateUptimePct.toFixed(3)}% {t("uptime")}
                </div>
              )}
            </div>
          </div>
        </div>

        {/* Open incidents. Rendered ABOVE the component grid: a visitor who
            arrived because something is broken should read the explanation
            before hunting for a red dot. Renders nothing when all is well. */}
        <ActiveIncidents incidents={activeIncidents} />

        {/* Sections */}
        <div className="space-y-6">
          {sections.length === 0 ? (
            <Card>
              <CardContent className="py-12 text-center">
                <p className="text-muted-foreground">
                  {t("noSectionsConfigured")}
                </p>
              </CardContent>
            </Card>
          ) : (
            sections
              .sort((a, b) => a.position - b.position)
              .map((section) => (
                <SectionCard
                  key={section.uid}
                  section={section}
                  showAvailability={page.showAvailability}
                  showResponseTime={page.showResponseTime}
                  historyDays={page.historyDays}
                  availabilityThresholds={page.availabilityThresholds}
                  affectedSeverities={affectedSeverities}
                />
              ))
          )}
        </div>

        {/* Recent updates timeline */}
        {page.recentUpdates && page.recentUpdates.length > 0 && (
          <section aria-label="Recent updates" className="mt-8">
            <h2 className="text-lg font-semibold mb-4">
              {t("status.recentUpdates")}
            </h2>
            <StatusUpdatesTimeline updates={page.recentUpdates} />
          </section>
        )}

        {/* Past incidents, collapsed by default — history matters, but not
            more than the current state. */}
        <IncidentHistory org={org} slug={page.slug} />

        {/* Subscribe to updates (email double opt-in) + RSS/Atom feed */}
        <section aria-label="Subscribe to updates" className="mt-8">
          <SubscribeWidget
            org={org}
            statusPageUid={page.uid}
            feedUrl={feedUrl}
          />
        </section>

        {/* Footer — outbound brand link to solidping.io. text-brand
            (pink) signals "leaves this page" vs internal nav which
            stays primary blue.

            `sp-footer` / `sp-powered-by` / `sp-version` are documented
            custom-CSS hooks (public API) — see
            web/docs/docs/features/status-pages.md. Do not rename. */}
        <div className="sp-footer mt-12 text-center text-xs text-muted-foreground flex flex-col items-center gap-1">
          {/* White label (spec 2026-08-21-07): `hideBranding` on the PUBLIC
              payload is already the AND of the org's `whiteLabel` entitlement
              and the page's opt-in — the server resolves it so this component
              never has to know what a plan is. The version line stays either
              way: it is operational information about the instance, not a
              SolidPing advertisement, and support answers depend on it. */}
          {page.hideBranding ? null : (
            <a
              href="https://www.solidping.io"
              target="_blank"
              rel="noreferrer noopener"
              className="sp-powered-by text-brand hover:underline"
            >
              {t("poweredBy")}
            </a>
          )}
          {versionInfo ? (
            // Machine-generated chrome. Note this one does NOT churn (useVersion
            // is staleTime: Infinity, never refetched) — opted out for
            // consistency / defence in depth. See NO_TRANSLATE above.
            <span className="sp-version" {...NO_TRANSLATE}>
              v{versionInfo.version}
            </span>
          ) : null}
        </div>
      </div>
    </div>
  );
}
