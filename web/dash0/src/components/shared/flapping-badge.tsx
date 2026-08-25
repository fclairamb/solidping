import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// Amber tone for an incident that opened or reopened at an escalated
// adaptive-recovery flap level (spec 2026-08-24-05). A single exported
// constant so the incidents list and the incident detail page can never
// drift apart, mirroring the sloStateBadgeClass (lib/slo-format.ts) /
// getEventTone (components/dashboard/event-display.tsx) precedent of a
// plain class-string helper rather than a one-off inline className.
export const flappingBadgeClass =
  "bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/20";

/**
 * FlappingBadge renders "flapping ×N" for an incident.flapLevel > 0 — the
 * check's flap count at the moment this incident opened or last reopened.
 * Like Ipv6CapabilityBadge / EventTypeBadge, the component does not
 * self-hide: callers decide whether flapLevel warrants rendering it at all
 * (`(incident.flapLevel ?? 0) > 0`).
 *
 * `className` lets a call site layer on its own sizing (e.g. the incidents
 * list's `text-xs font-normal`) without duplicating the amber tone string.
 */
export function FlappingBadge({
  flapLevel,
  t,
  className,
}: {
  flapLevel: number;
  t: (key: string, options?: Record<string, unknown>) => string;
  className?: string;
}) {
  return (
    <Badge
      variant="outline"
      className={cn(flappingBadgeClass, className)}
      title={t("flappingHint", { count: flapLevel })}
      data-testid="incident-flapping-badge"
    >
      {t("flapping", { count: flapLevel })}
    </Badge>
  );
}
