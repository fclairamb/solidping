import { useTranslation } from "react-i18next";
import { AlertTriangle, ArrowUp, ArrowDown, Clock } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import type { Check } from "@/api/hooks";
import { statusStyle } from "@/lib/status-style";
import { LiveDuration, LiveDurationAgo } from "@/components/shared/relative-time";

interface CheckSummaryCardsProps {
  check: Check;
  totalIncidents: number;
}

export function CheckSummaryCards({
  check,
  totalIncidents,
}: CheckSummaryCardsProps) {
  const { t } = useTranslation("checks");
  const summaryStatus = check.status ?? check.lastResult?.status;
  const isUp = summaryStatus === "up";
  // warning/degraded count as up (not down) — route the down decision through
  // the shared util so only hard failures show the red "currently down" card.
  const isDown = statusStyle(summaryStatus).isDown;

  return (
    <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
      {/* Uptime / Downtime card */}
      <Card>
        <CardContent className="pt-6">
          <div className="flex items-center gap-2 text-sm text-muted-foreground mb-1">
            {isUp ? (
              <ArrowUp className="h-4 w-4 text-green-500" />
            ) : isDown ? (
              <ArrowDown className="h-4 w-4 text-red-500" />
            ) : (
              <ArrowUp className="h-4 w-4" />
            )}
            {isUp
              ? t("detail.summary.currentlyUp")
              : isDown
                ? t("detail.summary.currentlyDown")
                : t("detail.summary.statusFallback")}
          </div>
          <div className="text-2xl font-bold">
            {check.lastStatusChange?.time ? (
              <LiveDuration since={check.lastStatusChange.time} />
            ) : (
              t("detail.summary.unknown")
            )}
          </div>
        </CardContent>
      </Card>

      {/* Last checked card */}
      <Card>
        <CardContent className="pt-6">
          <div className="flex items-center gap-2 text-sm text-muted-foreground mb-1">
            <Clock className="h-4 w-4" />
            {t("detail.summary.lastChecked")}
          </div>
          <div className="text-2xl font-bold">
            {check.lastResult?.timestamp ? (
              <LiveDurationAgo since={check.lastResult.timestamp} />
            ) : (
              t("detail.summary.never")
            )}
          </div>
        </CardContent>
      </Card>

      {/* Incidents card */}
      <Card data-testid="incidents-card">
        <CardContent className="pt-6">
          <div className="flex items-center gap-2 text-sm text-muted-foreground mb-1">
            <AlertTriangle className="h-4 w-4" />
            {t("detail.summary.incidents")}
          </div>
          <div className="text-2xl font-bold">{totalIncidents}</div>
        </CardContent>
      </Card>
    </div>
  );
}
