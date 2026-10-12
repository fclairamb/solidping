/**
 * HTTP timing phases (spec 2026-10-12-08), in display order. Each key is a
 * float-millisecond metric the HTTP checker writes only when that phase ran.
 */
export const HTTP_TIMING_PHASES = [
  { id: "redirect", key: "redirect_time_ms" },
  { id: "dns", key: "dns_time_ms" },
  { id: "connect", key: "connect_time_ms" },
  { id: "tls", key: "tls_time_ms" },
  { id: "ttfb", key: "ttfb_time_ms" },
  { id: "download", key: "download_time_ms" },
] as const;

export type HttpTimingPhaseId = (typeof HTTP_TIMING_PHASES)[number]["id"];

export interface HttpTimingPhase {
  id: HttpTimingPhaseId;
  key: string;
  ms: number;
}

/** The phases present in a metrics map, in display order. A missing or
 * non-numeric key is a phase that did not run and is left out. */
export function extractHttpTimingPhases(
  metrics: Record<string, unknown> | null | undefined,
): HttpTimingPhase[] {
  if (!metrics) return [];
  const phases: HttpTimingPhase[] = [];
  for (const { id, key } of HTTP_TIMING_PHASES) {
    const value = metrics[key];
    if (typeof value === "number" && Number.isFinite(value) && value >= 0) {
      phases.push({ id, key, ms: value });
    }
  }
  return phases;
}
