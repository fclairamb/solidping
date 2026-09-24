import * as React from "react";

import { cn } from "@/lib/utils";

export interface ProgressProps extends React.HTMLAttributes<HTMLDivElement> {
  /** Current value. */
  value: number;
  /** Maximum value the bar represents (the 100% mark). Must be > 0. */
  max: number;
  /**
   * When true (the default), the bar turns destructive once value >= max.
   * The filled width is always capped at 100% so overflow stays graceful.
   */
  destructiveWhenFull?: boolean;
  indicatorClassName?: string;
}

/**
 * Progress is a lightweight, dependency-free progress bar. The fill width is
 * clamped to [0, 100]% so an over-quota value (value > max) caps at full and
 * flips to the destructive color instead of overflowing the track.
 *
 * `indicatorClassName` recolors the fill through cn(), which drops the
 * gradient for a flat bg-<color> override (see lib/utils.ts).
 */
function Progress({
  value,
  max,
  destructiveWhenFull = true,
  className,
  indicatorClassName,
  ...props
}: ProgressProps) {
  const ratio = max > 0 ? value / max : 0;
  const pct = Math.min(100, Math.max(0, ratio * 100));
  const isFull = max > 0 && value >= max;

  return (
    <div
      role="progressbar"
      aria-valuenow={value}
      aria-valuemin={0}
      aria-valuemax={max}
      className={cn(
        "relative h-2 w-full overflow-hidden rounded-full bg-secondary",
        className,
      )}
      {...props}
    >
      <div
        className={cn(
          "h-full rounded-full transition-all",
          // The fill is the decorative --accent-gradient over bg-primary. A
          // full destructive bar must be RED: bg-none removes the gradient,
          // which bg-destructive alone (a background-COLOR) would not.
          destructiveWhenFull && isFull
            ? "bg-none bg-destructive"
            : "bg-primary bg-accent-gradient",
          indicatorClassName,
        )}
        style={{ width: `${pct}%` }}
      />
    </div>
  );
}

export { Progress };
