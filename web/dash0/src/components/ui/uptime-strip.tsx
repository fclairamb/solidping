import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

export interface UptimeBucket {
  periodStart: string;
  availabilityPct?: number;
  durationMs?: number;
}

interface UptimeStripProps {
  buckets: UptimeBucket[];
  className?: string;
}

// Color a single hourly cell from its availability. Mirrors the green/yellow/red
// classes used by StatusTimeline's StatusBar so the two visuals stay consistent.
// No data for the hour → muted gray.
function cellClass(availabilityPct?: number): string {
  if (availabilityPct === undefined) return "bg-gray-300 dark:bg-gray-600";
  if (availabilityPct >= 100) return "bg-green-500";
  if (availabilityPct <= 0) return "bg-red-500";
  return "bg-yellow-500";
}

function dotClass(availabilityPct?: number): string {
  if (availabilityPct === undefined) return "bg-gray-400";
  if (availabilityPct >= 100) return "bg-green-400";
  if (availabilityPct <= 0) return "bg-red-400";
  return "bg-yellow-400";
}

function UptimeCell({ bucket }: { bucket: UptimeBucket }) {
  const date = new Date(bucket.periodStart);
  const hourStr = date.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  });
  const availLabel =
    bucket.availabilityPct === undefined
      ? "No data"
      : `${bucket.availabilityPct.toFixed(bucket.availabilityPct % 1 === 0 ? 0 : 1)}%`;

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div
          className={cn(
            // rounded-[2px], not rounded-sm: at this cell width the theme's
            // radius rounded the cells into pills and the strip stopped
            // reading as a continuous timeline.
            "h-6 flex-1 min-w-[2px] rounded-[2px] origin-center transition-transform duration-150 ease-out hover:scale-y-125 cursor-pointer",
            cellClass(bucket.availabilityPct),
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
                dotClass(bucket.availabilityPct),
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
 * Cell color is derived from each bucket's availabilityPct (green at 100,
 * yellow in between, red at 0, gray when the hour has no data).
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
