export interface MaintenanceHint {
  /** i18n key under the status namespace. */
  key: string;
  count?: number;
}

/**
 * Relative hint for a maintenance occurrence: "in progress", or how far away
 * the start is, in the coarsest sensible unit.
 */
export function maintenanceHint(
  active: boolean,
  startsAt: string,
  now: number = Date.now(),
): MaintenanceHint {
  if (active) return { key: "maintenanceInProgress" };
  const minutes = Math.max(
    1,
    Math.round((new Date(startsAt).getTime() - now) / 60000),
  );
  if (minutes < 60) return { key: "maintenanceStartsInMinutes", count: minutes };
  const hours = Math.round(minutes / 60);
  if (hours < 48) return { key: "maintenanceStartsInHours", count: hours };
  return {
    key: "maintenanceStartsInDays",
    count: Math.round(hours / 24),
  };
}
