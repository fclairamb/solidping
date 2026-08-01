import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import { ExternalLink } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Progress } from "@/components/ui/progress";
import { useEntitlements } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/organization/usage")({
  component: UsagePage,
});

interface UsageRowProps {
  label: string;
  current: number;
  limit?: number | null;
  unlimitedLabel: string;
  /** Format a numeric value for display (e.g. round checks/min). */
  format?: (n: number) => string;
}

function UsageRow({ label, current, limit, unlimitedLabel, format }: UsageRowProps) {
  const fmt = format ?? ((n: number) => String(n));
  const unlimited = limit === null || limit === undefined;
  const over = !unlimited && current > limit;

  return (
    <div className="space-y-1.5" data-testid={`usage-row-${label}`}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-0.5">
        <span className="text-sm font-medium">{label}</span>
        <span
          className={`text-sm tabular-nums ${over ? "text-destructive font-semibold" : "text-muted-foreground"}`}
        >
          {unlimited ? (
            <>
              {fmt(current)} <span className="text-muted-foreground">/ {unlimitedLabel}</span>
            </>
          ) : (
            `${fmt(current)} / ${fmt(limit)}`
          )}
        </span>
      </div>
      {!unlimited && <Progress value={current} max={limit} />}
    </div>
  );
}

function UsagePage() {
  const { t } = useTranslation(["org", "common"]);
  const { org } = Route.useParams();
  const { data, isLoading, isError } = useEntitlements(org, { withUsage: true });

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("usage.title")}</CardTitle>
        <CardDescription>{t("usage.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        {isLoading ? (
          <div className="space-y-6">
            {[0, 1, 2, 3, 4].map((i) => (
              <div key={i} className="space-y-2">
                <Skeleton className="h-4 w-40" />
                <Skeleton className="h-2 w-full" />
              </div>
            ))}
          </div>
        ) : isError || !data ? (
          <p className="text-sm text-destructive">{t("usage.loadError")}</p>
        ) : (
          <>
            <div
              className="flex items-center justify-between gap-2 border-b pb-4"
              data-testid="current-plan"
            >
              <span className="text-sm text-muted-foreground">{t("usage.plan")}</span>
              <span className="flex items-center gap-1.5 text-sm font-semibold">
                {data.displayEmoji && (
                  <span aria-hidden className="text-base leading-none">
                    {data.displayEmoji}
                  </span>
                )}
                {data.displayName ?? "—"}
              </span>
            </div>
            <div className="space-y-5">
              <UsageRow
                label={t("usage.checks")}
                current={data.usage?.checks ?? 0}
                limit={data.limits.maxChecks}
                unlimitedLabel={t("usage.unlimited")}
              />
              <div className="space-y-1">
                <UsageRow
                  label={t("usage.checksPerMinute")}
                  current={data.usage?.checksPerMinute ?? 0}
                  limit={data.limits.maxChecksPerMinute}
                  unlimitedLabel={t("usage.unlimited")}
                  format={(n) => (Number.isInteger(n) ? String(n) : n.toFixed(1))}
                />
                <p className="text-xs text-muted-foreground">{t("usage.multiRegionNote")}</p>
              </div>
              <UsageRow
                label={t("usage.users")}
                current={data.usage?.ssoUsers ?? 0}
                limit={data.limits.maxUsers}
                unlimitedLabel={t("usage.unlimited")}
              />
              <UsageRow
                label={t("usage.privateLocationAgents")}
                current={data.usage?.agents ?? 0}
                limit={data.limits.maxDeportedAgents}
                unlimitedLabel={t("usage.unlimited")}
              />
              <UsageRow
                label={t("usage.customDomains")}
                current={data.usage?.customDomains ?? 0}
                limit={data.limits.maxCustomDomains}
                unlimitedLabel={t("usage.unlimited")}
              />
            </div>
            {data.upgradeUrl && (
              <div className="flex justify-end pt-2">
                <Button asChild>
                  <a href={data.upgradeUrl} target="_blank" rel="noreferrer">
                    {t("usage.upgrade")}
                    <ExternalLink className="ml-2 h-4 w-4" />
                  </a>
                </Button>
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
