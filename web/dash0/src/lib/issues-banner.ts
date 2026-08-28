import type { TFunction } from "i18next";

/**
 * Composes the red "Issues detected" banner's subtitle from two
 * INDEPENDENTLY pluralized fragments and joins only the non-zero ones.
 *
 * Before this fix a single `count: hardDownCount` selector drove one
 * `issuesSub_*` key for the whole sentence: with hardDownCount === 0 and
 * incidentsCount > 0 (check recovered, incident still open — exactly the
 * state that keeps the red branch alive via
 * `hardDownCount > 0 || incidentsCount > 0`) i18next picked `issuesSub_zero`,
 * "No active incidents", flatly contradicting the banner that fired *because*
 * of that incident. It also meant the incidents fragment could never
 * pluralize on its own ("1 check down, 1 active incident**s**").
 *
 * - down > 0, incidents > 0 → "2 checks down, 1 active incident"
 * - down > 0, incidents = 0 → "2 checks down"
 * - down = 0, incidents > 0 → "1 active incident"
 *
 * (down === 0 and incidents === 0 never reaches this — that state is the
 * all-green branch.)
 */
export function formatIssuesSubtitle(
  t: TFunction,
  hardDownCount: number,
  incidentsCount: number,
): string {
  const parts = [
    hardDownCount > 0 ? t("banner.issuesSubDown", { count: hardDownCount }) : null,
    incidentsCount > 0
      ? t("banner.issuesSubIncidents", { count: incidentsCount })
      : null,
  ].filter((part): part is string => part !== null);
  return parts.join(", ");
}
