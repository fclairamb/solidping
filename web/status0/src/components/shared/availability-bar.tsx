import { useTranslation } from "react-i18next";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import type { AvailabilityPoint } from "@/api/hooks";
import { statusStyle } from "@/lib/status-style";

function getBarColor(status: string) {
  // noData keeps its distinct light-gray bar; every other status (including
  // the amber warning/degraded pair) routes through the shared util.
  if (status === "noData" || status === "unknown")
    return "bg-status-neutral/40";
  return statusStyle(status).color;
}

interface AvailabilityBarProps {
  dailyAvailability: AvailabilityPoint[];
  overallAvailabilityPct?: number;
  historyDays: number;
  // bucketUnit is the per-segment granularity. "hour" renders the 24h view (24
  // hourly segments, hour-formatted tooltips, "24h ago → now" axis); anything
  // else renders the daily view unchanged.
  bucketUnit?: "day" | "hour" | string;
}

export function AvailabilityBar({
  dailyAvailability,
  overallAvailabilityPct,
  historyDays,
  bucketUnit,
}: AvailabilityBarProps) {
  const { t, i18n } = useTranslation();

  const isHourly = bucketUnit === "hour";

  const formatDate = (dateStr: string) => {
    const date = new Date(dateStr + "T00:00:00");
    return date.toLocaleDateString(i18n.language, {
      month: "short",
      day: "numeric",
    });
  };

  // Hourly tooltips show the bucket hour (date + HH:00) from the RFC3339 time.
  const formatHour = (point: AvailabilityPoint) => {
    const iso = point.time ?? `${point.date}T00:00:00Z`;
    const date = new Date(iso);
    return date.toLocaleString(i18n.language, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  };

  return (
    <div className="mt-2">
      {/* `py-1` reserves room for the hover lift below so a grown segment is
          not clipped by the card's own overflow. */}
      <div className="flex gap-[3px] py-1">
        {dailyAvailability.map((point) => (
          <Tooltip key={point.time ?? point.date}>
            <TooltipTrigger asChild>
              <div
                data-testid="availability-bar-segment"
                // rounded-[2px], not rounded-sm: --radius is 10px here, so
                // rounded-sm (6px) turned these ~20px-wide segments into blobs.
                // Hover grows the segment instead of fading it — the old
                // opacity fade desaturated the status color, which is the one
                // thing on this page that has to stay readable.
                className={`h-7 flex-1 origin-center rounded-[2px] ${getBarColor(point.status)} transition-transform duration-150 ease-out hover:scale-y-[1.18]`}
              />
            </TooltipTrigger>
            {/* translate="no" — this whole subtree is poll-driven text whose
                shape changes between renders (the noData branch swaps one <p>
                for another). A machine translator re-parents those text nodes
                into <font> wrappers and React's next commit then fails with
                "removeChild on Node". See NO_TRANSLATE in status-page-view.tsx. */}
            <TooltipContent translate="no">
              {/* Status is carried by a dot rather than by the tooltip's own
                  background, so the surface stays neutral in both themes and
                  the color still says up / degraded / down at a glance. */}
              <p className="flex items-center gap-1.5 font-medium">
                <span
                  aria-hidden="true"
                  className={`inline-block size-2 shrink-0 rounded-full ${getBarColor(point.status)}`}
                />
                {isHourly ? formatHour(point) : formatDate(point.date)}
              </p>
              {point.status !== "noData" ? (
                <p className="mt-0.5 pl-3.5 text-muted-foreground tabular-nums">
                  {point.availabilityPct.toFixed(2)}% {t("uptime")}
                </p>
              ) : (
                <p className="mt-0.5 pl-3.5 text-muted-foreground">
                  {t("noData")}
                </p>
              )}
            </TooltipContent>
          </Tooltip>
        ))}
      </div>
      {/* Same reasoning: every label here is recomputed from poll data, and the
          middle span appears/disappears with it. */}
      <div
        className="mt-1 flex justify-between text-xs text-muted-foreground"
        data-testid="availability-axis"
        translate="no"
      >
        <span>
          {isHourly
            ? t("hoursAgo", { count: 24 })
            : t("daysAgo", { count: historyDays })}
        </span>
        {overallAvailabilityPct != null && (
          <span className="font-medium text-foreground">
            {overallAvailabilityPct.toFixed(3)}% {t("uptime")}
          </span>
        )}
        <span>{t("today")}</span>
      </div>
    </div>
  );
}
