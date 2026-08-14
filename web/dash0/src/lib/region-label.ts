import type { RegionDefinition } from "@/api/hooks";

// regionDisplayLabel resolves a region slug to its friendly "{emoji} {name}"
// display label from the region definitions (the `regions` system parameter,
// fetched via useRegions). Falls back to the raw slug when no definition
// matches — workers and historical results can report regions that were never
// defined, and a raw slug beats rendering nothing.
export function regionDisplayLabel(
  regions: RegionDefinition[] | undefined,
  slug: string,
): string {
  const def = regions?.find((r) => r.slug === slug);
  return def ? `${def.emoji} ${def.name}` : slug;
}

// sortRegionSlugs orders region slugs into a single canonical chip order,
// shared by every surface that lists observed regions (the Response Times
// chart and the Recent Results filter): standard regions first, then
// custom/private regions, then slugs with no matching definition at all
// (historical results from since-deleted regions). Within each group, sort
// alphabetically by the user-visible name (falling back to the raw slug),
// case-insensitively — so org-relative private slugs like
// `@stonaltech/s3ns-paris` no longer jump the queue just because `@` sorts
// before letters.
export function sortRegionSlugs(
  regions: RegionDefinition[] | undefined,
  slugs: string[],
): string[] {
  const groupOf = (def: RegionDefinition | undefined): number => {
    if (!def) return 2; // unknown slug, no definition
    return def.private ? 1 : 0; // custom/private, then standard
  };
  return [...slugs].sort((a, b) => {
    const defA = regions?.find((r) => r.slug === a);
    const defB = regions?.find((r) => r.slug === b);
    const groupDiff = groupOf(defA) - groupOf(defB);
    if (groupDiff !== 0) return groupDiff;
    const nameA = (defA?.name ?? a).toLowerCase();
    const nameB = (defB?.name ?? b).toLowerCase();
    return nameA.localeCompare(nameB);
  });
}
