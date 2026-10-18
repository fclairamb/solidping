import type { ReactNode } from "react";

import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export type KpiTileTone = "neutral" | "primary" | "ok" | "warning" | "error";

export interface KpiTileProps {
  label: string;
  value: number | string;
  /** A bare lucide icon; the tile puts it on a chip colored by `tone`. */
  icon: ReactNode;
  sub?: string;
  /** Sits to the right of the value, e.g. a status Badge. */
  badge?: ReactNode;
  /** Colors the value, e.g. text-destructive when it needs attention. */
  valueClassName?: string;
  /**
   * What the number means right now: "primary" for a plain count, a status
   * tone when it carries one (error when something is down, ok when all is
   * clear). Colors the icon chip and a faint wash from the chip's corner.
   * "neutral" (the default) has no wash.
   */
  tone?: KpiTileTone;
  className?: string;
}

// The chip behind the icon and the wash's start color, per tone. Alpha tints
// of the status tokens, so neither theme needs a dark: variant.
const TONE: Record<KpiTileTone, { chip: string; wash?: string }> = {
  neutral: { chip: "bg-muted text-muted-foreground" },
  primary: { chip: "bg-primary/10 text-primary", wash: "from-primary/10" },
  ok: { chip: "bg-status-ok/15 text-status-ok-foreground", wash: "from-status-ok/10" },
  warning: {
    chip: "bg-status-warning/15 text-status-warning-foreground",
    wash: "from-status-warning/10",
  },
  error: {
    chip: "bg-status-error/15 text-status-error-foreground",
    wash: "from-status-error/12",
  },
};

// A sentence-case label, a tone-colored icon chip, a semibold tabular value
// and an optional sub line. The wash is a tonal gradient from the chip's
// corner, fading out before the middle of the tile: it says which tile needs
// a look without a glow or a saturated fill. Hover deepens the shadow; the
// tile itself never moves.
export function KpiTile({
  label,
  value,
  icon,
  sub,
  badge,
  valueClassName,
  tone = "neutral",
  className,
}: KpiTileProps) {
  const { chip, wash } = TONE[tone];
  return (
    <Card
      data-slot="kpi-tile"
      data-tone={tone}
      className={cn(
        "relative h-full overflow-hidden transition-shadow hover:shadow-card-hover",
        className,
      )}
    >
      {wash ? (
        <div
          aria-hidden="true"
          data-slot="kpi-tile-wash"
          className={cn(
            "pointer-events-none absolute inset-0 bg-linear-to-bl to-transparent to-60%",
            wash,
          )}
        />
      ) : null}
      <div className="relative flex h-full flex-col gap-2 p-4 sm:p-5">
        <div className="flex items-start justify-between gap-2">
          <p
            data-slot="kpi-tile-label"
            className="text-sm font-medium leading-snug text-muted-foreground"
          >
            {label}
          </p>
          <span
            data-slot="kpi-tile-icon"
            className={cn(
              "flex h-7 w-7 shrink-0 items-center justify-center rounded-md [&_svg]:h-4 [&_svg]:w-4",
              chip,
            )}
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
