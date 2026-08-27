import { useTranslation } from "react-i18next";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  availabilityCellClass,
  availabilityDotClass,
  formatAvailabilityPct,
  type AvailabilityStatus,
} from "@/lib/availability-status";
import { cn } from "@/lib/utils";

/** One cell of the strip. Shaped like the API's AvailabilityBucket so a response
 * row can be passed straight through. `availabilityPct` null (with
 * `hasData: false`) is the no-data state — gray, never green. */
export interface AvailabilityStripCell {
  periodStart: string;
  periodEnd: string;
  hasData: boolean;
  availabilityPct: number | null;
  totalChecks: number;
  successfulChecks: number;
  status: AvailabilityStatus;
}

interface AvailabilityStripProps {
  cells: AvailabilityStripCell[];
  /** Rendered inside the tooltip above the timestamps. */
  className?: string;
  /** Prefix for the per-cell `data-testid`, so more than one strip can live on
   * a page (the design reference renders several). */
  testIdPrefix?: string;
  /** Cell height. The chart strip is deliberately shorter than the dashboard's
   * 24h strip so it reads as an axis annotation, not a second chart. */
  height?: "sm" | "md";
}

/** Compact "Mon 14:00 → 20:00" style span label, adapted to the bucket width. */
function formatSpan(startIso: string, endIso: string): string {
  const start = new Date(startIso);
  const end = new Date(endIso);
  const spanMs = end.getTime() - start.getTime();
  const dayOpts: Intl.DateTimeFormatOptions = {
    month: "short",
    day: "numeric",
  };
  const timeOpts: Intl.DateTimeFormatOptions = {
    hour: "2-digit",
    minute: "2-digit",
  };

  // A whole-day bucket has no useful clock component — showing "00:00 → 00:00"
  // is noise, and the reader wants the date.
  if (spanMs >= 24 * 3_600_000)
    return start.toLocaleDateString([], { ...dayOpts, year: undefined });

  return `${start.toLocaleDateString([], dayOpts)} ${start.toLocaleTimeString(
    [],
    timeOpts,
  )} → ${end.toLocaleTimeString([], timeOpts)}`;
}

function StripCell({
  cell,
  testId,
  height,
}: {
  cell: AvailabilityStripCell;
  testId?: string;
  height: "sm" | "md";
}) {
  const { t } = useTranslation("checks");
  const pctLabel = formatAvailabilityPct(cell.availabilityPct);
  const statusLabel = t(`detail.availabilityStrip.status.${cell.status}`);
  const headline = pctLabel ?? t("detail.availabilityStrip.noData");

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div
          role="img"
          aria-label={`${formatSpan(cell.periodStart, cell.periodEnd)}: ${headline}`}
          data-testid={testId}
          data-status={cell.status}
          className={cn(
            // rounded-[2px], not rounded-sm: at this cell width the theme radius
            // rounds the cells into pills and the strip stops reading as a
            // continuous timeline.
            "flex-1 min-w-[2px] rounded-[2px] origin-center cursor-pointer",
            "transition-transform duration-150 ease-out hover:scale-y-125",
            height === "sm" ? "h-3" : "h-6",
            availabilityCellClass(cell.status),
          )}
        />
      </TooltipTrigger>
      <TooltipContent>
        <div className="text-xs space-y-0.5">
          <p className="font-medium flex items-center gap-1.5 tabular-nums">
            <span
              className={cn(
                "inline-block h-2 w-2 rounded-full",
                availabilityDotClass(cell.status),
              )}
            />
            {headline}
            {/* Only when there IS data: for a no-data cell the status word is
                the headline, and printing it twice reads as a bug. */}
            {cell.hasData && (
              <span className="text-muted-foreground font-normal">
                {statusLabel}
              </span>
            )}
          </p>
          <p className="text-muted-foreground tabular-nums">
            {formatSpan(cell.periodStart, cell.periodEnd)}
          </p>
          {cell.hasData && (
            <p className="text-muted-foreground tabular-nums">
              {t("detail.availabilityStrip.probes", {
                up: cell.successfulChecks,
                total: cell.totalChecks,
              })}
            </p>
          )}
        </div>
      </TooltipContent>
    </Tooltip>
  );
}

/**
 * A color-banded availability strip: one cell per bucket, oldest → newest.
 *
 * Purely presentational — pass already-bucketed cells in, no fetching. Cell
 * colour comes from the SERVER's `status` (uptimebar.Classify), the same
 * classifier the public status page and the badge uptime bar use, so identical
 * numbers can never be painted differently on two surfaces. A `noData` cell is
 * gray: no data is a distinct third state, never a manufactured 100%.
 */
export function AvailabilityStrip({
  cells,
  className,
  testIdPrefix,
  height = "sm",
}: AvailabilityStripProps) {
  return (
    <div
      className={cn("flex items-stretch gap-0.5", className)}
      data-testid={testIdPrefix}
    >
      {cells.map((cell, index) => (
        <StripCell
          key={cell.periodStart || index}
          cell={cell}
          height={height}
          testId={testIdPrefix ? `${testIdPrefix}-cell` : undefined}
        />
      ))}
    </div>
  );
}
