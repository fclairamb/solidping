import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import type { AvailabilityPoint } from "@/api/hooks";
import { statusStyle } from "@/lib/status-style";
import { segmentGeometry } from "@/lib/segment-layout";

// Preferred gap between two segments, in CSS pixels.
const TARGET_GAP_PX = 3;
// Height of the bar, and the corner radius of a segment. Both live here rather
// than in classes because the segments are SVG shapes now — see
// lib/segment-layout.ts for why the bar is drawn instead of laid out.
const BAR_HEIGHT_PX = 28;
const SEGMENT_RADIUS_PX = 2;
// Width assumed before the row has been measured. `preserveAspectRatio="none"`
// scales the drawing to the real width either way, so a stale or placeholder
// value shifts nothing visually except the corner radius, and only until the
// first measurement lands.
const ASSUMED_ROW_WIDTH_PX = 1000;

function getBarColor(status: string) {
  // noData keeps its distinct light-gray bar; every other status (including
  // the amber warning/degraded pair) routes through the shared util.
  if (status === "noData" || status === "unknown")
    return "bg-status-neutral/40";
  return statusStyle(status).color;
}

// Segment fill for the drawn bar. noData keeps the muted, half-strength
// treatment its `bg-status-neutral/40` class gave it; every other status takes
// the solid colour straight from the shared style table.
function getSegmentFill(status: string): { fill: string; opacity: number } {
  if (status === "noData" || status === "unknown")
    return { fill: "var(--status-neutral)", opacity: 0.4 };
  return { fill: statusStyle(status).barFill, opacity: 1 };
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

  // Measure the row so the drawing's user units are CSS pixels — that keeps the
  // corner radius round and the geometry exact. Nothing breaks if the
  // measurement is stale: the SVG just scales.
  const rowRef = useRef<HTMLDivElement>(null);
  const [rowWidth, setRowWidth] = useState(0);

  useEffect(() => {
    const el = rowRef.current;
    if (!el) return;
    const measure = () => setRowWidth(el.getBoundingClientRect().width);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const viewWidth = rowWidth > 0 ? rowWidth : ASSUMED_ROW_WIDTH_PX;
  const geometry = segmentGeometry(
    viewWidth,
    dailyAvailability.length,
    TARGET_GAP_PX,
  );

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
      <div ref={rowRef} className="py-1">
        {geometry && (
          <svg
            width="100%"
            height={BAR_HEIGHT_PX}
            viewBox={`0 0 ${viewWidth} ${BAR_HEIGHT_PX}`}
            // "none": the drawing is stretched to the row's real width, so the
            // segments stay identical to each other whatever that width is.
            preserveAspectRatio="none"
            className="block"
          >
            {dailyAvailability.map((point, index) => {
              const { fill, opacity } = getSegmentFill(point.status);
              return (
                <Tooltip
                  key={point.time ?? point.date}
                  // Segments are narrow and packed tightly; the shared
                  // TooltipProvider's hoverable-content "grace area" tracks a
                  // POINTER-IN-TRANSIT flag at the provider level (not per
                  // Tooltip root). While that flag is set — which happens the
                  // instant the pointer leaves an open segment's trigger,
                  // heading toward its much-wider floating content — every
                  // OTHER trigger's onPointerMove handler becomes a no-op
                  // (Radix source: TooltipTrigger's onPointerMove is gated on
                  // `!providerContext.isPointerInTransitRef.current`). Moving
                  // the pointer along the bar can then leave a neighboring
                  // segment's tooltip never asked to open while the old one
                  // stays open or closes without a successor — a stale/blank
                  // tooltip. These tooltips are read-only labels; nothing here
                  // needs the pointer to travel into the content itself, so
                  // disabling hoverable content removes the grace area (and
                  // the shared transit flag) entirely and makes every segment
                  // open/close independently and immediately.
                  disableHoverableContent
                >
                  <TooltipTrigger asChild>
                    <rect
                      data-testid="availability-bar-segment"
                      x={index * geometry.pitch}
                      y={0}
                      width={geometry.width}
                      height={BAR_HEIGHT_PX}
                      rx={SEGMENT_RADIUS_PX}
                      fill={fill}
                      fillOpacity={opacity}
                      // Hover grows the segment instead of fading it — the old
                      // opacity fade desaturated the status color, which is the
                      // one thing on this page that has to stay readable.
                      // fill-box scopes the scale to the rect itself; without it
                      // an SVG child scales about the whole canvas.
                      className="origin-center [transform-box:fill-box] transition-transform duration-150 ease-out hover:scale-y-[1.18]"
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
              );
            })}
          </svg>
        )}
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
