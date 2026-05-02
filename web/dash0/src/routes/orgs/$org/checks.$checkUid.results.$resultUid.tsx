import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
import { useResult, type OrgResultDetail, type ResultFallbackInfo } from "@/api/hooks";

export const Route = createFileRoute(
  "/orgs/$org/checks/$checkUid/results/$resultUid",
)({
  component: ResultDetailPage,
});

function statusBadgeClass(status?: string): string {
  switch (status) {
    case "up":
      return "bg-green-500/10 text-green-500";
    case "down":
      return "bg-red-500/10 text-red-500";
    case "created":
      return "bg-blue-500/10 text-blue-500";
    default:
      return "";
  }
}

function formatPercent(v?: number): string {
  if (v === undefined || v === null) return "-";
  return `${v.toFixed(2)} %`;
}

function formatMetric(name: string, value: unknown): string {
  if (value === null || value === undefined) return "-";
  if (typeof value !== "number") return String(value);

  const lower = name.toLowerCase();
  if (lower.endsWith("_pct")) return `${value.toFixed(2)} %`;

  const isDuration = lower.includes("duration") || lower.includes("latency") || lower.endsWith("_ms");
  if ((lower.endsWith("_min") || lower.endsWith("_max") || lower.endsWith("_avg")) && isDuration) {
    return `${value.toFixed(2)} ms`;
  }

  if (isDuration) return `${value.toFixed(2)} ms`;

  return value.toFixed(2);
}

function formatDate(iso?: string): string {
  if (!iso) return "-";
  return new Date(iso).toLocaleString();
}

function FallbackBanner({
  fallback,
  data,
  t,
}: {
  fallback: ResultFallbackInfo;
  data: OrgResultDetail;
  t: ReturnType<typeof useTranslation>["t"];
}) {
  const reasonKey =
    fallback.reason === "rolled_up_to_hour"
      ? "checks:resultDetail.fallback.rolledUpToHour"
      : fallback.reason === "rolled_up_to_day"
        ? "checks:resultDetail.fallback.rolledUpToDay"
        : "checks:resultDetail.fallback.rolledUpToMonth";

  return (
    <div className="rounded-md border border-yellow-500/30 bg-yellow-500/10 p-4">
      <div className="flex items-start gap-3">
        <AlertTriangle className="h-5 w-5 mt-0.5 text-yellow-600 dark:text-yellow-500" />
        <div className="space-y-1 text-sm">
          <div className="font-medium">{t("checks:resultDetail.fallback.title")}</div>
          <div>
            {t(reasonKey, {
              start: formatDate(data.periodStart),
              end: formatDate(data.periodEnd),
              ts: formatDate(fallback.requestedAt),
            })}
          </div>
          <div className="text-muted-foreground">
            {t("checks:resultDetail.fallback.originalUid", { uid: fallback.requestedUid })}
          </div>
        </div>
      </div>
    </div>
  );
}

function ResultDetailPage() {
  const { t } = useTranslation(["checks", "common"]);
  const navigate = useNavigate();
  const { org, checkUid, resultUid } = Route.useParams();
  const { data, isLoading, error, refetch } = useResult(org, checkUid, resultUid);

  if (isLoading) {
    return (
      <div className="space-y-6 max-w-3xl">
        <div className="flex items-center gap-4">
          <Skeleton className="h-10 w-10 rounded" />
          <Skeleton className="h-8 w-64" />
        </div>
        <Skeleton className="h-32 rounded-lg" />
        <Skeleton className="h-48 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("checks:resultDetail.title")}
        backTo="/orgs/$org/checks/$checkUid"
        backLabel={t("checks:resultDetail.back")}
        onRetry={() => refetch()}
      />
    );
  }

  if (!data) return null;

  const isAggregate = data.periodType && data.periodType !== "raw";

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex items-center gap-2">
        <Button
          variant="ghost"
          size="icon"
          onClick={() =>
            navigate({
              to: "/orgs/$org/checks/$checkUid",
              params: { org, checkUid },
              search: { graphPeriod: undefined, graphFull: undefined },
            })
          }
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-2xl font-semibold">
          {t("checks:resultDetail.title")} {data.uid?.slice(0, 8) ?? ""}
        </h1>
        {data.status && (
          <Badge variant="secondary" className={statusBadgeClass(data.status)}>
            {data.status}
          </Badge>
        )}
        {data.periodType && (
          <Badge variant="outline">{data.periodType}</Badge>
        )}
      </div>

      {data.fallback && <FallbackBanner fallback={data.fallback} data={data} t={t} />}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("checks:resultDetail.period")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2 text-sm">
          <div>
            <span className="text-muted-foreground">{t("checks:resultDetail.period")}: </span>
            <code className="font-mono">{formatDate(data.periodStart)}</code>
            {data.periodEnd && (
              <>
                {" – "}
                <code className="font-mono">{formatDate(data.periodEnd)}</code>
              </>
            )}
          </div>
          {data.region && (
            <div>
              <span className="text-muted-foreground">{t("checks:resultDetail.region")}: </span>
              <code className="font-mono">{data.region}</code>
            </div>
          )}
          {data.durationMs !== undefined && (
            <div>
              <span className="text-muted-foreground">{t("checks:resultDetail.duration")}: </span>
              <code className="font-mono">{data.durationMs.toFixed(2)} ms</code>
            </div>
          )}
          {(data.durationMinMs !== undefined || data.durationMaxMs !== undefined) && (
            <div className="flex gap-4">
              {data.durationMinMs !== undefined && (
                <span>
                  <span className="text-muted-foreground">{t("checks:resultDetail.durationMin")}: </span>
                  <code className="font-mono">{data.durationMinMs.toFixed(2)} ms</code>
                </span>
              )}
              {data.durationMaxMs !== undefined && (
                <span>
                  <span className="text-muted-foreground">{t("checks:resultDetail.durationMax")}: </span>
                  <code className="font-mono">{data.durationMaxMs.toFixed(2)} ms</code>
                </span>
              )}
            </div>
          )}
        </CardContent>
      </Card>

      {isAggregate && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("checks:resultDetail.availability")}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            {data.totalChecks !== undefined && (
              <div>
                <span className="text-muted-foreground">{t("checks:resultDetail.totalChecks")}: </span>
                <code className="font-mono">{data.totalChecks}</code>
              </div>
            )}
            {data.successfulChecks !== undefined && (
              <div>
                <span className="text-muted-foreground">{t("checks:resultDetail.successfulChecks")}: </span>
                <code className="font-mono">{data.successfulChecks}</code>
              </div>
            )}
            {data.availabilityPct !== undefined && (
              <div>
                <span className="text-muted-foreground">{t("checks:resultDetail.availability")}: </span>
                <code className="font-mono">{formatPercent(data.availabilityPct)}</code>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {data.metrics && Object.keys(data.metrics).length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("checks:resultDetail.metrics")}</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-2 text-sm">
              {Object.entries(data.metrics).map(([key, value]) => (
                <div key={key} className="flex justify-between gap-4 border-b py-1 last:border-0">
                  <span className="text-muted-foreground">{key}</span>
                  <code className="font-mono">{formatMetric(key, value)}</code>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {data.output && Object.keys(data.output).length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("checks:resultDetail.output")}</CardTitle>
          </CardHeader>
          <CardContent>
            <pre className="max-h-96 overflow-auto rounded-md bg-muted p-3 text-xs">
              {JSON.stringify(data.output, null, 2)}
            </pre>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
