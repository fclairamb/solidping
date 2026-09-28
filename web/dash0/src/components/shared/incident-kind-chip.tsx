import { Activity, CircleArrowDown, Flame, type LucideIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { IncidentDetail } from "@/api/hooks";
import {
  incidentKindOf,
  incidentKindTextClass,
  type IncidentKind,
} from "@/lib/incident-kind";
import { cn } from "@/lib/utils";

interface KindStyle {
  icon: LucideIcon;
  /** Tinted fill + border, active incidents only. */
  activeSurface: string;
}

const KIND_STYLES: Record<IncidentKind, KindStyle> = {
  check: { icon: CircleArrowDown, activeSurface: "border-red-500/30 bg-red-500/10" },
  degraded: { icon: Activity, activeSurface: "border-amber-500/30 bg-amber-500/10" },
  slo_burn: { icon: Flame, activeSurface: "border-violet-500/30 bg-violet-500/10" },
};

/**
 * IncidentKindChip says what an incident is about (Down / Degraded / SLO
 * burn) and, independently, whether it is still open: an active incident gets
 * a tinted fill and border, a resolved one a transparent fill and a neutral
 * border while the icon and label keep the kind colour. So an active degraded
 * incident no longer looks like an outage, and a resolved outage still reads
 * as an outage.
 */
export function IncidentKindChip({
  kind,
  state,
  className,
}: {
  kind: string | undefined | null;
  state: IncidentDetail["state"];
  className?: string;
}) {
  const { t } = useTranslation("incidents");
  const resolvedKind = incidentKindOf(kind);
  const style = KIND_STYLES[resolvedKind];
  const Icon = style.icon;
  const active = state === "active";

  return (
    <span
      data-testid="incident-kind-chip"
      data-kind={resolvedKind}
      data-state={active ? "active" : "resolved"}
      title={active ? t("active") : t("resolved")}
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs font-semibold whitespace-nowrap",
        incidentKindTextClass(resolvedKind),
        active ? style.activeSurface : "border-border bg-transparent",
        className,
      )}
    >
      <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      {t(`kind.${resolvedKind}`)}
    </span>
  );
}
