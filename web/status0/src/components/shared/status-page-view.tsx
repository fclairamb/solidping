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
  type StatusPage,
  type StatusPageSection,
  type StatusPageResource,
} from "@/api/hooks";
import { useTranslation } from "react-i18next";
import { AvailabilityBar } from "./availability-bar";
import { ResponseTimeChart } from "./response-time-chart";
import { LanguageSwitcher } from "./language-switcher";
import { StatusUpdatesTimeline } from "./status-updates-timeline";
import { SubscribeWidget } from "./subscribe-widget";
import { statusStyle } from "@/lib/status-style";
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

function getOverallStatus(sections: StatusPageSection[]): string {
  let hasWarning = false;
  for (const section of sections) {
    for (const resource of section.resources ?? []) {
      const s = resource.check?.status;
      if (s === "error" || s === "down") return "error";
      if (s === "warning" || s === "degraded") hasWarning = true;
    }
  }
  if (hasWarning) return "warning";
  return "ok";
}

interface ResourceCardProps {
  resource: StatusPageResource;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyDays: number;
}

function ResourceCard({
  resource,
  showAvailability,
  showResponseTime,
  historyDays,
}: ResourceCardProps) {
  const { t } = useTranslation();
  const name = resource.publicName || resource.check?.name || t("unknown");
  const status = resource.check?.status ?? "unknown";
  const inMaintenance = resource.check?.inMaintenance ?? false;
  const avail = resource.availability;

  return (
    <div className="py-3 px-4">
      {/* Header row */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Tooltip>
            <TooltipTrigger>
              <span
                className={`inline-block h-2.5 w-2.5 rounded-full ${getStatusColor(status)}`}
              />
            </TooltipTrigger>
            <TooltipContent>{t(getStatusLabelKey(status))}</TooltipContent>
          </Tooltip>
          <span className="text-sm font-medium">{name}</span>
          {resource.check?.type && (
            <Badge variant="outline" className="text-xs">
              {resource.check.type}
            </Badge>
          )}
        </div>
        <div className="flex items-center gap-2">
          {showAvailability && avail?.overallAvailabilityPct != null && (
            <span className="text-sm font-medium text-green-600">
              {avail.overallAvailabilityPct.toFixed(3)}%
            </span>
          )}
          {inMaintenance ? (
            <Badge
              variant="warning"
              data-testid="resource-maintenance-badge"
            >
              {t("scheduledMaintenance")}
            </Badge>
          ) : (
            <Badge variant={getStatusBadgeVariant(status)}>
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
      {showResponseTime && avail?.responseTimeData && (
        <ResponseTimeChart data={avail.responseTimeData} />
      )}
    </div>
  );
}

interface SectionCardProps {
  section: StatusPageSection;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyDays: number;
}

function SectionCard({
  section,
  showAvailability,
  showResponseTime,
  historyDays,
}: SectionCardProps) {
  const { t } = useTranslation();
  const resources = section.resources ?? [];

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-lg">{section.name}</CardTitle>
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
  const overallStatus = getOverallStatus(sections);
  const { data: versionInfo } = useVersion();
  const feedUrl = `/api/v1/status-pages/${org}/${page.slug}/feed.xml`;
  // Outside preview mode this is just page.customCss; with ?preview=1 the
  // dash0 appearance editor can drive it live over postMessage.
  const customCss = usePreviewCss(page.customCss);

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
      <header className="border-b border-border">
        <div className="mx-auto max-w-3xl px-4 py-3 flex items-center justify-between">
          <div className="flex items-center gap-3">
            {/* `sp-logo` is added by <Logo> itself; `sp-page-name` here.
                Both are documented custom-CSS hooks (public API) — see
                web/docs/docs/features/status-pages.md. Do not rename. */}
            <Logo size={32} />
            <span className="sp-page-name font-semibold text-base">
              {page.name}
            </span>
          </div>
          <div className="flex items-center gap-2">
            <LanguageSwitcher />
          </div>
        </div>
      </header>

      <div className="mx-auto max-w-3xl px-4 py-12">
        {/* Status hero — color is determined exclusively by status.
            Brand color is never used here so the banner reads as
            green / yellow / red, not as a sibling of the brand bar. */}
        <div className="mb-8 text-center">
          {page.description && (
            <p className="mt-2 text-muted-foreground">{page.description}</p>
          )}
          <div className="mt-4">
            <Badge
              variant={getStatusBadgeVariant(overallStatus)}
              className="text-sm px-4 py-1"
            >
              {overallStatus === "ok"
                ? t("allSystemsOperational")
                : overallStatus === "warning"
                  ? t("someSystemsDegraded")
                  : t("systemOutage")}
            </Badge>
          </div>
        </div>

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
          <a
            href="https://www.solidping.io"
            target="_blank"
            rel="noreferrer noopener"
            className="sp-powered-by text-brand hover:underline"
          >
            {t("poweredBy")}
          </a>
          {versionInfo ? (
            // translate="no": the version string is rewritten on every poll,
            // and Chrome auto-translate re-parents such text nodes into <font>
            // wrappers, which breaks React reconciliation (see main.tsx).
            <span className="sp-version" translate="no">
              v{versionInfo.version}
            </span>
          ) : null}
        </div>
      </div>
    </div>
  );
}
