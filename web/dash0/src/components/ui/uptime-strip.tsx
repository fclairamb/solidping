import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  availabilityCellClass,
  availabilityDotClass,
  classifyAvailability,
  formatAvailabilityPct,
} from "@/lib/availability-status";
import { cn } from "@/lib/utils";

export interface UptimeBucket {
  periodStart: string;
  availabilityPct?: number;
  durationMs?: number;
  /** Probe counts when the source row carried them. They exist only to feed the
   * shared small-bucket guard (a cell with a single failed sample is never
   * painted red); when they are absent the guard is skipped rather than assumed
   * satisfied. */
  totalChecks?: number;
  successfulChecks?: number;
}

interface UptimeStripProps {
  buckets: UptimeBucket[];
  className?: string;
}

/** Failures for the shared guard, or undefined when the bucket did not carry
 * counts — see UptimeBucket. */
function failuresOf(bucket: UptimeBucket): number | undefined {
  if (bucket.totalChecks === undefined || bucket.successfulChecks === undefined)
    return undefined;
  return Math.max(0, bucket.totalChecks - bucket.successfulChecks);
}

function UptimeCell({ bucket }: { bucket: UptimeBucket }) {
  const date = new Date(bucket.periodStart);
  const hourStr = date.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  });
  // The SHARED classification (mirrors the server's uptimebar.Classify), not a
  // local 100/0/else mapping — this strip used to be a fourth green/amber/red
  // rule that disagreed with the status page and the badge bar.
  const status = classifyAvailability(
    bucket.availabilityPct ?? null,
    failuresOf(bucket),
  );
  const availLabel = formatAvailabilityPct(bucket.availabilityPct) ?? "No data";

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div
          data-status={status}
          className={cn(
            // rounded-[2px], not rounded-sm: at this cell width the theme's
            // radius rounded the cells into pills and the strip stopped
            // reading as a continuous timeline.
            "h-6 flex-1 min-w-[2px] rounded-[2px] origin-center transition-transform duration-150 ease-out hover:scale-y-125 cursor-pointer",
            availabilityCellClass(status),
          )}
        />
      </TooltipTrigger>
      {/* No color override: the tooltip rides the shared popover surface so it
          themes with the app (the old hardcoded gray-900 slab stayed dark in
          light mode and washed out in dark mode). */}
      <TooltipContent>
        <div className="text-xs space-y-0.5">
          <p className="font-medium flex items-center gap-1.5 tabular-nums">
            <span
              className={cn(
                "inline-block h-2 w-2 rounded-full",
                availabilityDotClass(status),
              )}
            />
            {availLabel}
          </p>
          <p className="text-muted-foreground tabular-nums">{hourStr}</p>
          {bucket.durationMs !== undefined && (
            <p className="text-muted-foreground tabular-nums">
              {Math.round(bucket.durationMs)}ms
            </p>
          )}
        </div>
      </TooltipContent>
    </Tooltip>
  );
}

/**
 * UptimeStrip renders one hourly cell per provided bucket, oldest → newest.
 * Pure presentational component: pass already-grouped buckets in, no fetching.
 *
 * Cell colour comes from the shared `classifyAvailability` mapping in
 * `@/lib/availability-status` — the TypeScript twin of the server's
 * `uptimebar.Classify` — so this strip, the chart availability strip, the public
 * status page and the badge uptime bar all paint the same numbers the same way.
 */
export function UptimeStrip({ buckets, className }: UptimeStripProps) {
  return (
    <div className={cn("flex items-center gap-0.5", className)}>
      {buckets.map((bucket, index) => (
        <UptimeCell key={bucket.periodStart || index} bucket={bucket} />
      ))}
    </div>
  );
}
