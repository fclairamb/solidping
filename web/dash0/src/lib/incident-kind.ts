import type { IncidentDetail } from "@/api/hooks";

export type IncidentKind = NonNullable<IncidentDetail["kind"]>;

export const INCIDENT_KINDS: IncidentKind[] = ["check", "degraded", "slo_burn"];

/**
 * Normalizes an incident's kind. Every row written before the kind column
 * existed is an outage, and older API builds omit the field, so anything
 * missing or unknown reads as "check".
 */
export function incidentKindOf(kind: string | undefined | null): IncidentKind {
  return INCIDENT_KINDS.includes(kind as IncidentKind)
    ? (kind as IncidentKind)
    : "check";
}

// One place for the per-kind text colour so the kind chip, the incidents
// list's "ongoing · 8m" text and the design reference never drift apart.
const KIND_TEXT_CLASS: Record<IncidentKind, string> = {
  check: "text-red-700 dark:text-red-400",
  degraded: "text-amber-700 dark:text-amber-400",
  slo_burn: "text-violet-700 dark:text-violet-400",
};

/** The kind's text colour, for the chip and for copy that sits next to it. */
export function incidentKindTextClass(kind: string | undefined | null): string {
  return KIND_TEXT_CLASS[incidentKindOf(kind)];
}
