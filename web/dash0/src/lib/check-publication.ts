import { apiFetch } from "@/api/client";
import type {
  Check,
  StatusPage,
  StatusPageSection,
  StatusPageSectionSelector,
} from "@/api/hooks";

/**
 * Where a check is already visible on a status page, and HOW it got there.
 *
 * The three routes are not interchangeable and the UI must not pretend they
 * are (spec 2026-09-16-11):
 *
 * - `direct` — a resource row targeting this check. The operator put it there.
 * - `group` — a resource row targeting the check's GROUP. Status page resources
 *   target a check XOR a check group, and a group renders as ONE rolled-up
 *   public component, so the check is already published without a row of its
 *   own. Adding a direct row on top would publish it twice, once inside the
 *   roll-up and once beside it.
 * - `selector` — a section membership rule ("all checks" / "by label") owns the
 *   row. Deleting or duplicating it here would be undone by the next
 *   reconcile; the rule is the thing to change.
 */
export type PublicationVia = "direct" | "group" | "selector";

export interface CheckPublication {
  pageUid: string;
  pageName: string;
  sectionUid: string;
  sectionName: string;
  via: PublicationVia;
}

/**
 * Whether a section's membership rule would claim this check.
 *
 * This is a CLIENT-side re-implementation of the server's matcher, used only as
 * a backstop: the reconciler materializes real rows, so a matched check is
 * normally already visible as a `managedBySelector` resource. It matters in the
 * window between creating a check and the next reconcile, which is precisely
 * the moment this feature exists to explain.
 *
 * Label semantics mirror the server: every key must be present and the value
 * must match exactly (AND, no wildcards).
 */
export function selectorMatchesCheck(
  selector: StatusPageSectionSelector | null | undefined,
  check: Pick<Check, "labels" | "checkGroupUid">,
): boolean {
  if (!selector) return false;
  if (selector.all) return true;
	if (selector.checkGroupUid) return selector.checkGroupUid === check.checkGroupUid;
  const wanted = selector.labels;
  if (!wanted || Object.keys(wanted).length === 0) return false;
  const have = check.labels ?? {};
  return Object.entries(wanted).every(([key, value]) => have[key] === value);
}

function sectionPublication(
  section: StatusPageSection,
  check: Pick<Check, "uid" | "checkGroupUid" | "labels">,
): PublicationVia | undefined {
  let via: PublicationVia | undefined;

  for (const resource of section.resources ?? []) {
    if (resource.checkUid && resource.checkUid === check.uid) {
      // A direct hit is the most specific answer available — stop here.
      return resource.managedBySelector ? "selector" : "direct";
    }
    if (
      resource.checkGroupUid &&
      check.checkGroupUid &&
      resource.checkGroupUid === check.checkGroupUid
    ) {
      // Keep scanning: a direct row later in the same section is a better
      // description of the same fact.
      via = "group";
    }
  }
  if (via) return via;

  return selectorMatchesCheck(section.selector, check) ? "selector" : undefined;
}

/**
 * Every placement of `check` across the given pages (which must have been
 * loaded with their sections and resources).
 *
 * At most one entry per section: a section shows a check once.
 */
export function findCheckPublications(
  check: Pick<Check, "uid" | "checkGroupUid" | "labels">,
  pages: StatusPage[],
): CheckPublication[] {
  const found: CheckPublication[] = [];
  for (const page of pages) {
    for (const section of page.sections ?? []) {
      const via = sectionPublication(section, check);
      if (!via) continue;
      found.push({
        pageUid: page.uid,
        pageName: page.name,
        sectionUid: section.uid,
        sectionName: section.name,
        via,
      });
    }
  }
  return found;
}

/** Loads every status page of the org with its sections and resources. */
export async function fetchStatusPagesWithSections(
  org: string,
): Promise<StatusPage[]> {
  const list = await apiFetch<{ data?: StatusPage[] }>(
    `/api/v1/orgs/${org}/status-pages`,
  );
  const pages = list.data ?? [];
  return Promise.all(
    pages.map((page) =>
      apiFetch<StatusPage>(
        `/api/v1/orgs/${org}/status-pages/${page.uid}?with=sections`,
      ),
    ),
  );
}

/**
 * Imperative counterpart of `findCheckPublications`, for callers outside the
 * React Query tree (the post-create toast on `checks.new`).
 */
export async function fetchCheckPublications(
  org: string,
  check: Pick<Check, "uid" | "checkGroupUid" | "labels">,
): Promise<CheckPublication[]> {
  return findCheckPublications(check, await fetchStatusPagesWithSections(org));
}
