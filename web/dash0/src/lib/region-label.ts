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
