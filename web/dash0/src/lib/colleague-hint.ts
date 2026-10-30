/**
 * localStorage key remembering a dismissed colleague hint (spec 2026-10-30-04),
 * per user and suggested org: a new suggestion shows again.
 */
export function colleagueHintDismissKey(userUid: string, orgSlug: string): string {
  return `solidping:colleague-hint-dismissed:${userUid}:${orgSlug}`;
}
