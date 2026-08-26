import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import { AlertTriangle, Check, ExternalLink, Minus } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
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
import { CheckRateLimitBanner } from "@/components/shared/check-rate-limit-banner";
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

interface FeatureRowProps {
  label: string;
  included: boolean;
  includedLabel: string;
  notIncludedLabel: string;
  hint?: string;
}

/**
 * A boolean entitlement, rendered as an on/off statement rather than a bar.
 *
 * UsageRow's progress bar is meaningless for a feature flag — there is nothing
 * to be 40% of — so this deliberately does not reuse it. The "not included"
 * side is muted rather than destructive: not having a feature is a plan fact,
 * not a breach.
 */
function FeatureRow({
  label,
  included,
  includedLabel,
  notIncludedLabel,
  hint,
}: FeatureRowProps) {
  return (
    <div className="space-y-1" data-testid={`usage-feature-${label}`}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-0.5">
        <span className="text-sm font-medium">{label}</span>
        <span
          className={`flex items-center gap-1.5 text-sm ${included ? "font-medium" : "text-muted-foreground"}`}
        >
          {included ? (
            <Check className="h-3.5 w-3.5" aria-hidden />
          ) : (
            <Minus className="h-3.5 w-3.5" aria-hidden />
          )}
          {included ? includedLabel : notIncludedLabel}
        </span>
      </div>
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
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
            {/*
              Above the plan and the bars on purpose: an org whose executions
              are being dropped needs the explanation before the numbers, not
              after them. No usage link here — this IS the usage page.
            */}
            <CheckRateLimitBanner
              org={org}
              checksPerMinute={data.checksPerMinute}
              upgradeUrl={data.upgradeUrl}
            />
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
                label={t("usage.slos")}
                current={data.usage?.slos ?? 0}
                limit={data.limits.maxSlos}
                unlimitedLabel={t("usage.unlimited")}
              />
              <UsageRow
                label={t("usage.customDomains")}
                current={data.usage?.customDomains ?? 0}
                limit={data.limits.maxCustomDomains}
                unlimitedLabel={t("usage.unlimited")}
              />
              {/*
                WhatsApp is a consumption counter rather than a live resource
                count: it resets at the start of each UTC month and only ever
                goes up within one.
              */}
              <UsageRow
                label={t("usage.whatsappThisMonth")}
                current={data.usage?.whatsappThisMonth ?? 0}
                limit={data.limits.maxWhatsappPerMonth}
                unlimitedLabel={t("usage.unlimited")}
              />
              {/*
                White labelling is the one entitlement with no number attached,
                so it renders as an on/off statement rather than a usage bar.
              */}
              <FeatureRow
                label={t("usage.whiteLabel")}
                included={data.limits.whiteLabel === true}
                includedLabel={t("usage.included")}
                notIncludedLabel={t("usage.notIncluded")}
                hint={t("usage.whiteLabelHint")}
              />
            </div>
            {/*
              An instance-spend guard that refused a send dropped an ALERT, so
              it has to be visible to the organization, not only in the
              operator's logs. Rendered as a warning rather than a usage row:
              it is an incident to act on, not a level to watch.
            */}
            {data.usage?.smsGuard &&
              (data.usage.smsGuard.globalRunawayBreaches > 0 ||
                data.usage.smsGuard.countryBlockedBreaches > 0) && (
                <Alert variant="warning" data-testid="usage-sms-guard-breach">
                  <AlertTriangle className="h-4 w-4" />
                  <AlertTitle>{t("usage.smsGuard.title")}</AlertTitle>
                  <AlertDescription>
                    {data.usage.smsGuard.globalRunawayBreaches > 0 && (
                      <p>
                        {t("usage.smsGuard.runaway", {
                          count: data.usage.smsGuard.globalRunawayBreaches,
                          limit: data.usage.smsGuard.globalRunawayPerHour,
                        })}
                      </p>
                    )}
                    {data.usage.smsGuard.countryBlockedBreaches > 0 && (
                      <p>
                        {t("usage.smsGuard.country", {
                          count: data.usage.smsGuard.countryBlockedBreaches,
                          countries: (data.usage.smsGuard.allowedCountries ?? []).join(", "),
                        })}
                      </p>
                    )}
                  </AlertDescription>
                </Alert>
              )}
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
