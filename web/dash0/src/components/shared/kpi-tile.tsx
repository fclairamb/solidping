import type { ReactNode } from "react";

import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export interface KpiTileProps {
  label: string;
  value: number | string;
  /** A bare lucide icon; the tile mutes it. */
  icon: ReactNode;
  sub?: string;
  /** Sits to the right of the value, e.g. a status Badge. */
  badge?: ReactNode;
  /** Colors the value, e.g. text-destructive when it needs attention. */
  valueClassName?: string;
  className?: string;
}

// One flat look for every tile: a sentence-case label, a muted icon, a
// semibold tabular value and an optional sub line. Color is spent on the
// value only, and only when it means something (a down count, a low
// availability). Hover deepens the shadow; the tile itself never moves.
export function KpiTile({
  label,
  value,
  icon,
  sub,
  badge,
  valueClassName,
  className,
}: KpiTileProps) {
  return (
    <Card
      data-slot="kpi-tile"
      className={cn("h-full transition-shadow hover:shadow-card-hover", className)}
    >
      <div className="flex h-full flex-col gap-2 p-4 sm:p-5">
        <div className="flex items-start justify-between gap-2">
          <p
            data-slot="kpi-tile-label"
            className="text-sm font-medium leading-snug text-muted-foreground"
          >
            {label}
          </p>
          <span
            data-slot="kpi-tile-icon"
            className="shrink-0 text-muted-foreground [&_svg]:h-4 [&_svg]:w-4"
          >
            {icon}
          </span>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
          <div
            data-slot="kpi-tile-value"
            className={cn(
              "text-2xl font-semibold tracking-tight tabular-nums text-foreground sm:text-3xl",
              valueClassName,
            )}
          >
            {value}
          </div>
          {badge}
        </div>
        {sub ? (
          <p data-slot="kpi-tile-sub" className="truncate text-xs text-muted-foreground">
            {sub}
          </p>
        ) : null}
      </div>
    </Card>
  );
}
