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

// One step lighter than the chip's active fill (incident-kind-chip.tsx), so
// the chip still reads as the stronger element sitting on top of it, and one
// step stronger in dark mode where 5% disappears into the card.
const ACTIVE_ROW_CLASS: Record<IncidentKind, string> = {
  check: "bg-red-500/5 hover:bg-red-500/10 dark:bg-red-500/10 dark:hover:bg-red-500/15",
  degraded:
    "bg-amber-500/5 hover:bg-amber-500/10 dark:bg-amber-500/10 dark:hover:bg-amber-500/15",
  slo_burn:
    "bg-violet-500/5 hover:bg-violet-500/10 dark:bg-violet-500/10 dark:hover:bg-violet-500/15",
};

/**
 * The incidents list's row surface: a kind-coloured tint while the incident
 * is still open, the neutral hover once it resolves. The chip says WHICH kind
 * it is; this says "it has not ended yet" — which is the one thing a
 * `?state=all` list needs to show at a glance, when open and closed rows are
 * otherwise identical. Resolved rows keep the card's own background, so the
 * tint is the only difference between them.
 *
 * The colour follows the kind rather than being one red for every open
 * incident: an active degraded incident is not an outage, and the chip made
 * exactly that distinction first.
 */
export function incidentRowClass(
  state: string | undefined | null,
  kind: string | undefined | null,
): string {
  if (state !== "active") return "hover:bg-muted/40";
  return ACTIVE_ROW_CLASS[incidentKindOf(kind)];
}
