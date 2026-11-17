/** The result-output key a script's named steps are stored under (spec
 * 2026-11-17-04). */
export const STEPS_OUTPUT_KEY = "steps";

export type CheckStepStatus = "ok" | "failed" | "skipped";

export interface CheckStep {
  name: string;
  status: CheckStepStatus;
  durationMs: number;
  error?: string;
  screenshot?: boolean;
}

/** Reads the steps list out of a result output, dropping malformed entries. */
export function parseSteps(output: unknown): CheckStep[] {
  if (!output || typeof output !== "object") return [];
  const raw = (output as Record<string, unknown>)[STEPS_OUTPUT_KEY];
  if (!Array.isArray(raw)) return [];
  const steps: CheckStep[] = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const entry = item as Record<string, unknown>;
    if (typeof entry.name !== "string") continue;
    const status =
      entry.status === "failed" || entry.status === "skipped" ? entry.status : "ok";
    steps.push({
      name: entry.name,
      status,
      durationMs: typeof entry.durationMs === "number" ? entry.durationMs : 0,
      error: typeof entry.error === "string" ? entry.error : undefined,
      screenshot: entry.screenshot === true,
    });
  }
  return steps;
}
