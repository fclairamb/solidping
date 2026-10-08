import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useResults } from "@/api/hooks";
import { cn } from "@/lib/utils";
import {
  componentTimelines,
  healthComponents,
  type HealthStatus,
} from "@/lib/health-components";

const DOT: Record<HealthStatus, string> = {
  ok: "bg-emerald-500",
  warning: "bg-amber-500",
  failed: "bg-destructive",
  skipped: "bg-muted-foreground/40",
  unknown: "bg-muted-foreground/40",
};

const TIMELINE_RESULTS = 30;

function Dot({ status, className }: { status: HealthStatus; className?: string }) {
  return <span className={cn("inline-block rounded-full", DOT[status], className)} title={status} />;
}

/**
 * HealthCard is the application health section of the check page (spec
 * 2026-10-03-05): one row per component of the last result, and a small
 * per-component timeline built from the last results' outputs.
 */
export function HealthCard({
  org,
  checkUid,
  output,
}: {
  org: string;
  checkUid: string;
  output: Record<string, unknown> | undefined;
}) {
  const { t } = useTranslation("checks");
  const components = healthComponents(output);
  const results = useResults(org, {
    checkUid,
    periodType: "raw",
    with: "output",
    size: TIMELINE_RESULTS,
  });
  const timelines = componentTimelines((results.data?.data ?? []).map((r) => r.output));
  const timelineOf = (name: string) => timelines.find((tl) => tl.name === name)?.statuses ?? [];

  return (
    <Card data-testid="health-card">
      <CardHeader>
        <CardTitle>{t("health.detail.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {components.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="health-no-components">
            {t("health.detail.noComponents")}
          </p>
        ) : (
          <ul className="divide-y" data-testid="health-components">
            {components.map((component) => (
              <li
                key={component.name}
                className="flex flex-col gap-1 py-2 sm:flex-row sm:items-center sm:gap-4"
                data-testid={`health-component-${component.name}`}
                data-status={component.status}
              >
                <div className="flex min-w-0 flex-1 items-start gap-2">
                  <Dot status={component.status} className="mt-1.5 h-2.5 w-2.5 shrink-0" />
                  <div className="min-w-0">
                    <div className="text-sm font-medium">
                      {component.label ?? component.name}
                      {component.summary && (
                        <span className="ml-2 font-normal text-muted-foreground">{component.summary}</span>
                      )}
                      {component.ignored && (
                        <span className="ml-2 text-xs text-muted-foreground">
                          ({t("health.detail.ignored")})
                        </span>
                      )}
                    </div>
                    {component.message && (
                      <p className="break-words text-xs text-muted-foreground">{component.message}</p>
                    )}
                  </div>
                </div>
                <div
                  className="flex items-center gap-0.5"
                  aria-label={t("health.detail.timeline")}
                  data-testid={`health-timeline-${component.name}`}
                >
                  {timelineOf(component.name).map((status, index) => (
                    <Dot key={index} status={status} className="h-3 w-1.5 rounded-sm" />
                  ))}
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
