/**
 * Whether a check's notification-channel bindings actually changed.
 *
 * `PUT /orgs/{org}/checks/{uid}/channels` is a *replace* — the edit page used to
 * issue it on every save, because the form always carries a `connectionUids`
 * array in edit mode (it is seeded from the check's existing bindings, and an
 * empty array is still an array). Sending a request whose only effect is to
 * write back exactly what is already stored is wasteful for everyone, and for a
 * demo session it is worse than wasteful: that route is deliberately outside
 * the demo allowlist (spec 2026-09-06-02 §2), so the no-op write was answered
 * `403 DEMO_READ_ONLY` and took a perfectly good check edit down with it.
 *
 * The comparison is order-insensitive — the picker's selection order is not
 * meaningful and the API returns bindings in its own order — and tolerant of
 * duplicates on either side, which a set-replace endpoint collapses anyway. It
 * deliberately does NOT treat `undefined` as "no bindings": a caller that has
 * not loaded the current bindings yet must not be told "nothing changed".
 *
 * @param selected the uids the form is submitting
 * @param existing the uids currently bound, or `undefined` when not yet known
 */
export function connectionBindingsChanged(
  selected: string[] | undefined,
  existing: string[] | undefined,
): boolean {
  if (selected === undefined) return false;
  if (existing === undefined) return true;

  const before = new Set(existing);
  const after = new Set(selected);

  if (before.size !== after.size) return true;

  for (const uid of after) {
    if (!before.has(uid)) return true;
  }

  return false;
}
