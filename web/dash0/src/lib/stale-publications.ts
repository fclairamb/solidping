import type { IncidentPublication, StalePublication } from "@/api/hooks";

/**
 * Derivations behind the "your public page still says something is broken"
 * warning (spec 2026-09-02-05).
 *
 * The state it describes is invisible everywhere else in dash0: the checks list
 * knows nothing about publications, and the incident it came from is resolved,
 * so the incidents list has moved on too. The only surfaces that show it are
 * the public status page and the wallboard — neither of which the operator is
 * looking at when they wonder why the TV is amber.
 */

/** One page's worth of stale publications, for a one-line-per-page banner. */
export interface StalePublicationPageGroup {
  pageUid: string;
  pageName: string;
  publications: IncidentPublication[];
}

/**
 * Groups stale publications by the page they sit on, preserving the order the
 * pages arrived in.
 *
 * Per page rather than one flat list because the remedy is per page — the
 * operator opens that page's incident and closes it — and because "3 open on
 * Public status" reads as one problem, while three identical lines read as
 * three.
 */
export function groupStaleByPage(
  stale: StalePublication[],
): StalePublicationPageGroup[] {
  const groups: StalePublicationPageGroup[] = [];
  const index = new Map<string, StalePublicationPageGroup>();

  for (const entry of stale) {
    let group = index.get(entry.page.uid);

    if (!group) {
      group = {
        pageUid: entry.page.uid,
        pageName: entry.page.name,
        publications: [],
      };
      index.set(entry.page.uid, group);
      groups.push(group);
    }

    group.publications.push(entry.publication);
  }

  return groups;
}

/**
 * Whether a publication the server handed us is one the banner should show.
 *
 * Applied client-side on top of `?stale=true` on purpose. A server that does
 * not understand the parameter — an older backend behind a newer dashboard, a
 * proxy that strips query strings — answers the unfiltered listing, and the
 * banner would then accuse an org of neglect for every publication it has open
 * during a genuine outage. A warning that fires when nothing is wrong is worse
 * than no warning at all: it is the thing operators learn to ignore.
 *
 * The rule itself mirrors the server's: a resolved entry is never stale (there
 * is nothing left to close), and a free-form entry tracks no incident, so no
 * recovery can contradict it.
 */
export function isStalePublication(pub: IncidentPublication): boolean {
  if (pub.state === "resolved" || pub.resolvedAt) {
    return false;
  }

  if (!pub.incidentUid) {
    return false;
  }

  return pub.stale === true;
}
