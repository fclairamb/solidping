// Touched-or-submitted gating for check form errors: a server finding about a
// field is displayed only once the user focused/edited that field (or tried to
// submit). Controls are identified by their id, name and data-testid, since the
// server's field name ("host", "port", …) matches one of them by convention.

const norm = (v: string): string => v.toLowerCase().replace(/[^a-z0-9]/g, "");

/** Normalized identifiers of a form control, empty for non-controls. */
export function touchIdentifiers(target: EventTarget | null): string[] {
  if (!(target instanceof HTMLElement)) return [];
  if (!["INPUT", "TEXTAREA", "SELECT", "BUTTON"].includes(target.tagName)) {
    return [];
  }
  return [
    target.id,
    target.getAttribute("name") ?? "",
    target.getAttribute("data-testid") ?? "",
  ]
    .map(norm)
    .filter(Boolean);
}

/** Whether a control matching the server field `name` was touched. */
export function isFieldTouched(
  touched: readonly string[],
  name: string,
): boolean {
  const n = norm(name);
  if (!n) return false;
  return touched.some((id) => id === n || id === `check${n}input`);
}
