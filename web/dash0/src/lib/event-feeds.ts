/** i18n key of an event feed scope choice (spec 2026-11-24-12):
 *  "incident.*" → "incident_all", "check.created" → "check_created". */
export function scopeKey(choice: string): string {
  return choice.replace(/\.\*$/, "_all").replace(/\./g, "_");
}
