import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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

function getStatusColor(status: string) {
  switch (status) {
    case "ok":
    case "up":
      return "bg-green-500";
    case "error":
    case "down":
      return "bg-red-500";
    case "warning":
    case "degraded":
      return "bg-yellow-500";
    default:
      return "bg-gray-400";
  }
}

function getStatusBadgeVariant(status: string) {
  switch (status) {
    case "ok":
    case "up":
      return "success" as const;
    case "error":
    case "down":
      return "destructive" as const;
    case "warning":
    case "degraded":
      return "warning" as const;
    default:
      return "secondary" as const;
  }
}

function getStatusLabelKey(status: string) {
  switch (status) {
    case "ok":
    case "up":
      return "operational";
    case "error":
    case "down":
      return "outage";
    case "warning":
    case "degraded":
      return "degraded";
    default:
      return "unknown";
  }
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
          <Badge variant={getStatusBadgeVariant(status)}>
            {t(getStatusLabelKey(status))}
          </Badge>
        </div>
      </div>

      {/* Availability bars */}
      {showAvailability && avail?.dailyAvailability && (
        <AvailabilityBar
          dailyAvailability={avail.dailyAvailability}
          overallAvailabilityPct={avail.overallAvailabilityPct}
          historyDays={historyDays}
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

export function StatusPageView({ page }: { page: StatusPage }) {
  const { t } = useTranslation();
  const sections = page.sections ?? [];
  const overallStatus = getOverallStatus(sections);
  const { data: versionInfo } = useVersion();

  return (
    <div className="min-h-screen">
      <div className="mx-auto max-w-3xl px-4 py-12 relative">
        <div className="absolute top-4 right-4">
          <LanguageSwitcher />
        </div>
        {/* Header */}
        <div className="mb-8 text-center">
          <h1 className="text-3xl font-bold tracking-tight">{page.name}</h1>
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

        {/* Footer */}
        <div className="mt-12 text-center text-xs text-muted-foreground">
          {t("poweredBy")}{versionInfo ? ` v${versionInfo.version}` : ""}
        </div>
      </div>
    </div>
  );
}
