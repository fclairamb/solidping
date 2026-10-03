// Helpers for the application health check's result output (spec
// 2026-10-03-05): the per-component rows and the per-component timeline built
// from the last results' outputs.

export type HealthStatus = "ok" | "warning" | "failed" | "skipped" | "unknown";

export interface HealthComponent {
  name: string;
  label?: string;
  status: HealthStatus;
  summary?: string;
  message?: string;
  ignored?: boolean;
}

const STATUSES: readonly string[] = ["ok", "warning", "failed", "skipped", "unknown"];

/** The components of a health result output, [] when it carries none. */
export function healthComponents(output: Record<string, unknown> | undefined): HealthComponent[] {
  const raw = output?.components;
  if (!Array.isArray(raw)) return [];
  const out: HealthComponent[] = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const entry = item as Record<string, unknown>;
    if (typeof entry.name !== "string") continue;
    const status =
      typeof entry.status === "string" && STATUSES.includes(entry.status)
        ? (entry.status as HealthStatus)
        : "unknown";
    out.push({
      name: entry.name,
      label: typeof entry.label === "string" ? entry.label : undefined,
      status,
      summary: typeof entry.summary === "string" ? entry.summary : undefined,
      message: typeof entry.message === "string" ? entry.message : undefined,
      ignored: entry.ignored === true,
    });
  }
  return out;
}

/**
 * One timeline per component over `outputs` (newest first, as the results API
 * returns them): the statuses oldest to newest, "unknown" where a result did
 * not list the component. Components keep the order of the newest output that
 * lists them first.
 */
export function componentTimelines(
  outputs: (Record<string, unknown> | undefined)[],
): { name: string; statuses: HealthStatus[] }[] {
  const chronological = [...outputs].reverse().map(healthComponents);
  const names: string[] = [];
  for (const components of [...chronological].reverse()) {
    for (const component of components) {
      if (!names.includes(component.name)) names.push(component.name);
    }
  }
  return names.map((name) => ({
    name,
    statuses: chronological.map(
      (components) => components.find((c) => c.name === name)?.status ?? "unknown",
    ),
  }));
}
