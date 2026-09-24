import type { ReactNode } from "react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export interface KpiTileProps {
  label: string;
  value: number | string;
  icon: ReactNode;
  sub?: string;
  /** Sits to the right of the value. On the hero, pass a solid bg-white chip
   * (see AVAILABILITY_TIER_HERO_BADGE): a pale translucent status badge loses
   * its meaning on the gradient. */
  badge?: ReactNode;
  valueClassName?: string;
  className?: string;
  /**
   * "hero" is the page's headline number: the hero gradient, white text, a
   * tinted shadow. ONE hero tile per page (spec 2026-09-24-02); every other
   * tile is "default".
   */
  variant?: "default" | "hero";
}

// The hover lift, on every tile. Behind motion-safe: so a reduced-motion
// user gets the shadow change without the movement.
const LIFT = "transition motion-safe:hover:-translate-y-0.5";

// The hero surface. It paints --hero-gradient (bg-hero-gradient, the token is
// untouched) but CROPPED: bg-size 180% anchored bottom-right shows only the
// gradient's darker end, t in [1 - 1/1.8, 1] = [0.444, 1]. The design
// reference's rule is that white small text only sits where the gradient is
// dark, and this tile puts its small label top-left (the gradient's light
// start) and its sub line bottom-left, wrapping across the whole width on a
// 172px tile, so no placement could keep them off the light end. At 90% white
// (text-gradient-foreground/90) the lightest visible point still reads
// >= 4.69:1; kpi-tile.test.tsx computes it from these very classes.
const HERO_SURFACE =
  "relative overflow-hidden rounded-xl bg-primary bg-hero-gradient bg-size-[180%_180%] bg-bottom-right text-gradient-foreground shadow-hero hover:shadow-hero";
const HERO_SMALL_TEXT = "text-gradient-foreground/90";

export function KpiTile({
  label,
  value,
  icon,
  sub,
  badge,
  valueClassName,
  className,
  variant = "default",
}: KpiTileProps) {
  const hero = variant === "hero";
  const body = (
    <>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle
          data-slot="kpi-tile-label"
          className={cn(
            "text-xs font-semibold tracking-wider uppercase",
            hero ? HERO_SMALL_TEXT : "text-muted-foreground",
          )}
        >
          {label}
        </CardTitle>
        <div
          data-slot="kpi-tile-icon"
          className={cn(
            "flex h-7 w-7 shrink-0 items-center justify-center rounded-lg",
            hero
              ? "bg-white/15 text-gradient-foreground"
              : "bg-muted/60 text-muted-foreground",
          )}
        >
          {icon}
        </div>
      </CardHeader>
      <CardContent className="space-y-1">
        <div className="flex items-baseline justify-between gap-2">
          <div
            data-slot="kpi-tile-value"
            className={cn(
              "text-2xl sm:text-3xl font-bold tracking-tight tabular-nums",
              hero ? "text-gradient-foreground" : "text-foreground",
              valueClassName,
            )}
          >
            {value}
          </div>
          {badge}
        </div>
        {sub ? (
          <p
            data-slot="kpi-tile-sub"
            className={cn("text-xs mt-1", hero ? HERO_SMALL_TEXT : "text-muted-foreground")}
          >
            {sub}
          </p>
        ) : null}
      </CardContent>
    </>
  );

  if (hero) {
    // Not a <Card>: no border, and none of Card's dark-mode inset shadow,
    // which would replace the tinted hero shadow in dark mode.
    return (
      <div
        data-slot="kpi-tile"
        data-variant="hero"
        className={cn(HERO_SURFACE, LIFT, className)}
      >
        {body}
      </div>
    );
  }

  return (
    <Card
      data-slot="kpi-tile"
      data-variant="default"
      className={cn(LIFT, "hover:shadow-card-hover", className)}
    >
      {body}
    </Card>
  );
}
