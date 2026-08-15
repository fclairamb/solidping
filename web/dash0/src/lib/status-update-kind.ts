// Tone classes for a status update's `kind`, shared between every surface
// that renders one (the status-updates list, and the incident detail page's
// status-update timeline) so the same kind always reads the same color.
const TONE_AMBER =
  "border-amber-500/20 bg-amber-500/10 text-amber-700 dark:text-amber-400";
const TONE_ORANGE =
  "border-orange-500/20 bg-orange-500/10 text-orange-700 dark:text-orange-400";
const TONE_BLUE =
  "border-blue-500/20 bg-blue-500/10 text-blue-700 dark:text-blue-400";
const TONE_EMERALD =
  "border-emerald-500/20 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400";
const TONE_VIOLET =
  "border-violet-500/20 bg-violet-500/10 text-violet-700 dark:text-violet-400";
const TONE_SLATE =
  "border-slate-500/20 bg-slate-500/10 text-slate-700 dark:text-slate-400";

export const STATUS_UPDATE_KINDS = [
  "investigating",
  "identified",
  "monitoring",
  "resolved",
  "maintenance",
  "info",
] as const;

export type StatusUpdateKind = (typeof STATUS_UPDATE_KINDS)[number];

const KIND_TONES: Record<string, string> = {
  investigating: TONE_AMBER,
  identified: TONE_ORANGE,
  monitoring: TONE_BLUE,
  resolved: TONE_EMERALD,
  maintenance: TONE_VIOLET,
  info: TONE_SLATE,
};

// An unrecognized kind falls back to the neutral slate tone rather than
// inventing a new color.
export function statusUpdateKindTone(kind: string): string {
  return KIND_TONES[kind] ?? TONE_SLATE;
}
