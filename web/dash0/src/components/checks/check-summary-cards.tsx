import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, ArrowUp, ArrowDown, Clock } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import type { Check } from "@/api/hooks";
import { statusStyle } from "@/lib/status-style";
import { lastRealResultAt, regionsDisagree } from "@/lib/check-freshness";
import { LiveDuration, LiveDurationAgo } from "@/components/shared/relative-time";

interface CheckSummaryCardsProps {
  org: string;
  check: Check;
  totalIncidents: number;
}

export function CheckSummaryCards({
  org,
  check,
  totalIncidents,
}: CheckSummaryCardsProps) {
  const { t } = useTranslation("checks");
  const summaryStatus = check.status ?? check.lastResult?.status;
  const isUp = summaryStatus === "up";
  // warning/degraded count as up (not down) — route the down decision through
  // the shared util so only hard failures show the red "currently down" card.
  const isDown = statusStyle(summaryStatus).isDown;
  const isStale = summaryStatus === "stale";
  // The "status for X" timer counts from when the check entered its CURRENT
  // status (checks.status_changed_at), not from the last raw result.
  const statusSince = check.statusChangedAt ?? check.lastStatusChange?.time;
  // "Last checked" is the newest REAL result — never an abandoned or lifecycle
  // row, which used to let one live region hide a dead one (spec 2026-09-25-02).
  const lastChecked = lastRealResultAt(check);
  const showRegionAges = regionsDisagree(check.regionFreshness);

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
            ) : isStale ? (
              <Clock className="h-4 w-4" />
            ) : (
              <ArrowUp className="h-4 w-4" />
            )}
            {isUp
              ? t("detail.summary.currentlyUp")
              : isDown
                ? t("detail.summary.currentlyDown")
                : isStale
                  ? t("detail.summary.noDataFor")
                  : t("detail.summary.statusFallback")}
          </div>
          <div className="text-2xl font-bold" data-testid="summary-status-duration">
            {statusSince ? (
              <LiveDuration since={statusSince} />
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
          <div className="text-2xl font-bold" data-testid="summary-last-checked">
            {lastChecked ? (
              <LiveDurationAgo since={lastChecked} />
            ) : (
              t("detail.summary.never")
            )}
          </div>
          {/* One live region must not hide a dead one: when regions disagree,
              every region's own age is listed under the newest. */}
          {showRegionAges && (
            <ul
              className="mt-2 space-y-0.5 text-xs text-muted-foreground"
              data-testid="summary-region-ages"
            >
              {check.regionFreshness?.map((region) => (
                <li
                  key={region.region}
                  className={region.stale ? "font-medium text-foreground" : undefined}
                >
                  {region.region || t("detail.freshness.defaultRegion")}:{" "}
                  {region.lastResultAt ? (
                    <LiveDurationAgo since={region.lastResultAt} />
                  ) : (
                    t("detail.freshness.noResultRecently")
                  )}
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      {/* Incidents card — clickable KPI-tile pattern (see design reference's
          "KPI tiles" section) once there's actually somewhere to go. With
          zero incidents the card stays a static tile: nothing to drill into. */}
      {totalIncidents > 0 ? (
        <Link
          to="/orgs/$org/incidents"
          params={{ org }}
          search={{ checkUid: check.uid, state: "all", showSuppressed: undefined }}
          className="block rounded-xl focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Card
            data-testid="incidents-card"
            className="cursor-pointer transition hover:-translate-y-0.5 hover:bg-accent/40 hover:shadow-card-hover"
          >
            <CardContent className="pt-6">
              <div className="flex items-center gap-2 text-sm text-muted-foreground mb-1">
                <AlertTriangle className="h-4 w-4" />
                {t("detail.summary.incidents")}
              </div>
              <div className="text-2xl font-bold">{totalIncidents}</div>
            </CardContent>
          </Card>
        </Link>
      ) : (
        <Card data-testid="incidents-card">
          <CardContent className="pt-6">
            <div className="flex items-center gap-2 text-sm text-muted-foreground mb-1">
              <AlertTriangle className="h-4 w-4" />
              {t("detail.summary.incidents")}
            </div>
            <div className="text-2xl font-bold">{totalIncidents}</div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
